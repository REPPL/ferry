package dotfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/REPPL/ferry/internal/paths"
	"github.com/REPPL/ferry/internal/statefile"
)

// lastAppliedVersion is the current on-disk schema version of the last-applied
// store. Version 2 adds the "deployed" map: the per-target last-deployed
// content snapshot (the "last-deployed baseline"), content-addressed by the same
// hash the "applied" map records, so `apply` can persist the exact bytes it last
// wrote to each target — EXCEPT a secret-routed target (one whose bytes were
// rendered from the secret store), whose bytes are deliberately NOT snapshotted:
// only its "applied" hash is recorded, keeping plaintext secrets out of this
// non-secret bookkeeping file (the secret-at-rest boundary). Version 1 is the
// envelope without "deployed"; a file with
// no "version" field is the original v0.3.x form (a bare name->hash map). Both
// read forward and are migrated to the current form on the next mutating open,
// preserving every recorded hash. See internal/statefile for the shared version
// contract.
const lastAppliedVersion = 2

// lastAppliedFile is the versioned on-disk envelope for the last-applied store.
// Deployed maps a content hash (the same hash stored in Applied) to the exact
// bytes ferry last wrote for a target with that hash — the last-deployed
// baseline. It is content-addressed, so targets sharing identical content share
// one entry, and it is pruned on every save to the hashes Applied still
// references, keeping it bounded by the managed-target count. It omits when
// empty, so a store with no snapshots serialises the same shape as version 1
// (plus the bumped version). It intentionally OMITS the bytes of a secret-routed
// target (rendered from the secret store): CommitLastApplied records only that
// target's Applied hash, so a plaintext secret is never written into this
// non-secret file. Such a target therefore has an Applied hash with no Deployed
// entry — indistinguishable on disk from the pre-baseline bootstrap case.
type lastAppliedFile struct {
	Version  int               `json:"version"`
	Applied  map[string]string `json:"applied"`
	Deployed map[string][]byte `json:"deployed,omitempty"`
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
	deployed map[string][]byte // content hash -> last-deployed bytes (the baseline)
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
		deployed: map[string][]byte{},
		readOnly: readOnly,
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	// A zero-length file cannot be a valid store (the smallest valid form is a JSON
	// object). Treat it as an empty store rather than hard-failing: a torn write from
	// a crash then degrades to first-touch reclassification instead of wedging every
	// ferry command behind a manual file deletion.
	if len(data) == 0 {
		return s, nil
	}
	// Resolve the on-disk schema version. A file a newer ferry wrote is refused
	// here (FutureVersionError) BEFORE any decode or write, so a downgraded ferry
	// leaves it untouched rather than corrupting it.
	version, migrate, err := statefile.Resolve(s.path, data, lastAppliedVersion)
	if err != nil {
		return nil, err
	}
	// A migration can be one of two shapes, distinguished by whether the file
	// already carries a "version" field: the pre-versioning v0.3.x form is a bare
	// name->hash map; a versioned-but-older file (version 1) is an envelope that
	// simply lacks "deployed". Both preserve every recorded hash on the way to
	// version 2 — the v1 envelope's "applied" carries over verbatim and "deployed"
	// starts empty (populated by the next apply, the "confirm every target once"
	// bootstrap).
	_, wasVersioned := statefile.PeekVersion(data)
	if migrate && !wasVersioned {
		// The original v0.3.x form is a bare name->hash map: decode it directly.
		if err := json.Unmarshal(data, &s.applied); err != nil {
			return nil, err
		}
	} else {
		// Strict envelope decode (current-version reads AND versioned-but-older
		// migrations): an unknown top-level key means the payload is not where the
		// schema says (e.g. a hand-edit put hashes beside "version" instead of under
		// "applied"). Silently reading that as an EMPTY store would let the next save
		// permanently overwrite the record with no backup, so it is a clean refusal.
		env, err := decodeEnvelope(data, s.path, version)
		if err != nil {
			return nil, err
		}
		s.applied = env.Applied
		s.deployed = env.Deployed
	}
	if migrate && !readOnly {
		// Migrate-on-read, mutating path only: back the pre-migration file up first
		// (write-once sibling backup, keyed by the version being migrated away from),
		// then rewrite it in the current versioned envelope form. The read-only
		// status/diff path decodes in memory and writes nothing.
		if _, err := statefile.BackupForMigration(s.path, version); err != nil {
			return nil, err
		}
		if err := s.save(); err != nil {
			return nil, err
		}
	}
	if s.applied == nil {
		s.applied = map[string]string{}
	}
	if s.deployed == nil {
		s.deployed = map[string][]byte{}
	}
	return s, nil
}

// decodeEnvelope strictly decodes versioned last-applied bytes into the current
// envelope, rejecting any unknown top-level key (a hand-edit that put payload
// beside the schema's fields, which a lenient decode would silently read as an
// EMPTY store the next save would overwrite with no backup). A version-1 file has
// no "deployed" key, so its Deployed decodes nil; the caller normalises that to
// an empty map.
func decodeEnvelope(data []byte, path string, version int) (lastAppliedFile, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var env lastAppliedFile
	if err := dec.Decode(&env); err != nil {
		return lastAppliedFile{}, fmt.Errorf("dotfile: state file %s is not a valid version %d last-applied record (%v) — the file has been left untouched; repair or remove it", path, version, err)
	}
	return env, nil
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

// set records a new last-applied hash for a target and persists the store,
// WITHOUT recording a deployed-content snapshot. It is the hash-only path (the
// capture full-file reconcile, which advances the sync point but does not carry
// the exact bytes to snapshot here). It errors on a read-only store (status/diff),
// which must never mutate state.
func (s *Store) set(name, hash string) error {
	return s.record(name, hash, nil)
}

// setDeployed records a new last-applied hash for a target AND the exact bytes
// ferry deployed for it — the last-deployed baseline — then persists the store.
// It is the apply write path's setter (writeContent / CommitLastApplied), where
// the deployed content is in hand. The snapshot is content-addressed by hash, so
// it re-reads via LastDeployedSnapshot(name). A nil content records only the hash
// (equivalent to set) — the path CommitLastApplied takes for a secret-routed
// target, so its plaintext bytes never reach this file. Errors on a read-only store.
func (s *Store) setDeployed(name, hash string, content []byte) error {
	return s.record(name, hash, content)
}

// record is the single mutating core behind set/setDeployed: it advances the
// last-applied hash and, when content is non-nil, stores the content-addressed
// last-deployed snapshot, then persists. save() prunes any snapshot the applied
// map no longer references, so a superseded baseline never lingers.
func (s *Store) record(name, hash string, content []byte) error {
	if s.readOnly {
		return errors.New("dotfile: cannot persist last-applied on a read-only store")
	}
	s.applied[name] = hash
	if content != nil {
		if s.deployed == nil {
			s.deployed = map[string][]byte{}
		}
		s.deployed[hash] = content
	}
	return s.save()
}

// LastDeployedSnapshot returns the exact bytes ferry last deployed for a target
// — the last-deployed baseline — resolved through the target's recorded
// last-applied hash. ok is false when the target has no record, or when its hash
// has no stored snapshot (a hash set by the hash-only path — including every
// secret-routed target, whose bytes are deliberately never snapshotted — a
// v0.3.x/v1 record migrated forward before any apply re-established the snapshot,
// or a superseded hash whose snapshot has been pruned). Callers treat a missing
// snapshot as "no baseline yet" — the first-apply-after-upgrade bootstrap case.
func (s *Store) LastDeployedSnapshot(name string) (content []byte, ok bool) {
	hash, ok := s.applied[name]
	if !ok {
		return nil, false
	}
	c, ok := s.deployed[hash]
	if !ok {
		return nil, false
	}
	return c, true
}

// save atomically rewrites the store file (temp + rename), 0600, in the current
// versioned envelope form. It first prunes the deployed-snapshot map to the
// hashes the applied map still references, so the last-deployed baseline stays
// bounded by the managed-target count and a superseded snapshot never lingers.
func (s *Store) save() error {
	s.pruneDeployed()
	data, err := json.MarshalIndent(lastAppliedFile{Version: lastAppliedVersion, Applied: s.applied, Deployed: s.deployed}, "", "  ")
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
	// fsync the temp before rename so the DATA is durable, and fsync the directory
	// after rename so the rename itself survives a crash. Without this a power loss
	// can leave the store zero-length/truncated, and openStoreAt would then hard-fail
	// every apply/capture/status until the file is deleted by hand. Mirrors
	// backup.AtomicWrite, the codebase's canonical crash-safe write.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	return fsyncDir(filepath.Dir(s.path))
}

// fsyncDir flushes a directory entry (e.g. the rename that published a state file)
// so it survives a crash. A missing/unopenable dir is not fatal here — the write
// already succeeded; durability of the rename is best-effort on platforms that
// reject directory fsync.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return nil
	}
	return nil
}

// pruneDeployed drops every last-deployed snapshot whose hash is no longer
// referenced by any applied entry. A target's hash advances on each redeploy, so
// its old snapshot becomes orphaned; pruning keeps the deployed map bounded by
// the number of distinct current target contents rather than growing without
// limit. It is a no-op when there are no snapshots.
func (s *Store) pruneDeployed() {
	if len(s.deployed) == 0 {
		return
	}
	referenced := make(map[string]bool, len(s.applied))
	for _, hash := range s.applied {
		referenced[hash] = true
	}
	for hash := range s.deployed {
		if !referenced[hash] {
			delete(s.deployed, hash)
		}
	}
}
