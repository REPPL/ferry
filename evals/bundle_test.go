package evals

// Route-3 (offline bundle) behavioral evals for `ferry export` / `ferry import`.
// Each test drives the REAL binary via the harness and asserts an OBSERVABLE
// outcome (zip members/bytes, files present/absent, exit codes, stdout wording,
// tripwires) — never ferry internals. They are RED-until-impl: requireBin(t) (via
// Sandbox.Ferry) SKIPS every test when FERRY_BIN is unset, so the package stays
// green before cmd/export.go + cmd/import.go + internal/bundle land.
//
// Crafted-malicious bundles (zip-slip, .git, dup, bad-checksum, newer-version,
// over-cap, symlink) are BUILT HERE with archive/zip + a hand-written
// ferry-bundle.json, so a hostile-input test never depends on export producing bad
// output.
//
// ── COORDINATION NOTE FOR THE IMPLEMENTATION ──────────────────────────────────
// The manifest JSON shape below (bundleManifest / bundleEntry) is a REASONABLE
// GUESS at what PLAN-v0.2.0-route3-bundle specifies: a top-level format version, a
// writer ferry version, an include-local flag, and a per-entry list of
// {path, size, sha256}. The PLAN does NOT nail down the exact JSON field names.
// The implementation MUST either (a) match these field names, or (b) update this
// struct + the manifestBytes() writer to the field names it chooses. The
// crafted-malicious tests depend on the manifest being parseable by the binary, so
// this is the single coordination point between these evals and the impl.
// The MANIFEST_MEMBER constant ("ferry-bundle.json") is likewise the assumed
// in-zip manifest member name; adjust in one place if the impl differs.

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// MANIFEST_MEMBER is the assumed in-zip manifest member name (PLAN: "ferry-bundle.json").
const manifestMember = "ferry-bundle.json"

// bundleFormatVersion is the assumed CURRENT format version the impl supports. The
// newer-version test uses a value strictly greater than this to trigger refusal.
const bundleFormatVersion = 1

// bundleEntry describes one payload file in the manifest (canonical relative path,
// uncompressed size, hex SHA256). Field names are a coordination point — see the
// file header note; the impl must match or adjust.
type bundleEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// bundleManifest is the assumed shape of ferry-bundle.json. Coordination point.
type bundleManifest struct {
	FormatVersion int           `json:"format_version"`
	FerryVersion  string        `json:"ferry_version"`
	IncludeLocal  bool          `json:"include_local"`
	Entries       []bundleEntry `json:"entries"`
}

// A fake, NON-FUNCTIONAL secret — realistic shape the scanner keys on, never a real key.
const fakeExportSecret = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUFAKEFAKEFAKEFAKEFAKEdeadbeefcafe0000\n" +
	"-----END OPENSSH PRIVATE KEY-----\n"

// -----------------------------------------------------------------------------
// Test-side bundle helpers (impl must NOT gain these — they live in test code).
// -----------------------------------------------------------------------------

// manifestBytes marshals a manifest to indented JSON.
func manifestBytes(t *testing.T, m bundleManifest) []byte {
	t.Helper()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("manifestBytes: %v", err)
	}
	return data
}

// sha256Hex returns the hex sha256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// zipMember is one crafted payload member: name, decompressed data, and mode
// (regular unless ModeSymlink/other special bit is set). The zip WRITER records the
// real (accurate) UncompressedSize64 in the central directory from the bytes
// written — so a member holding large but highly-compressible data yields a small
// on-disk zip whose CENTRAL DIRECTORY still declares the large uncompressed size
// (used by the per-file size-cap test to trip a metadata cap without a huge file).
type zipMember struct {
	name string
	data []byte
	mode os.FileMode
}

func writeCraftedZip(t *testing.T, path string, manifest []byte, members []zipMember) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	add := func(name string, data []byte, mode os.FileMode) {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("writeCraftedZip: create %q: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("writeCraftedZip: write %q: %v", name, err)
		}
	}

	// Manifest member first (mirrors export; order is not load-bearing for import).
	add(manifestMember, manifest, 0o644)
	for _, m := range members {
		mode := m.mode
		if mode == 0 {
			mode = 0o644
		}
		add(m.name, m.data, mode)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("writeCraftedZip: close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writeCraftedZip: write file: %v", err)
	}
}

// readZipMembers returns the set of member names in a zip (empty on error).
func readZipMembers(t *testing.T, path string) map[string]*zip.File {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("readZipMembers: open %q: %v", path, err)
	}
	t.Cleanup(func() { zr.Close() })
	out := map[string]*zip.File{}
	for _, f := range zr.File {
		out[f.Name] = f
	}
	return out
}

// zipMemberBytes returns the decompressed bytes of one member.
func zipMemberBytes(t *testing.T, f *zip.File) []byte {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatalf("zipMemberBytes: open %q: %v", f.Name, err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		t.Fatalf("zipMemberBytes: read %q: %v", f.Name, err)
	}
	return buf.Bytes()
}

// seedExportRepo turns s.Repo into a git working tree with a committed ferry.toml
// (the required shared manifest) plus the given extra tracked files, and points
// config.toml at the repo so `ferry export` can resolve scope. Returns nothing;
// callers add untracked/ignored/local files themselves.
func seedExportRepo(t *testing.T, s *Sandbox, tracked map[string]string) {
	t.Helper()
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest) // writes ferry.toml + config.toml -> s.Repo
	for rel, body := range tracked {
		s.WriteRepoFile(t, rel, body)
	}
	gitCommitAll(t, s.Repo, "seed export repo")
}

// bundleOutPath returns a per-test out path OUTSIDE the repo (a fresh temp dir).
func bundleOutPath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "ferry-bundle.zip")
}

// gitCommitPaths stages ONLY the named paths (not `git add -A`) and commits them,
// so a test can keep other working-tree files genuinely untracked.
func gitCommitPaths(t *testing.T, repo, msg string, paths ...string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(append([]string{"add", "--"}, paths...)...)
	run("commit", "-q", "-m", msg)
}

// gitForceAdd force-stages (`git add -f`) the named paths (bypassing .gitignore)
// and commits — used to track a normally-gitignored local-layer file for a test.
func gitForceAdd(t *testing.T, repo, msg string, paths ...string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(append([]string{"add", "-f", "--"}, paths...)...)
	run("commit", "-q", "-m", msg)
}

// gitLsFiles returns the tracked-file set (`git ls-files`) of the repo.
func gitLsFiles(t *testing.T, repo string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "ls-files")
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-files: %v\n%s", err, out)
	}
	return strings.Fields(string(out))
}

// containsString reports whether ss contains v.
func containsString(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// exportOK runs `ferry export --out <out>` on the seeded repo, requiring exit 0,
// and returns the out path. It is only reached when FERRY_BIN is set (otherwise
// Ferry skips).
func exportOK(t *testing.T, s *Sandbox, extraArgs ...string) string {
	t.Helper()
	out := bundleOutPath(t)
	args := append([]string{"export", "--out", out}, extraArgs...)
	stdout, stderr, code := s.Ferry(args...)
	if code != 0 {
		t.Fatalf("ferry export exited %d; stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("ferry export exited 0 but wrote no bundle at %s: %v", out, err)
	}
	return out
}

// craftValidBundle builds a minimal, internally-consistent VALID bundle at path
// with the given regular-file members, so the malicious tests can start from a
// good bundle and corrupt exactly ONE property. Returns the manifest used.
func craftValidBundle(t *testing.T, path string, includeLocal bool, files map[string]string) bundleManifest {
	t.Helper()
	m := bundleManifest{
		FormatVersion: bundleFormatVersion,
		FerryVersion:  "0.2.0-test",
		IncludeLocal:  includeLocal,
	}
	var members []zipMember
	for rel, body := range files {
		data := []byte(body)
		m.Entries = append(m.Entries, bundleEntry{Path: rel, Size: int64(len(data)), SHA256: sha256Hex(data)})
		members = append(members, zipMember{name: rel, data: data, mode: 0o644})
	}
	writeCraftedZip(t, path, manifestBytes(t, m), members)
	return m
}

// importTargetAbsent returns a fresh, NON-existent target dir path for import.
func importTargetAbsent(t *testing.T) string {
	return filepath.Join(t.TempDir(), "import-target")
}

// assertTargetLacks asserts the import target dir did not gain the given
// forbidden relative path (bundle content that must never land).
func assertTargetLacks(t *testing.T, target, rel string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(target, rel)); err == nil {
		t.Errorf("import laid down forbidden bundle path %q under the target", rel)
	}
}

// assertTargetEmpty asserts a rejected import left the target dir absent OR empty —
// the validate-before-extract contract means a hostile bundle produces NO payload at
// the target. Used by every import-refusal test so partial extraction cannot pass.
func assertTargetEmpty(t *testing.T, target string) {
	t.Helper()
	ents, err := os.ReadDir(target)
	if err != nil {
		if os.IsNotExist(err) {
			return // absent is the cleanest outcome
		}
		t.Fatalf("assertTargetEmpty: read %q: %v", target, err)
	}
	if len(ents) != 0 {
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("validate-before-extract: rejected import left %d entrie(s) at target %q: %v", len(ents), target, names)
	}
}

// -----------------------------------------------------------------------------
// Command surface
// -----------------------------------------------------------------------------

// TestBundleCommandsExist covers AC-export-exists / AC-import-exists: both verbs
// resolve (`--help` exits 0) and appear in top-level `ferry --help`.
func TestBundleCommandsExist_AC_export_import_exists(t *testing.T) {
	t.Parallel()

	for _, verb := range []string{"export", "import"} {
		verb := verb
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t) // fresh sandbox so requireBin's skip targets THIS subtest
			if _, _, code := s.Ferry(verb, "--help"); code != 0 {
				t.Errorf("AC-%s-exists: `ferry %s --help` exited %d (must resolve, exit 0)", verb, verb, code)
			}
		})
	}
	t.Run("in-top-level-help", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		out, errOut, _ := s.Ferry("--help")
		combined := out + errOut
		for _, verb := range []string{"export", "import"} {
			if !containsAnyFold(combined, verb) {
				t.Errorf("AC-%s-exists: `%s` not listed in `ferry --help`\n%s", verb, verb, combined)
			}
		}
	})
}

// -----------------------------------------------------------------------------
// Export — inclusion set
// -----------------------------------------------------------------------------

// TestExportTrackedOnly covers AC-export-tracked-only (C1): export bundles ONLY
// git-tracked files — an untracked file, a gitignored file, and an editor backup
// present in the working tree are NONE of them zip members.
func TestExportTrackedOnly_AC_export_tracked_only(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	seedExportRepo(t, s, map[string]string{
		"shared.txt": "tracked shared content\n",
	})
	// Add a gitignored file, an editor backup, and an untracked file — none tracked.
	s.WriteRepoFile(t, ".gitignore", "ignored.txt\n*~\n")
	s.WriteRepoFile(t, "untracked.txt", "untracked junk\n")
	s.WriteRepoFile(t, "ignored.txt", "ignored junk\n")
	s.WriteRepoFile(t, "shared.txt~", "editor backup junk\n")
	// Commit ONLY .gitignore (NOT `git add -A`) so untracked.txt / shared.txt~ stay
	// genuinely UNTRACKED and ignored.txt / *~ stay ignored. A `git add -A` here would
	// track the junk and defeat the very thing this AC checks.
	gitCommitPaths(t, s.Repo, "add gitignore", ".gitignore")
	// Sanity: git must report exactly the intended untracked/ignored set.
	if tracked := gitLsFiles(t, s.Repo); containsString(tracked, "untracked.txt") || containsString(tracked, "shared.txt~") || containsString(tracked, "ignored.txt") {
		t.Fatalf("test setup invariant: junk files leaked into git tracking: %v", tracked)
	}

	out := exportOK(t, s)
	members := readZipMembers(t, out)
	for _, forbidden := range []string{"untracked.txt", "ignored.txt", "shared.txt~"} {
		if _, ok := members[forbidden]; ok {
			t.Errorf("AC-export-tracked-only: non-tracked file %q leaked into the bundle", forbidden)
		}
	}
	if _, ok := members["shared.txt"]; !ok {
		t.Errorf("AC-export-tracked-only: tracked shared.txt missing from the bundle")
	}
}

// TestExportBundleShape covers AC-export-bundle: the zip is readable, carries a
// parseable ferry-bundle.json with per-entry canonical path+size+sha256, and the
// payload member set EXACTLY equals the manifest entry set (manifest member
// excluded from that comparison).
func TestExportBundleShape_AC_export_bundle(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	seedExportRepo(t, s, map[string]string{"shared.txt": "hello bundle\n"})

	out := exportOK(t, s)
	members := readZipMembers(t, out)

	mf, ok := members[manifestMember]
	if !ok {
		t.Fatalf("AC-export-bundle: %s missing from the bundle (members: %v)", manifestMember, keysOf(members))
	}
	var m bundleManifest
	if err := json.Unmarshal(zipMemberBytes(t, mf), &m); err != nil {
		t.Fatalf("AC-export-bundle: %s is not parseable with the assumed shape (COORDINATION: adjust bundleManifest to the impl's field names): %v", manifestMember, err)
	}
	if len(m.Entries) == 0 {
		t.Fatalf("AC-export-bundle: manifest has no entries")
	}
	// Manifest-level metadata the AC promises: a format version and the writer ferry
	// version must both be recorded.
	if m.FormatVersion == 0 {
		t.Errorf("AC-export-bundle: manifest records no format version (COORDINATION: field name?)")
	}
	if strings.TrimSpace(m.FerryVersion) == "" {
		t.Errorf("AC-export-bundle: manifest records no writer ferry version")
	}
	// Per-entry path+size+sha256 must match the actual member bytes, and every entry
	// path must be CANONICAL: relative (no absolute), forward-slash, no `..` traversal.
	manifestSet := map[string]bool{}
	for _, e := range m.Entries {
		manifestSet[e.Path] = true
		if filepath.IsAbs(e.Path) || strings.HasPrefix(e.Path, "/") {
			t.Errorf("AC-export-bundle: manifest entry path %q is absolute (must be a canonical relative path)", e.Path)
		}
		slashed := filepath.ToSlash(e.Path)
		if slashed != e.Path {
			t.Errorf("AC-export-bundle: manifest entry path %q is not canonical forward-slash form", e.Path)
		}
		for _, seg := range strings.Split(slashed, "/") {
			if seg == ".." {
				t.Errorf("AC-export-bundle: manifest entry path %q contains a `..` traversal segment", e.Path)
			}
		}
		f, ok := members[e.Path]
		if !ok {
			t.Errorf("AC-export-bundle: manifest lists %q but it is not a zip member", e.Path)
			continue
		}
		data := zipMemberBytes(t, f)
		if int64(len(data)) != e.Size {
			t.Errorf("AC-export-bundle: %q size %d != manifest %d", e.Path, len(data), e.Size)
		}
		if got := sha256Hex(data); got != e.SHA256 {
			t.Errorf("AC-export-bundle: %q sha256 mismatch (member %s != manifest %s)", e.Path, got, e.SHA256)
		}
	}
	// Equality (M9): payload member set == manifest entry set (manifest member excluded).
	for name := range members {
		if name == manifestMember {
			continue
		}
		if !manifestSet[name] {
			t.Errorf("AC-export-bundle: payload member %q has no manifest entry (equality rule)", name)
		}
	}
}

// TestExportSecretWithheld covers AC-export-secret-withheld: a tracked shared file
// containing a high-confidence secret (a fake key) is absent from the zip, reported
// withheld, and export still exits 0 (skip, not abort).
func TestExportSecretWithheld_AC_export_secret_withheld(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	// A clean required file (ferry.toml via seed) + a secret-bearing OPTIONAL file.
	seedExportRepo(t, s, map[string]string{
		"safe.txt":   "nothing secret here\n",
		"secret.txt": "# config\n" + fakeExportSecret,
	})

	out := bundleOutPath(t)
	stdout, stderr, code := s.Ferry("export", "--out", out)
	if code != 0 {
		t.Fatalf("AC-export-secret-withheld: export must SKIP the secret file (exit 0), got exit %d\n%s\n%s", code, stdout, stderr)
	}
	members := readZipMembers(t, out)
	if _, ok := members["secret.txt"]; ok {
		t.Errorf("AC-export-secret-withheld: secret-bearing file is a bundle member (must be withheld)")
	}
	// The secret bytes must appear NOWHERE in the zip.
	zipBytes, _ := os.ReadFile(out)
	if bytes.Contains(zipBytes, []byte("BEGIN OPENSSH PRIVATE KEY")) {
		t.Errorf("AC-export-secret-withheld: fake secret bytes present in the bundle bytes")
	}
	// It must be reported withheld — and the report must NAME THE PATH (`secret.txt`)
	// AND give a secret reason (per the PLAN's `withheld: <path> (contains a secret)`),
	// not merely a generic "skipped" with no path/reason.
	report := stdout + stderr
	if !containsAnyFold(report, "secret.txt") {
		t.Errorf("AC-export-secret-withheld: the withheld report did not name the path secret.txt\n%s", report)
	}
	if !containsAnyFold(report, "withheld", "withhold", "secret") {
		t.Errorf("AC-export-secret-withheld: the withheld report did not cite a secret/withheld reason\n%s", report)
	}
	if _, ok := members["safe.txt"]; !ok {
		t.Errorf("AC-export-secret-withheld: the safe file should still be bundled")
	}
}

// TestExportPathnameSecret covers AC-export-pathname-secret (M10): a tracked file
// whose PATH component is secret-shaped is withheld — neither the file nor its path
// appears in the zip or manifest.
func TestExportPathnameSecret_AC_export_pathname_secret(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	// A high-entropy token used as a FILENAME. A realistic fake AWS-style key id.
	secretName := "AKIAIOSFODNN7EXAMPLEXYZ.txt"
	seedExportRepo(t, s, map[string]string{
		"safe.txt": "ok\n",
		secretName: "benign body\n",
	})

	out := bundleOutPath(t)
	stdout, stderr, code := s.Ferry("export", "--out", out)
	if code != 0 {
		t.Fatalf("AC-export-pathname-secret: export must SKIP the secret-named file (exit 0), got %d\n%s\n%s", code, stdout, stderr)
	}
	members := readZipMembers(t, out)
	const secretFrag = "AKIAIOSFODNN7EXAMPLE"
	for name := range members {
		if strings.Contains(name, secretFrag) {
			t.Errorf("AC-export-pathname-secret: secret-shaped path %q leaked as a bundle member", name)
		}
	}
	// The withholding must be REPORTED (the file was dropped for a secret-shaped path).
	// The report must cite a withheld/secret reason; it need not echo the secret token
	// itself (echoing it would re-leak it), so we assert the reason, not the raw token.
	report := stdout + stderr
	if !containsAnyFold(report, "withheld", "withhold", "secret", "path") {
		t.Errorf("AC-export-pathname-secret: export did not report the withheld secret-named file\n%s", report)
	}
	if strings.Contains(report, secretFrag) {
		t.Errorf("AC-export-pathname-secret: the report itself echoed the secret path token (re-leak)\n%s", report)
	}
	// The path must ALSO be absent from the manifest entries (a token in a filename
	// must not leak via the manifest) — parse ferry-bundle.json and check every entry.
	if mf, ok := members[manifestMember]; ok {
		var m bundleManifest
		if err := json.Unmarshal(zipMemberBytes(t, mf), &m); err != nil {
			t.Fatalf("AC-export-pathname-secret: manifest unparseable (COORDINATION: adjust bundleManifest shape): %v", err)
		}
		for _, e := range m.Entries {
			if strings.Contains(e.Path, secretFrag) {
				t.Errorf("AC-export-pathname-secret: secret-shaped path %q leaked as a MANIFEST entry", e.Path)
			}
		}
	}
	// Belt-and-braces: the secret path fragment must not appear in the raw zip bytes.
	if zb, _ := os.ReadFile(out); bytes.Contains(zb, []byte(secretFrag)) {
		t.Errorf("AC-export-pathname-secret: secret path fragment present in raw bundle bytes")
	}
}

// TestExportRequiredAbort covers AC-export-required-abort (M11): if the required
// shared manifest (ferry.toml) is MISSING *or would be WITHHELD by the secret gate*,
// export aborts with no bundle written (an unusable bundle is never produced).
func TestExportRequiredAbort_AC_export_required_abort(t *testing.T) {
	t.Parallel()

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		s.InitGitRepo(t)
		// Point config.toml at the repo but do NOT create/commit ferry.toml.
		if err := os.WriteFile(s.ConfigTOMLPath(), []byte("repo = \""+s.Repo+"\"\n"), 0o644); err != nil {
			t.Fatalf("write config.toml: %v", err)
		}
		s.WriteRepoFile(t, "other.txt", "no manifest here\n")
		gitCommitAll(t, s.Repo, "no ferry.toml")

		out := bundleOutPath(t)
		stdout, stderr, code := s.Ferry("export", "--out", out)
		if code == 0 {
			t.Errorf("AC-export-required-abort[missing]: export exited 0 despite a missing required ferry.toml (must abort)")
		}
		if _, err := os.Stat(out); err == nil {
			t.Errorf("AC-export-required-abort[missing]: a bundle was written despite the abort")
		}
		if !containsAnyFold(stdout+stderr, "ferry.toml", "required", "manifest", "missing") {
			t.Errorf("AC-export-required-abort[missing]: abort message did not name the missing required file\n%s\n%s", stdout, stderr)
		}
	})

	t.Run("required-would-be-withheld", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		s.InitGitRepo(t)
		if err := os.WriteFile(s.ConfigTOMLPath(), []byte("repo = \""+s.Repo+"\"\n"), 0o644); err != nil {
			t.Fatalf("write config.toml: %v", err)
		}
		// The REQUIRED shared manifest itself carries a high-confidence secret, so the
		// secret gate would withhold it. Per M11 this must ABORT (not skip), since a
		// bundle without the required manifest is unusable.
		s.WriteRepoFile(t, "ferry.toml", baseManifest+"\n# leaked:\n"+fakeExportSecret)
		gitCommitAll(t, s.Repo, "ferry.toml with a secret")

		out := bundleOutPath(t)
		stdout, stderr, code := s.Ferry("export", "--out", out)
		if code == 0 {
			t.Errorf("AC-export-required-abort[withheld]: export exited 0 though the required ferry.toml would be withheld (must abort, not skip)")
		}
		if _, err := os.Stat(out); err == nil {
			t.Errorf("AC-export-required-abort[withheld]: a bundle was written though the required file was withheld")
		}
		// And the secret bytes must never have been written into a bundle.
		if zb, _ := os.ReadFile(out); bytes.Contains(zb, []byte("BEGIN OPENSSH PRIVATE KEY")) {
			t.Errorf("AC-export-required-abort[withheld]: secret bytes present in an on-disk bundle")
		}
		if !containsAnyFold(stdout+stderr, "ferry.toml", "required", "secret", "withheld", "manifest") {
			t.Errorf("AC-export-required-abort[withheld]: abort message did not explain the withheld required file\n%s\n%s", stdout, stderr)
		}
	})
}

// TestExportNoSSH covers AC-export-no-ssh / AC-export-no-symlink (C3): the ~/.ssh
// tripwire is intact after export, no ~/.ssh-derived path enters the bundle, and a
// tracked symlink pointing at ~/.ssh is refused (not followed).
func TestExportNoSSH_AC_export_no_ssh(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SSHTripwire(t)
	seedExportRepo(t, s, map[string]string{"shared.txt": "ok\n"})

	// A tracked symlink in the repo pointing at a fake ~/.ssh key.
	linkPath := filepath.Join(s.Repo, "leak-link")
	if err := os.Symlink(s.HomePath(".ssh", "id_ed25519"), linkPath); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
	// Track it (git records symlinks; export must still refuse to FOLLOW it).
	gitCommitAll(t, s.Repo, "add ssh-pointing symlink")

	out := bundleOutPath(t)
	s.Ferry("export", "--out", out) // may exit 0 (skip) or non-zero (refuse) — observable is below

	// SSH tripwire intact: nothing under ~/.ssh was read-and-copied or modified.
	// (AssertSSHUntouched proves no MODIFICATION/creation/deletion; the "never reads"
	// half is bounded here to no-copy-into-bundle — a read that fed the bundle would
	// surface the key bytes below. A syscall-level no-open trace is out of scope for
	// this harness; the no-copy + no-ssh-path assertions are the observable floor.)
	s.AssertSSHUntouched(t)

	if _, err := os.Stat(out); err == nil {
		members := readZipMembers(t, out)
		if f, ok := members["leak-link"]; ok {
			// If a member exists under that name, it must NOT contain the SSH key body.
			if bytes.Contains(zipMemberBytes(t, f), []byte("BEGIN OPENSSH PRIVATE KEY")) {
				t.Errorf("AC-export-no-ssh: symlink was followed — SSH key material entered the bundle")
			}
		}
		// No member's bytes may carry the fake private key.
		zipBytes, _ := os.ReadFile(out)
		if bytes.Contains(zipBytes, []byte("BEGIN OPENSSH PRIVATE KEY")) {
			t.Errorf("AC-export-no-ssh: fake ~/.ssh key material present in bundle bytes")
		}
		// No member PATH may be ~/.ssh-derived: neither the payload members nor the
		// manifest entries may name a `.ssh` path (a path leak via the manifest).
		for name := range members {
			if name == manifestMember {
				continue
			}
			if strings.Contains(filepath.ToSlash(name), ".ssh/") || filepath.Base(name) == ".ssh" {
				t.Errorf("AC-export-no-ssh: a bundle member has an ~/.ssh-derived path %q", name)
			}
		}
		if mf, ok := members[manifestMember]; ok {
			var m bundleManifest
			if json.Unmarshal(zipMemberBytes(t, mf), &m) == nil {
				for _, e := range m.Entries {
					if strings.Contains(filepath.ToSlash(e.Path), ".ssh/") {
						t.Errorf("AC-export-no-ssh: manifest lists an ~/.ssh-derived entry path %q", e.Path)
					}
				}
			}
		}
	}

	// Strengthen "never READS under ~/.ssh": where an fs_usage trace is usable on
	// macOS, ENFORCE that export opens NO path under ~/.ssh (an exporter that opened
	// and discarded the key would pass the no-copy/no-modify floor above but fail
	// here). Capability-skip with a precise reason when the tracer isn't privileged;
	// AssertSSHUntouched + no-key-in-bundle remain the always-available floor.
	if runtime.GOOS == "darwin" {
		sshDir := s.HomePath(".ssh")
		if fsUsageUsable(t, s) {
			traceOut := filepath.Join(t.TempDir(), "trace-bundle.zip")
			if opens := traceSSHOpens(t, s, sshDir, "export", "--out", traceOut); len(opens) > 0 {
				t.Errorf("AC-export-no-ssh: `export` opened path(s) under ~/.ssh/ (export must not read SSH material):\n%s", strings.Join(opens, "\n"))
			}
		} else {
			t.Log("AC-export-no-ssh: fs_usage not usable (needs privilege); no-open(~/.ssh) read-trace skipped — AssertSSHUntouched + no-key-in-bundle is the floor.")
		}
	}
}

// TestExportNoSymlink covers AC-export-no-symlink (C3) standalone: an ORDINARY
// tracked symlink (pointing at an in-repo regular file, nothing to do with ~/.ssh)
// is refused on export — regular files only. It must not appear as a bundle member,
// and its target's content must not be smuggled in under the symlink's name.
func TestExportNoSymlink_AC_export_no_symlink(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	// Only ferry.toml + a clean shared file are TRACKED. real.txt is UNTRACKED, so its
	// REAL_TARGET_MARKER can only reach the bundle if the symlink was followed.
	seedExportRepo(t, s, map[string]string{"shared.txt": "clean\n"})
	s.WriteRepoFile(t, "real.txt", "REAL_TARGET_MARKER\n") // untracked
	// A TRACKED symlink pointing at that in-repo file.
	linkPath := filepath.Join(s.Repo, "alias-link")
	if err := os.Symlink(filepath.Join(s.Repo, "real.txt"), linkPath); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
	gitCommitPaths(t, s.Repo, "add ordinary symlink", "alias-link")

	out := bundleOutPath(t)
	s.Ferry("export", "--out", out) // may skip+report or abort; the observable is below
	if _, err := os.Stat(out); err != nil {
		return // export aborted rather than producing a bundle: acceptable (symlink refused)
	}
	members := readZipMembers(t, out)
	// "Regular files only": the tracked symlink must be REFUSED — it must NOT appear as
	// a bundle member at all (neither a symlink member NOR a followed regular copy).
	if _, ok := members["alias-link"]; ok {
		t.Errorf("AC-export-no-symlink: the tracked symlink appeared as a bundle member (must be refused; regular files only)")
	}
	// And its target's bytes must not have been smuggled in under any member name.
	if zb, _ := os.ReadFile(out); bytes.Contains(zb, []byte("REAL_TARGET_MARKER")) {
		// real.txt itself is NOT tracked/committed as a shared file here, so the marker
		// must not appear at all — its presence means the symlink was followed.
		t.Errorf("AC-export-no-symlink: the symlink target's bytes entered the bundle (symlink was followed)")
	}
}

// TestExportOutOutsideRepo covers AC-export-out-outside-repo (M5): an --out under
// the repo root is refused and no bundle is written there.
func TestExportOutOutsideRepo_AC_export_out_outside_repo(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	seedExportRepo(t, s, map[string]string{"shared.txt": "ok\n"})

	inside := s.RepoPath("sub", "bundle.zip")
	stdout, stderr, code := s.Ferry("export", "--out", inside)
	if code == 0 {
		t.Errorf("AC-export-out-outside-repo: export accepted an --out UNDER the repo root (must refuse)")
	}
	if _, err := os.Stat(inside); err == nil {
		t.Errorf("AC-export-out-outside-repo: a bundle was written inside the repo at %q", inside)
	}
	if !containsAnyFold(stdout+stderr, "outside", "inside the repo", "under the repo", "repo root", "out") {
		t.Errorf("AC-export-out-outside-repo: refusal did not explain the out-must-be-outside-repo rule\n%s\n%s", stdout, stderr)
	}
}

// TestExportLocalGated covers AC-export-local-gated: without --include-local the
// local layer (BOTH local/** AND ferry.local.toml) is absent from the bundle; with
// it, both are present. ferry.local.toml is normally gitignored, so the test
// force-tracks it (`git add -f`) — the impl must still treat it as local-layer
// content gated behind --include-local. (COORDINATION: whether export force-includes
// ferry.local.toml under --include-local, or requires it tracked, is an impl choice;
// this test asserts the GATING either way — absent without the flag, present with it
// only if it is a candidate at all.)
func TestExportLocalGated_AC_export_local_gated(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	seedExportRepo(t, s, map[string]string{
		"shared.txt":            "shared\n",
		"local/zsh/zshrc.local": "export LOCAL=1\n",
	})
	// ferry.local.toml is part of the local layer; force-track it (normally gitignored).
	s.WriteRepoFile(t, "ferry.local.toml", "# per-machine overrides\n")
	gitForceAdd(t, s.Repo, "add ferry.local.toml", "ferry.local.toml")

	localMembers := []string{"local/zsh/zshrc.local", "ferry.local.toml"}

	// Default: entire local layer excluded.
	out := exportOK(t, s)
	def := readZipMembers(t, out)
	for _, lm := range localMembers {
		if _, ok := def[lm]; ok {
			t.Errorf("AC-export-local-gated: local-layer member %q present WITHOUT --include-local", lm)
		}
	}

	// With --include-local: the WHOLE local layer is added back — BOTH local/** AND the
	// (force-tracked) ferry.local.toml. The PLAN defines the local layer as
	// `local/**` + `ferry.local.toml`, so both must be members under the flag.
	withLocal := readZipMembers(t, exportOK(t, s, "--include-local"))
	for _, lm := range localMembers {
		if _, ok := withLocal[lm]; !ok {
			t.Errorf("AC-export-local-gated: local-layer member %q absent WITH --include-local", lm)
		}
	}
}

// TestExportPrintsSHA covers AC-export-prints-sha (C4): export prints the bundle's
// overall SHA256 and it equals the SHA256 of the written zip.
func TestExportPrintsSHA_AC_export_prints_sha(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	seedExportRepo(t, s, map[string]string{"shared.txt": "ok\n"})

	out := bundleOutPath(t)
	stdout, stderr, code := s.Ferry("export", "--out", out)
	if code != 0 {
		t.Fatalf("export exited %d\n%s\n%s", code, stdout, stderr)
	}
	zipBytes, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	want := sha256Hex(zipBytes)
	// The AC promises the SHA256 is printed to STDOUT (the out-of-band anchor the user
	// conveys), so assert it on stdout specifically — stderr-only would not satisfy it.
	if !strings.Contains(strings.ToLower(stdout), want) {
		t.Errorf("AC-export-prints-sha: STDOUT does not contain the bundle's SHA256 %s\nstdout:\n%s\nstderr:\n%s", want, stdout, stderr)
	}
}

// -----------------------------------------------------------------------------
// Import — round-trip and configuration
// -----------------------------------------------------------------------------

// TestImportRoundtrip covers AC-import-roundtrip: export on A → import into fresh
// $HOME B → B's shared files are byte-identical to A's originals.
func TestImportRoundtrip_AC_import_roundtrip(t *testing.T) {
	t.Parallel()
	a := NewSandbox(t)
	files := map[string]string{
		"shared.txt":     "roundtrip A content\n",
		"nested/dir.txt": "nested A content\n",
	}
	seedExportRepo(t, a, files)
	out := exportOK(t, a)

	b := NewSandbox(t)
	target := importTargetAbsent(t)
	if _, errOut, code := b.Ferry("import", "--out", target, out); code != 0 {
		t.Fatalf("AC-import-roundtrip: import exited %d\n%s", code, errOut)
	}
	for rel, body := range files {
		got, err := os.ReadFile(filepath.Join(target, rel))
		if err != nil {
			t.Errorf("AC-import-roundtrip: shared file %q missing after import: %v", rel, err)
			continue
		}
		if string(got) != body {
			t.Errorf("AC-import-roundtrip: %q not byte-identical after roundtrip\nwant: %q\ngot:  %q", rel, body, got)
		}
	}
	// ferry.toml is the required shared manifest and part of "B's shared files"; it must
	// round-trip byte-identically too. Compare against A's on-disk source.
	srcToml, err := os.ReadFile(a.RepoPath("ferry.toml"))
	if err != nil {
		t.Fatalf("AC-import-roundtrip: could not read A's ferry.toml: %v", err)
	}
	gotToml, err := os.ReadFile(filepath.Join(target, "ferry.toml"))
	if err != nil {
		t.Errorf("AC-import-roundtrip: ferry.toml (required shared manifest) missing after import: %v", err)
	} else if !bytes.Equal(gotToml, srcToml) {
		t.Errorf("AC-import-roundtrip: ferry.toml not byte-identical after roundtrip\nwant: %q\ngot:  %q", srcToml, gotToml)
	}
}

// TestImportWritesConfig covers AC-import-writes-config (M4): after import,
// ~/.config/ferry/config.toml exists and records the newly created repo path.
func TestImportWritesConfig_AC_import_writes_config(t *testing.T) {
	t.Parallel()
	a := NewSandbox(t)
	seedExportRepo(t, a, map[string]string{"shared.txt": "ok\n"})
	out := exportOK(t, a)

	b := NewSandbox(t)
	if _, errOut, code := b.Ferry("import", out); code != 0 {
		t.Fatalf("AC-import-writes-config: import exited %d\n%s", code, errOut)
	}
	cfg, err := os.ReadFile(b.ConfigTOMLPath())
	if err != nil {
		t.Fatalf("AC-import-writes-config: config.toml not written by import: %v", err)
	}
	repoPath := extractRepoPath(string(cfg))
	if repoPath == "" {
		t.Fatalf("AC-import-writes-config: config.toml records no repo path\n%s", cfg)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "shared.txt")); err != nil {
		t.Errorf("AC-import-writes-config: recorded repo %q does not contain the imported shared file: %v", repoPath, err)
	}
}

// TestImportThenApply covers AC-import-then-apply: full move export→import→apply
// deploys the managed dotfile into B's $HOME.
func TestImportThenApply_AC_import_then_apply(t *testing.T) {
	t.Parallel()
	a := NewSandbox(t)
	// baseManifest manages .zshrc; provide the managed source in the two likely spots.
	seedExportRepo(t, a, map[string]string{
		".zshrc":          "# managed zshrc from A\n",
		"dotfiles/.zshrc": "# managed zshrc from A\n",
	})
	out := exportOK(t, a)

	b := NewSandbox(t)
	if _, errOut, code := b.Ferry("import", out); code != 0 {
		t.Fatalf("AC-import-then-apply: import exited %d\n%s", code, errOut)
	}
	if _, errOut, code := b.Ferry("apply"); code != 0 {
		t.Fatalf("AC-import-then-apply: apply after import exited %d\n%s", code, errOut)
	}
	got, err := os.ReadFile(b.HomePath(".zshrc"))
	if err != nil {
		t.Fatalf("AC-import-then-apply: managed .zshrc not deployed on B: %v", err)
	}
	if !strings.Contains(string(got), "managed zshrc from A") {
		t.Errorf("AC-import-then-apply: deployed .zshrc lacks the expected content; got %q", got)
	}
}

// -----------------------------------------------------------------------------
// Import — integrity & validation gates (crafted-malicious bundles)
// -----------------------------------------------------------------------------

// TestImportIntegrity covers AC-import-integrity (M2): a bundle with one flipped
// payload byte (sha256 mismatch) is rejected before extraction; nothing lands.
func TestImportIntegrity_AC_import_integrity(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	path := filepath.Join(t.TempDir(), "flipped.zip")
	// Craft a bundle whose manifest sha256 does NOT match the member bytes.
	body := []byte("good body\n")
	m := bundleManifest{
		FormatVersion: bundleFormatVersion, FerryVersion: "0.2.0-test",
		Entries: []bundleEntry{{Path: "shared.txt", Size: int64(len(body)), SHA256: sha256Hex([]byte("DIFFERENT"))}},
	}
	writeCraftedZip(t, path, manifestBytes(t, m), []zipMember{{name: "shared.txt", data: body}})

	target := importTargetAbsent(t)
	_, errOut, code := s.Ferry("import", "--out", target, path)
	if code == 0 {
		t.Errorf("AC-import-integrity: import accepted a bundle with a per-entry sha256 mismatch (must reject)")
	}
	// validate-before-extract: NOTHING extracted at the target (not just the one file).
	assertTargetEmpty(t, target)
	// The AC's verify text promises a checksum/integrity message — gating.
	if !containsAnyFold(errOut, "checksum", "sha256", "integrity", "corrupt", "mismatch") {
		t.Errorf("AC-import-integrity: rejection did not cite the integrity/checksum failure the AC promises:\n%s", errOut)
	}
}

// TestImportExpectSHA covers AC-import-expect-sha (C4): --expect-sha256 <wrong>
// aborts before extract; the correct value permits import.
func TestImportExpectSHA_AC_import_expect_sha(t *testing.T) {
	t.Parallel()
	a := NewSandbox(t)
	seedExportRepo(t, a, map[string]string{"shared.txt": "ok\n"})
	out := exportOK(t, a)

	// Wrong expected hash aborts, nothing extracted.
	b := NewSandbox(t)
	target := importTargetAbsent(t)
	wrong := strings.Repeat("0", 64)
	_, errOut, code := b.Ferry("import", "--expect-sha256", wrong, "--out", target, out)
	if code == 0 {
		t.Errorf("AC-import-expect-sha: import accepted a wrong --expect-sha256 (must abort)")
	}
	assertTargetEmpty(t, target) // aborted before extract: nothing at the target
	// The AC promises a checksum-mismatch message — gating.
	if !containsAnyFold(errOut, "checksum", "sha256", "mismatch", "expect", "match") {
		t.Errorf("AC-import-expect-sha: refusal did not cite the --expect-sha256 mismatch the AC promises:\n%s", errOut)
	}

	// Correct expected hash permits import.
	zipBytes, _ := os.ReadFile(out)
	correct := sha256Hex(zipBytes)
	c := NewSandbox(t)
	target2 := importTargetAbsent(t)
	if _, errOut2, code2 := c.Ferry("import", "--expect-sha256", correct, "--out", target2, out); code2 != 0 {
		t.Errorf("AC-import-expect-sha: import with the CORRECT --expect-sha256 was rejected (exit %d)\n%s", code2, errOut2)
	}
}

// TestImportZipSlip covers AC-import-zipslip: a ../escape (and an absolute-path)
// entry is refused; nothing is written outside the target. Each subtest asserts the
// EXACT escape destination the malicious entry resolves to stays absent (not just an
// unrelated sentinel) — so an importer that actually wrote the escape path fails.
func TestImportZipSlip_AC_import_zipslip(t *testing.T) {
	t.Parallel()

	t.Run("dotdot", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		base := t.TempDir()
		target := filepath.Join(base, "import-target")
		// `../escape.txt` from <base>/import-target resolves to <base>/escape.txt.
		escape := filepath.Join(base, "escape.txt")
		escapeSnap := s.SnapshotFile(t, escape) // absent

		path := filepath.Join(t.TempDir(), "zipslip.zip")
		body := []byte("evil\n")
		m := bundleManifest{
			FormatVersion: bundleFormatVersion, FerryVersion: "0.2.0-test",
			Entries: []bundleEntry{{Path: "../escape.txt", Size: int64(len(body)), SHA256: sha256Hex(body)}},
		}
		writeCraftedZip(t, path, manifestBytes(t, m), []zipMember{{name: "../escape.txt", data: body}})

		_, _, code := s.Ferry("import", "--out", target, path)
		if code == 0 {
			t.Errorf("AC-import-zipslip[dotdot]: import accepted a `../escape.txt` traversal entry (must refuse)")
		}
		escapeSnap.AssertUnchanged(t) // the EXACT escape destination must stay absent
		assertTargetEmpty(t, target)
	})

	t.Run("absolute", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		// An absolute-path entry aimed at a unique path under a fresh temp dir; that
		// exact destination must stay absent (no reliance on /tmp being writable).
		abs := filepath.Join(t.TempDir(), "abs-escape.txt")
		absSnap := s.SnapshotFile(t, abs) // absent

		path := filepath.Join(t.TempDir(), "zipslip-abs.zip")
		body := []byte("evil\n")
		m := bundleManifest{
			FormatVersion: bundleFormatVersion, FerryVersion: "0.2.0-test",
			Entries: []bundleEntry{{Path: abs, Size: int64(len(body)), SHA256: sha256Hex(body)}},
		}
		writeCraftedZip(t, path, manifestBytes(t, m), []zipMember{{name: abs, data: body}})

		target := importTargetAbsent(t)
		_, _, code := s.Ferry("import", "--out", target, path)
		if code == 0 {
			t.Errorf("AC-import-zipslip[absolute]: import accepted an absolute-path entry %q (must refuse)", abs)
		}
		absSnap.AssertUnchanged(t) // the exact absolute destination must stay absent
		assertTargetEmpty(t, target)
	})
}

// TestImportRejectGit covers AC-import-reject-git (C2): a bundle carrying a
// .git/hooks/pre-commit (or any .git/**) entry is refused; no .git content lands.
func TestImportRejectGit_AC_import_reject_git(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	path := filepath.Join(t.TempDir(), "githook.zip")
	hook := []byte("#!/bin/sh\necho pwned\n")
	m := bundleManifest{
		FormatVersion: bundleFormatVersion, FerryVersion: "0.2.0-test",
		Entries: []bundleEntry{
			{Path: "shared.txt", Size: 3, SHA256: sha256Hex([]byte("ok\n"))},
			{Path: ".git/hooks/pre-commit", Size: int64(len(hook)), SHA256: sha256Hex(hook)},
		},
	}
	writeCraftedZip(t, path, manifestBytes(t, m), []zipMember{
		{name: "shared.txt", data: []byte("ok\n")},
		{name: ".git/hooks/pre-commit", data: hook, mode: 0o755},
	})

	target := importTargetAbsent(t)
	_, _, code := s.Ferry("import", "--out", target, path)
	if code == 0 {
		t.Errorf("AC-import-reject-git: import accepted a bundle carrying a .git/** entry (must refuse)")
	}
	// The bundle's hook must never land (any .git may only be ferry's own re-init).
	if data, err := os.ReadFile(filepath.Join(target, ".git", "hooks", "pre-commit")); err == nil && bytes.Contains(data, []byte("pwned")) {
		t.Errorf("AC-import-reject-git: the bundle's .git/hooks/pre-commit was laid down at the target")
	}
	// The whole bundle is refused (validate-before-extract): even the benign
	// shared.txt sibling must not have been extracted.
	assertTargetEmpty(t, target)
}

// TestImportRejectSymlink covers AC-import-reject-symlink (C3): a symlink OR special
// (non-regular) entry in the bundle is refused; nothing is materialised. The PLAN
// says "REJECT all symlink and special entries", so both a symlink-mode entry AND a
// device/FIFO-mode entry are exercised.
func TestImportRejectSymlink_AC_import_reject_symlink(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mode os.FileMode
	}{
		{"symlink", os.ModeSymlink | 0o777},
		{"device", os.ModeDevice | os.ModeCharDevice | 0o666},
		{"fifo", os.ModeNamedPipe | 0o644},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			path := filepath.Join(t.TempDir(), tc.name+".zip")
			body := []byte("/etc/passwd") // arbitrary bytes for the non-regular entry
			m := bundleManifest{
				FormatVersion: bundleFormatVersion, FerryVersion: "0.2.0-test",
				Entries: []bundleEntry{{Path: "evil-entry", Size: int64(len(body)), SHA256: sha256Hex(body)}},
			}
			writeCraftedZip(t, path, manifestBytes(t, m), []zipMember{
				{name: "evil-entry", data: body, mode: tc.mode},
			})

			target := importTargetAbsent(t)
			_, _, code := s.Ferry("import", "--out", target, path)
			if code == 0 {
				t.Errorf("AC-import-reject-symlink[%s]: import accepted a non-regular (%s-mode) entry (must refuse)", tc.name, tc.name)
			}
			// Nothing non-regular may be materialised, and (validate-before-extract)
			// nothing at all lands.
			if fi, err := os.Lstat(filepath.Join(target, "evil-entry")); err == nil && fi.Mode()&(os.ModeSymlink|os.ModeDevice|os.ModeNamedPipe|os.ModeSocket) != 0 {
				t.Errorf("AC-import-reject-symlink[%s]: a non-regular entry from the bundle was materialised at the target", tc.name)
			}
			assertTargetEmpty(t, target)
		})
	}
}

// TestImportNewerVersion covers AC-import-newer-version-refused (M7): a manifest
// with a higher format version is refused with an upgrade message.
func TestImportNewerVersion_AC_import_newer_version_refused(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	path := filepath.Join(t.TempDir(), "newer.zip")
	body := []byte("ok\n")
	m := bundleManifest{
		FormatVersion: bundleFormatVersion + 100, // far-future format
		FerryVersion:  "9.9.9-future",
		Entries:       []bundleEntry{{Path: "shared.txt", Size: int64(len(body)), SHA256: sha256Hex(body)}},
	}
	writeCraftedZip(t, path, manifestBytes(t, m), []zipMember{{name: "shared.txt", data: body}})

	target := importTargetAbsent(t)
	stdout, errOut, code := s.Ferry("import", "--out", target, path)
	if code == 0 {
		t.Errorf("AC-import-newer-version-refused: import accepted a newer-than-supported format version (must refuse)")
	}
	assertTargetEmpty(t, target)
	// The AC promises refusal "with an upgrade message" — this is a stated part of the
	// promise, so it is a gating assertion, not a soft log.
	if !containsAnyFold(stdout+errOut, "version", "upgrade", "newer", "unsupported") {
		t.Errorf("AC-import-newer-version-refused: refusal did not mention the version/upgrade the AC promises\n%s\n%s", stdout, errOut)
	}
}

// TestImportResourceCaps covers AC-import-resource-caps (M8): a bundle exceeding the
// entry-count / per-file / total-size caps is refused before extraction (zip-bomb
// defense), observable from the zip central directory metadata alone.
func TestImportResourceCaps_AC_import_resource_caps(t *testing.T) {
	t.Parallel()

	// (1) ENTRY-COUNT cap from REAL central-directory metadata: a bundle with a very
	// large number of members whose per-entry checksums+sizes are ALL VALID, so the
	// ONLY thing wrong is the count. A checksum/manifest-mismatch path cannot explain
	// this refusal — it must be the entry-count cap (the real CD-metadata signal M8
	// describes). Kept modest (5000) so crafting stays fast but comfortably above any
	// sane cap.
	t.Run("entry-count", func(t *testing.T) {
		t.Parallel()
		requireBin(t) // skip BEFORE crafting 5000 members when the binary is absent
		s := NewSandbox(t)
		path := filepath.Join(t.TempDir(), "manyentries.zip")
		const n = 5000
		m := bundleManifest{FormatVersion: bundleFormatVersion, FerryVersion: "0.2.0-test"}
		members := make([]zipMember, 0, n)
		for i := 0; i < n; i++ {
			name := "f" + itoa(i) + ".txt"
			body := []byte("x\n")
			m.Entries = append(m.Entries, bundleEntry{Path: name, Size: int64(len(body)), SHA256: sha256Hex(body)})
			members = append(members, zipMember{name: name, data: body})
		}
		writeCraftedZip(t, path, manifestBytes(t, m), members)

		target := importTargetAbsent(t)
		stdout, errOut, code := s.Ferry("import", "--out", target, path)
		if code == 0 {
			t.Errorf("AC-import-resource-caps[entry-count]: import accepted a %d-entry bundle (must refuse on the entry-count cap)", n)
		}
		assertTargetEmpty(t, target)
		if !containsAnyFold(stdout+errOut, "cap", "too many", "entries", "count", "limit", "exceed") {
			t.Errorf("AC-import-resource-caps[entry-count]: refusal did not cite the entry-count cap the AC promises\n%s\n%s", stdout, errOut)
		}
	})

	// (2) PER-FILE SIZE cap from REAL central-directory metadata: one member holds a
	// large but highly-compressible payload (zeros). The on-disk zip stays tiny (the
	// deflate stream is small), but the CENTRAL DIRECTORY records the REAL large
	// UncompressedSize64 — so an import reading declared per-entry uncompressed size
	// from metadata must refuse WITHOUT decompressing. Manifest size + sha256 match
	// the real bytes, so this refusal cannot be a checksum/manifest mismatch: it is
	// the per-file size cap (the classic zip-bomb metadata signal M8 describes).
	t.Run("per-file-size", func(t *testing.T) {
		t.Parallel()
		requireBin(t) // skip BEFORE allocating the large payload when the binary is absent
		s := NewSandbox(t)
		path := filepath.Join(t.TempDir(), "bomb.zip")
		const big = 512 << 20 // 512 MiB uncompressed (compresses to a few KiB of zeros)
		body := make([]byte, big)
		m := bundleManifest{
			FormatVersion: bundleFormatVersion, FerryVersion: "0.2.0-test",
			Entries: []bundleEntry{{Path: "bomb.bin", Size: int64(len(body)), SHA256: sha256Hex(body)}},
		}
		writeCraftedZip(t, path, manifestBytes(t, m), []zipMember{{name: "bomb.bin", data: body}})
		// Sanity: the crafted zip really is tiny on disk while declaring a huge member.
		if fi, err := os.Stat(path); err == nil && fi.Size() > (4<<20) {
			t.Logf("per-file-size: crafted zip is %d bytes (expected << member size)", fi.Size())
		}

		target := importTargetAbsent(t)
		stdout, errOut, code := s.Ferry("import", "--out", target, path)
		if code == 0 {
			t.Errorf("AC-import-resource-caps[per-file-size]: import accepted a bundle whose central directory declares a %d-byte member (must refuse on the per-file size cap)", big)
		}
		assertTargetEmpty(t, target)
		if !containsAnyFold(stdout+errOut, "cap", "too large", "size", "limit", "exceed") {
			t.Errorf("AC-import-resource-caps[per-file-size]: refusal did not cite the size cap the AC promises\n%s\n%s", stdout, errOut)
		}
	})

	// (3) SUMMED TOTAL-SIZE cap from REAL central-directory metadata: MANY members,
	// each individually MODEST (8 MiB — below any plausible per-file cap), but whose
	// SUMMED declared uncompressed size (128 × 8 MiB = 1 GiB) exceeds the total cap.
	// Every member's manifest size+sha256 is valid, so the refusal must be the total
	// cap, not a per-file or checksum failure. (COORDINATION: if the impl's per-file
	// cap is below 8 MiB this could trip that instead — still a caps refusal; the
	// per-entry size here is deliberately modest to target the TOTAL cap.)
	t.Run("total-size", func(t *testing.T) {
		t.Parallel()
		requireBin(t) // skip BEFORE crafting the members when the binary is absent
		s := NewSandbox(t)
		path := filepath.Join(t.TempDir(), "total.zip")
		const per = 8 << 20 // 8 MiB per member
		const count = 128   // sums to 1 GiB declared
		shared := make([]byte, per)
		sum := sha256Hex(shared)
		m := bundleManifest{FormatVersion: bundleFormatVersion, FerryVersion: "0.2.0-test"}
		members := make([]zipMember, 0, count)
		for i := 0; i < count; i++ {
			name := "chunk" + itoa(i) + ".bin"
			m.Entries = append(m.Entries, bundleEntry{Path: name, Size: per, SHA256: sum})
			members = append(members, zipMember{name: name, data: shared})
		}
		writeCraftedZip(t, path, manifestBytes(t, m), members)

		target := importTargetAbsent(t)
		stdout, errOut, code := s.Ferry("import", "--out", target, path)
		if code == 0 {
			t.Errorf("AC-import-resource-caps[total-size]: import accepted a bundle whose central directory sums to %d bytes (must refuse on the total-size cap)", int64(per)*count)
		}
		assertTargetEmpty(t, target)
		if !containsAnyFold(stdout+errOut, "cap", "too large", "total", "size", "limit", "exceed") {
			t.Errorf("AC-import-resource-caps[total-size]: refusal did not cite the total-size cap the AC promises\n%s\n%s", stdout, errOut)
		}
	})
}

// itoa is a tiny int->string helper to avoid importing strconv just for names.
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

// TestImportDupEntries covers AC-import-dup-entries (M9): duplicate or
// case-folding-colliding entry names are refused.
func TestImportDupEntries_AC_import_dup_entries(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		a, b string
	}{
		{"exact", "dup.txt", "dup.txt"},
		{"casefold", "File.txt", "file.txt"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			path := filepath.Join(t.TempDir(), "dup.zip")
			d1 := []byte("one\n")
			d2 := []byte("two\n")
			m := bundleManifest{
				FormatVersion: bundleFormatVersion, FerryVersion: "0.2.0-test",
				Entries: []bundleEntry{
					{Path: tc.a, Size: int64(len(d1)), SHA256: sha256Hex(d1)},
					{Path: tc.b, Size: int64(len(d2)), SHA256: sha256Hex(d2)},
				},
			}
			writeCraftedZip(t, path, manifestBytes(t, m), []zipMember{
				{name: tc.a, data: d1}, {name: tc.b, data: d2},
			})

			target := importTargetAbsent(t)
			stdout, errOut, code := s.Ferry("import", "--out", target, path)
			if code == 0 {
				t.Errorf("AC-import-dup-entries[%s]: import accepted duplicate/colliding entries %q + %q (must refuse)", tc.name, tc.a, tc.b)
			}
			assertTargetEmpty(t, target)
			// The AC's verify text promises a duplicate-entry message — gating.
			if !containsAnyFold(stdout+errOut, "duplicate", "collide", "collision", "same name", "already", "case") {
				t.Errorf("AC-import-dup-entries[%s]: refusal did not cite the duplicate/colliding entry the AC promises\n%s\n%s", tc.name, stdout, errOut)
			}
		})
	}
}

// TestImportNoClobber covers AC-import-no-clobber (N3): importing into a non-empty
// existing target refuses and leaves the pre-existing content untouched.
func TestImportNoClobber_AC_import_no_clobber(t *testing.T) {
	t.Parallel()
	a := NewSandbox(t)
	seedExportRepo(t, a, map[string]string{"shared.txt": "ok\n"})
	out := exportOK(t, a)

	b := NewSandbox(t)
	target := filepath.Join(t.TempDir(), "existing-nonempty")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	sentinel := filepath.Join(target, "PRE_EXISTING.txt")
	if err := os.WriteFile(sentinel, []byte("do not clobber\n"), 0o644); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}
	snap := b.SnapshotFile(t, sentinel)

	stdout, errOut, code := b.Ferry("import", "--out", target, out)
	if code == 0 {
		t.Errorf("AC-import-no-clobber: import into a non-empty target exited 0 (must refuse)")
	}
	snap.AssertUnchanged(t)
	// The AC's verify text promises a "target not empty" message — gating.
	if !containsAnyFold(stdout+errOut, "empty", "exists", "not empty", "non-empty", "already") {
		t.Errorf("AC-import-no-clobber: refusal did not cite the non-empty target the AC promises:\n%s\n%s", stdout, errOut)
	}
}

// TestImportLocalGated covers AC-import-local-gated: the local layer lands ONLY when
// the bundle was written --include-local AND import is run --include-local (double
// opt-in); a bundle written WITHOUT --include-local never yields local content.
func TestImportLocalGated_AC_import_local_gated(t *testing.T) {
	t.Parallel()
	a := NewSandbox(t)
	seedExportRepo(t, a, map[string]string{
		"shared.txt":            "shared\n",
		"local/zsh/zshrc.local": "export LOCAL=1\n",
	})
	// ferry.local.toml is part of the local layer too; force-track it (normally
	// gitignored) so the include-local bundle carries it and the gating covers it.
	a.WriteRepoFile(t, "ferry.local.toml", "# per-machine overrides\n")
	gitForceAdd(t, a.Repo, "add ferry.local.toml", "ferry.local.toml")

	withLocal := exportOK(t, a, "--include-local")
	withoutLocal := exportOK(t, a) // no local layer inside

	localRel := filepath.Join("local", "zsh", "zshrc.local")

	// NOTE on ferry.local.toml: M4 says import writes a fresh ferry.local.toml TEMPLATE
	// "unless --include-local content was imported", so a ferry.local.toml at the target
	// may be ferry's own template rather than bundle content — we therefore assert
	// gating on local/** (unambiguously bundle content) and, for ferry.local.toml, only
	// that WITHOUT --include-local its BUNDLE content is not laid down (checked via its
	// distinctive body marker, not mere presence).
	const localTOMLMarker = "per-machine overrides"

	// (a) include-local bundle, imported WITHOUT the flag → no local content: local/**
	// absent, and no ferry.local.toml carrying the BUNDLE's marker body (a template is ok).
	b1 := NewSandbox(t)
	tgt1 := importTargetAbsent(t)
	if _, errOut, code := b1.Ferry("import", "--out", tgt1, withLocal); code != 0 {
		t.Fatalf("import(a) exited %d\n%s", code, errOut)
	}
	assertTargetLacks(t, tgt1, localRel)
	if data, err := os.ReadFile(filepath.Join(tgt1, "ferry.local.toml")); err == nil && strings.Contains(string(data), localTOMLMarker) {
		t.Errorf("AC-import-local-gated: bundle's ferry.local.toml content was laid down WITHOUT --include-local")
	}

	// (b) include-local bundle, imported WITH the flag → local content present.
	b2 := NewSandbox(t)
	tgt2 := importTargetAbsent(t)
	if _, errOut, code := b2.Ferry("import", "--include-local", "--out", tgt2, withLocal); code != 0 {
		t.Fatalf("import(b) exited %d\n%s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(tgt2, localRel)); err != nil {
		t.Errorf("AC-import-local-gated: double opt-in did not lay down local/** : %v", err)
	}
	// Double opt-in also lands the bundle's ferry.local.toml CONTENT (its marker body),
	// not merely a fresh template — the whole local layer is imported. An importer that
	// always writes a template regardless would fail this.
	if data, err := os.ReadFile(filepath.Join(tgt2, "ferry.local.toml")); err != nil {
		t.Errorf("AC-import-local-gated: double opt-in did not lay down ferry.local.toml: %v", err)
	} else if !strings.Contains(string(data), localTOMLMarker) {
		t.Errorf("AC-import-local-gated: double opt-in wrote a fresh ferry.local.toml template instead of the bundle's content (marker %q missing)", localTOMLMarker)
	}

	// (c) bundle WITHOUT the local layer, imported WITH the flag → still no local content
	// (the bundle carries none): local/** absent, and no ferry.local.toml with the
	// bundle's marker body (a fresh template is still permitted per M4).
	b3 := NewSandbox(t)
	tgt3 := importTargetAbsent(t)
	if _, errOut, code := b3.Ferry("import", "--include-local", "--out", tgt3, withoutLocal); code != 0 {
		t.Fatalf("import(c) exited %d\n%s", code, errOut)
	}
	assertTargetLacks(t, tgt3, localRel)
	if data, err := os.ReadFile(filepath.Join(tgt3, "ferry.local.toml")); err == nil && strings.Contains(string(data), localTOMLMarker) {
		t.Errorf("AC-import-local-gated: bundle without a local layer yielded its ferry.local.toml content under --include-local")
	}
}

// keysOf returns the sorted-ish key list of a member map for error messages.
func keysOf(m map[string]*zip.File) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// -----------------------------------------------------------------------------
// Regression — final-review findings (v0.2.0 offline bundle)
// -----------------------------------------------------------------------------

// fakeBinaryKey is NON-FUNCTIONAL binary content carrying a private-key marker plus a
// NUL byte, so it classifies as BINARY (the line-based text gate can't scan it) yet
// still embeds a high-signal key header the binary-safe scanner keys on. Never a real
// key.
var fakeBinaryKey = append(append([]byte{0x00, 0x01, 0xff, 0x00},
	[]byte("-----BEGIN OPENSSH PRIVATE KEY-----FAKEbin0000")...), 0x00, 0x00)

// TestExportBinaryKeyWithheld is a regression for the CRITICAL binary/secret
// incoherence: a tracked BINARY file that embeds private-key markers must be WITHHELD
// on export (reported, never bundled) — export no longer includes binaries blindly.
func TestExportBinaryKeyWithheld_regression(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	seedExportRepo(t, s, map[string]string{"safe.txt": "clean\n"})
	// A tracked binary carrying key material.
	if err := os.WriteFile(s.RepoPath("blob.bin"), fakeBinaryKey, 0o644); err != nil {
		t.Fatalf("write blob.bin: %v", err)
	}
	gitCommitAll(t, s.Repo, "add key-bearing binary")

	out := bundleOutPath(t)
	stdout, stderr, code := s.Ferry("export", "--out", out)
	if code != 0 {
		t.Fatalf("export must SKIP the key-bearing binary (exit 0), got %d\n%s\n%s", code, stdout, stderr)
	}
	members := readZipMembers(t, out)
	if _, ok := members["blob.bin"]; ok {
		t.Errorf("regression: key-bearing binary leaked into the bundle (must be withheld)")
	}
	// The key marker bytes must appear NOWHERE in the produced zip.
	if zb, _ := os.ReadFile(out); bytes.Contains(zb, []byte("BEGIN OPENSSH PRIVATE KEY")) {
		t.Errorf("regression: key marker bytes present in the bundle bytes")
	}
	// Reported withheld, naming the path and a key/withheld reason.
	report := stdout + stderr
	if !containsAnyFold(report, "blob.bin") || !containsAnyFold(report, "withheld", "key", "material") {
		t.Errorf("regression: withheld report did not name blob.bin with a key reason\n%s", report)
	}
	if _, ok := members["safe.txt"]; !ok {
		t.Errorf("regression: the clean file should still be bundled")
	}
}

// TestImportBinaryKeyRefused is a regression for the symmetric import re-gate: a
// crafted bundle carrying a BINARY payload with key markers is REFUSED on import (the
// same binary-safe scan export applies), so a bundle carrying key bytes never lands.
func TestImportBinaryKeyRefused_regression(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	path := filepath.Join(t.TempDir(), "binkey.zip")
	m := bundleManifest{
		FormatVersion: bundleFormatVersion, FerryVersion: "0.2.0-test",
		Entries: []bundleEntry{{Path: "blob.bin", Size: int64(len(fakeBinaryKey)), SHA256: sha256Hex(fakeBinaryKey)}},
	}
	writeCraftedZip(t, path, manifestBytes(t, m), []zipMember{{name: "blob.bin", data: fakeBinaryKey}})

	target := importTargetAbsent(t)
	stdout, errOut, code := s.Ferry("import", "--out", target, path)
	if code == 0 {
		t.Errorf("regression: import accepted a binary payload carrying key material (must refuse)")
	}
	assertTargetEmpty(t, target)
	if !containsAnyFold(stdout+errOut, "key", "secret", "material", "refus") {
		t.Errorf("regression: refusal did not cite the embedded key material\n%s\n%s", stdout, errOut)
	}
}

// TestBinaryRoundtrip is a regression: a CLEAN binary (NUL bytes, no key markers)
// round-trips through export→import byte-identically — the binary scan must not
// over-withhold ordinary binary assets.
func TestBinaryRoundtrip_regression(t *testing.T) {
	t.Parallel()
	a := NewSandbox(t)
	cleanBin := []byte{0x00, 0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xde, 0xad}
	seedExportRepo(t, a, map[string]string{"shared.txt": "hi\n"})
	if err := os.WriteFile(a.RepoPath("logo.bin"), cleanBin, 0o644); err != nil {
		t.Fatalf("write logo.bin: %v", err)
	}
	gitCommitAll(t, a.Repo, "add clean binary")
	out := exportOK(t, a)
	if _, ok := readZipMembers(t, out)["logo.bin"]; !ok {
		t.Fatalf("regression: clean binary was not bundled (over-withheld)")
	}

	b := NewSandbox(t)
	target := importTargetAbsent(t)
	if _, errOut, code := b.Ferry("import", "--out", target, out); code != 0 {
		t.Fatalf("regression: import of a clean-binary bundle exited %d\n%s", code, errOut)
	}
	got, err := os.ReadFile(filepath.Join(target, "logo.bin"))
	if err != nil {
		t.Fatalf("regression: clean binary missing after import: %v", err)
	}
	if !bytes.Equal(got, cleanBin) {
		t.Errorf("regression: clean binary not byte-identical after roundtrip\nwant %v\ngot  %v", cleanBin, got)
	}
}

// TestRoundtripGitignoreByteIdentical is a regression for the .gitignore-drift MAJOR:
// export→import→re-export is byte-identical, and import does NOT mutate a bundled
// .gitignore (no appended local-layer lines, no newly-tracked .gitignore appearing).
func TestRoundtripGitignoreByteIdentical_regression(t *testing.T) {
	t.Parallel()
	a := NewSandbox(t)
	// Source repo tracks a .gitignore that already covers the local layer (as the real
	// config repo does) plus some unrelated content.
	seedExportRepo(t, a, map[string]string{
		".gitignore": "ferry.local.toml\nlocal/\n*.log\n",
		"shared.txt": "content\n",
	})
	out1 := exportOK(t, a)

	// Import into a fresh B.
	b := NewSandbox(t)
	target := importTargetAbsent(t)
	if _, errOut, code := b.Ferry("import", "--out", target, out1); code != 0 {
		t.Fatalf("import exited %d\n%s", code, errOut)
	}
	// The imported .gitignore must be BYTE-IDENTICAL to the source's (no mutation).
	src, _ := os.ReadFile(a.RepoPath(".gitignore"))
	got, err := os.ReadFile(filepath.Join(target, ".gitignore"))
	if err != nil {
		t.Fatalf("regression: .gitignore missing after import: %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Errorf("regression: import mutated the bundled .gitignore\nwant %q\ngot  %q", src, got)
	}

	// Re-export from B (point B's config at the imported repo — import wrote it) and
	// compare the payload members: the shared tree must be byte-identical, so a
	// re-exported .gitignore matches the original.
	out2 := filepath.Join(t.TempDir(), "reexport.zip")
	if _, errOut, code := b.Ferry("export", "--out", out2); code != 0 {
		t.Fatalf("re-export exited %d\n%s", code, errOut)
	}
	m1 := readZipMembers(t, out1)
	m2 := readZipMembers(t, out2)
	for name, f1 := range m1 {
		if name == manifestMember {
			continue
		}
		f2, ok := m2[name]
		if !ok {
			t.Errorf("regression: re-export dropped member %q present in the first export", name)
			continue
		}
		if !bytes.Equal(zipMemberBytes(t, f1), zipMemberBytes(t, f2)) {
			t.Errorf("regression: member %q not byte-identical across export→import→re-export", name)
		}
	}
	// And no NEW payload member appeared on re-export (e.g. a freshly-created .gitignore
	// where the source had none — here it did have one, but guard against extra drift).
	for name := range m2 {
		if name == manifestMember {
			continue
		}
		if _, ok := m1[name]; !ok {
			t.Errorf("regression: re-export gained a new member %q (round-trip drift)", name)
		}
	}
}

// TestExportOutUnderSSHRefused is a regression for the bundle-FILE-path MAJOR: an
// --out that resolves under ~/.ssh is refused BEFORE any write; the ~/.ssh tripwire is
// intact and no bundle is written there.
func TestExportOutUnderSSHRefused_regression(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SSHTripwire(t)
	seedExportRepo(t, s, map[string]string{"shared.txt": "ok\n"})

	outUnderSSH := s.HomePath(".ssh", "x.zip")
	stdout, stderr, code := s.Ferry("export", "--out", outUnderSSH)
	if code == 0 {
		t.Errorf("regression: export accepted an --out under ~/.ssh (must refuse)")
	}
	s.AssertSSHUntouched(t)
	if _, err := os.Stat(outUnderSSH); err == nil {
		t.Errorf("regression: a bundle was written under ~/.ssh at %q", outUnderSSH)
	}
	if !containsAnyFold(stdout+stderr, "ssh", "refus", "out") {
		t.Errorf("regression: refusal did not explain the ~/.ssh out rule\n%s\n%s", stdout, stderr)
	}
}

// TestImportBundleUnderSSHRefused is a regression for the bundle-FILE-path MAJOR: an
// import whose bundle path resolves under ~/.ssh is refused BEFORE the file is read;
// the tripwire is intact and nothing lands at the target.
func TestImportBundleUnderSSHRefused_regression(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SSHTripwire(t)
	// A path under ~/.ssh; it need not exist — the guard must refuse before opening it.
	bundleUnderSSH := s.HomePath(".ssh", "bundle.zip")

	target := importTargetAbsent(t)
	stdout, stderr, code := s.Ferry("import", "--out", target, bundleUnderSSH)
	if code == 0 {
		t.Errorf("regression: import accepted a bundle path under ~/.ssh (must refuse)")
	}
	s.AssertSSHUntouched(t)
	assertTargetEmpty(t, target)
	if !containsAnyFold(stdout+stderr, "ssh", "refus", "bundle") {
		t.Errorf("regression: refusal did not explain the ~/.ssh bundle-path rule\n%s\n%s", stdout, stderr)
	}
}
