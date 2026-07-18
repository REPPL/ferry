package work

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/statefile"
)

func testKey() string { return strings.Repeat("c", 40) }

func TestState_MissingFileIsEmpty(t *testing.T) {
	s, err := LoadStateAt(t.TempDir(), testKey())
	if err != nil {
		t.Fatalf("LoadStateAt: %v", err)
	}
	if s.Baseline != nil || len(s.Acks) != 0 || s.LastReceive != nil || len(s.Written) != 0 {
		t.Errorf("empty state not empty: %+v", s)
	}
}

func TestState_RoundTrip(t *testing.T) {
	root := t.TempDir()
	s, err := LoadStateAt(root, testKey())
	if err != nil {
		t.Fatal(err)
	}
	s.Baseline = &Baseline{
		Op: "pack", Seq: 7, Bundle: strings.Repeat("d", 64),
		At:    "2026-07-18T10:00:00Z",
		Files: map[string]map[string]string{ItemNext: {"NEXT.md": strings.Repeat("e", 64)}},
	}
	s.Acks = []Ack{{Item: ItemNext, Path: "NEXT.md", SHA256: strings.Repeat("f", 64)}}
	s.LastReceive = &ReceiveRecord{SnapshotID: "snap-1", Seq: 7, Paths: []string{"/home/bob/x"}}
	s.RecordWritten("/home/bob/b", "/home/bob/a", "/home/bob/b")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := LoadStateAt(root, testKey())
	if err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if got.Baseline == nil || got.Baseline.Seq != 7 || got.Baseline.Files[ItemNext]["NEXT.md"] != strings.Repeat("e", 64) {
		t.Errorf("baseline lost: %+v", got.Baseline)
	}
	if got.LastReceive == nil || got.LastReceive.SnapshotID != "snap-1" {
		t.Errorf("last receive lost: %+v", got.LastReceive)
	}
	if want := []string{"/home/bob/a", "/home/bob/b"}; len(got.Written) != 2 || got.Written[0] != want[0] || got.Written[1] != want[1] {
		t.Errorf("Written = %v, want %v (sorted, deduped)", got.Written, want)
	}
	if !got.Acknowledged(ItemNext, "NEXT.md", strings.Repeat("f", 64)) {
		t.Error("recorded acknowledgement not found")
	}
	if got.Acknowledged(ItemNext, "NEXT.md", strings.Repeat("0", 64)) {
		t.Error("acknowledgement matched a different content hash — must be pinned")
	}
}

func TestState_FutureVersionRefused(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, testKey()+".json")
	if err := os.WriteFile(path, []byte(`{"version": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadStateAt(root, testKey())
	var fve *statefile.FutureVersionError
	if !errors.As(err, &fve) {
		t.Fatalf("err = %v, want *statefile.FutureVersionError", err)
	}
}

func TestState_CorruptRefusedButEmptyTolerated(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, testKey()+".json")

	// Zero-length (torn write) degrades to an empty state.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStateAt(root, testKey()); err != nil {
		t.Errorf("zero-length state file refused, want empty state: %v", err)
	}

	// An unversioned object is not a work state file: refuse, never guess.
	if err := os.WriteFile(path, []byte(`{"written": ["/x"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStateAt(root, testKey()); err == nil {
		t.Error("unversioned state file accepted, want refusal")
	}
}

func TestState_BadKeyRefused(t *testing.T) {
	for _, bad := range []string{"", "short", "../../etc/passwd", strings.Repeat("g", 40)} {
		if _, err := LoadStateAt(t.TempDir(), bad); err == nil {
			t.Errorf("LoadStateAt accepted key %q, want refusal", bad)
		}
	}
}
