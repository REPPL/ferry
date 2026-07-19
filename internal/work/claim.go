package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/backup"
	"github.com/REPPL/ferry/internal/statefile"
)

// Baton tracking is advisory, not locking: per-account claim files record who
// packed and received what. One human across their own accounts is the threat
// model; divergence is detected and surfaced, not prevented. Each claim file
// is written ONLY by its owning account and merged on read.

// claimVersion is the current claim-file schema version.
const claimVersion = 1

// Claim event operations.
const (
	OpPack     = "pack"
	OpReceive  = "receive"
	OpTakeBack = "take-back"
)

// ClaimEvent is one recorded baton event. At is RFC3339, display-only —
// ordering within an account's claim is append order, and cross-account
// ordering is by bundle sequence, never by timestamp.
type ClaimEvent struct {
	Op     string `json:"op"`
	Seq    uint64 `json:"seq"`
	Bundle string `json:"bundle_sha256,omitempty"`
	At     string `json:"at,omitempty"`
}

// Claim is one account's claim file: an append-only event history.
type Claim struct {
	Version int          `json:"version"`
	Account string       `json:"account"`
	Events  []ClaimEvent `json:"events"`
}

// claimAccount pins the account spelling: local@host, each part starting
// alphanumeric — which also keeps the derived filename free of separators
// and dot-only names.
var claimAccount = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*@[A-Za-z0-9][A-Za-z0-9._-]*$`)

// claimFileName matches "claim.<account>.json".
var claimFileName = regexp.MustCompile(`^claim\.(.+)\.json$`)

// AppendClaim appends one event to THIS account's claim file for the project.
// The file is only ever written by its owner, so no cross-account write race
// exists; the write is atomic against a torn read by the other account.
func (st *Store) AppendClaim(key, account string, ev ClaimEvent) error {
	if !rootSHA.MatchString(key) {
		return fmt.Errorf("work: store key %q is not a full commit SHA", key)
	}
	if !claimAccount.MatchString(account) {
		return fmt.Errorf("work: claim account %q is not of the form user@host", account)
	}
	switch ev.Op {
	case OpPack, OpReceive, OpTakeBack:
	default:
		return fmt.Errorf("work: unknown claim op %q", ev.Op)
	}
	if err := st.ensureProjectDir(key); err != nil {
		return err
	}
	path := filepath.Join(st.ProjectDir(key), "claim."+account+".json")
	claim := Claim{Version: claimVersion, Account: account}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// First event for this account.
	case err != nil:
		return err
	default:
		loaded, err := decodeClaim(path, data)
		if err != nil {
			return err
		}
		if loaded.Account != account {
			return fmt.Errorf("work: claim file %s belongs to %q, not %q — refusing to write another account's claim", path, loaded.Account, account)
		}
		claim = *loaded
	}
	claim.Version = claimVersion
	claim.Events = append(claim.Events, ev)
	out, err := json.MarshalIndent(claim, "", "  ")
	if err != nil {
		return err
	}
	// World-readable: the OTHER account must be able to read it to merge.
	return backup.AtomicWrite(path, out, 0o644)
}

// Claims reads and merges every account's claim file for the project, sorted
// by account for deterministic output. A missing project directory reads as
// no claims.
func (st *Store) Claims(key string) ([]Claim, error) {
	if !rootSHA.MatchString(key) {
		return nil, fmt.Errorf("work: store key %q is not a full commit SHA", key)
	}
	entries, err := os.ReadDir(st.ProjectDir(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var claims []Claim
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := claimFileName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		path := filepath.Join(st.ProjectDir(key), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		claim, err := decodeClaim(path, data)
		if err != nil {
			return nil, err
		}
		claims = append(claims, *claim)
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].Account < claims[j].Account })
	return claims, nil
}

// decodeClaim parses a claim file with the standard version gate.
func decodeClaim(path string, data []byte) (*Claim, error) {
	v, versioned := statefile.PeekVersion(data)
	if !versioned {
		return nil, fmt.Errorf("work: claim file %s carries no schema version — it looks corrupt", path)
	}
	if v > claimVersion {
		return nil, &statefile.FutureVersionError{Path: path, Found: v, Supported: claimVersion}
	}
	if v < 1 {
		return nil, fmt.Errorf("work: claim file %s declares invalid schema version %d", path, v)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var c Claim
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("work: parse claim file %s: %w", path, err)
	}
	if !claimAccount.MatchString(c.Account) {
		return nil, fmt.Errorf("work: claim file %s names malformed account %q", path, c.Account)
	}
	return &c, nil
}
