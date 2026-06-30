package dotfile

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/REPPL/ferry/internal/paths"
)

// stateFileName is the per-machine last-applied record, kept under ferry's
// state dir (NOT in the repo): it records, per managed target, the content hash
// of the bytes ferry last wrote to the home destination. It is the "middle"
// term of the three-way comparison — the boundary between repo and live that
// tells local edits apart from a repo-ahead update.
const stateFileName = "dotfile-last-applied.json"

// dirPerm / filePerm mirror the secret-bearing-by-default posture of the rest
// of ferry's state store: the last-applied record names managed dotfiles, so it
// is owner-only.
const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// Store is the persisted last-applied map (target name -> content hash). The
// zero value is not usable; build it with OpenStore (real state dir) or
// OpenStoreAt (an explicit dir, for tests with t.TempDir()).
type Store struct {
	path    string
	applied map[string]string // target name -> last-applied content hash
}

// OpenStore opens the last-applied store under ferry's real state dir
// (~/.local/state/ferry), resolved via internal/paths.
func OpenStore() (*Store, error) {
	dir, err := paths.StateDir()
	if err != nil {
		return nil, err
	}
	return OpenStoreAt(dir)
}

// OpenStoreAt opens the last-applied store under an explicit state dir. Tests
// pass a t.TempDir() so the real ~ is never touched. A missing file is an empty
// store, not an error (first run on a machine).
func OpenStoreAt(stateDir string) (*Store, error) {
	if stateDir == "" {
		return nil, errors.New("dotfile: empty state dir")
	}
	if err := os.MkdirAll(stateDir, dirPerm); err != nil {
		return nil, err
	}
	if err := os.Chmod(stateDir, dirPerm); err != nil {
		return nil, err
	}
	s := &Store{
		path:    filepath.Join(stateDir, stateFileName),
		applied: map[string]string{},
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.applied); err != nil {
		return nil, err
	}
	if s.applied == nil {
		s.applied = map[string]string{}
	}
	return s, nil
}

// LastApplied returns the recorded last-applied hash for a target name. ok is
// false when the target has never been applied on this machine — the
// "unmanaged" case the conflict rules treat specially.
func (s *Store) LastApplied(name string) (hash string, ok bool) {
	h, ok := s.applied[name]
	return h, ok
}

// set records a new last-applied hash for a target and persists the store.
func (s *Store) set(name, hash string) error {
	s.applied[name] = hash
	return s.save()
}

// save atomically rewrites the store file (temp + rename), 0600.
func (s *Store) save() error {
	data, err := json.MarshalIndent(s.applied, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".dotfile-state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed away
	if err := tmp.Chmod(filePerm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}
