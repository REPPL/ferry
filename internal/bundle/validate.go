package bundle

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/REPPL/ferry/internal/secret"
)

// Resource caps (M8). These bound the work a hostile bundle can force BEFORE any
// decompression, read from the zip central directory (metadata only):
//
//   - maxEntries       — payload member count (zip-bomb-by-count defense).
//   - maxEntrySize     — declared uncompressed size of any single member.
//   - maxTotalSize     — summed declared uncompressed size of all members.
//   - maxManifestSize  — declared uncompressed size of ferry-bundle.json; caps the
//     JSON parse (the manifest is read into memory).
//
// Values are generous for a real dotfiles bundle yet far below anything a
// zip-bomb needs. They are a package-level policy, not tunables.
const (
	maxEntries      = 2000
	maxEntrySize    = 64 << 20  // 64 MiB per member
	maxTotalSize    = 256 << 20 // 256 MiB total
	maxManifestSize = 4 << 20   // 4 MiB manifest
)

// ValidatedEntry is one payload member that passed every check: its canonical
// relative path, byte size, and the *zip.File to stream on extract. local marks a
// local-layer member (local/** or ferry.local.toml) for the double-opt-in gate.
type ValidatedEntry struct {
	Path  string
	Size  int64
	file  *zip.File
	local bool
}

// Validated is the in-memory description of a bundle that passed validation. It
// holds an open zip reader (Close when done) plus the checked entry list and the
// manifest metadata the caller needs (IncludeLocal, FerryVersion). No payload has
// been written to disk at this point — Extract does that, under the caller's
// target dir only.
type Validated struct {
	Entries      []ValidatedEntry
	IncludeLocal bool
	FerryVersion string
	OverallSHA   string

	reader *zip.ReadCloser
}

// Close releases the underlying zip reader. Callers Validate then (on success)
// Extract then Close; on a validation error there is nothing to close.
func (v *Validated) Close() error {
	if v.reader != nil {
		return v.reader.Close()
	}
	return nil
}

// Validate opens the bundle at path and checks it WITHOUT extracting, in the
// order the security model requires:
//
//  1. If expectedSHA256 is non-empty, hash the whole file and abort on mismatch
//     FIRST — before opening the zip at all.
//  2. From the central directory (no decompression): enforce the manifest-size
//     cap, entry-count cap, per-entry size cap, and total-size cap.
//  3. Parse the (size-capped) manifest; refuse a format version newer than
//     supported (upgrade message).
//  4. Require payload-member set == manifest entry set exactly (manifest member
//     excluded); per entry check canonical path, regular mode, no case-fold
//     duplicate, declared size, content SHA256, and re-run the secret gate.
//
// On success it returns a *Validated the caller Extracts from (and must Close).
// Any failure returns an error and nothing is written anywhere.
func Validate(path, expectedSHA256 string, includeLocalWanted bool) (*Validated, error) {
	overall, err := fileSHA256(path)
	if err != nil {
		return nil, fmt.Errorf("bundle: hash %q: %w", path, err)
	}
	if expectedSHA256 != "" {
		if !strings.EqualFold(overall, strings.TrimSpace(expectedSHA256)) {
			return nil, fmt.Errorf("bundle: --expect-sha256 mismatch: expected %s, bundle is %s (checksum does not match — refusing)", expectedSHA256, overall)
		}
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("bundle: open %q: %w", path, err)
	}
	// From here on, close the reader on any error path.
	closeOnErr := true
	defer func() {
		if closeOnErr {
			zr.Close()
		}
	}()

	// --- (2) central-directory caps, metadata only, no decompression ---
	var manifestFile *zip.File
	payload := make([]*zip.File, 0, len(zr.File))
	var total uint64
	for _, f := range zr.File {
		if f.Name == manifestMember {
			if manifestFile != nil {
				return nil, fmt.Errorf("bundle: more than one %s member", manifestMember)
			}
			if f.UncompressedSize64 > maxManifestSize {
				return nil, fmt.Errorf("bundle: manifest declares %d bytes, over the %d cap", f.UncompressedSize64, maxManifestSize)
			}
			manifestFile = f
			continue
		}
		payload = append(payload, f)
		if len(payload) > maxEntries {
			return nil, fmt.Errorf("bundle: too many entries (over the %d cap) — refusing", maxEntries)
		}
		if f.UncompressedSize64 > maxEntrySize {
			return nil, fmt.Errorf("bundle: entry %q declares %d bytes, over the per-file %d cap — refusing (size limit exceeded)", f.Name, f.UncompressedSize64, maxEntrySize)
		}
		total += f.UncompressedSize64
		if total > maxTotalSize {
			return nil, fmt.Errorf("bundle: total declared size over the %d cap — refusing (total size limit exceeded)", maxTotalSize)
		}
	}
	if manifestFile == nil {
		return nil, fmt.Errorf("bundle: missing %s manifest", manifestMember)
	}

	// --- (3) parse the size-capped manifest ---
	manifest, err := readManifest(manifestFile)
	if err != nil {
		return nil, err
	}
	if manifest.FormatVersion > formatVersion {
		return nil, fmt.Errorf("bundle: format version %d is newer than this ferry supports (%d) — upgrade ferry to import this bundle", manifest.FormatVersion, formatVersion)
	}

	// --- (4) manifest<->payload equality + per-entry checks ---
	// Build the raw payload map, REJECTING duplicate raw member names: two members
	// with the same name would silently overwrite in the map, leaving one member
	// unvalidated while the bundle still passed the equality rule. A duplicate raw
	// name is by itself an invalid bundle.
	payloadByName := make(map[string]*zip.File, len(payload))
	for _, f := range payload {
		if _, dup := payloadByName[f.Name]; dup {
			return nil, fmt.Errorf("bundle: duplicate payload member name %q — refusing", f.Name)
		}
		payloadByName[f.Name] = f
	}

	// Whether the bundle actually carries local-layer members. If it does but the
	// manifest says include_local=false, the bundle is internally inconsistent (a
	// writer that stamped the flag false must not have added the local layer).
	hasLocalEntries := false

	validated := make([]ValidatedEntry, 0, len(manifest.Entries))
	fold := map[string]string{}
	manifestNames := map[string]bool{}
	var actualTotal uint64 // cumulative ACTUAL decompressed bytes, capped at maxTotalSize

	for _, e := range manifest.Entries {
		clean, err := canonicalRel(e.Path)
		if err != nil {
			return nil, fmt.Errorf("bundle: manifest entry %q: %w", e.Path, err)
		}
		if clean != e.Path {
			return nil, fmt.Errorf("bundle: manifest entry %q is not in canonical form (%q)", e.Path, clean)
		}
		if prev, dup := fold[foldKey(clean)]; dup {
			return nil, fmt.Errorf("bundle: duplicate entry %q collides with %q (case-fold) — refusing", clean, prev)
		}
		fold[foldKey(clean)] = clean
		manifestNames[clean] = true

		isLocal := isLocalLayerPath(clean)
		if isLocal {
			hasLocalEntries = true
		}

		f, ok := payloadByName[clean]
		if !ok {
			return nil, fmt.Errorf("bundle: manifest lists %q but the bundle has no such member — refusing", clean)
		}
		if !f.Mode().IsRegular() {
			return nil, fmt.Errorf("bundle: entry %q is not a regular file (symlink/special refused)", clean)
		}
		// Reject setuid/setgid/sticky members outright — a payload dotfile never needs
		// them and they are an attack surface. IsRegular() above already excludes type
		// bits; this rejects the remaining dangerous permission bits.
		if f.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return nil, fmt.Errorf("bundle: entry %q has a setuid/setgid/sticky bit (refused)", clean)
		}
		if int64(f.UncompressedSize64) != e.Size {
			return nil, fmt.Errorf("bundle: entry %q declares size %d but the manifest says %d — refusing", clean, f.UncompressedSize64, e.Size)
		}

		// Decompress + hash + secret-gate the content. Bound the ACTUAL read to the
		// DECLARED size (e.Size), not the global per-file cap: a member with a tiny
		// declared size that decompresses far larger (many such members could each hit
		// the per-file cap and blow the total cap) is caught here, and cumulative actual
		// bytes are checked against the total cap.
		got, n, content, err := readAndHash(f, e.Size)
		if err != nil {
			return nil, fmt.Errorf("bundle: read entry %q: %w", clean, err)
		}
		if n != e.Size {
			return nil, fmt.Errorf("bundle: entry %q size %d != manifest %d — refusing", clean, n, e.Size)
		}
		actualTotal += uint64(n)
		if actualTotal > maxTotalSize {
			return nil, fmt.Errorf("bundle: decompressed total over the %d cap — refusing (total size limit exceeded)", maxTotalSize)
		}
		if !strings.EqualFold(got, e.SHA256) {
			return nil, fmt.Errorf("bundle: entry %q sha256 mismatch (integrity/checksum failure) — refusing", clean)
		}
		// Symmetric secret re-gate with export (defense in depth): classify text vs
		// binary the SAME way export does (a NUL byte marks binary), then apply the SAME
		// check export applied. A TEXT payload runs the line-based text gate; a BINARY
		// payload runs the binary-safe key-marker scan (secret.HasKeyMarker). Using the
		// text gate on binary bytes would both mis-scan it and DIVERGE from export — a
		// binary that export bundled could then fail its own import. This way an export
		// always imports, and a binary carrying key material is refused on both sides.
		if isProbablyText(content) {
			if secret.IsBlockedFromRepo(string(content)) {
				return nil, fmt.Errorf("bundle: entry %q contains a secret — refusing to import", clean)
			}
		} else if secret.HasKeyMarker(content) {
			return nil, fmt.Errorf("bundle: entry %q contains key material — refusing to import", clean)
		}

		validated = append(validated, ValidatedEntry{Path: clean, Size: e.Size, file: f, local: isLocal})
	}

	// Equality: no unlisted payload member (ferry-bundle.json already excluded).
	for _, f := range payload {
		if !manifestNames[f.Name] {
			return nil, fmt.Errorf("bundle: payload member %q has no manifest entry (equality rule) — refusing", f.Name)
		}
	}

	// Consistency: a bundle carrying local-layer members MUST declare include_local.
	// (The reverse — include_local=true with no local members — is allowed: an empty
	// local layer is fine.)
	if hasLocalEntries && !manifest.IncludeLocal {
		return nil, fmt.Errorf("bundle: carries local-layer entries but its manifest says include_local=false (inconsistent) — refusing")
	}

	// Double opt-in: local-layer entries are only handed to the caller for extraction
	// when the bundle declares include_local AND the importer asked for it. Everything
	// was validated/hashed/secret-scanned above regardless; here we merely filter what
	// Extract will write.
	returnLocal := manifest.IncludeLocal && includeLocalWanted
	entries := validated
	if !returnLocal {
		entries = make([]ValidatedEntry, 0, len(validated))
		for _, ve := range validated {
			if ve.local {
				continue
			}
			entries = append(entries, ve)
		}
	}

	closeOnErr = false
	return &Validated{
		Entries:      entries,
		IncludeLocal: manifest.IncludeLocal,
		FerryVersion: manifest.FerryVersion,
		OverallSHA:   overall,
		reader:       zr,
	}, nil
}

// isProbablyText reports whether payload bytes are text (safe for the line-based
// text secret gate) or binary. A NUL byte marks it binary. This MUST classify a
// given payload identically to cmd/export's isProbablyText so the import re-gate is
// symmetric with export: text → text gate, binary → key-marker scan. Replicated
// (not shared) because internal/bundle cannot import cmd; both are the same trivial
// NUL check.
func isProbablyText(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}

// isLocalLayerPath reports whether a canonical relative path is part of the local
// layer — `local/**` or the top-level `ferry.local.toml`. The local layer is gated
// behind the writer's include_local AND the importer's --include-local (double
// opt-in); non-local entries always extract.
func isLocalLayerPath(clean string) bool {
	return clean == "ferry.local.toml" || strings.HasPrefix(clean, "local/")
}

// readManifest reads the size-capped manifest member (its size was already checked
// against maxManifestSize in the central-directory pass) and parses it. The
// io.LimitReader is a second guard in case a crafted local header understates the
// central-directory size.
func readManifest(f *zip.File) (bundleManifest, error) {
	rc, err := f.Open()
	if err != nil {
		return bundleManifest{}, fmt.Errorf("bundle: open manifest: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxManifestSize+1))
	if err != nil {
		return bundleManifest{}, fmt.Errorf("bundle: read manifest: %w", err)
	}
	if uint64(len(data)) > maxManifestSize {
		return bundleManifest{}, fmt.Errorf("bundle: manifest exceeds the %d cap on read", maxManifestSize)
	}
	var m bundleManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return bundleManifest{}, fmt.Errorf("bundle: manifest is not valid JSON: %w", err)
	}
	return m, nil
}

// readAndHash decompresses a payload member, returning its hex SHA256, byte count,
// and content. The read is bounded to declaredSize+1 bytes: a member that decompresses
// LARGER than its manifest-declared size is caught (the caller rejects n != declared),
// so a tiny-declared entry cannot secretly decompress up to the global per-file cap and
// blow the total budget. declaredSize already passed the central-directory per-file cap.
func readAndHash(f *zip.File, declaredSize int64) (string, int64, []byte, error) {
	rc, err := f.Open()
	if err != nil {
		return "", 0, nil, err
	}
	defer rc.Close()
	// LimitReader bounds the actual read to one byte over the DECLARED size (never more
	// than the global per-file cap), so a lying local header or an over-decompressing
	// member reads at most min(declared, maxEntrySize)+1 — the caller then rejects any
	// n != declared.
	limit := declaredSize + 1
	if limit > maxEntrySize+1 {
		limit = maxEntrySize + 1
	}
	var buf bytes.Buffer
	sum, n, err := hashReaderTee(io.LimitReader(rc, limit), &buf)
	if err != nil {
		return "", n, nil, err
	}
	return sum, n, buf.Bytes(), nil
}

// hashReaderTee streams r through SHA256 while copying it into sink, returning the
// hex digest and byte count. Wraps hashReader's contract but also captures content.
func hashReaderTee(r io.Reader, sink io.Writer) (string, int64, error) {
	return hashReader(io.TeeReader(r, sink))
}

// Extract materialises every validated entry into a FRESH, private staging directory
// that this call creates and owns, and returns that directory's path. It does NOT
// write into the caller's final location — the caller (cmd/import) MOVES the returned
// dir into place after its own no-clobber check.
//
// Extracting into a directory ferry just created (0700, empty) is the core defense
// against a symlinked-parent TOCTOU: a lexical under-the-target string check can be
// defeated when a parent component of the final target already exists as a symlink to
// outside, so MkdirAll/OpenFile would follow it. Here there are no pre-existing
// components to follow — the tree is empty and ours. As defense in depth it still
// Lstats each directory component it builds and refuses if any turns out to be a
// symlink, and re-canonicalises + re-confines every entry under the staging root.
//
// stagingParent is where the private temp dir is created (e.g. the caller's chosen
// parent of the final target, so the later move is a same-filesystem rename). Pass ""
// to use the OS temp dir. On ANY error the staging dir is removed, so extraction is
// all-or-nothing and no partial output survives.
func (v *Validated) Extract(stagingParent string) (stagingDir string, err error) {
	stagingDir, err = os.MkdirTemp(stagingParent, "ferry-import-")
	if err != nil {
		return "", fmt.Errorf("bundle: create staging dir: %w", err)
	}
	// Own the dir at 0700 (MkdirTemp already uses 0700, but be explicit).
	if err := os.Chmod(stagingDir, 0o700); err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", fmt.Errorf("bundle: harden staging dir: %w", err)
	}
	root, err := filepath.Abs(stagingDir)
	if err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", err
	}
	root = filepath.Clean(root)

	for _, e := range v.Entries {
		clean, err := canonicalRel(e.Path)
		if err != nil {
			_ = os.RemoveAll(stagingDir)
			return "", fmt.Errorf("bundle: refusing to extract %q: %w", e.Path, err)
		}
		dest := filepath.Join(root, filepath.FromSlash(clean))
		if !underDir(root, dest) {
			_ = os.RemoveAll(stagingDir)
			return "", fmt.Errorf("bundle: entry %q would escape the staging dir — refusing", e.Path)
		}
		if err := mkdirNoSymlink(root, filepath.Dir(dest)); err != nil {
			_ = os.RemoveAll(stagingDir)
			return "", fmt.Errorf("bundle: mkdir for %q: %w", clean, err)
		}
		if err := writeMember(e.file, dest, e.Size); err != nil {
			_ = os.RemoveAll(stagingDir)
			return "", fmt.Errorf("bundle: extract %q: %w", clean, err)
		}
	}
	return stagingDir, nil
}

// mkdirNoSymlink creates dir and any missing parents UP TO (but not below) root,
// building the tree component by component and Lstat-refusing any existing component
// that is a symlink. root itself is trusted (ferry just created it). Because the tree
// starts empty and ferry-owned, this only ever meets dirs ferry made — the Lstat
// checks are belt-and-braces against a component racing in as a symlink.
func mkdirNoSymlink(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := root
	for _, seg := range strings.Split(rel, string(os.PathSeparator)) {
		if seg == "" {
			continue
		}
		cur = filepath.Join(cur, seg)
		fi, statErr := os.Lstat(cur)
		switch {
		case statErr == nil:
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing to descend through symlinked component %q", cur)
			}
			if !fi.IsDir() {
				return fmt.Errorf("component %q exists and is not a directory", cur)
			}
		case os.IsNotExist(statErr):
			if err := os.Mkdir(cur, 0o755); err != nil {
				return err
			}
		default:
			return statErr
		}
	}
	return nil
}

// writeMember streams one member to dest (O_EXCL, mode 0644), re-verifying its
// size as it writes; archive/zip validates the member CRC32 as the reader reaches
// EOF (the read here runs through EOF via wantSize+1), so a corrupted stream errors
// from io.Copy. Validate verified the content SHA256; here only the size is
// re-checked (the CRC32 covers content integrity between validate and extract).
// On any mismatch it removes the partial file and errors.
func writeMember(f *zip.File, dest string, wantSize int64) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, io.LimitReader(rc, wantSize+1))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dest)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return closeErr
	}
	if n != wantSize {
		_ = os.Remove(dest)
		return fmt.Errorf("size changed during extract (%d != %d)", n, wantSize)
	}
	return nil
}

// underDir reports whether path is dir itself or a strict descendant, by pure path
// arithmetic (both must be clean+absolute). The zip-slip confinement predicate.
func underDir(dir, path string) bool {
	if path == dir {
		return true
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// foldKey normalises a path for case-fold duplicate detection. Case-insensitive
// filesystems (default macOS) treat File.txt and file.txt as the same file, so a
// bundle carrying both would clobber on extract — refuse at validation.
func foldKey(p string) string { return strings.ToLower(p) }

// fileSHA256 returns the hex SHA256 of the whole file at path, streaming it.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum, _, err := hashReader(f)
	if err != nil {
		return "", err
	}
	return sum, nil
}
