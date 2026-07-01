package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/bundle"
	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/secret"
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Write a portable, secret-scanned bundle of the config repo",
	Long: `Write a portable bundle of the config repo for an offline move.

export bundles ONLY git-tracked shared files (never untracked/ignored junk),
secret-scans every text file's content AND every path, and refuses ~/.ssh and
symlink entries. A tracked binary is scanned for embedded private-key markers
and withheld if any are found, otherwise bundled. The result is a self-contained
.zip you move to another account or
machine and ingest with "ferry import". Secrets and the per-machine local layer
are never included unless you pass --include-local. export prints the bundle's
SHA256 so you can verify the move with "ferry import --expect-sha256".`,
	Args: cobra.NoArgs,
	RunE: runExport,
}

// reasonSecretPath is the sentinel withhold reason for an entry dropped because a
// component of its PATH is secret-shaped. It is handled specially in the report so
// the secret-shaped token is never echoed back (which would re-leak it).
const reasonSecretPath = "__secret_path__"

// runExport writes a portable, secret-scanned bundle of the config repo's TRACKED
// files. The inclusion set is `git ls-files` (tracked files only — no untracked
// junk, editor backups, or ignored files), MINUS an explicit exclusion list:
// `.git/**`, the local layer (unless --include-local), ferry state, and the
// resolved --out path. Every candidate is routed through safeRepoPath (refusing a
// symlink or anything resolving under ~/.ssh) before it is read, and secret-gated
// on BOTH its content and its relative path. A required shared file
// (ferry.toml) that is missing or would be withheld ABORTS — an unusable bundle is
// never produced.
func runExport(c *cobra.Command, _ []string) error {
	if err := preflightGit(); err != nil {
		return err
	}
	ctx, err := loadContext()
	if err != nil {
		return err
	}
	out := c.OutOrStdout()

	includeLocal, _ := c.Flags().GetBool("include-local")
	outPath, _ := c.Flags().GetString("out")
	if strings.TrimSpace(outPath) == "" {
		outPath = "ferry-bundle.zip"
	}
	// Expand a leading ~ before resolving/guarding, mirroring import's bundle-path
	// handling, so a literal `--out ~/.ssh/x.zip` is normalised to the real path and
	// the ~/.ssh guard below sees (and refuses) it — not a literal "~" directory.
	expandedOut, err := expandUser(outPath)
	if err != nil {
		return err
	}
	absOut, err := filepath.Abs(expandedOut)
	if err != nil {
		return fmt.Errorf("resolve --out %q: %w", outPath, err)
	}
	// The bundle file must never live under ~/.ssh: a lexical repo-containment check
	// (below) is not enough — `--out ~/.ssh/x.zip` or a symlinked --out that escapes
	// there must be refused BEFORE the zip is written. guardRepoPath resolves the path
	// lexically (never touching ~/.ssh) and refuses anything at/under it.
	if _, err := guardRepoPath("--out", absOut); err != nil {
		return err
	}

	repoRoot, err := filepath.Abs(ctx.RepoPath)
	if err != nil {
		return err
	}
	repoRoot = filepath.Clean(repoRoot)

	// --out must NOT be inside the repo (M5): the bundle can never contain itself,
	// and the entry set is collected before the zip is created.
	if rel, rerr := filepath.Rel(repoRoot, absOut); rerr == nil &&
		rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("--out %q is inside the config repo (%s); the bundle must be written OUTSIDE the repo so it never contains itself", outPath, repoRoot)
	}

	// Enumerate the TRACKED file set via `git ls-files -z` (NUL-delimited so paths
	// with spaces/newlines survive). This is the ONLY inclusion source — untracked,
	// ignored, and editor-backup files never enter.
	tracked, err := gitTrackedFiles(repoRoot)
	if err != nil {
		return err
	}

	var sources []bundle.Source
	var withheld []string
	sawRequired := false

	for _, rel := range tracked {
		data, reason, ok := classifyExportEntry(repoRoot, rel, includeLocal)
		if rel == config.SharedManifestName {
			// The required shared manifest: track whether it survives every gate.
			if ok {
				sawRequired = true
			} else {
				// Missing/withheld required file → abort (M11); no unusable bundle.
				return fmt.Errorf("required shared manifest %s cannot be bundled (%s) — aborting so no unusable bundle is produced", config.SharedManifestName, reason)
			}
		}
		if !ok {
			if reason == reasonSecretPath {
				// Do NOT echo a secret-shaped path token (that would re-leak it):
				// report the withholding with the path redacted, not the token.
				withheld = append(withheld, "withheld: <redacted path> (contains a secret in its path)")
			} else if reason != "" {
				withheld = append(withheld, fmt.Sprintf("withheld: %s (%s)", rel, reason))
			}
			continue
		}
		sources = append(sources, bundle.Source{
			RelPath: filepath.ToSlash(rel),
			AbsPath: filepath.Join(repoRoot, filepath.FromSlash(rel)),
			// Pass the bytes classifyExportEntry already read behind the symlink
			// guard, so bundle.Write never re-opens the path (read TOCTOU closed).
			Data: data,
		})
	}

	if !sawRequired {
		return fmt.Errorf("required shared manifest %s is not tracked in the config repo — aborting (a bundle without it is unusable; run `ferry init`/`ferry capture` first)", config.SharedManifestName)
	}
	if len(sources) == 0 {
		return fmt.Errorf("no files to bundle after gating — aborting")
	}

	sha, err := bundle.Write(absOut, version, includeLocal, sources)
	if err != nil {
		return fmt.Errorf("write bundle: %w", err)
	}

	for _, w := range withheld {
		fmt.Fprintln(out, w)
	}
	fmt.Fprintf(out, "wrote bundle: %s\n", absOut)
	fmt.Fprintf(out, "bundle sha256: %s\n", sha)
	fmt.Fprintf(out, "summary: %d file(s) bundled, %d withheld\n", len(sources), len(withheld))
	fmt.Fprintln(out, "convey the sha256 out-of-band; import with `ferry import --expect-sha256 <sha256>` to verify.")
	return nil
}

// classifyExportEntry decides whether a tracked repo-relative path is bundled. It
// returns (data, reason, ok): ok=true means include it (data is the vetted content
// to bundle); ok=false with a non-empty reason means WITHHOLD (report it); ok=false
// with an empty reason means silently exclude (a structurally-excluded path such as
// the local layer without --include-local).
//
// The gates, in order: exclusion set (local layer / .git / VCS), the secret-shaped
// PATH gate (FIRST, so a secret-shaped filename can NEVER reach a code path that
// echoes the path — every other refusal message names `rel`), the ~/.ssh + symlink
// guard (safeRepoPath), regular-file-only, and finally a secret in the file CONTENT.
// Text files run the line-based text gate (IsBlockedFromRepo); a binary (non-text)
// tracked file runs the binary-safe key-marker scan (secret.HasKeyMarker) over its raw
// bytes and is WITHHELD if it carries private-key material — symmetric with
// import/validate, so an export always re-imports and a key-bearing binary is refused
// on both sides.
func classifyExportEntry(repoRoot, rel string, includeLocal bool) ([]byte, string, bool) {
	slash := filepath.ToSlash(rel)

	// Structural exclusions (silent — not "withheld", just not part of the set).
	if isVCSPath(slash) {
		return nil, "", false
	}
	if !includeLocal && isLocalLayerRel(slash) {
		return nil, "", false
	}

	// Secret-shaped PATH gate FIRST (M10): a token in a filename must not leak via
	// the manifest OR via any refusal message. This runs BEFORE safeRepoPath / Lstat
	// so that a secret-shaped path that is ALSO a symlink or otherwise refusable can
	// never reach a branch that echoes `rel`/`abs` — every other refusal below names
	// the path, but the reasonSecretPath sentinel is reported with the path REDACTED.
	if secretInPath(slash) {
		return nil, reasonSecretPath, false
	}

	abs := filepath.Join(repoRoot, filepath.FromSlash(rel))

	// ~/.ssh + symlink guard BEFORE any read: safeRepoPath refuses a symlink
	// (regular files only) or anything resolving under ~/.ssh / escaping the repo.
	if _, err := safeRepoPath(repoRoot, abs); err != nil {
		return nil, "refused: " + err.Error(), false
	}

	fi, err := os.Lstat(abs)
	if err != nil {
		return nil, "unreadable", false
	}
	if !fi.Mode().IsRegular() {
		// A tracked symlink/special entry: regular files only (C3).
		return nil, "not a regular file", false
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, "unreadable", false
	}
	// Binary (non-text) content: the line-based text secret gate can't scan it, but a
	// binary can still carry embedded private-key material. Run the BINARY-SAFE key
	// marker scan (secret.HasKeyMarker) over the raw bytes; if it hits, WITHHOLD it
	// (never bundle key material). This scan is symmetric with import/validate, which
	// applies the SAME HasKeyMarker check to binary payloads, so an export always
	// re-imports and a binary carrying key bytes is refused on both sides. A clean
	// binary is included (already tracked/user-vetted, past every path/symlink/~.ssh
	// gate).
	if !isProbablyText(data) {
		if secret.HasKeyMarker(data) {
			return nil, "contains key material", false
		}
		return data, "", true
	}
	// Secret in CONTENT (parity with capture): withhold, never bundle.
	if secret.IsBlockedFromRepo(string(data)) {
		return nil, "contains a secret", false
	}

	return data, "", true
}

// gitTrackedFiles returns the repo's tracked-file set (`git ls-files -z`), as
// forward-slash relative paths. Only tracked files can enter the bundle.
func gitTrackedFiles(repoRoot string) ([]string, error) {
	outStr, err := runGitIn(repoRoot, "ls-files", "-z")
	if err != nil {
		return nil, fmt.Errorf("git ls-files in %s failed: %w\n%s", repoRoot, err, outStr)
	}
	var files []string
	for _, p := range strings.Split(outStr, "\x00") {
		if p == "" {
			continue
		}
		files = append(files, p)
	}
	return files, nil
}

// isVCSPath reports whether a forward-slash relative path has a VCS/control
// component (`.git`, `.hg`, `.svn`, `.bzr`) — those must never be bundled.
func isVCSPath(slash string) bool {
	for _, seg := range strings.Split(slash, "/") {
		switch strings.ToLower(seg) {
		case ".git", ".hg", ".svn", ".bzr":
			return true
		}
	}
	return false
}

// isLocalLayerRel reports whether a forward-slash relative path is part of the
// local layer — `local/**` or the top-level `ferry.local.toml`. Excluded unless
// --include-local.
func isLocalLayerRel(slash string) bool {
	return slash == config.LocalManifestName || strings.HasPrefix(slash, "local/")
}

// secretInPath reports whether any component of a forward-slash relative path is a
// high-confidence secret-shaped token (M10). Each component is scanned as an opaque
// value so a token used as a filename is caught.
func secretInPath(slash string) bool {
	for _, seg := range strings.Split(slash, "/") {
		if secret.GateValue(seg).BlockedFromRepo {
			return true
		}
	}
	return false
}

// isProbablyText reports whether data is safe to run the text secret gate over. A
// NUL byte marks it binary; a large file is treated as text (the gate handles it).
// A binary (non-text) tracked file skips the text gate but is instead run through the
// binary-safe key-marker scan (secret.HasKeyMarker) by the caller — the SAME check
// import/validate applies to binary payloads, so export and import classify a given
// file identically.
func isProbablyText(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}

func init() {
	exportCmd.Flags().String("out", "", "path to write the bundle (default ./ferry-bundle.zip; must be OUTSIDE the repo)")
	exportCmd.Flags().Bool("include-local", false, "also bundle the per-machine local layer (local/**, ferry.local.toml)")
	rootCmd.AddCommand(exportCmd)
}
