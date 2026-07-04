package dotfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	// (e.g. .gitconfig) has NO safe merge point, so `.local` is WHOLE-FILE: a
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
	// reads this to choose the overlay path; ApplyWholeFileOverlay implements the
	// whole-file-replace path in this package.
	Overlay OverlayMode
}

// defaultPerm is the file mode used when materializing a target whose home
// destination does not already exist. An existing destination's mode is
// preserved by the Backuper's prior-state capture, so this only governs a
// first-ever write.
const defaultPerm os.FileMode = 0o644

// RepoSubdir is the repo subdirectory that holds dotfile sources.
const RepoSubdir = "dotfiles"

// sshDirName is the one directory ferry must NEVER manage: the absolute,
// most-important security contract is that ferry never reads, copies, captures,
// or modifies anything under ~/.ssh/. TargetFor is the single enforcement point
// for that hands-off contract — see ErrForbiddenSSHPath.
const sshDirName = ".ssh"

// ErrForbiddenSSHPath is returned by TargetFor when a declared dotfile name
// resolves to a home target that IS, or is under, ~/.ssh/. ferry's top security
// invariant is that it never touches ~/.ssh/, and this is the boundary that
// makes a Target under ~/.ssh/ impossible to construct: apply/capture/status all
// route through TargetFor, so a manifest declaring `.ssh/config` or
// `.ssh/id_ed25519` is refused here, before any read/back-up/write can happen.
var ErrForbiddenSSHPath = errors.New("dotfile: refusing a target under ~/.ssh (ferry never touches ~/.ssh)")

// ErrPathEscapesHome is returned by TargetFor when a declared dotfile name does
// not resolve to a path strictly within $HOME — an absolute path, or one that
// climbs out via `..`. A managed dotfile must live inside $HOME; anything else
// is a path-traversal attempt and is refused before a Target is produced.
var ErrPathEscapesHome = errors.New("dotfile: dotfile name escapes $HOME")

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
// authored WITH a leading dot per docs/configuration.md (`dotfiles = [".zshrc"]`),
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
	if err := validateHomeTarget(home, homeDest); err != nil {
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
// It is the SAME security boundary as TargetFor: the resolved destination is
// validated by validateHomeTarget, so an absolute rel, a `..` climb out of
// $HOME, or anything at/under ~/.ssh is refused before a Target exists. The
// returned Target has no repo source (callers supply effective content in
// memory via ClassifyContent) and defaults Overlay to OverlayWholeFileReplace.
func NestedTarget(home, rel, name string) (Target, error) {
	if filepath.IsAbs(rel) {
		return Target{}, ErrPathEscapesHome
	}
	dest := filepath.Join(home, rel)
	if err := validateHomeTarget(home, dest); err != nil {
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

// validateHomeTarget enforces the two boundary rules on a resolved home target:
// it must sit strictly within $HOME (no `..` climb, no absolute escape) and must
// NOT be ~/.ssh or anything under it. home and dest are both cleaned before
// comparison so `.ssh/../.ssh/config`-style tricks cannot slip through.
func validateHomeTarget(home, dest string) error {
	cleanHome := filepath.Clean(home)
	cleanDest := filepath.Clean(dest)

	// Must be strictly within $HOME: the path from HOME to dest may not start
	// with "..", and dest may not equal HOME itself.
	rel, err := filepath.Rel(cleanHome, cleanDest)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrPathEscapesHome
	}

	// Must not be ~/.ssh nor under it. Compare the first path segment of the
	// HOME-relative path: ".ssh" itself, or ".ssh/<anything>".
	first := rel
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		first = rel[:i]
	}
	if first == sshDirName {
		return ErrForbiddenSSHPath
	}
	return nil
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
