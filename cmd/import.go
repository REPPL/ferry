package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/bundle"
	"github.com/REPPL/ferry/internal/config"
)

var importCmd = &cobra.Command{
	Use:   "import <bundle>",
	Short: "Ingest a ferry bundle into a fresh config repo",
	Long: `Ingest a portable ferry bundle into a fresh config repo.

import validates the bundle FULLY before writing anything (integrity, resource
caps, path/symlink/.git rejection, secret re-scan, version), then lays the shared
files down into a fresh repo (default ~/.config/ferry/repo), git-inits it, writes
ferry's machine config, and stops. It REFUSES a non-empty target (no clobber).
Pass --expect-sha256 <hash> to verify the bundle against the SHA256 export
printed. Run "ferry apply" afterwards to reconcile this machine.`,
	Args: cobra.ExactArgs(1),
	RunE: runImport,
}

// runImport validates a bundle and, only if every check passes, ingests it into a
// fresh config repo. The write flow makes the target ALL-OR-NOTHING: bundle.Validate
// (all validation, nothing written) → no-clobber check on the final target →
// Validated.Extract into a fresh ferry-owned staging dir → (only when the bundle
// carried NO .gitignore) ensure the local layer is gitignored + write a
// ferry.local.toml template → git init + initial commit, ALL INSIDE the staging dir →
// os.Rename the finished staging tree onto the (absent) target as the last, atomic
// step → write ~/.config/ferry/config.toml (rolling the target back if that fails).
// The bundled/shared tree is laid down BYTE-IDENTICAL — import never appends to an
// imported .gitignore — so export→import→re-export is byte-identical. Because every
// repo-shaping step happens before the rename, a git/gitignore/template failure cleans
// up via the staging cleanup and the target never half-appears. It does NOT run apply.
func runImport(c *cobra.Command, args []string) error {
	if err := preflightGit(); err != nil {
		return err
	}
	out := c.OutOrStdout()

	bundlePath := args[0]
	includeLocal, _ := c.Flags().GetBool("include-local")
	expectSHA, _ := c.Flags().GetString("expect-sha256")
	outDir, _ := c.Flags().GetString("out")

	// The bundle file must never be READ from under ~/.ssh: refuse a bundle path that
	// resolves there (or via a symlink that escapes there) BEFORE it is opened. Expand
	// a leading ~ first so the guard sees the real path. This mirrors export's --out
	// guard, keeping the whole bundle file hands-off with ~/.ssh on both sides.
	expandedBundle, err := expandUser(bundlePath)
	if err != nil {
		return err
	}
	absBundle, err := filepath.Abs(expandedBundle)
	if err != nil {
		return fmt.Errorf("resolve bundle path %q: %w", bundlePath, err)
	}
	if _, err := guardRepoPath("bundle", absBundle); err != nil {
		return err
	}
	bundlePath = absBundle

	// Resolve the final target BEFORE validation so its parent exists for a
	// same-filesystem staging→rename move.
	target, err := resolveImportTarget(outDir)
	if err != nil {
		return err
	}
	// Guard the target against ~/.ssh and harden its config-dir chain (the default
	// lives under ~/.config/ferry): bundle content is only ever written here.
	if _, err := guardRepoPath("import target", target); err != nil {
		return err
	}
	if isDefaultImportTarget(outDir) {
		if err := hardenConfigDirForRepo(target); err != nil {
			return err
		}
	}

	// No-clobber (N3): refuse a target that exists and is non-empty. Checked before
	// extraction so a hostile bundle never lands, and before the rename would clobber.
	if !dirEmptyOrAbsent(target) {
		return fmt.Errorf("import target %q already exists and is not empty — refusing to overwrite (choose an empty --out, or remove it first)", target)
	}
	// If the target is an EMPTY directory, clear it now (early, having just confirmed
	// it is empty) so the move-into-place step below can require an ABSENT target and
	// never delete anything at move time. os.Remove refuses a non-empty dir, so if it
	// raced non-empty between the check and here we abort without clobbering.
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("import target %q became non-empty — refusing to overwrite: %w", target, err)
	}

	// FULL validation — nothing is written anywhere on any error.
	validated, err := bundle.Validate(bundlePath, expectSHA, includeLocal)
	if err != nil {
		return err
	}
	defer validated.Close()

	// Extract into a FRESH ferry-owned staging dir under the target's parent, so the
	// follow-up move is a same-filesystem rename.
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("prepare import target parent: %w", err)
	}
	staging, err := validated.Extract(parent)
	if err != nil {
		return err
	}
	// From here, any failure must remove the staging dir (all-or-nothing).
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	// Shape the repo ENTIRELY INSIDE the staging dir, BEFORE the rename: (only when the
	// bundle carried NO .gitignore) ensure the local layer is gitignored, and (unless
	// the bundle's own local layer was imported) write the ferry.local.toml template,
	// THEN git init + add + commit. Any failure here returns with cleanupStaging still
	// true → the staging tree is removed and the target never appears.

	// Do NOT mutate a bundled/shared .gitignore: the shared tree must be laid down
	// BYTE-IDENTICAL so export→import→re-export is byte-identical (no .gitignore drift,
	// no newly-tracked .gitignore appearing). The source repo already gitignores the
	// local layer by design (it is gitignored there), so a bundled .gitignore already
	// covers it. Only when the bundle carried NO .gitignore at all (a genuinely fresh
	// repo) do we create one to keep the machine-local layer ignored — and only then,
	// so we never append to an imported .gitignore.
	if !bundleCarried(validated, ".gitignore") {
		if err := ensureLocalLayerIgnored(staging); err != nil {
			return err
		}
	}

	// Create a ferry.local.toml TEMPLATE unless the bundle's own local layer was
	// imported (double opt-in: the bundle declared include_local AND import asked for
	// it). When local content was imported, ferry.local.toml is bundle content and
	// must not be overwritten by a template. Written before the commit so it, too, is
	// part of the initial tree.
	importedLocal := validated.IncludeLocal && includeLocal
	if !importedLocal {
		// Write into staging but SUPPRESS the helper's stdout: it would name the
		// temporary staging path, not the final target. A clean, target-referenced
		// line is printed after the rename succeeds.
		if err := ensureLocalManifest(io.Discard, staging); err != nil {
			return err
		}
	}

	// git init + initial commit so the tree is a real committable working tree
	// (ferry re-inits git itself, AFTER validation — never from bundle contents).
	if outStr, gerr := runGitIn(staging, "init"); gerr != nil {
		return fmt.Errorf("git init failed: %w\n%s", gerr, outStr)
	}
	for _, gargs := range [][]string{
		{"add", "-A"},
		{"-c", "user.name=ferry", "-c", "user.email=ferry@localhost", "commit", "-m", "ferry import: ingest bundle"},
	} {
		if cout, gerr := runGitIn(staging, gargs...); gerr != nil {
			return fmt.Errorf("git %s failed: %w\n%s", strings.Join(gargs, " "), gerr, cout)
		}
	}

	// Move the staged tree into place. The target must be ABSENT at move time: ferry
	// extracts to `staging` (not target) and only ever created target's PARENT, so in
	// the normal flow target does not exist here. We do NOT remove anything at target
	// (os.Remove would delete a raced-in file/symlink unconditionally — a TOCTOU
	// clobber). Instead we require absence and let os.Rename create target. If ANYTHING
	// exists at target (raced in during validation/extract, or a leftover), we refuse
	// rather than delete it — the user clears it explicitly. This has no check-then-
	// delete window: there is no delete.
	if _, lerr := os.Lstat(target); lerr == nil {
		return fmt.Errorf("import target %q exists — refusing to overwrite (remove it first, or choose an empty --out)", target)
	} else if !os.IsNotExist(lerr) {
		return fmt.Errorf("stat import target %q: %w", target, lerr)
	}
	if err := os.Rename(staging, target); err != nil {
		return fmt.Errorf("move imported files into place: %w", err)
	}
	cleanupStaging = false // the staging dir IS the target now

	// The one ferry-owned write left after the rename: the machine config
	// (~/.config/ferry/config.toml → target repo). If it fails the target is a
	// freshly-created, ferry-owned repo with no prior contents (it was absent/empty
	// before this import), so rolling it back is safe and leaves a re-runnable state.
	hostname, herr := os.Hostname()
	if herr != nil || strings.TrimSpace(hostname) == "" {
		hostname = "unknown"
	}
	if err := config.SaveMachineConfig(config.MachineConfig{Hostname: hostname, Repo: target}); err != nil {
		_ = os.RemoveAll(target) // roll the just-placed target back so a re-run is clean
		return fmt.Errorf("write machine config: %w", err)
	}

	fmt.Fprintf(out, "imported bundle into %s\n", target)
	if !importedLocal {
		fmt.Fprintf(out, "created per-machine manifest: %s (commented template; gitignored)\n", filepath.Join(target, config.LocalManifestName))
	}
	fmt.Fprintf(out, "wrote ferry config (repo: %s)\n", target)
	fmt.Fprintln(out, "next: run `ferry apply` to reconcile this machine.")
	return nil
}

// bundleCarried reports whether the validated bundle includes a payload entry at the
// given canonical (forward-slash) relative path. Used to decide whether import may
// touch .gitignore: if the bundle carried one, it is laid down byte-identical and
// never mutated (round-trip byte-identity).
func bundleCarried(v *bundle.Validated, rel string) bool {
	for _, e := range v.Entries {
		if e.Path == rel {
			return true
		}
	}
	return false
}

// resolveImportTarget returns the final target directory for an import: the
// explicit --out (expanded, absolutised) when given, else ferry's neutral default
// ~/.config/ferry/repo.
func resolveImportTarget(outDir string) (string, error) {
	if strings.TrimSpace(outDir) != "" {
		expanded, err := expandUser(outDir)
		if err != nil {
			return "", err
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			return "", fmt.Errorf("resolve --out %q: %w", outDir, err)
		}
		return filepath.Clean(abs), nil
	}
	return defaultRepoDir()
}

// isDefaultImportTarget reports whether the import target is ferry's default under
// ~/.config/ferry (so its config-dir chain is hardened, mirroring init).
func isDefaultImportTarget(outDir string) bool {
	return strings.TrimSpace(outDir) == ""
}

func init() {
	importCmd.Flags().String("out", "", "target repo directory (default ~/.config/ferry/repo; must be empty/absent)")
	importCmd.Flags().String("expect-sha256", "", "verify the bundle's overall SHA256 before importing (out-of-band tamper check)")
	importCmd.Flags().Bool("include-local", false, "also import the bundle's local layer (only if it was exported --include-local)")
}
