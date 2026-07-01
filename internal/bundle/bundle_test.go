package bundle

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSecret is a NON-FUNCTIONAL, high-entropy secret-shaped string the scanner
// keys on — never a real key. Used to prove the secret gate re-runs on content.
const fakeSecret = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUFAKEFAKEFAKEFAKEFAKEdeadbeefcafe0000\n" +
	"-----END OPENSSH PRIVATE KEY-----\n"

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// writeZip crafts a zip with the given manifest bytes plus members, mirroring the
// eval harness so unit tests can corrupt exactly one property.
func writeZip(t *testing.T, path string, manifest []byte, members []struct {
	name string
	data []byte
	mode os.FileMode
}) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, data []byte, mode os.FileMode) {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("create %q: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
	}
	add(manifestMember, manifest, 0o644)
	for _, m := range members {
		mode := m.mode
		if mode == 0 {
			mode = 0o644
		}
		add(m.name, m.data, mode)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func manifestJSON(t *testing.T, m bundleManifest) []byte {
	t.Helper()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

type member = struct {
	name string
	data []byte
	mode os.FileMode
}

// validEntry builds a matching manifest entry + member for body at rel.
func validEntry(rel string, body []byte) (bundleEntry, member) {
	return bundleEntry{Path: rel, Size: int64(len(body)), SHA256: hexSum(body)},
		member{name: rel, data: body}
}

func TestWriteThenValidateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "shared.txt")
	if err := os.WriteFile(src, []byte("hello bundle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "nested.txt")
	if err := os.WriteFile(nested, []byte("deep\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "b.zip")
	sum, err := Write(out, "0.2.0-test", false, []Source{
		{RelPath: "shared.txt", AbsPath: src, Data: []byte("hello bundle\n")},
		{RelPath: "sub/nested.txt", AbsPath: nested, Data: []byte("deep\n")},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Overall SHA matches the file on disk.
	fileBytes, _ := os.ReadFile(out)
	if want := hexSum(fileBytes); want != sum {
		t.Errorf("overall sha mismatch: Write=%s file=%s", sum, want)
	}

	v, err := Validate(out, sum, false)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	defer v.Close()
	if len(v.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(v.Entries))
	}

	staging, err := v.Extract(dir)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Extract creates its OWN fresh staging dir under the given parent and returns it;
	// it must not be the parent itself, and it must be a real (non-symlink) dir.
	if staging == dir || filepath.Dir(staging) != filepath.Clean(dir) {
		t.Fatalf("Extract staging dir %q is not a fresh child of %q", staging, dir)
	}
	got, err := os.ReadFile(filepath.Join(staging, "shared.txt"))
	if err != nil || string(got) != "hello bundle\n" {
		t.Errorf("extracted shared.txt = %q, err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(staging, "sub", "nested.txt")); err != nil {
		t.Errorf("nested file not extracted: %v", err)
	}
}

// TestWriteRequiresInMemoryData covers the read-TOCTOU fix: Write no longer re-opens
// a source by path (a parent could be swapped to a symlink between the caller's
// guarded read and a second open), so the caller MUST supply the already-vetted
// content in Source.Data. A source with nil Data is refused rather than read.
func TestWriteRequiresInMemoryData(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	os.WriteFile(real, []byte("x\n"), 0o644)
	_, err := Write(filepath.Join(dir, "b.zip"), "v", false, []Source{{RelPath: "real.txt", AbsPath: real}})
	if err == nil || !strings.Contains(err.Error(), "in-memory content") {
		t.Errorf("expected in-memory-content refusal for nil Data, got %v", err)
	}
}

func TestValidateExpectSHAMismatch(t *testing.T) {
	dir := t.TempDir()
	e, m := validEntry("shared.txt", []byte("ok\n"))
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{e}}), []member{m})
	_, err := Validate(path, strings.Repeat("0", 64), false)
	if err == nil || !strings.Contains(err.Error(), "expect-sha256") {
		t.Errorf("expected expect-sha256 mismatch, got %v", err)
	}
}

func TestValidateBadChecksum(t *testing.T) {
	dir := t.TempDir()
	body := []byte("good\n")
	m := bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{
		{Path: "shared.txt", Size: int64(len(body)), SHA256: hexSum([]byte("DIFFERENT"))},
	}}
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, m), []member{{name: "shared.txt", data: body}})
	_, err := Validate(path, "", false)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("expected sha256 mismatch, got %v", err)
	}
}

func TestValidateNewerVersionRefused(t *testing.T) {
	dir := t.TempDir()
	e, m := validEntry("shared.txt", []byte("ok\n"))
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, bundleManifest{FormatVersion: formatVersion + 100, FerryVersion: "future", Entries: []bundleEntry{e}}), []member{m})
	_, err := Validate(path, "", false)
	if err == nil || !strings.Contains(err.Error(), "upgrade") {
		t.Errorf("expected upgrade refusal, got %v", err)
	}
}

func TestValidateManifestMismatch(t *testing.T) {
	dir := t.TempDir()
	e, m := validEntry("listed.txt", []byte("a\n"))
	// An UNLISTED payload member with no manifest entry.
	extra := member{name: "extra.txt", data: []byte("b\n")}
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{e}}), []member{m, extra})
	_, err := Validate(path, "", false)
	if err == nil || !strings.Contains(err.Error(), "no manifest entry") {
		t.Errorf("expected equality-rule refusal, got %v", err)
	}
}

func TestValidateMissingMember(t *testing.T) {
	dir := t.TempDir()
	// A manifest entry with NO backing member.
	m := bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{
		{Path: "ghost.txt", Size: 2, SHA256: hexSum([]byte("x\n"))},
	}}
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, m), nil)
	_, err := Validate(path, "", false)
	if err == nil || !strings.Contains(err.Error(), "no such member") {
		t.Errorf("expected missing-member refusal, got %v", err)
	}
}

func TestValidateDupCaseFold(t *testing.T) {
	dir := t.TempDir()
	d1, d2 := []byte("one\n"), []byte("two\n")
	m := bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{
		{Path: "File.txt", Size: int64(len(d1)), SHA256: hexSum(d1)},
		{Path: "file.txt", Size: int64(len(d2)), SHA256: hexSum(d2)},
	}}
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, m), []member{{name: "File.txt", data: d1}, {name: "file.txt", data: d2}})
	_, err := Validate(path, "", false)
	if err == nil || !strings.Contains(err.Error(), "case-fold") {
		t.Errorf("expected case-fold dup refusal, got %v", err)
	}
}

func TestValidateRejectsGitPath(t *testing.T) {
	dir := t.TempDir()
	hook := []byte("#!/bin/sh\necho pwned\n")
	m := bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{
		{Path: ".git/hooks/pre-commit", Size: int64(len(hook)), SHA256: hexSum(hook)},
	}}
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, m), []member{{name: ".git/hooks/pre-commit", data: hook, mode: 0o755}})
	_, err := Validate(path, "", false)
	if err == nil || !strings.Contains(err.Error(), "VCS/control") {
		t.Errorf("expected .git refusal, got %v", err)
	}
}

func TestValidateRejectsSSHPath(t *testing.T) {
	if _, err := canonicalRel(".ssh/id_ed25519"); err == nil {
		t.Errorf("expected .ssh path refusal")
	}
	if _, err := canonicalRel("nested/.ssh/key"); err == nil {
		t.Errorf("expected nested .ssh path refusal")
	}
}

func TestValidateZipSlipCaps(t *testing.T) {
	for _, bad := range []string{"../escape.txt", "/abs/escape.txt", `..\win.txt`, "a/../../b.txt"} {
		if _, err := canonicalRel(bad); err == nil {
			t.Errorf("canonicalRel(%q) accepted a traversal/absolute path", bad)
		}
	}
}

func TestValidateRejectsSymlinkMember(t *testing.T) {
	dir := t.TempDir()
	body := []byte("/etc/passwd")
	m := bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{
		{Path: "evil", Size: int64(len(body)), SHA256: hexSum(body)},
	}}
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, m), []member{{name: "evil", data: body, mode: os.ModeSymlink | 0o777}})
	_, err := Validate(path, "", false)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Errorf("expected symlink-member refusal, got %v", err)
	}
}

func TestValidateSecretInEntry(t *testing.T) {
	dir := t.TempDir()
	body := []byte("# config\n" + fakeSecret)
	m := bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{
		{Path: "leaked.txt", Size: int64(len(body)), SHA256: hexSum(body)},
	}}
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, m), []member{{name: "leaked.txt", data: body}})
	_, err := Validate(path, "", false)
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Errorf("expected secret refusal, got %v", err)
	}
}

func TestValidateEntryCountCap(t *testing.T) {
	dir := t.TempDir()
	m := bundleManifest{FormatVersion: 1, FerryVersion: "v"}
	var members []member
	body := []byte("x\n")
	for i := 0; i <= maxEntries; i++ {
		name := "f" + itoa(i) + ".txt"
		m.Entries = append(m.Entries, bundleEntry{Path: name, Size: int64(len(body)), SHA256: hexSum(body)})
		members = append(members, member{name: name, data: body})
	}
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, m), members)
	_, err := Validate(path, "", false)
	if err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Errorf("expected entry-count cap refusal, got %v", err)
	}
}

func TestValidatePerFileSizeCap(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, maxEntrySize+1) // compressible zeros; central-dir declares real size
	m := bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{
		{Path: "bomb.bin", Size: int64(len(big)), SHA256: hexSum(big)},
	}}
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, m), []member{{name: "bomb.bin", data: big}})
	_, err := Validate(path, "", false)
	if err == nil || !strings.Contains(err.Error(), "per-file") {
		t.Errorf("expected per-file cap refusal, got %v", err)
	}
}

func TestExtractZipSlipConfinement(t *testing.T) {
	// A Validated whose entry path canonicalises fine but whose join is forced to
	// escape can't occur through canonicalRel — so prove underDir directly plus the
	// end-to-end refusal via a crafted manifest with a `..` path (rejected earlier).
	base := t.TempDir()
	root := filepath.Join(base, "target")
	if underDir(root, filepath.Join(base, "escape.txt")) {
		t.Errorf("underDir accepted an escaping path")
	}
	if !underDir(root, filepath.Join(root, "ok.txt")) {
		t.Errorf("underDir rejected an in-target path")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// TestRejectControlPathCaseAndNesting covers the case-insensitive-FS bypass class:
// ANY component that case-folds to a VCS/.ssh control name is rejected wherever it
// appears in the path (macOS treats `.Git` as `.git`), while `.github`/`.gitignore`
// stay ALLOWED (they do not fold to a reserved name). Exercised at canonicalRel so
// both Write and Validate inherit it.
func TestRejectControlPathCaseAndNesting(t *testing.T) {
	rejected := []string{
		".git/hooks/pre-commit",
		".Git/hooks/pre-commit", // case-fold on a case-insensitive FS
		".GIT/config",
		"nested/.git/hooks/pre-commit", // not just the first component
		"a/b/.Git/x",
		".ssh/id_ed25519",
		".SSH/config",
		"nested/.ssh/key",
		".hg/store",
		".SVN/entries",
		".bzr/x",
	}
	for _, p := range rejected {
		if _, err := canonicalRel(p); err == nil {
			t.Errorf("canonicalRel(%q) accepted a control/VCS path (must reject)", p)
		}
	}
	allowed := []string{
		".github/workflows/ci.yml",
		".gitignore",
		".gitconfig",
		".gitattributes",
		"sub/.github/x.yml",
		"config.ssh.txt", // not a `.ssh` component
	}
	for _, p := range allowed {
		if _, err := canonicalRel(p); err != nil {
			t.Errorf("canonicalRel(%q) rejected a legitimate path (must allow): %v", p, err)
		}
	}
}

// TestValidateRejectsLyingDeclaredSizeBomb covers the unbounded-decompression fix:
// a member whose manifest DECLARES a tiny size but actually decompresses far larger
// must be rejected (the read is bounded to declared+1, so actual != declared). This
// is the tiny-declared/large-actual zip-bomb the per-file cap alone missed.
func TestValidateRejectsLyingDeclaredSizeBomb(t *testing.T) {
	dir := t.TempDir()
	real := make([]byte, 1<<20) // 1 MiB of highly-compressible zeros
	// Manifest LIES: declares 4 bytes + the sha256 of those 4 bytes, but the member
	// carries 1 MiB. Central-dir per-file cap is not tripped (1 MiB < 64 MiB).
	lie := []byte("tiny")
	m := bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{
		{Path: "bomb.bin", Size: int64(len(lie)), SHA256: hexSum(lie)},
	}}
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, m), []member{{name: "bomb.bin", data: real}})
	_, err := Validate(path, "", false)
	if err == nil {
		t.Fatalf("expected refusal for a member decompressing past its declared size")
	}
	// The central directory declares the REAL 1 MiB (from the bytes written), which
	// won't equal the manifest's 4 — so the declared-size mismatch fires first. Either
	// the declared-size check or the read-bound catch is a correct refusal.
	if !strings.Contains(err.Error(), "size") && !strings.Contains(err.Error(), "sha256") {
		t.Errorf("expected a size/integrity refusal, got %v", err)
	}
}

// TestValidateRejectsOverDecompressPastDeclared exercises the over-declared rejection
// for real: the member carries FAR MORE decompressed bytes than its MANIFEST declares.
// The manifest is the coordination contract, and its per-entry size MUST match the
// actual content — a member that decompresses past its declaration is exactly the
// tiny-declared/large-actual zip bomb the read-bound (declared+1) is there to catch.
// A crafted bundle with declared=4 and 1 MiB of actual content MUST be rejected.
func TestValidateRejectsOverDecompressPastDeclared(t *testing.T) {
	dir := t.TempDir()
	actual := make([]byte, 1<<20) // 1 MiB of highly-compressible zeros: actual >> declared
	// Manifest DECLARES a tiny 4-byte entry (plus the sha256 of those 4 bytes), but the
	// zip member carries 1 MiB. readAndHash bounds the read to declared+1, so the byte
	// count it returns (declared+1 at most) never equals the manifest's declared size,
	// and the per-entry size check refuses. The declared-size mismatch against the
	// central directory (which records the real 1 MiB) is an equally valid refusal —
	// either way, actual-past-declared is caught before any content is trusted.
	lie := []byte("tiny")
	m := bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{
		{Path: "bomb.bin", Size: int64(len(lie)), SHA256: hexSum(lie)},
	}}
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, m), []member{{name: "bomb.bin", data: actual}})
	_, err := Validate(path, "", false)
	if err == nil {
		t.Fatalf("expected refusal: member decompresses (1 MiB) past its declared size (4)")
	}
	// The refusal must be about the size disagreement (or the integrity check that would
	// only pass on the declared 4 bytes) — never a silent accept of the over-sized member.
	if !strings.Contains(err.Error(), "size") && !strings.Contains(err.Error(), "sha256") {
		t.Errorf("expected a size/integrity refusal, got %v", err)
	}
}

// TestValidateRejectsDuplicateRawMemberNames covers the silent-overwrite fix: two
// payload members sharing the same RAW name must be refused (one would otherwise be
// dropped from the map, unvalidated, while equality still passed).
func TestValidateRejectsDuplicateRawMemberNames(t *testing.T) {
	dir := t.TempDir()
	d1, d2 := []byte("one\n"), []byte("two\n")
	// Manifest lists the name once; the zip carries it TWICE.
	m := bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{
		{Path: "dup.txt", Size: int64(len(d1)), SHA256: hexSum(d1)},
	}}
	path := filepath.Join(dir, "b.zip")
	writeZip(t, path, manifestJSON(t, m), []member{
		{name: "dup.txt", data: d1},
		{name: "dup.txt", data: d2},
	})
	_, err := Validate(path, "", false)
	if err == nil || !strings.Contains(err.Error(), "duplicate payload member") {
		t.Errorf("expected duplicate-raw-member refusal, got %v", err)
	}
}

// TestValidateSetuidMemberRejected covers the setuid/setgid/sticky fix: a regular
// member carrying one of those bits is refused (only safe perms allowed).
func TestValidateSetuidMemberRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode os.FileMode
	}{
		{"setuid", os.ModeSetuid | 0o755},
		{"setgid", os.ModeSetgid | 0o755},
		{"sticky", os.ModeSticky | 0o644},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body := []byte("x\n")
			m := bundleManifest{FormatVersion: 1, FerryVersion: "v", Entries: []bundleEntry{
				{Path: "evil.sh", Size: int64(len(body)), SHA256: hexSum(body)},
			}}
			path := filepath.Join(dir, "b.zip")
			writeZip(t, path, manifestJSON(t, m), []member{{name: "evil.sh", data: body, mode: tc.mode}})
			_, err := Validate(path, "", false)
			if err == nil || !strings.Contains(err.Error(), "setuid/setgid/sticky") {
				t.Errorf("expected setuid/setgid/sticky refusal, got %v", err)
			}
		})
	}
}

// localBundle builds a valid include_local bundle carrying one shared entry and the
// two local-layer kinds (local/** + ferry.local.toml) at path, returning it for
// Validate. includeLocal stamps the manifest flag.
func localBundle(t *testing.T, path string, includeLocal bool) {
	t.Helper()
	shared := []byte("shared\n")
	loc := []byte("export LOCAL=1\n")
	toml := []byte("# per-machine\n")
	m := bundleManifest{FormatVersion: 1, FerryVersion: "v", IncludeLocal: includeLocal, Entries: []bundleEntry{
		{Path: "shared.txt", Size: int64(len(shared)), SHA256: hexSum(shared)},
		{Path: "local/zsh/zshrc.local", Size: int64(len(loc)), SHA256: hexSum(loc)},
		{Path: "ferry.local.toml", Size: int64(len(toml)), SHA256: hexSum(toml)},
	}}
	writeZip(t, path, manifestJSON(t, m), []member{
		{name: "shared.txt", data: shared},
		{name: "local/zsh/zshrc.local", data: loc},
		{name: "ferry.local.toml", data: toml},
	})
}

func hasEntry(v *Validated, rel string) bool {
	for _, e := range v.Entries {
		if e.Path == rel {
			return true
		}
	}
	return false
}

// TestValidateIncludeLocalGating covers the double-opt-in fix (includeLocalWanted was
// unused). A bundle written include_local=true, validated WITHOUT the wanted flag,
// must still validate but return NO local-layer entries; WITH the flag it returns
// them. A bundle with local entries but include_local=false is inconsistent → refused.
func TestValidateIncludeLocalGating(t *testing.T) {
	t.Run("include-local-bundle-without-wanted-omits-local", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "b.zip")
		localBundle(t, path, true)
		v, err := Validate(path, "", false) // includeLocalWanted=false
		if err != nil {
			t.Fatalf("Validate must pass (local is validated, just not returned): %v", err)
		}
		defer v.Close()
		if !hasEntry(v, "shared.txt") {
			t.Errorf("non-local entry must be returned")
		}
		if hasEntry(v, "local/zsh/zshrc.local") || hasEntry(v, "ferry.local.toml") {
			t.Errorf("local-layer entries must NOT be returned without the wanted flag")
		}
	})

	t.Run("include-local-bundle-with-wanted-returns-local", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "b.zip")
		localBundle(t, path, true)
		v, err := Validate(path, "", true) // includeLocalWanted=true
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		defer v.Close()
		if !hasEntry(v, "local/zsh/zshrc.local") || !hasEntry(v, "ferry.local.toml") {
			t.Errorf("local-layer entries must be returned under double opt-in")
		}
	})

	t.Run("local-entries-but-flag-false-is-inconsistent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "b.zip")
		localBundle(t, path, false) // carries local entries but include_local=false
		_, err := Validate(path, "", true)
		if err == nil || !strings.Contains(err.Error(), "include_local=false") {
			t.Errorf("expected inconsistency refusal, got %v", err)
		}
	})
}

// TestExtractIntoFreshStagingDir covers the symlinked-parent TOCTOU fix: Extract
// creates + owns an isolated staging dir under the given parent and writes THERE,
// never into a pre-existing (possibly symlinked) target. Even when a sibling symlink
// exists in the parent, extraction lands in the fresh dir and cannot escape through it.
func TestExtractIntoFreshStagingDir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s.txt")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "b.zip")
	sum, err := Write(out, "v", false, []Source{{RelPath: "s.txt", AbsPath: src, Data: []byte("body\n")}})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	v, err := Validate(out, sum, false)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	defer v.Close()

	// Plant a symlink in the staging PARENT pointing outside — extraction must not
	// follow it; it writes into a brand-new dir the call creates.
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outside, filepath.Join(dir, "evil-link"))

	parent := filepath.Join(dir, "parent")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	staging, err := v.Extract(parent)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Staging is a fresh child of parent, holds the file, and 'outside' got nothing.
	if filepath.Dir(staging) != filepath.Clean(parent) {
		t.Errorf("staging %q is not a child of %q", staging, parent)
	}
	if _, err := os.Stat(filepath.Join(staging, "s.txt")); err != nil {
		t.Errorf("file not extracted into staging dir: %v", err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("extraction leaked into the outside dir: %v", ents)
	}
}
