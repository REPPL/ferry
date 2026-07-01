package bundle

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// Source is one payload file the caller (cmd/export) has already enumerated
// (git ls-files), secret-gated (content + path name), and cleared against the
// ~/.ssh guard (safeRepoPath). RelPath is the canonical forward-slash path the
// entry gets in the bundle.
//
// The caller has ALREADY read and validated the content once (behind the symlink
// guard). To close the read TOCTOU — a parent dir swapped to a symlink between the
// caller's guarded read and a second open here — the caller passes that vetted
// content in Data and Write uses it verbatim, never re-opening AbsPath. AbsPath is
// retained only for diagnostics/messages.
type Source struct {
	RelPath string
	AbsPath string
	Data    []byte
}

// Write builds a bundle at outPath from the given payload sources plus a
// ferry-bundle.json manifest, and returns the OVERALL SHA256 of the written file
// (the out-of-band anchor export prints). The caller has already gated the
// sources; Write adds defense in depth:
//
//   - each source path is re-canonicalised (canonicalRel) and re-rejected if it
//     traverses, is absolute, is a VCS/control path, or has a .ssh component;
//   - each source must be a REGULAR file — a symlink or special file is refused
//     (Lstat, no follow), so the writer never smuggles link/device content;
//   - each member is recorded with a SAFE mode (0644, or 0755 when any execute bit
//     is set) — setuid/setgid/sticky and type bits are stripped.
//
// ferryVersion is stamped into the manifest; includeLocal records whether the
// caller added the local layer (import re-gates it). Sources must be non-empty and
// free of duplicate (case-folded) paths; Write refuses either. Each source must
// carry its already-vetted content in Data (Write never re-opens AbsPath — that
// would reintroduce a read TOCTOU past the caller's symlink guard).
func Write(outPath, ferryVersion string, includeLocal bool, sources []Source) (string, error) {
	if len(sources) == 0 {
		return "", fmt.Errorf("bundle: no payload files to write")
	}

	// Sort by path for a deterministic member order and stable manifest, and detect
	// case-fold duplicates before touching the filesystem.
	sorted := make([]Source, len(sources))
	copy(sorted, sources)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RelPath < sorted[j].RelPath })
	seen := map[string]string{}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	manifest := bundleManifest{
		FormatVersion: formatVersion,
		FerryVersion:  ferryVersion,
		IncludeLocal:  includeLocal,
	}

	for _, src := range sorted {
		rel, err := canonicalRel(src.RelPath)
		if err != nil {
			return "", fmt.Errorf("bundle: refusing entry %q: %w", src.RelPath, err)
		}
		if prev, dup := seen[foldKey(rel)]; dup {
			return "", fmt.Errorf("bundle: duplicate entry %q collides with %q (case-fold)", rel, prev)
		}
		seen[foldKey(rel)] = rel

		// Content is the caller's already-vetted, in-memory bytes: Write never
		// re-opens AbsPath, so a parent dir swapped to a symlink after the caller's
		// guarded read cannot be followed here (read TOCTOU closed). Lstat is used
		// only to derive a portable member mode; on any stat failure fall back to a
		// safe default rather than re-reading content.
		if src.Data == nil {
			return "", fmt.Errorf("bundle: entry %q has no in-memory content (caller must supply Source.Data)", rel)
		}
		mode := os.FileMode(0o644)
		if fi, statErr := os.Lstat(src.AbsPath); statErr == nil {
			mode = safeMode(fi.Mode())
		}

		data := src.Data
		sum := sha256.Sum256(data)

		hdr := &zip.FileHeader{Name: rel, Method: zip.Deflate}
		hdr.SetMode(mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return "", fmt.Errorf("bundle: create member %q: %w", rel, err)
		}
		if _, err := w.Write(data); err != nil {
			return "", fmt.Errorf("bundle: write member %q: %w", rel, err)
		}

		manifest.Entries = append(manifest.Entries, bundleEntry{
			Path:   rel,
			Size:   int64(len(data)),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("bundle: marshal manifest: %w", err)
	}
	mh := &zip.FileHeader{Name: manifestMember, Method: zip.Deflate}
	mh.SetMode(0o644)
	mw, err := zw.CreateHeader(mh)
	if err != nil {
		return "", fmt.Errorf("bundle: create manifest member: %w", err)
	}
	if _, err := mw.Write(manifestJSON); err != nil {
		return "", fmt.Errorf("bundle: write manifest member: %w", err)
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("bundle: finalise zip: %w", err)
	}

	// Write the file, then hash the exact bytes on disk so the returned anchor is
	// the SHA256 the user will re-compute over the delivered file.
	if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
		return "", fmt.Errorf("bundle: write %q: %w", outPath, err)
	}
	overall := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(overall[:]), nil
}

// safeMode reduces an on-disk mode to a portable payload mode: 0755 when any
// execute bit is set, else 0644. Type bits, setuid/setgid/sticky, and group/other
// nuances are dropped — extraction never needs them and they are an attack surface.
func safeMode(m os.FileMode) os.FileMode {
	if m.Perm()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// hashReader streams r through SHA256 without buffering it whole, returning the
// hex digest and the number of bytes read. Used by Validate to hash a decompressed
// entry after its declared size has already passed the caps.
func hashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
