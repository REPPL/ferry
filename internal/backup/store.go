package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/REPPL/ferry/internal/paths"
)

// Permission constants for the secret-bearing state store. Directories are
// owner-only (0700) and stored payloads owner-only (0600); we never loosen
// these and we preserve a STRICTER original mode when one was recorded.
const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// Engine is a transactional backup/restore engine bound to one state store.
//
// The zero value is not usable; construct with New (real ~/.local/state/ferry)
// or NewAt (an explicit root, used by tests with t.TempDir()).
type Engine struct {
	root        string // state dir root (paths.StateDir or a test temp dir)
	baselineDir string
	journalDir  string
	snapshotDir string
	lockPath    string
	resources   map[string]Resource // domain -> plist-style resource hook
}

// New builds an Engine rooted at ferry's real state directory
// (~/.local/state/ferry), resolved via internal/paths.
func New() (*Engine, error) {
	root, err := paths.StateDir()
	if err != nil {
		return nil, err
	}
	return NewAt(root)
}

// NewAt builds an Engine rooted at an explicit state directory. Tests pass a
// t.TempDir() so the real ~ is never touched. The layout mirrors internal/paths
// (baseline/, journal/, lock) and adds snapshots/ for pre-restore snapshots.
func NewAt(root string) (*Engine, error) {
	if root == "" {
		return nil, fmt.Errorf("backup: empty state root")
	}
	e := &Engine{
		root:        root,
		baselineDir: filepath.Join(root, "baseline"),
		journalDir:  filepath.Join(root, "journal"),
		snapshotDir: filepath.Join(root, "snapshots"),
		lockPath:    filepath.Join(root, "lock"),
		resources:   map[string]Resource{},
	}
	// Create the store eagerly with 0700 so secrets never land in a
	// world-readable directory even momentarily.
	for _, d := range []string{e.root, e.baselineDir, filepath.Join(e.baselineDir, "blobs"), e.journalDir, e.snapshotDir} {
		if err := os.MkdirAll(d, dirPerm); err != nil {
			return nil, err
		}
		// MkdirAll honours umask, which may have widened the mode; force 0700.
		if err := os.Chmod(d, dirPerm); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// Register attaches a plist-style Resource for the named domain so scoped
// restore and apply can drive its own transaction. Wave-2 domain code calls
// this; the file engine itself never needs it.
func (e *Engine) Register(r Resource) {
	e.resources[r.Domain()] = r
}

// keyFor maps an absolute path to a stable, collision-resistant store key.
// We hash the cleaned absolute path: filenames in the store must not depend on
// the original path's own separators or length, and two different paths must
// never share a key. The original path is recorded inside the entry metadata.
func keyFor(absPath string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(absPath)))
	return hex.EncodeToString(sum[:])
}

// effectiveMode returns the mode to store a payload under: never looser than
// 0600, but PRESERVING a stricter original (e.g. a 0400 read-only secret).
func effectiveMode(orig os.FileMode) os.FileMode {
	perm := orig.Perm()
	// Keep only the bits that are also set in 0600 — i.e. drop group/other and
	// any execute bit, yielding a mode no looser than 0600 but as strict as the
	// original owner bits were (0400 stays 0400, 0600 stays 0600).
	return perm & filePerm
}
