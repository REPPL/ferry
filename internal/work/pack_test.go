package work

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packFixture is a complete two-sided pack setup: a fake home containing a
// project repo with work-local files, agent memory, and a transcript store.
type packFixture struct {
	home  string
	repo  string
	id    Identity
	lc    Locator
	st    *Store
	state *State
}

func newPackFixture(t *testing.T) *packFixture {
	t.Helper()
	home := t.TempDir()
	repo := filepath.Join(home, "src", "proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-q", "-m", "root")

	id, err := ProjectIdentity(repo)
	if err != nil {
		t.Fatal(err)
	}
	lc := Locator{Home: home, ProjectDir: repo, StoreKey: id.Key}

	writeFileT(t, filepath.Join(repo, ".abcd", ".work.local", "NEXT.md"), "# NEXT\ncontinue here\n")
	writeFileT(t, filepath.Join(repo, ".abcd", ".work.local", "run-journal.json"), `{"runs":[]}`)
	memDir := filepath.Join(home, ".claude", "projects", ClaudeProjectsKey(repo), "memory")
	writeFileT(t, filepath.Join(memDir, "MEMORY.md"), "# Memory index\n")
	writeFileT(t, filepath.Join(memDir, "fact.md"), "a fact\n")
	writeFileT(t, filepath.Join(home, ".abcd", "history", id.Key, "sess-1.jsonl"), `{"redacted":true}`)

	st, err := OpenStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	state, err := LoadStateAt(filepath.Join(home, ".local", "state", "ferry"), id.Key)
	if err != nil {
		t.Fatal(err)
	}
	return &packFixture{home: home, repo: repo, id: id, lc: lc, st: st, state: state}
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func defaultOpts() PackOptions {
	return PackOptions{Account: "alice@studio", FerryVersion: "test", Now: "2026-07-18T10:00:00Z"}
}

func TestPack_HappyPath(t *testing.T) {
	fx := newPackFixture(t)
	res, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts())
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if res.Ref.Seq != 1 {
		t.Errorf("seq = %d, want 1", res.Ref.Seq)
	}

	// The stored bundle is a zip whose members and manifest agree.
	data, err := os.ReadFile(res.Ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("stored bundle is not a zip: %v", err)
	}
	members := map[string]bool{}
	var manifestData []byte
	for _, f := range zr.File {
		members[f.Name] = true
		if f.Name == ManifestMember {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			buf := new(bytes.Buffer)
			if _, err := buf.ReadFrom(rc); err != nil {
				t.Fatal(err)
			}
			rc.Close()
			manifestData = buf.Bytes()
		}
	}
	for _, want := range []string{
		"next/NEXT.md", "run-journal/run-journal.json",
		"agent-memory/MEMORY.md", "agent-memory/fact.md",
		"transcripts/sess-1.jsonl", ManifestMember,
	} {
		if !members[want] {
			t.Errorf("bundle missing member %q (have %v)", want, members)
		}
	}
	m, err := DecodeManifest(manifestData)
	if err != nil {
		t.Fatalf("bundle manifest: %v", err)
	}
	if m.Seq != res.Ref.Seq || m.Key != fx.id.Key || m.ScanVerdict != ScanVerdictClean {
		t.Errorf("manifest = seq %d key %s verdict %s", m.Seq, m.Key, m.ScanVerdict)
	}

	// Claim, baseline, and marker are all recorded.
	claims, err := fx.st.Claims(fx.id.Key)
	if err != nil || len(claims) != 1 || claims[0].Events[0].Op != OpPack {
		t.Errorf("claims = %+v, %v", claims, err)
	}
	if fx.state.Baseline == nil || fx.state.Baseline.Op != OpPack || fx.state.Baseline.Seq != 1 {
		t.Errorf("baseline = %+v", fx.state.Baseline)
	}
	mk, ok, err := ReadHandoverMarker(fx.repo)
	if err != nil || !ok {
		t.Fatalf("marker: %v ok=%v", err, ok)
	}
	if mk.Seq != 1 || mk.Bundle != res.Ref.SHA256 || mk.Files[ItemNext]["NEXT.md"] == "" {
		t.Errorf("marker = %+v", mk)
	}

	// The marker itself must never be packed as cargo.
	if members["run-journal/"+HandoverMarkerName] || members["next/"+HandoverMarkerName] {
		t.Error("handover marker leaked into the cargo")
	}
}

func TestPack_RefusesWithoutHandoverNote(t *testing.T) {
	fx := newPackFixture(t)
	if err := os.Remove(filepath.Join(fx.repo, ".abcd", ".work.local", "NEXT.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts()); err == nil {
		t.Fatal("pack without NEXT.md succeeded, want refusal")
	}
	opts := defaultOpts()
	opts.AllowEmpty = true
	res, err := Pack(fx.st, fx.lc, fx.id, fx.state, opts)
	if err != nil {
		t.Fatalf("pack --allow-empty: %v", err)
	}
	for _, it := range res.Manifest.Items {
		if it.Name == ItemNext && (it.Included || it.Reason != "missing") {
			t.Errorf("next item = %+v, want excluded as missing", it)
		}
	}
}

func TestPack_SecretGateAbortsAndAckReleases(t *testing.T) {
	fx := newPackFixture(t)
	secretText := "api_key = \"sk-ferrytest-FAKE1234567890abcdefghijklmnopqrstuv\"\n"
	notePath := filepath.Join(fx.repo, ".abcd", ".work.local", "NEXT.md")
	writeFileT(t, notePath, secretText)

	_, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts())
	var sge *SecretGateError
	if !errors.As(err, &sge) {
		t.Fatalf("err = %v, want *SecretGateError", err)
	}
	if len(sge.Findings) == 0 || sge.Findings[0].Item != ItemNext {
		t.Fatalf("findings = %+v", sge.Findings)
	}
	// Nothing was written on abort.
	if refs, _ := fx.st.Bundles(fx.id.Key); len(refs) != 0 {
		t.Errorf("bundles after abort = %+v, want none", refs)
	}

	// Acknowledge the finding, pinned to the exact content hash: pack passes.
	fx.state.Acks = append(fx.state.Acks, Ack{
		Item: sge.Findings[0].Item, Path: sge.Findings[0].Path, SHA256: sge.Findings[0].SHA256,
	})
	res, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts())
	if err != nil {
		t.Fatalf("pack after ack: %v", err)
	}
	if res.Manifest.ScanVerdict != ScanVerdictAcknowledged {
		t.Errorf("verdict = %s, want acknowledged", res.Manifest.ScanVerdict)
	}

	// The pin is to content: changed flagged content re-aborts.
	writeFileT(t, notePath, secretText+"more\n")
	if _, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts()); !errors.As(err, &sge) {
		t.Fatalf("err after content change = %v, want *SecretGateError again", err)
	}
}

func TestPack_ExcludeIsRecorded(t *testing.T) {
	fx := newPackFixture(t)
	opts := defaultOpts()
	opts.Excludes = []string{ItemTranscripts}
	res, err := Pack(fx.st, fx.lc, fx.id, fx.state, opts)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	var found bool
	for _, it := range res.Manifest.Items {
		if it.Name == ItemTranscripts {
			found = true
			if it.Included || it.Reason != "excluded" {
				t.Errorf("transcripts item = %+v, want excluded", it)
			}
		}
	}
	if !found {
		t.Error("transcripts item missing from manifest")
	}

	opts.Excludes = []string{"no-such-item"}
	if _, err := Pack(fx.st, fx.lc, fx.id, fx.state, opts); err == nil {
		t.Error("unknown --exclude accepted, want refusal")
	}
}

func TestPack_MissingOptionalItemsTolerated(t *testing.T) {
	fx := newPackFixture(t)
	if err := os.RemoveAll(filepath.Join(fx.home, ".abcd", "history", fx.id.Key)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(fx.home, ".claude")); err != nil {
		t.Fatal(err)
	}
	res, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts())
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	for _, it := range res.Manifest.Items {
		switch it.Name {
		case ItemAgentMemory, ItemTranscripts:
			if it.Included || it.Reason != "missing" {
				t.Errorf("%s = %+v, want missing", it.Name, it)
			}
		}
	}
}

func TestPack_SymlinkInCargoRefused(t *testing.T) {
	fx := newPackFixture(t)
	memDir := filepath.Join(fx.home, ".claude", "projects", ClaudeProjectsKey(fx.repo), "memory")
	if err := os.Symlink(filepath.Join(fx.home, "elsewhere"), filepath.Join(memDir, "link.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts()); err == nil {
		t.Fatal("pack with symlinked cargo member succeeded, want refusal")
	}
}

func TestPack_SequencesAdvance(t *testing.T) {
	fx := newPackFixture(t)
	if _, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts()); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(fx.repo, ".abcd", ".work.local", "NEXT.md"), "# NEXT\nupdated\n")
	res, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Ref.Seq != 2 {
		t.Errorf("second pack seq = %d, want 2", res.Ref.Seq)
	}
	if fx.state.Baseline.Seq != 2 {
		t.Errorf("baseline seq = %d, want 2", fx.state.Baseline.Seq)
	}
}

// contentHash mirrors the pack-side hashing for assertions.
func contentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestPack_MarkerRecordsContentHashes(t *testing.T) {
	fx := newPackFixture(t)
	note := "# NEXT\ncontinue here\n"
	if _, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts()); err != nil {
		t.Fatal(err)
	}
	mk, ok, err := ReadHandoverMarker(fx.repo)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got := mk.Files[ItemNext]["NEXT.md"]; got != contentHash(note) {
		t.Errorf("marker hash = %s, want %s", got, contentHash(note))
	}
	if len(mk.Bundle) != 64 || !isLowerHex(mk.Bundle) {
		t.Errorf("marker bundle hash malformed: %q", mk.Bundle)
	}
}

func TestPack_SecretShapedFileNameRefusedWithoutEcho(t *testing.T) {
	fx := newPackFixture(t)
	memDir := filepath.Join(fx.home, ".claude", "projects", ClaudeProjectsKey(fx.repo), "memory")
	secretName := "sk-ferrytest-FAKE1234567890abcdefghijklmnopqrstuv.md"
	writeFileT(t, filepath.Join(memDir, secretName), "innocent content\n")

	_, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts())
	var sge *SecretGateError
	if !errors.As(err, &sge) {
		t.Fatalf("err = %v, want *SecretGateError for a secret-shaped file NAME", err)
	}
	// The refusal must not echo the secret-shaped name back (mirrors bundle
	// export's withheld-path handling).
	if msg := err.Error(); len(msg) > 0 && strings.Contains(msg, "sk-ferrytest") {
		t.Errorf("refusal echoes the secret-shaped name:\n%s", msg)
	}

	// --exclude releases the pack.
	opts := defaultOpts()
	opts.Excludes = []string{ItemAgentMemory}
	if _, err := Pack(fx.st, fx.lc, fx.id, fx.state, opts); err != nil {
		t.Fatalf("pack with the item excluded: %v", err)
	}
}
