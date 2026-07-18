package work

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/REPPL/ferry/internal/statefile"
)

// ManifestMember is the zip member holding the cargo manifest inside a
// .ferrywork bundle. The member name and the manifest schema are cargo-
// specific — deliberately distinct from the config bundle's manifest.
const ManifestMember = "ferry-work.json"

// manifestVersion is the current cargo manifest schema version. On-disk
// formats carry the standard version envelope and follow the compatibility
// policy: forward-only migration, newer-version refusal.
const manifestVersion = 1

// Scan verdicts recorded in the manifest: the pack-side secret gate either
// found nothing, or every finding was explicitly acknowledged (pinned to
// file + content hash in local state).
const (
	ScanVerdictClean        = "clean"
	ScanVerdictAcknowledged = "acknowledged"
)

// ManifestFile is one packed file within an item. Path is the canonical
// forward-slash path RELATIVE to the item's root (never absolute, never
// traversing) — receive resolves it against the destination account's own
// item location, so a manifest can never name an arbitrary target.
type ManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// ManifestItem records one cargo item's inclusion. An excluded item carries
// its reason ("missing", "excluded") so `work status` can surface the gap —
// a silent gap in a handover is worse than a loud one.
type ManifestItem struct {
	Name     string         `json:"name"`
	Included bool           `json:"included"`
	Reason   string         `json:"reason,omitempty"`
	Files    []ManifestFile `json:"files,omitempty"`
}

// Manifest is the cargo bundle's manifest member. RepoPath and Home are the
// SOURCE account's paths, recorded for path rewriting and diagnostics only —
// receive always recomputes destinations forward from its own account.
// PackedAt is display-only: ordering is by Seq, never by timestamp (clocks
// differ across machines and exFAT timestamps are coarse).
type Manifest struct {
	Version      int            `json:"version"`
	FerryVersion string         `json:"ferry_version"`
	Key          string         `json:"key"`
	Roots        []string       `json:"roots"`
	RepoPath     string         `json:"repo_path"`
	Home         string         `json:"home"`
	Seq          uint64         `json:"seq"`
	PackedBy     string         `json:"packed_by"`
	PackedAt     string         `json:"packed_at"`
	ScanVerdict  string         `json:"scan_verdict"`
	Items        []ManifestItem `json:"items"`
}

// Encode serialises the manifest, stamping the current schema version and
// validating first, so a malformed manifest is refused at write time rather
// than discovered by the receiving account.
func (m *Manifest) Encode() ([]byte, error) {
	m.Version = manifestVersion
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("work: manifest: %w", err)
	}
	return json.MarshalIndent(m, "", "  ")
}

// DecodeManifest parses and validates a cargo manifest. A manifest written by
// a newer ferry is refused (*statefile.FutureVersionError); an unversioned or
// unknown-shaped payload is refused rather than guessed at — receive writes
// files from this data, so it is validated as untrusted input.
func DecodeManifest(data []byte) (*Manifest, error) {
	v, versioned := statefile.PeekVersion(data)
	if !versioned {
		return nil, fmt.Errorf("work: %s carries no schema version — not a cargo manifest", ManifestMember)
	}
	if v > manifestVersion {
		return nil, &statefile.FutureVersionError{Path: ManifestMember, Found: v, Supported: manifestVersion}
	}
	if v < 1 {
		return nil, fmt.Errorf("work: %s declares invalid schema version %d", ManifestMember, v)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("work: parse %s: %w", ManifestMember, err)
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("work: %s: %w", ManifestMember, err)
	}
	return &m, nil
}

// validate enforces the manifest contract shared by Encode and Decode.
func (m *Manifest) validate() error {
	if !rootSHA.MatchString(m.Key) {
		return fmt.Errorf("key %q is not a full commit SHA", m.Key)
	}
	if len(m.Roots) == 0 {
		return fmt.Errorf("empty root-SHA set")
	}
	keyInRoots := false
	for _, r := range m.Roots {
		if !rootSHA.MatchString(r) {
			return fmt.Errorf("root %q is not a full commit SHA", r)
		}
		if r == m.Key {
			keyInRoots = true
		}
	}
	if !keyInRoots {
		return fmt.Errorf("key %s is not in the root-SHA set", m.Key)
	}
	seen := map[string]bool{}
	for _, it := range m.Items {
		if it.Name == "" {
			return fmt.Errorf("item with empty name")
		}
		if seen[it.Name] {
			return fmt.Errorf("duplicate item %q", it.Name)
		}
		seen[it.Name] = true
		if !it.Included && len(it.Files) > 0 {
			return fmt.Errorf("item %q is excluded but lists files", it.Name)
		}
		for _, f := range it.Files {
			if err := validateCargoRel(f.Path); err != nil {
				return fmt.Errorf("item %q: %w", it.Name, err)
			}
			if len(f.SHA256) != 64 || !isLowerHex(f.SHA256) {
				return fmt.Errorf("item %q: file %q has malformed sha256", it.Name, f.Path)
			}
			if f.Size < 0 {
				return fmt.Errorf("item %q: file %q has negative size", it.Name, f.Path)
			}
		}
	}
	return nil
}

// validateCargoRel refuses any member path that is not a clean, relative,
// forward-slash path: no traversal, no absolute paths, no backslashes, no
// "."/".." segments, no empty segments. Receive joins these against the
// destination item root, so this is the first containment layer.
func validateCargoRel(p string) error {
	if p == "" {
		return fmt.Errorf("empty file path")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("absolute file path %q", p)
	}
	if strings.ContainsRune(p, '\\') {
		return fmt.Errorf("backslash in file path %q", p)
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("NUL in file path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "":
			return fmt.Errorf("empty segment in file path %q", p)
		case ".", "..":
			return fmt.Errorf("dot segment in file path %q", p)
		}
	}
	return nil
}

// isLowerHex reports whether s is entirely lowercase hex.
func isLowerHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
