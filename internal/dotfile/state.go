package dotfile

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/REPPL/ferry/internal/paths"
	"github.com/REPPL/ferry/internal/statefile"
)

// lastAppliedVersion is the current on-disk schema version of the last-applied
// store. Version 1 is the first versioned form: a JSON envelope carrying this
// integer plus the applied map. A file with no "version" field is the original
// v0.3.x form (a bare name->hash map) and reads as version 1, migrated forward on
// the next mutating open. See internal/statefile for the shared version contract.
const lastAppliedVersion = 1

// lastAppliedFile is the versioned on-disk envelope for the last-applied store.
type lastAppliedFile struct {
	Version int               `json:"version"`
	Applied map[string]string `json:"applied"`
}

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
//
// A store opened read-only (OpenStoreReadOnly / OpenStoreAtReadOnly) has
// readOnly=true: it never creates the state dir and refuses to persist. This is
// the path status/diff use so that read-only commands create no ferry state.
type Store struct {
	path     string
	applied  map[string]string // target name -> last-applied content hash
	readOnly bool              // true for a status/diff store: no mkdir, no save
}

// OpenStore opens the last-applied store under ferry's real state dir
// (~/.local/state/ferry), resolved via internal/paths. It creates the state dir
// if absent (the mutating apply/capture path); use OpenStoreReadOnly for the
// write-free status/diff path.
func OpenStore() (*Store, error) {
	dir, err := paths.StateDir()
	if err != nil {
		return nil, err
	}
	return OpenStoreAt(dir)
}

// OpenStoreReadOnly opens the last-applied store under ferry's real state dir
// WITHOUT creating it. It is the path read-only commands (status, diff) use:
// classification needs the last-applied hashes but must not create
// ~/.local/state/ferry. An absent state dir or record yields an empty store
// (every target reads as first-touch), not an error. A read-only store refuses
// to persist (set/CommitLastApplied return an error), so it can never mutate state.
func OpenStoreReadOnly() (*Store, error) {
	dir, err := paths.StateDir()
	if err != nil {
		return nil, err
	}
	return OpenStoreAtReadOnly(dir)
}

// OpenStoreAt opens the last-applied store under an explicit state dir, creating
// it if absent. Tests pass a t.TempDir() so the real ~ is never touched. A
// missing file is an empty store, not an error (first run on a machine).
func OpenStoreAt(stateDir string) (*Store, error) {
	return openStoreAt(stateDir, false)
}

// OpenStoreAtReadOnly opens the last-applied store under an explicit state dir
// WITHOUT creating it (the read-only status/diff path). Tests pass a t.TempDir().
// A missing dir or file yields an empty store; the store refuses to persist.
func OpenStoreAtReadOnly(stateDir string) (*Store, error) {
	return openStoreAt(stateDir, true)
}

func openStoreAt(stateDir string, readOnly bool) (*Store, error) {
	if stateDir == "" {
		return nil, errors.New("dotfile: empty state dir")
	}
	// Symlink-harden the state dir BEFORE any mkdir/read/write/rename — for the
	// read-only path too. HardenStoreDir refuses if any component from $HOME down
	// to the state dir is a symlink, so neither a status/diff read (OpenStoreReadOnly,
	// reached from buildPlan, terminal HasBaselineReadOnly, restore baseline check)
	// nor an apply/capture write can read or write through a ~/.local/state/ferry that
	// has been symlinked into ~/.ssh or a system path. The check is lexical, creates
	// no dirs, never touches ~/.ssh, and a test store rooted at t.TempDir() (not under
	// $HOME) is a no-op, so the explicit-root test constructors keep working.
	if err := paths.HardenStoreDir(stateDir); err != nil {
		return nil, err
	}
	if !readOnly {
		// Mutating path: ensure the owner-only state dir exists before we may
		// persist into it. The read-only path skips this so status/diff create
		// no ferry state — an absent dir simply reads as "no last-applied records".
		if err := os.MkdirAll(stateDir, dirPerm); err != nil {
			return nil, err
		}
		if err := os.Chmod(stateDir, dirPerm); err != nil {
			return nil, err
		}
	}
	s := &Store{
		path:     filepath.Join(stateDir, stateFileName),
		applied:  map[string]string{},
		readOnly: readOnly,
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	// Resolve the on-disk schema version. A file a newer ferry wrote is refused
	// here (FutureVersionError) BEFORE any decode or write, so a downgraded ferry
	// leaves it untouched rather than corrupting it.
	version, migrate, err := statefile.Resolve(s.path, data, lastAppliedVersion)
	if err != nil {
		return nil, err
	}
	if migrate {
		// The original v0.3.x form is a bare name->hash map: decode it directly.
		if err := json.Unmarshal(data, &s.applied); err != nil {
			return nil, err
		}
		// Migrate-on-read, but only on the mutating path: back the pre-migration
		// file up first, then rewrite it in the current versioned envelope form.
		// The read-only status/diff path decodes in memory and writes nothing.
		if !readOnly {
			if _, err := statefile.BackupForMigration(s.path, version); err != nil {
				return nil, err
			}
			if err := s.save(); err != nil {
				return nil, err
			}
		}
	} else {
		var env lastAppliedFile
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, err
		}
		s.applied = env.Applied
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

// RecordedNames returns the bare names of every dotfile with a last-applied
// record on this machine — the keys of the store's applied map — sorted for
// determinism. An empty or absent store yields an empty slice (not nil-vs-len
// confusion is irrelevant to callers, but it is never an error). It works on
// both the normal and read-only store: enumeration is a pure read.
func (s *Store) RecordedNames() []string {
	names := make([]string, 0, len(s.applied))
	for name := range s.applied {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// set records a new last-applied hash for a target and persists the store. It
// errors on a read-only store (status/diff), which must never mutate state.
func (s *Store) set(name, hash string) error {
	if s.readOnly {
		return errors.New("dotfile: cannot persist last-applied on a read-only store")
	}
	s.applied[name] = hash
	return s.save()
}

// save atomically rewrites the store file (temp + rename), 0600, in the current
// versioned envelope form.
func (s *Store) save() error {
	data, err := json.MarshalIndent(lastAppliedFile{Version: lastAppliedVersion, Applied: s.applied}, "", "  ")
	if err != nil {
		return err
	}
	// Re-harden at the lowest write layer so no future caller of save() can write
	// into a state dir that became a symlink after open. No-op for a t.TempDir().
	if err := paths.HardenStoreDir(filepath.Dir(s.path)); err != nil {
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
