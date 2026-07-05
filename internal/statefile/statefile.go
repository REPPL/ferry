// Package statefile provides the on-disk schema-versioning contract shared by
// ferry's own persisted state files: the dotfile last-applied store, the agents
// target record, and the backup journal manifest. Each such file is a JSON
// object carrying an integer "version" at its top level. This package supplies
// the three primitives every versioned store shares, so the rule lives in one
// place rather than being re-implemented per store:
//
//   - Resolve: read the declared version, decide whether the on-disk form must
//     be migrated forward, and refuse a file a NEWER ferry wrote.
//   - FutureVersionError: the typed, forward-compatibility refusal.
//   - BackupForMigration: preserve the pre-migration bytes before a rewrite.
//
// The pre-1.0 rule is forward-only: a file with no "version" field is the
// original (pre-versioning) form and reads as version 1; a file whose version
// exceeds what this ferry understands is refused, never rewritten, so a
// downgraded ferry cannot corrupt state a newer one owns.
package statefile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// statePerm is ferry's owner-only posture for its secret-bearing state store; a
// pre-migration backup copy inherits it so a preserved copy is never looser than
// the file it came from.
const statePerm os.FileMode = 0o600

// PeekVersion reports the schema version declared by a state file's raw bytes. A
// state file is a JSON object; a versioned one carries an integer "version"
// field. When that field is present and an integer, versioned is true and
// version is its value. When the bytes do not parse as a JSON object, carry no
// "version" field, or carry a non-integer one, versioned is false and version is
// 0 — the caller treats that as the original, pre-versioning on-disk form
// (schema version 1). Reading only the "version" field (via json.RawMessage)
// keeps this robust against a legacy map that happens to hold a "version" key
// whose value is a string, not a number.
func PeekVersion(data []byte) (version int, versioned bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return 0, false
	}
	raw, ok := obj["version"]
	if !ok {
		return 0, false
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

// Resolve inspects a state file's raw bytes against the version this ferry
// supports and returns the resolved on-disk version, whether the file must be
// migrated forward, and any refusal. An unversioned legacy file resolves to
// version 1 with migrate=true: its on-disk form predates the "version" field, so
// a mutating read rewrites it into the current versioned form. A file whose
// declared version exceeds supported yields a *FutureVersionError (and the
// caller MUST leave the file untouched). A file already at a supported version
// needs no migration.
//
// Scope: today the only forward transition is unversioned -> version 1, so
// migrate is true ONLY for that case. When a future release introduces a
// version 2, a versioned-but-older file (v < supported) will also need
// migrate=true AND its own pre-migration backup — revisit this function and its
// callers then, rather than relying on save() to silently upgrade it.
func Resolve(path string, data []byte, supported int) (version int, migrate bool, err error) {
	v, versioned := PeekVersion(data)
	if !versioned {
		// Pre-versioning form: resolve to schema version 1 and flag it for a
		// forward migration so a mutating read reshapes it into the envelope.
		return 1, true, nil
	}
	if v > supported {
		return v, false, &FutureVersionError{Path: path, Found: v, Supported: supported}
	}
	return v, false, nil
}

// FutureVersionError is returned when a state file declares a schema version
// newer than the running ferry understands. The read path returns it WITHOUT
// modifying the file, so a downgraded ferry refuses rather than corrupting state
// a newer ferry owns. The message names the file and both versions so the
// refusal is self-explanatory.
type FutureVersionError struct {
	Path      string // the offending state file
	Found     int    // the on-disk schema version
	Supported int    // the highest version this ferry understands
}

func (e *FutureVersionError) Error() string {
	return fmt.Sprintf(
		"state file %s was written by a newer ferry (on-disk schema version %d; this ferry understands up to version %d) — upgrade ferry to read it; the file has been left untouched",
		e.Path, e.Found, e.Supported)
}

// BackupForMigration preserves the pre-migration bytes of a state file before a
// forward migration rewrites it, so the migration is recoverable and a manual
// downgrade still has the original. It copies path to the sibling
// "<path>.pre-v<found>.bak" (found = the schema version being migrated away
// from), owner-only, atomically (temp + rename). An existing backup is NOT
// overwritten — the first, genuinely-original copy wins — so a repeated migrate
// never clobbers it. It returns the backup path.
//
// This is ferry's OWN-state backup, deliberately distinct from the $HOME
// Backuper the domains write through: that transactional engine records the
// pre-ferry state of managed $HOME files into an immutable baseline that a
// restore reverts. Ferry's internal state files are not managed $HOME files and
// must never enter that baseline (a restore would then try to revert ferry's own
// bookkeeping), so their pre-migration copy is a plain owner-only sibling.
//
// Precondition: the caller MUST have symlink-hardened filepath.Dir(path) (via
// paths.HardenStoreDir) before calling. statefile cannot import paths, so it
// does not re-harden; every current caller opens its store through a hardened
// state dir before it reaches a migration.
func BackupForMigration(path string, found int) (string, error) {
	bakPath := fmt.Sprintf("%s.pre-v%d.bak", path, found)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ferry-statebak-*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once linked away or on any error
	if err := tmp.Chmod(statePerm); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	// Publish write-once via os.Link so the "first, original copy wins" guarantee
	// holds unconditionally — even against a concurrent writer that is not under
	// the apply lock. EEXIST means a backup already exists; keep it, never clobber.
	if err := os.Link(tmpName, bakPath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return bakPath, nil
		}
		return "", err
	}
	return bakPath, nil
}
