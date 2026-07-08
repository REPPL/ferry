package dotfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/REPPL/ferry/internal/sshguard"
)

// Backuper is the small slice of the backup engine the dotfile domain depends
// on for writing. The concrete *backup.Engine is wired to this in Wave 2; until
// then tests use a fake. BackupAndWrite must snapshot the prior state of the
// target (baseline-if-first + journal) and then write content atomically
// (temp + rename) with mode perm. It is the ONLY way this domain mutates the
// home target, so every overwrite is reversible.
type Backuper interface {
	BackupAndWrite(target string, content []byte, perm os.FileMode) error
}

// OverlayMode is how a domain's per-machine `.local` overlay composes onto the
// shared content (PLAN.md "Per-domain overlay strategy"). It tells the apply
// command which overlay path a target takes; the dotfile domain itself never
// composes the overlay (that is the cmd/apply.go owner's job), it only reports
// the safe mode for each target.
type OverlayMode string

const (
	// OverlayIncludeSidecar: the domain has a real include/append point (zsh:
	// shared ~/.zshrc `source`s ~/.zshrc.local last). The shared file is always
	// deployed, and the per-machine overlay is materialized as a SEPARATE sidecar
	// home file (~/.<bare>.local) that the shared file pulls in. Hunk-level
	// `[l]ocal` routing is allowed.
	OverlayIncludeSidecar OverlayMode = "include-sidecar"
	// OverlayWholeFileReplace: a generic dotfile WITHOUT an include mechanism
	// (e.g. .vimrc) has NO safe merge point, so `.local` is WHOLE-FILE: a
	// per-machine full copy in local/<domain>/ is deployed INSTEAD OF the shared
	// content (local wins). When no local copy exists the shared content is
	// deployed. Hunk-level `[l]ocal` routing is DISALLOWED for these.
	OverlayWholeFileReplace OverlayMode = "whole-file-replace"
)

// Target is a single declared dotfile: the repo-side source path and the home
// destination it materializes to. The name (e.g. "zshrc") is the bare key under
// the repo's dotfiles/ directory.
type Target struct {
	Name string // bare key, e.g. "zshrc"
	Repo string // absolute repo-side source path, e.g. <repo>/dotfiles/zshrc
	Home string // absolute home destination, e.g. ~/.zshrc

	// Overlay is the per-machine `.local` overlay mode for this target's domain
	// (PLAN.md "Per-domain overlay strategy"). TargetFor defaults it to
	// OverlayWholeFileReplace — the safe default for a GENERIC dotfile with no
	// include point: there is no merge point, so the local copy (if any) replaces
	// the shared file. An include-style domain (zsh) is built with
	// IncludeSidecarTarget so its overlay is a sidecar instead. The apply command
	// reads this to choose the overlay path, composing the local-vs-shared
	// whole-file choice into the effective content it deploys via
	// ApplyContentDeferred.
	Overlay OverlayMode
}

// defaultPerm is the file mode used when materializing a target whose home
// destination does not already exist. An existing destination's mode is
// preserved by the Backuper's prior-state capture, so this only governs a
// first-ever write.
const defaultPerm os.FileMode = 0o644

// DefaultPerm is the fresh-write file mode for a plain dotfile whose home
// destination does not yet exist (0644). Callers that drive ApplyContentDeferred
// with in-memory content (no repo file, so no on-disk mode to preserve) pass it
// as the first-ever-write mode, matching the file-based apply's own default.
func DefaultPerm() os.FileMode { return defaultPerm }

// RepoSubdir is the repo subdirectory that holds dotfile sources.
const RepoSubdir = "dotfiles"

// ErrForbiddenSSHPath is returned by TargetFor when a declared dotfile name
// resolves to a home target that IS, or is under, ~/.ssh/. ferry's top security
// invariant is that it never touches ~/.ssh/, and this is the boundary that
// makes a Target under ~/.ssh/ impossible to construct: apply/capture/status all
// route through TargetFor, so a manifest declaring `.ssh/config` or
// `.ssh/id_ed25519` is refused here, before any read/back-up/write can happen.
// It aliases sshguard.ErrForbiddenSSHPath so the lexical and resolved guards
// share one sentinel identity across the dotfile and backup boundaries.
var ErrForbiddenSSHPath = sshguard.ErrForbiddenSSHPath

// ErrPathEscapesHome is returned by TargetFor when a declared dotfile name does
// not resolve to a path strictly within $HOME — an absolute path, or one that
// climbs out via `..`. A managed dotfile must live inside $HOME; anything else
// is a path-traversal attempt and is refused before a Target is produced. It
// aliases sshguard.ErrPathEscapesHome so the lexical and resolved guards share
// one sentinel identity across the dotfile and backup boundaries.
var ErrPathEscapesHome = sshguard.ErrPathEscapesHome

// TargetFor builds the repo<->home mapping for a single dotfile name, given the
// repo root and the home directory. It is the SECURITY BOUNDARY for the
// "ferry never touches ~/.ssh" contract and for path-traversal: it refuses any
// name whose resolved home target is ~/.ssh itself or under it
// (ErrForbiddenSSHPath), and any name that does not resolve strictly within
// $HOME (ErrPathEscapesHome) — an absolute path or a `..` climb. Because
// apply/capture/status all obtain their Target through TargetFor, a Target under
// ~/.ssh or outside $HOME is IMPOSSIBLE to construct.
//
// Name convention (the single contract between config and this domain): the
// canonical internal name is the BARE name (e.g. "zshrc"). The manifest is
// authored WITH a leading dot per docs/reference/configuration.md (`dotfiles = [".zshrc"]`),
// so TargetFor normalizes at the boundary by stripping a single leading dot.
// Both ".zshrc" and "zshrc" therefore map to the same target:
//
//	repo source:      <repo>/dotfiles/zshrc   (stored dot-less for cleaner listings)
//	home destination: <home>/.zshrc           (the leading dot ferry re-adds)
//
// This keeps the repo layout `dotfiles/zshrc` and never produces `dotfiles/.zshrc`
// or a double-dotted `~/..zshrc`. Callers that need a non-dotted or
// differently-named home path construct a Target directly (and are themselves
// responsible for honouring the ~/.ssh contract).
//
// The returned Target defaults Overlay to OverlayWholeFileReplace, the safe mode
// for a generic dotfile with no include point. Use IncludeSidecarTarget for an
// include-style domain (zsh).
func TargetFor(repoRoot, home, name string) (Target, error) {
	// An absolute declared name is a traversal attempt: a dotfile must be a
	// relative name under $HOME, never an absolute path.
	if filepath.IsAbs(name) {
		return Target{}, ErrPathEscapesHome
	}
	bare := strings.TrimPrefix(name, ".")

	// Resolve the home destination the same way it materializes (<home>/.bare),
	// then validate the CLEANED path so `..` and absolute names cannot escape.
	homeDest := filepath.Join(home, "."+bare)
	if err := ValidateHomeTarget(home, homeDest); err != nil {
		return Target{}, err
	}
	// Beyond the LEXICAL guard, re-run the symlink-RESOLVING containment check:
	// a nested name (e.g. ".config/foo") sits under intermediate directories, and
	// a symlinked intermediate (~/.config -> outside $HOME, or routed into ~/.ssh)
	// would let a lexically-valid write land through the link. NestedTarget does
	// this too; a flat dotfile whose only parent is $HOME short-circuits cleanly.
	if err := validateHomeTargetResolved(home, homeDest); err != nil {
		return Target{}, err
	}

	return Target{
		Name:    bare,
		Repo:    filepath.Join(repoRoot, RepoSubdir, bare),
		Home:    homeDest,
		Overlay: OverlayWholeFileReplace,
	}, nil
}

// NestedTarget builds a Target for a home-RELATIVE destination path that does
// NOT follow the dotfile "."-prefix convention — a nested path such as
// ".codex/AGENTS.md" or "<devtree>/CLAUDE.md". name is the caller's stable
// last-applied store key (e.g. "agents/codex"); rel is the destination path
// relative to home.
//
// It is the SAME security boundary as TargetFor — ValidateHomeTarget refuses
// an absolute rel, a `..` climb out of $HOME, and anything at/under ~/.ssh —
// PLUS a symlink-RESOLVING containment check: unlike a flat dotfile (whose
// only parent is $HOME itself), a nested destination sits under intermediate
// directories, and a symlinked intermediate (~/.claude -> /elsewhere, or a
// harness path routed through ~/w -> ~/.ssh) would let a lexically-valid
// write land outside $HOME or inside ~/.ssh. validateHomeTargetResolved walks
// the existing components and refuses any that resolve out. The returned
// Target has no repo source (callers supply effective content in memory via
// ClassifyContent) and defaults Overlay to OverlayWholeFileReplace.
func NestedTarget(home, rel, name string) (Target, error) {
	if filepath.IsAbs(rel) {
		return Target{}, ErrPathEscapesHome
	}
	dest := filepath.Join(home, rel)
	if err := ValidateHomeTarget(home, dest); err != nil {
		return Target{}, err
	}
	if err := validateHomeTargetResolved(home, dest); err != nil {
		return Target{}, err
	}
	return Target{
		Name:    name,
		Home:    dest,
		Overlay: OverlayWholeFileReplace,
	}, nil
}

// IncludeSidecarTarget is TargetFor for an include-style domain (zsh): the same
// ~/.ssh + traversal validation, but the Target's Overlay is
// OverlayIncludeSidecar so the apply command materializes the overlay as a
// sidecar (~/.<bare>.local) alongside the always-deployed shared file rather
// than replacing it.
func IncludeSidecarTarget(repoRoot, home, name string) (Target, error) {
	t, err := TargetFor(repoRoot, home, name)
	if err != nil {
		return Target{}, err
	}
	t.Overlay = OverlayIncludeSidecar
	return t, nil
}

// ValidateHomeTarget enforces the two LEXICAL boundary rules on a resolved home
// target: it must sit strictly within $HOME (no `..` climb, no absolute escape)
// and must NOT be ~/.ssh or anything under it. home and dest are both cleaned
// before comparison so `.ssh/../.ssh/config`-style tricks cannot slip through.
// It is the single lexical containment validator; adopt's bridge scan and the
// nested-target boundary both build on it. It does NOT resolve symlinks — that
// is validateHomeTargetResolved's job.
func ValidateHomeTarget(home, dest string) error {
	cleanHome := filepath.Clean(home)
	cleanDest := filepath.Clean(dest)

	// Must be strictly within $HOME: the path from HOME to dest may not start
	// with "..", and dest may not equal HOME itself.
	rel, err := filepath.Rel(cleanHome, cleanDest)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrPathEscapesHome
	}

	// Must not be ~/.ssh nor under it. Compare the first path segment of the
	// HOME-relative path: ".ssh" itself, or ".ssh/<anything>". The compare is
	// case-INSENSITIVE because the macOS default filesystem is: a target like
	// ".SSH/config" would otherwise pass this lexical guard yet be mapped by
	// the kernel into ~/.ssh. Folding also refuses ".SSH/..." on a
	// case-sensitive filesystem — acceptable fail-closed behaviour, since a
	// dotfile genuinely named ".SSH" is pathological.
	if sshguard.FirstSegmentIsSSH(rel) {
		return ErrForbiddenSSHPath
	}
	return nil
}

// validateHomeTargetResolved is the dotfile boundary's alias for the shared
// symlink-RESOLVING containment guard (internal/sshguard.ResolvedContainment):
// beyond the LEXICAL ValidateHomeTarget, it walks the EXISTING components
// between $HOME and dest's PARENT and refuses any that resolve outside $HOME
// (ErrPathEscapesHome) or at/under ~/.ssh (ErrForbiddenSSHPath). The SAME guard
// runs at the backup write/restore boundaries, so a symlinked intermediate
// parent cannot redirect a write no matter which path reaches it.
func validateHomeTargetResolved(home, dest string) error {
	return sshguard.ResolvedContainment(home, dest)
}

// hashBytes returns the lowercase hex sha256 of content. This is the canonical
// content hash used for the three-way comparison and for the last-applied
// record.
func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// hashFile returns the content hash of the file at path. A missing file yields
// ("", false, nil) — absence is a normal three-way input, not an error. A path
// that exists but is not a regular file (e.g. a symlink or directory) is an
// error: the dotfile domain materializes regular-file copies only, and a
// symlink at the home target is exactly the unsafe state copy-not-symlink
// exists to avoid.
func hashFile(path string) (hash string, exists bool, err error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return "", true, &UnexpectedKindError{Path: path, Mode: fi.Mode()}
	}
	if !fi.Mode().IsRegular() {
		return "", true, &UnexpectedKindError{Path: path, Mode: fi.Mode()}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", true, err
	}
	return hashBytes(content), true, nil
}

// UnexpectedKindError is returned when a home target is present but is not a
// regular file (a symlink or directory). The dotfile domain refuses to treat
// such a path as managed content.
type UnexpectedKindError struct {
	Path string
	Mode os.FileMode
}

func (e *UnexpectedKindError) Error() string {
	return "dotfile: unexpected path kind for " + e.Path + " (" + e.Mode.String() + ")"
}
