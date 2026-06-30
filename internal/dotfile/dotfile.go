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

// Target is a single declared dotfile: the repo-side source path and the home
// destination it materializes to. The name (e.g. "zshrc") is the bare key under
// the repo's dotfiles/ directory.
type Target struct {
	Name string // bare key, e.g. "zshrc"
	Repo string // absolute repo-side source path, e.g. <repo>/dotfiles/zshrc
	Home string // absolute home destination, e.g. ~/.zshrc
}

// defaultPerm is the file mode used when materializing a target whose home
// destination does not already exist. An existing destination's mode is
// preserved by the Backuper's prior-state capture, so this only governs a
// first-ever write.
const defaultPerm os.FileMode = 0o644

// RepoSubdir is the repo subdirectory that holds dotfile sources.
const RepoSubdir = "dotfiles"

// TargetFor builds the repo<->home mapping for a single dotfile name, given the
// repo root and the home directory.
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
// differently-named home path construct a Target directly.
func TargetFor(repoRoot, home, name string) Target {
	bare := strings.TrimPrefix(name, ".")
	return Target{
		Name: bare,
		Repo: filepath.Join(repoRoot, RepoSubdir, bare),
		Home: filepath.Join(home, "."+bare),
	}
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
