package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/backup"
	"github.com/REPPL/ferry/internal/paths"
	"github.com/REPPL/ferry/internal/statefile"
)

// stateVersion is the current per-project work-state schema version.
const stateVersion = 1

const (
	stateDirPerm  os.FileMode = 0o700
	stateFilePerm os.FileMode = 0o600
)

// Baseline records the content hashes of what the last pack or receive on
// THIS account carried, per item and canonical relative path. Divergence is
// detected by comparing destination content hashes against it — never
// timestamps.
type Baseline struct {
	// Op is "pack" or "receive" — which verb set this baseline.
	Op string `json:"op"`
	// Seq and Bundle name the cargo bundle the baseline came from.
	Seq    uint64 `json:"seq"`
	Bundle string `json:"bundle_sha256"`
	// At is RFC3339, display-only.
	At string `json:"at"`
	// Files maps item name -> canonical rel path -> content sha256.
	Files map[string]map[string]string `json:"files"`
}

// Ack is one acknowledged secret-gate finding, pinned to file + content hash:
// pack re-aborts if the flagged content changes, so an acknowledgement never
// silently covers new material.
type Ack struct {
	Item   string `json:"item"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Note   string `json:"note,omitempty"`
}

// ReceiveRecord records the last receive so `ferry work restore` can revert
// exactly it from its per-receive snapshot.
type ReceiveRecord struct {
	SnapshotID string `json:"snapshot_id"`
	Seq        uint64 `json:"seq"`
	Bundle     string `json:"bundle_sha256,omitempty"`
	At         string `json:"at,omitempty"`
	// Paths are the absolute destination paths this receive wrote.
	Paths []string `json:"paths,omitempty"`
}

// State is the per-project, per-account local work state: baselines,
// acknowledgements, the last receive, and every path work verbs have written
// (the enumeration `ferry restore work` reverts to baseline).
type State struct {
	path string

	Baseline    *Baseline
	Acks        []Ack
	LastReceive *ReceiveRecord
	Written     []string
}

// stateEnvelope is the on-disk form.
type stateEnvelope struct {
	Version     int            `json:"version"`
	Baseline    *Baseline      `json:"baseline,omitempty"`
	Acks        []Ack          `json:"acks,omitempty"`
	LastReceive *ReceiveRecord `json:"last_receive,omitempty"`
	Written     []string       `json:"written,omitempty"`
}

// LoadState loads the per-project work state for storeKey from ferry's state
// dir (~/.local/state/ferry/work/<key>.json).
func LoadState(storeKey string) (*State, error) {
	root, err := paths.StateDir()
	if err != nil {
		return nil, err
	}
	return LoadStateAt(root, storeKey)
}

// LoadStateAt is the testable core: it loads the state for storeKey under an
// explicit state root. A missing file is an empty state; a zero-length file
// (torn write) degrades to an empty state; a file a newer ferry wrote is
// refused untouched (*statefile.FutureVersionError); an unversioned or
// unknown-shaped file is refused, never guessed at.
func LoadStateAt(stateRoot, storeKey string) (*State, error) {
	if !rootSHA.MatchString(storeKey) {
		return nil, fmt.Errorf("work: store key %q is not a full commit SHA", storeKey)
	}
	dir := filepath.Join(stateRoot, "work")
	// Symlink-harden the state dir before reading through it, mirroring every
	// other ferry store (lexical, creates nothing, no-op outside $HOME).
	if err := paths.HardenStoreDir(dir); err != nil {
		return nil, err
	}
	s := &State{path: filepath.Join(dir, storeKey+".json")}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return s, nil
	}
	v, versioned := statefile.PeekVersion(data)
	if !versioned {
		return nil, fmt.Errorf("work: state file %s carries no schema version — it looks corrupt and has been left untouched; repair or remove it", s.path)
	}
	if v > stateVersion {
		return nil, &statefile.FutureVersionError{Path: s.path, Found: v, Supported: stateVersion}
	}
	if v < 1 {
		return nil, fmt.Errorf("work: state file %s declares invalid schema version %d", s.path, v)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var env stateEnvelope
	if err := dec.Decode(&env); err != nil {
		return nil, fmt.Errorf("work: parse state file %s: %w", s.path, err)
	}
	s.Baseline = env.Baseline
	s.Acks = env.Acks
	s.LastReceive = env.LastReceive
	s.Written = env.Written
	return s, nil
}

// Save persists the state atomically, owner-only.
func (s *State) Save() error {
	dir := filepath.Dir(s.path)
	if err := paths.HardenStoreDir(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, stateDirPerm); err != nil {
		return err
	}
	if err := os.Chmod(dir, stateDirPerm); err != nil {
		return err
	}
	env := stateEnvelope{
		Version:     stateVersion,
		Baseline:    s.Baseline,
		Acks:        s.Acks,
		LastReceive: s.LastReceive,
		Written:     s.Written,
	}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	return backup.AtomicWrite(s.path, data, stateFilePerm)
}

// Acknowledged reports whether the finding at (item, relPath) is acknowledged
// for EXACTLY this content hash.
func (s *State) Acknowledged(item, relPath, sha256Hex string) bool {
	for _, a := range s.Acks {
		if a.Item == item && a.Path == relPath && a.SHA256 == sha256Hex {
			return true
		}
	}
	return false
}

// RecordWritten merges paths into the sorted, deduplicated set of absolute
// paths work verbs have written on this account.
func (s *State) RecordWritten(pathsWritten ...string) {
	set := map[string]bool{}
	for _, p := range s.Written {
		set[p] = true
	}
	for _, p := range pathsWritten {
		set[p] = true
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	s.Written = out
}
