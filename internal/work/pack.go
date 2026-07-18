package work

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/REPPL/ferry/internal/backup"
	"github.com/REPPL/ferry/internal/secret"
	"github.com/REPPL/ferry/internal/statefile"
)

// HandoverMarkerName is the sidecar handover marker in .abcd/.work.local/.
// Pack records the baton there WITHOUT mutating any cargo file — marking the
// baton must not modify the baton (that would trip the divergence guard on
// every normal cycle and fight the user's editor).
const HandoverMarkerName = ".ferry-handover.json"

// handoverVersion is the marker's schema version.
const handoverVersion = 1

// HandoverMarker records the last pack from this project directory: bundle
// identity plus the content hashes of what was packed, so `work status` can
// tell "handed over, not modified since" from "modified after handover".
type HandoverMarker struct {
	Version int    `json:"version"`
	Seq     uint64 `json:"seq"`
	Bundle  string `json:"bundle_sha256"`
	At      string `json:"at,omitempty"`
	// Files maps item name -> canonical rel path -> content sha256.
	Files map[string]map[string]string `json:"files"`
}

// SecretFinding is one unacknowledged secret-gate hit, named precisely enough
// to act on. SHA256 is the flagged content's hash — the pin an
// acknowledgement must carry.
type SecretFinding struct {
	Item   string
	Path   string
	Rule   string
	Detail string
	SHA256 string
}

// SecretGateError aborts a pack: nothing was written. This is deliberately
// stricter than bundle export (which withholds the offending file and
// continues) — work-state cargo is small and personal, and a silent gap in a
// handover is worse than a loud stop.
type SecretGateError struct {
	Findings []SecretFinding
}

func (e *SecretGateError) Error() string {
	var b strings.Builder
	b.WriteString("work: the secret gate stopped the pack; nothing was written:\n")
	for _, f := range e.Findings {
		fmt.Fprintf(&b, "  %s/%s: %s (%s)\n", f.Item, f.Path, f.Detail, f.Rule)
	}
	b.WriteString("fix the content at its source, exclude the item (--exclude), or acknowledge the finding to pack it as-is")
	return b.String()
}

// PackOptions carries the caller's choices into a pack.
type PackOptions struct {
	// Excludes names items to leave out (recorded in the manifest; status
	// shows the gap).
	Excludes []string
	// AllowEmpty permits a pack without the handover note (memory/
	// transcript-only cargo).
	AllowEmpty bool
	// Account is this account's claim identity, user@host.
	Account string
	// FerryVersion is stamped into the manifest.
	FerryVersion string
	// Now is an RFC3339 timestamp for display fields; never used for
	// ordering.
	Now string
}

// PackResult reports a completed pack.
type PackResult struct {
	Ref        BundleRef
	Manifest   *Manifest
	MarkerPath string
}

// packedFile is one collected cargo file.
type packedFile struct {
	item string
	rel  string
	data []byte
	mode os.FileMode
	sha  string
}

// Pack bundles the project's work state into the store: collect every
// registry item, pass EVERY byte through the secret gate (fail closed, with
// the two auditable escape hatches), build the cargo zip, store it under the
// next sequence, append this account's claim, update the local baseline, and
// record the sidecar handover marker. The caller has already established the
// project identity (same guards as receive) and validated the account.
func Pack(st *Store, lc Locator, id Identity, state *State, opts PackOptions) (*PackResult, error) {
	items := BuiltinItems()
	byName := map[string]Item{}
	for _, it := range items {
		byName[it.Name] = it
	}
	excluded := map[string]bool{}
	for _, x := range opts.Excludes {
		if _, ok := byName[x]; !ok {
			return nil, fmt.Errorf("work: --exclude names unknown item %q", x)
		}
		excluded[x] = true
	}

	var (
		manifestItems []ManifestItem
		files         []packedFile
		findings      []SecretFinding
		acked         bool
	)
	for _, it := range items {
		if excluded[it.Name] {
			manifestItems = append(manifestItems, ManifestItem{Name: it.Name, Included: false, Reason: "excluded"})
			continue
		}
		root, err := it.Locate(lc)
		if err != nil {
			return nil, err
		}
		collected, missing, err := collectItem(it, root)
		if err != nil {
			return nil, err
		}
		if missing {
			if it.Required && !opts.AllowEmpty {
				return nil, fmt.Errorf("work: nothing to hand over — write the handover note (%s) first, or pass --allow-empty for memory/transcript-only cargo", root)
			}
			manifestItems = append(manifestItems, ManifestItem{Name: it.Name, Included: false, Reason: "missing"})
			continue
		}
		mi := ManifestItem{Name: it.Name, Included: true}
		for _, f := range collected {
			if hit, finding := gateFile(f); hit {
				if state.Acknowledged(f.item, f.rel, f.sha) {
					acked = true
				} else {
					findings = append(findings, finding)
				}
			}
			mi.Files = append(mi.Files, ManifestFile{Path: f.rel, Size: int64(len(f.data)), SHA256: f.sha})
		}
		manifestItems = append(manifestItems, mi)
		files = append(files, collected...)
	}
	if len(findings) > 0 {
		return nil, &SecretGateError{Findings: findings}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("work: nothing to pack — every item is missing or excluded")
	}

	verdict := ScanVerdictClean
	if acked {
		verdict = ScanVerdictAcknowledged
	}

	manifest := &Manifest{
		FerryVersion: opts.FerryVersion,
		Key:          id.Key,
		Roots:        id.Roots,
		RepoPath:     lc.ProjectDir,
		Home:         lc.Home,
		PackedBy:     opts.Account,
		PackedAt:     opts.Now,
		ScanVerdict:  verdict,
		Items:        manifestItems,
	}
	ref, err := st.WriteBundle(id.Key, func(seq uint64) ([]byte, error) {
		manifest.Seq = seq
		return buildCargoZip(manifest, files)
	})
	if err != nil {
		return nil, err
	}

	if err := st.AppendClaim(id.Key, opts.Account, ClaimEvent{Op: OpPack, Seq: ref.Seq, Bundle: ref.SHA256, At: opts.Now}); err != nil {
		return nil, fmt.Errorf("work: bundle %06d stored but recording the claim failed: %w", ref.Seq, err)
	}

	hashes := fileHashesByItem(files)
	state.Baseline = &Baseline{Op: OpPack, Seq: ref.Seq, Bundle: ref.SHA256, At: opts.Now, Files: hashes}
	if err := state.Save(); err != nil {
		return nil, fmt.Errorf("work: bundle %06d stored but saving local state failed: %w", ref.Seq, err)
	}

	markerPath, err := writeHandoverMarker(lc.ProjectDir, HandoverMarker{
		Seq: ref.Seq, Bundle: ref.SHA256, At: opts.Now, Files: hashes,
	})
	if err != nil {
		return nil, fmt.Errorf("work: bundle %06d stored but writing the handover marker failed: %w", ref.Seq, err)
	}
	return &PackResult{Ref: ref, Manifest: manifest, MarkerPath: markerPath}, nil
}

// collectItem gathers an item's files. A missing file/dir (or an empty dir)
// reports missing=true. A symlink or special file anywhere in an item is a
// refusal, never a silent skip — cargo must contain exactly what the manifest
// says.
func collectItem(it Item, root string) (collected []packedFile, missing bool, err error) {
	fi, statErr := os.Lstat(root)
	if errors.Is(statErr, fs.ErrNotExist) {
		return nil, true, nil
	}
	if statErr != nil {
		return nil, false, statErr
	}
	switch it.Kind {
	case KindFile:
		if !fi.Mode().IsRegular() {
			return nil, false, fmt.Errorf("work: %s is not a regular file (mode %v) — refusing to pack it", root, fi.Mode())
		}
		data, err := os.ReadFile(root)
		if err != nil {
			return nil, false, err
		}
		rel := filepath.Base(root)
		if err := validateCargoRel(rel); err != nil {
			return nil, false, fmt.Errorf("work: item %s: %w", it.Name, err)
		}
		return []packedFile{newPackedFile(it.Name, rel, data, fi.Mode())}, false, nil
	case KindDir:
		if !fi.IsDir() {
			return nil, false, fmt.Errorf("work: %s is not a directory (mode %v) — refusing to pack it", root, fi.Mode())
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("work: %s is not a regular file (mode %v) — refusing to pack it", p, d.Type())
			}
			relOS, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(relOS)
			if err := validateCargoRel(rel); err != nil {
				return fmt.Errorf("work: item %s: %w", it.Name, err)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			collected = append(collected, newPackedFile(it.Name, rel, data, info.Mode()))
			return nil
		})
		if err != nil {
			return nil, false, err
		}
		if len(collected) == 0 {
			return nil, true, nil
		}
		return collected, false, nil
	default:
		return nil, false, fmt.Errorf("work: item %s has unknown kind %q", it.Name, it.Kind)
	}
}

// newPackedFile hashes and mode-sanitises one collected file.
func newPackedFile(item, rel string, data []byte, mode os.FileMode) packedFile {
	sum := sha256.Sum256(data)
	return packedFile{item: item, rel: rel, data: data, mode: cargoMode(mode), sha: hex.EncodeToString(sum[:])}
}

// cargoMode reduces an on-disk mode to a portable member mode: 0755 when any
// execute bit is set, else 0644 — the same reduction the config bundle
// writer applies.
func cargoMode(m os.FileMode) os.FileMode {
	if m.Perm()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// gateFile passes one file through the secret gate. Text content is scanned
// line-wise (high-confidence findings block); non-text content is checked for
// embedded key material. Transcripts arrive pre-redacted by the project
// tooling; ferry re-scans anyway — defence in depth, and the gate also covers
// memory and the handover note, which nothing has scanned before.
func gateFile(f packedFile) (bool, SecretFinding) {
	if utf8.Valid(f.data) && !bytes.ContainsRune(f.data, 0) {
		fs := secret.ScanText(string(f.data))
		if fs.HasHigh() {
			first := fs[0]
			for _, fd := range fs {
				if fd.Confidence == secret.High {
					first = fd
					break
				}
			}
			return true, SecretFinding{Item: f.item, Path: f.rel, Rule: first.Rule, Detail: first.Detail, SHA256: f.sha}
		}
		return false, SecretFinding{}
	}
	if secret.HasBinarySecret(f.data) {
		return true, SecretFinding{Item: f.item, Path: f.rel, Rule: "binary-key-material", Detail: "embedded key material in binary content", SHA256: f.sha}
	}
	return false, SecretFinding{}
}

// buildCargoZip assembles the cargo bytes: every member under
// "<item>/<rel>", deterministic order, sanitised modes, the manifest last.
func buildCargoZip(m *Manifest, files []packedFile) ([]byte, error) {
	sorted := make([]packedFile, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].item != sorted[j].item {
			return sorted[i].item < sorted[j].item
		}
		return sorted[i].rel < sorted[j].rel
	})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range sorted {
		hdr := &zip.FileHeader{Name: f.item + "/" + f.rel, Method: zip.Deflate}
		hdr.SetMode(f.mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return nil, fmt.Errorf("work: create member %s/%s: %w", f.item, f.rel, err)
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, fmt.Errorf("work: write member %s/%s: %w", f.item, f.rel, err)
		}
	}
	manifestJSON, err := m.Encode()
	if err != nil {
		return nil, err
	}
	hdr := &zip.FileHeader{Name: ManifestMember, Method: zip.Deflate}
	hdr.SetMode(0o644)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(manifestJSON); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fileHashesByItem indexes collected files as item -> rel -> sha256.
func fileHashesByItem(files []packedFile) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, f := range files {
		if out[f.item] == nil {
			out[f.item] = map[string]string{}
		}
		out[f.item][f.rel] = f.sha
	}
	return out
}

// workLocalDir resolves the project's .abcd/.work.local directory and
// verifies no component of it is a symlink (comparing the symlink-resolved
// path against the resolved project dir), so a planted link cannot redirect
// the marker write. It creates the directory when absent.
func workLocalDir(projectDir string) (string, error) {
	resolvedProject, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(projectDir, ".abcd", ".work.local")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	want := filepath.Join(resolvedProject, ".abcd", ".work.local")
	if resolvedDir != want {
		return "", fmt.Errorf("work: %s resolves through a symlink (to %s) — refusing to write there", dir, resolvedDir)
	}
	return dir, nil
}

// writeHandoverMarker atomically records the sidecar marker.
func writeHandoverMarker(projectDir string, mk HandoverMarker) (string, error) {
	dir, err := workLocalDir(projectDir)
	if err != nil {
		return "", err
	}
	mk.Version = handoverVersion
	data, err := json.MarshalIndent(mk, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, HandoverMarkerName)
	if err := backup.AtomicWrite(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ReadHandoverMarker loads the project's handover marker. Absent is not an
// error: (nil, false, nil).
func ReadHandoverMarker(projectDir string) (*HandoverMarker, bool, error) {
	path := filepath.Join(projectDir, ".abcd", ".work.local", HandoverMarkerName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	v, versioned := statefile.PeekVersion(data)
	if !versioned {
		return nil, false, fmt.Errorf("work: handover marker %s carries no schema version — it looks corrupt", path)
	}
	if v > handoverVersion {
		return nil, false, &statefile.FutureVersionError{Path: path, Found: v, Supported: handoverVersion}
	}
	var mk HandoverMarker
	if err := json.Unmarshal(data, &mk); err != nil {
		return nil, false, fmt.Errorf("work: parse handover marker %s: %w", path, err)
	}
	return &mk, true, nil
}
