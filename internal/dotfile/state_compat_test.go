package dotfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/statefile"
)

// The v0.3.x last-applied fixture is a REAL file produced by the v0.3.2 binary
// (tag v0.3.2, commit cd648ae) driving `ferry apply` over a managed `.zshrc` in
// a throwaway sandbox HOME. Its keys are bare dotfile names and its values are
// content hashes, so it embeds nothing machine-specific and is committed verbatim.
const (
	compatLastApplied = "testdata/compat/dotfile-last-applied.json"
	futureLastApplied = "testdata/compat/future-dotfile-last-applied.json"
)

func seedState(t *testing.T, fixture string) string {
	t.Helper()
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, stateFileName), data, 0o600); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	return dir
}

// TestCompatMigratesV03xLastApplied proves the current code reads a real v0.3.x
// last-applied file, migrates it forward (backing the pre-migration file up
// first), and preserves every recorded hash.
func TestCompatMigratesV03xLastApplied(t *testing.T) {
	dir := seedState(t, compatLastApplied)
	statePath := filepath.Join(dir, stateFileName)

	original, _ := os.ReadFile(statePath)

	store, err := OpenStoreAt(dir)
	if err != nil {
		t.Fatalf("OpenStoreAt: %v", err)
	}

	// The recorded hash survives the migration unchanged.
	got, ok := store.LastApplied("zshrc")
	if !ok || got != "8c5b3b840aede84118740b0674286c619f4ca916a70d60b771e616d43e950d17" {
		t.Fatalf("LastApplied(zshrc) = %q,%v; want the v0.3.2 hash", got, ok)
	}

	// The file on disk is now the versioned envelope form.
	migrated, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read migrated: %v", err)
	}
	if v, versioned := statefile.PeekVersion(migrated); !versioned || v != lastAppliedVersion {
		t.Fatalf("migrated file version = %d,%v; want %d", v, versioned, lastAppliedVersion)
	}

	// The pre-migration bytes are preserved in a sibling backup, byte-for-byte.
	bak := statePath + ".pre-v1.bak"
	bakData, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("read backup %s: %v", bak, err)
	}
	if string(bakData) != string(original) {
		t.Fatalf("backup content mismatch:\n got %q\nwant %q", bakData, original)
	}
}

// TestCompatFutureVersionRefused proves a state file written by a newer ferry is
// refused with a clear, self-explanatory error and is left completely untouched.
func TestCompatFutureVersionRefused(t *testing.T) {
	dir := seedState(t, futureLastApplied)
	statePath := filepath.Join(dir, stateFileName)
	before, _ := os.ReadFile(statePath)

	_, err := OpenStoreAt(dir)
	var fv *statefile.FutureVersionError
	if !errors.As(err, &fv) {
		t.Fatalf("OpenStoreAt on a future-version file: got %v; want *statefile.FutureVersionError", err)
	}
	if !strings.Contains(fv.Error(), statePath) || fv.Found != 99 {
		t.Fatalf("refusal must name the file and version 99: %v", fv)
	}

	// The file is untouched and NO backup was written — the refusal corrupts nothing.
	after, _ := os.ReadFile(statePath)
	if string(after) != string(before) {
		t.Fatalf("future-version file was modified: got %q want %q", after, before)
	}
	if _, err := os.Stat(statePath + ".pre-v99.bak"); !os.IsNotExist(err) {
		t.Fatalf("a refused future-version file must not be backed up")
	}
}

// TestCompatReadOnlyDoesNotMigrate proves the write-free status/diff path reads a
// v0.3.x file correctly WITHOUT rewriting it or creating any ferry state, and
// still refuses a future-version file.
func TestCompatReadOnlyDoesNotMigrate(t *testing.T) {
	dir := seedState(t, compatLastApplied)
	statePath := filepath.Join(dir, stateFileName)
	before, _ := os.ReadFile(statePath)

	store, err := OpenStoreAtReadOnly(dir)
	if err != nil {
		t.Fatalf("OpenStoreAtReadOnly: %v", err)
	}
	if _, ok := store.LastApplied("zshrc"); !ok {
		t.Fatal("read-only store did not read the v0.3.x record")
	}
	after, _ := os.ReadFile(statePath)
	if string(after) != string(before) {
		t.Fatal("read-only open rewrote the state file")
	}
	if _, err := os.Stat(statePath + ".pre-v1.bak"); !os.IsNotExist(err) {
		t.Fatal("read-only open created a migration backup")
	}

	// A future-version file is refused on the read-only path too.
	fdir := seedState(t, futureLastApplied)
	if _, err := OpenStoreAtReadOnly(fdir); err == nil {
		t.Fatal("read-only open of a future-version file must refuse")
	}
}
