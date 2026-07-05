package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/REPPL/ferry/internal/statefile"
)

// seedRun materialises a journal run directory holding the given manifest
// fixture, with no COMPLETE marker so RollbackIncomplete treats it as an
// interrupted run. The manifest fixtures are real: manifest.json is produced by
// the v0.3.2 binary (tag v0.3.2), scrubbed of the sandbox HOME.
func seedRun(t *testing.T, e *Engine, runID, fixture string) string {
	t.Helper()
	dir := e.runDir(runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return manifestPath
}

// TestCompatJournalReadsV03xManifestAsV1 proves the current code reads a real
// v0.3.x manifest (which carries no "version" field) as schema version 1 with
// every recorded change intact.
func TestCompatJournalReadsV03xManifestAsV1(t *testing.T) {
	e, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runID := "20260705T141558.612951000-5c3848db343d"
	seedRun(t, e, runID, "testdata/compat/manifest.json")

	entry, err := e.loadEntry(runID)
	if err != nil {
		t.Fatalf("loadEntry: %v", err)
	}
	if entry.Version != journalVersion {
		t.Fatalf("legacy manifest version = %d; want %d", entry.Version, journalVersion)
	}
	if entry.RunID != runID {
		t.Fatalf("run id = %q; want %q", entry.RunID, runID)
	}
	if len(entry.Changes) != 1 || entry.Changes[0].Action != "write" {
		t.Fatalf("changes not parsed from v0.3.x manifest: %+v", entry.Changes)
	}
}

// TestCompatJournalFutureVersionRefused proves an interrupted run whose manifest
// a newer ferry wrote is refused — neither rolled back nor deleted — so nothing
// is corrupted.
func TestCompatJournalFutureVersionRefused(t *testing.T) {
	e, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runID := "20990101T000000.000000000-ffffffffffff"
	manifestPath := seedRun(t, e, runID, "testdata/compat/future-manifest.json")
	before, _ := os.ReadFile(manifestPath)

	rolled, err := e.RollbackIncomplete()
	var fv *statefile.FutureVersionError
	if !errors.As(err, &fv) {
		t.Fatalf("RollbackIncomplete over a future-version run: got %v; want *statefile.FutureVersionError", err)
	}
	if len(rolled) != 0 {
		t.Fatalf("a refused future-version run must not be rolled back; rolled=%v", rolled)
	}

	// The run directory and its manifest are untouched.
	if _, err := os.Stat(e.runDir(runID)); err != nil {
		t.Fatalf("refused run directory was removed: %v", err)
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("refused manifest was removed: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("refused manifest was modified")
	}
}
