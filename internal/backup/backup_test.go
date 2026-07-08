package backup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// jsonMarshalLockInfo builds a lockfile payload for a given owner pid, used by
// tests to plant a lock owned by another process.
func jsonMarshalLockInfo(pid int) ([]byte, error) {
	return json.MarshalIndent(lockInfo{PID: pid, AcquiredAt: time.Now().UTC()}, "", "  ")
}

// newEngine builds an Engine rooted at a fresh temp dir so the real ~ is never
// touched, and returns a separate temp "home" dir to host managed files.
func newEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	state := t.TempDir()
	e, err := NewAt(state)
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	home := t.TempDir()
	return e, home
}

func mustWrite(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// baselineBlobFile resolves the content-addressed baseline blob path for a
// managed path by reading the path's metadata for the recorded content hash.
func baselineBlobFile(t *testing.T, e *Engine, path string) string {
	t.Helper()
	st, ok, err := e.Baseline(path)
	if err != nil || !ok {
		t.Fatalf("Baseline(%s) ok=%v err=%v", path, ok, err)
	}
	if st.Blob == "" {
		t.Fatalf("Baseline(%s) has no content-addressed blob", path)
	}
	return e.baselineBlobPathFor(st.Blob)
}

func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

// --- store layout & permissions ------------------------------------------

func TestStoreDirsAre0700(t *testing.T) {
	e, _ := newEngine(t)
	for _, d := range []string{e.root, e.baselineDir, e.journalDir, e.snapshotDir} {
		if got := statMode(t, d); got != dirPerm {
			t.Errorf("dir %s mode = %o, want %o", d, got, dirPerm)
		}
	}
}

func TestStoredBlobsAre0600ByDefault(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")
	mustWrite(t, target, []byte("orig"), 0o644) // looser-than-0600 original

	if err := e.ensureBaseline(target); err != nil {
		t.Fatal(err)
	}
	blob := baselineBlobFile(t, e, target)
	if got := statMode(t, blob); got != 0o600 {
		t.Errorf("baseline blob mode = %o, want 0600 (never looser than store default)", got)
	}
	meta := e.baselineMetaPath(target)
	if got := statMode(t, meta); got != 0o600 {
		t.Errorf("baseline meta mode = %o, want 0600", got)
	}
}

func TestStoredBlobPreservesStricterMode(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "secret")
	mustWrite(t, target, []byte("k"), 0o400) // stricter than 0600

	if err := e.ensureBaseline(target); err != nil {
		t.Fatal(err)
	}
	if got := statMode(t, baselineBlobFile(t, e, target)); got != 0o400 {
		t.Errorf("baseline blob mode = %o, want 0400 (stricter original preserved)", got)
	}
}

func TestEffectiveMode(t *testing.T) {
	cases := []struct {
		orig os.FileMode
		want os.FileMode
	}{
		{0o644, 0o600}, // looser -> clamped to 0600
		{0o600, 0o600},
		{0o400, 0o400}, // stricter preserved
		{0o200, 0o200},
		{0o755, 0o600}, // exec + group/other dropped
	}
	for _, c := range cases {
		if got := effectiveMode(c.orig); got != c.want {
			t.Errorf("effectiveMode(%o) = %o, want %o", c.orig, got, c.want)
		}
	}
}

// --- baseline immutability ------------------------------------------------

func TestBaselineWrittenOnceNeverOverwritten(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")
	mustWrite(t, target, []byte("ORIGINAL"), 0o600)

	if e.HasBaseline(target) {
		t.Fatal("HasBaseline true before any touch")
	}
	if err := e.ensureBaseline(target); err != nil {
		t.Fatal(err)
	}
	if !e.HasBaseline(target) {
		t.Fatal("HasBaseline false after ensureBaseline")
	}

	// Mutate the file, then ensure again. The baseline MUST still hold ORIGINAL.
	mustWrite(t, target, []byte("CHANGED-LATER"), 0o600)
	if err := e.ensureBaseline(target); err != nil {
		t.Fatal(err)
	}
	blob, err := loadBlob(baselineBlobFile(t, e, target))
	if err != nil {
		t.Fatal(err)
	}
	if string(blob) != "ORIGINAL" {
		t.Errorf("baseline content = %q, want ORIGINAL (immutable)", blob)
	}
}

// TestBaselineConcurrentFirstTouchSingleWinner races two goroutines doing the
// first WriteBaseline for the same path with DIFFERENT blob bytes, asserting
// exactly one set of bytes survives and the surviving blob pairs with the
// surviving metadata (the write-once invariant under contention).
func TestBaselineConcurrentFirstTouchSingleWinner(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")

	// Two competing first-touch baselines for the same path, distinguishable by
	// content, both flagged HasBlob so each stages and may rename a blob.
	mkState := PathState{Path: target, Kind: KindFile, Mode: 0o600, HasBlob: true}

	var wg sync.WaitGroup
	for i, content := range [][]byte{[]byte("ALPHA"), []byte("BRAVO")} {
		wg.Add(1)
		go func(_ int, c []byte) {
			defer wg.Done()
			if err := e.writeBaseline(mkState, c); err != nil {
				t.Errorf("writeBaseline: %v", err)
			}
		}(i, content)
	}
	wg.Wait()

	// Exactly ONE metadata entry is committed (the single Link winner). No stray
	// temp files (.ferry-blob-*) remain in the baseline dir.
	ents, err := os.ReadDir(e.baselineDir)
	if err != nil {
		t.Fatal(err)
	}
	var metas, tmps int
	for _, ent := range ents {
		switch {
		case filepath.Ext(ent.Name()) == ".json":
			metas++
		case ent.Name() == "blobs":
			// content-addressed blob store; orphans here are harmless.
		default:
			tmps++
		}
	}
	if metas != 1 {
		t.Fatalf("after race: %d meta entries; want exactly 1 (single Link winner)", metas)
	}
	if tmps != 0 {
		t.Errorf("stray temp files left behind in baseline dir: %d", tmps)
	}

	// The surviving metadata references its OWN content-addressed blob; loading it
	// yields exactly one racer's bytes — never a mix, and never corrupted by the
	// loser (content-addressing makes the winner's bytes immutable by construction).
	st, ok, err := e.Baseline(target)
	if err != nil || !ok {
		t.Fatalf("Baseline ok=%v err=%v", ok, err)
	}
	blob, err := loadBlob(e.baselineBlobPathFor(st.Blob))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(blob); s != "ALPHA" && s != "BRAVO" {
		t.Errorf("surviving blob = %q, want exactly one racer's bytes", s)
	}
	if st.Blob != hashContent(blob) {
		t.Errorf("metadata blob hash %q does not match its bytes", st.Blob)
	}
}

// TestBaselineConcurrentSameBytesBothSucceed races two first-touch WriteBaseline
// calls for the same path with IDENTICAL original bytes. Content-addressing makes
// this trivially safe: both write the same hash → same name → idempotent. Both
// calls succeed, the surviving baseline is valid, and its blob matches the bytes.
func TestBaselineConcurrentSameBytesBothSucceed(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")
	original := []byte("SAME-ORIGINAL-BYTES")
	state := PathState{Path: target, Kind: KindFile, Mode: 0o600, HasBlob: true}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.writeBaseline(state, original); err != nil {
				t.Errorf("writeBaseline: %v", err)
			}
		}()
	}
	wg.Wait()

	st, ok, err := e.Baseline(target)
	if err != nil || !ok {
		t.Fatalf("Baseline ok=%v err=%v, want a valid baseline", ok, err)
	}
	blob, err := loadBlob(e.baselineBlobPathFor(st.Blob))
	if err != nil {
		t.Fatal(err)
	}
	if string(blob) != string(original) {
		t.Errorf("surviving blob = %q, want %q", blob, original)
	}
}

// TestBaselineKindValidation asserts a parseable-but-incomplete metadata entry
// (Path set, no/unknown Kind) is NOT counted as a baseline.
func TestBaselineKindValidation(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")

	// Path present but Kind missing → not a complete baseline.
	if err := os.WriteFile(e.baselineMetaPath(target), []byte(`{"path":"`+target+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if e.HasBaseline(target) {
		t.Fatal("metadata with no Kind counted as a baseline")
	}

	// Unknown Kind → also not a baseline.
	if err := os.WriteFile(e.baselineMetaPath(target), []byte(`{"path":"`+target+`","kind":"bogus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if e.HasBaseline(target) {
		t.Fatal("metadata with unknown Kind counted as a baseline")
	}

	// KindFile but no referenced blob → not a baseline (blob is missing).
	if err := os.WriteFile(e.baselineMetaPath(target), []byte(`{"path":"`+target+`","kind":"file","has_blob":true,"blob":"deadbeef"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if e.HasBaseline(target) {
		t.Fatal("KindFile metadata with a missing blob counted as a baseline")
	}
}

// TestRestoreSkipsCorruptBaselineEntry plants a corrupt/incomplete .json next to
// a valid baseline and asserts the full-restore enumeration ignores the junk
// entry (skips it) rather than failing the whole restore.
func TestRestoreSkipsCorruptBaselineEntry(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")
	mustWrite(t, target, []byte("ORIGINAL"), 0o600)

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, target, []byte("CHANGED"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}

	// Plant a corrupt metadata file (a hash key that doesn't matter; it just must
	// be a *.json in the baseline dir) to pollute the enumeration.
	if err := os.WriteFile(filepath.Join(e.baselineDir, "deadbeef.json"), []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Also plant a parseable-but-incomplete one.
	if err := os.WriteFile(filepath.Join(e.baselineDir, "cafef00d.json"), []byte(`{"path":"/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Restore(); err != nil {
		t.Fatalf("Restore failed instead of skipping junk entries: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "ORIGINAL" {
		t.Errorf("valid baseline not restored: live = %q, want ORIGINAL", got)
	}
}

// TestBaselineBlobNotRecreatedWhenPresent asserts the content-addressed blob is
// published write-once: a second backup of the same bytes hits the Link-EEXIST
// path and does NOT replace the existing blob's inode (its mode stays put).
func TestBaselineBlobNotRecreatedWhenPresent(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")
	mustWrite(t, target, []byte("BYTES"), 0o600)

	if err := e.ensureBaseline(target); err != nil {
		t.Fatal(err)
	}
	blobPath := baselineBlobFile(t, e, target)
	fiBefore, err := os.Stat(blobPath)
	if err != nil {
		t.Fatal(err)
	}

	// Publish the identical bytes again directly: must be a no-op (Stat-present or
	// Link-EEXIST), leaving the same inode and mode.
	st, _, err := e.Baseline(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.publishBlob(st.Blob, []byte("BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	fiAfter, err := os.Stat(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fiBefore, fiAfter) {
		t.Error("blob inode changed: it was re-created instead of left write-once")
	}
	if fiAfter.Mode().Perm() != fiBefore.Mode().Perm() {
		t.Errorf("blob mode changed from %o to %o", fiBefore.Mode().Perm(), fiAfter.Mode().Perm())
	}
}

// TestBaselinePartialMetadataIsNotABaseline plants an empty/partial metadata
// .json (as a crash mid-write would leave) and asserts HasBaseline reports false,
// so a subsequent touch still captures the TRUE pre-ferry baseline rather than
// permanently skipping it.
func TestBaselinePartialMetadataIsNotABaseline(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")
	mustWrite(t, target, []byte("ORIGINAL"), 0o600)

	// Simulate a legacy crash that left an empty metadata file behind.
	if err := os.WriteFile(e.baselineMetaPath(target), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if e.HasBaseline(target) {
		t.Fatal("empty/partial metadata counted as a baseline (would skip true baseline forever)")
	}
	if _, ok, err := e.Baseline(target); ok || err != nil {
		t.Fatalf("Baseline ok=%v err=%v, want ok=false err=nil for partial metadata", ok, err)
	}

	// A truncated (non-empty but unparseable) entry must also read as absent.
	if err := os.WriteFile(e.baselineMetaPath(target), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if e.HasBaseline(target) {
		t.Fatal("truncated metadata counted as a baseline")
	}
}

// TestBaselineCorruptEntryNotDeletedByBackup asserts a pre-existing corrupt
// metadata file is NOT deleted by a backup attempt (no racy repair path): the
// write path surfaces a CorruptBaselineError, the corrupt file stays put, and it
// continues to read as absent.
func TestBaselineCorruptEntryNotDeletedByBackup(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")
	mustWrite(t, target, []byte("ORIGINAL"), 0o600)

	corrupt := []byte("{not valid baseline json")
	if err := os.WriteFile(e.baselineMetaPath(target), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	err := e.ensureBaseline(target)
	var cbe *CorruptBaselineError
	if !errors.As(err, &cbe) {
		t.Fatalf("ensureBaseline err = %v, want *CorruptBaselineError", err)
	}
	// The corrupt file must be untouched (never deleted from the write path).
	got, readErr := os.ReadFile(e.baselineMetaPath(target))
	if readErr != nil {
		t.Fatalf("corrupt metadata was removed by the backup attempt: %v", readErr)
	}
	if string(got) != string(corrupt) {
		t.Errorf("corrupt metadata mutated: %q", got)
	}
	if e.HasBaseline(target) {
		t.Error("corrupt entry counted as a baseline")
	}
}

func TestBaselineCapturesAbsent(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "does-not-exist")

	if err := e.ensureBaseline(target); err != nil {
		t.Fatal(err)
	}
	st, ok, err := e.Baseline(target)
	if err != nil || !ok {
		t.Fatalf("Baseline ok=%v err=%v", ok, err)
	}
	if st.Kind != KindAbsent {
		t.Errorf("kind = %q, want absent", st.Kind)
	}
}

func TestBaselineCapturesSymlink(t *testing.T) {
	e, home := newEngine(t)
	link := filepath.Join(home, "link")
	if err := os.Symlink("/some/target", link); err != nil {
		t.Fatal(err)
	}
	if err := e.ensureBaseline(link); err != nil {
		t.Fatal(err)
	}
	st, ok, err := e.Baseline(link)
	if err != nil || !ok {
		t.Fatalf("Baseline ok=%v err=%v", ok, err)
	}
	if st.Kind != KindSymlink || st.Target != "/some/target" {
		t.Errorf("got kind=%q target=%q, want symlink /some/target", st.Kind, st.Target)
	}
}

// --- transactional write + atomicity --------------------------------------

func TestBackupAndWriteReplacesAtomically(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")
	mustWrite(t, target, []byte("old"), 0o600)

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, target, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(target)
	if string(got) != "new" {
		t.Errorf("live content = %q, want new", got)
	}
	// No temp files left behind in the target dir.
	ents, _ := os.ReadDir(home)
	for _, ent := range ents {
		if filepath.Ext(ent.Name()) == "" && len(ent.Name()) > 1 && ent.Name()[0] == '.' {
			t.Errorf("stray temp file: %s", ent.Name())
		}
	}
}

func TestBackupAndWriteRejectsRelativePath(t *testing.T) {
	e, _ := newEngine(t)
	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, "relative/path", []byte("x"), 0o600); err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestAtomicWriteSurvivesAndIsExact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := AtomicWrite(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello" {
		t.Errorf("content = %q", got)
	}
	if got := statMode(t, path); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
	// Rewrite atomically; old content fully replaced, no temp leftovers.
	if err := AtomicWrite(path, []byte("world!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "world!!" {
		t.Errorf("content after rewrite = %q", got)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("expected exactly the target file, got %d entries", len(ents))
	}
}

// --- journal rollback of an incomplete entry ------------------------------

func TestRollbackIncompleteRevertsDanglingRun(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")
	mustWrite(t, target, []byte("ORIGINAL"), 0o600)

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, target, []byte("HALF-DONE"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: do NOT Commit. The run is now incomplete.

	got, _ := os.ReadFile(target)
	if string(got) != "HALF-DONE" {
		t.Fatalf("pre-rollback live = %q", got)
	}

	rolled, err := e.RollbackIncomplete()
	if err != nil {
		t.Fatal(err)
	}
	if len(rolled) != 1 {
		t.Fatalf("rolled back %d runs, want 1", len(rolled))
	}
	got, _ = os.ReadFile(target)
	if string(got) != "ORIGINAL" {
		t.Errorf("post-rollback live = %q, want ORIGINAL", got)
	}
	// The run dir is gone, so a second rollback is a no-op.
	rolled2, err := e.RollbackIncomplete()
	if err != nil {
		t.Fatal(err)
	}
	if len(rolled2) != 0 {
		t.Errorf("second rollback touched %d runs, want 0", len(rolled2))
	}
}

func TestRollbackLeavesCompletedRunsAlone(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")
	mustWrite(t, target, []byte("old"), 0o600)

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, target, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}
	rolled, err := e.RollbackIncomplete()
	if err != nil {
		t.Fatal(err)
	}
	if len(rolled) != 0 {
		t.Errorf("rolled back %d completed runs, want 0", len(rolled))
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new" {
		t.Errorf("completed run was reverted: live = %q", got)
	}
}

func TestRollbackRecreatesPathThatDidNotExist(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "created-by-ferry")

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, target, []byte("new file"), 0o600); err != nil {
		t.Fatal(err)
	}
	// crash before commit
	if _, err := e.RollbackIncomplete(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("path that did not exist pre-run should be removed on rollback, lstat err=%v", err)
	}
}

// --- restore round-trip ---------------------------------------------------

func TestRestoreRoundTripByteAndModeIdentical(t *testing.T) {
	e, home := newEngine(t)
	fileA := filepath.Join(home, "a")
	fileB := filepath.Join(home, "b")
	mustWrite(t, fileA, []byte("AAA"), 0o600)
	mustWrite(t, fileB, []byte("BBB"), 0o400) // stricter mode

	// Stamp a known, fixed past mtime on each original so we can assert restore
	// returns the file mtime-identical (not freshly timestamped).
	wantMtimeA := time.Date(2021, 3, 14, 9, 26, 53, 0, time.UTC)
	wantMtimeB := time.Date(2019, 7, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(fileA, wantMtimeA, wantMtimeA); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fileB, wantMtimeB, wantMtimeB); err != nil {
		t.Fatal(err)
	}

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, fileA, []byte("AAA-changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, fileB, []byte("BBB-changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Restore(); err != nil {
		t.Fatal(err)
	}

	gotA, _ := os.ReadFile(fileA)
	if string(gotA) != "AAA" {
		t.Errorf("A content = %q, want AAA", gotA)
	}
	if m := statMode(t, fileA); m != 0o600 {
		t.Errorf("A mode = %o, want 0600", m)
	}
	gotB, _ := os.ReadFile(fileB)
	if string(gotB) != "BBB" {
		t.Errorf("B content = %q, want BBB", gotB)
	}
	if m := statMode(t, fileB); m != 0o400 {
		t.Errorf("B mode = %o, want 0400 (stricter original restored)", m)
	}
	if got := mtimeOf(t, fileA); !got.Equal(wantMtimeA) {
		t.Errorf("A mtime = %v, want original %v", got, wantMtimeA)
	}
	if got := mtimeOf(t, fileB); !got.Equal(wantMtimeB) {
		t.Errorf("B mtime = %v, want original %v", got, wantMtimeB)
	}
}

// mtimeOf returns the modification time of path (no symlink follow).
func mtimeOf(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return fi.ModTime()
}

func TestRestoreRemovesPathThatDidNotExist(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "new") // absent pre-ferry

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, target, []byte("ferry made this"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target should exist after write: %v", err)
	}

	if _, err := e.Restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("did-not-exist path should be removed on restore, lstat err=%v", err)
	}
}

func TestRestoreSymlinkRoundTrip(t *testing.T) {
	e, home := newEngine(t)
	link := filepath.Join(home, "link")
	if err := os.Symlink("/orig/target", link); err != nil {
		t.Fatal(err)
	}

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// Replace the symlink with a regular file.
	if err := e.BackupAndWrite(r, link, []byte("now a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Restore(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("restored path is not a symlink")
	}
	tgt, _ := os.Readlink(link)
	if tgt != "/orig/target" {
		t.Errorf("symlink target = %q, want /orig/target", tgt)
	}
}

// TestRestoreSymlinkRestampsOwnMtime asserts a restored symlink gets its ORIGINAL
// link mtime back (lstat, NOFOLLOW) — not a fresh timestamp. os.Chtimes follows the
// link and cannot set a link's own mtime, so restore uses lchtimes (unix.Lutimes /
// utimensat+AT_SYMLINK_NOFOLLOW) on the supported platforms.
func TestRestoreSymlinkRestampsOwnMtime(t *testing.T) {
	e, home := newEngine(t)
	link := filepath.Join(home, "link")
	if err := os.Symlink("/orig/target", link); err != nil {
		t.Fatal(err)
	}
	// Stamp a known past mtime on the symlink's OWN node (NOFOLLOW). On platforms
	// where lchtimes is a no-op there is nothing meaningful to assert.
	wantMtime := time.Date(2020, 5, 4, 8, 15, 16, 0, time.UTC)
	if err := lchtimes(link, wantMtime); err != nil {
		t.Fatalf("lchtimes seed: %v", err)
	}
	if got := mtimeOf(t, link); !got.Equal(wantMtime) {
		t.Skipf("platform cannot set a link's own mtime (got %v); restamp not asserted", got)
	}

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// Replace the symlink with a regular file, capturing the original link state.
	if err := e.BackupAndWrite(r, link, []byte("now a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Restore(); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("restored path is not a symlink")
	}
	if got := mtimeOf(t, link); !got.Equal(wantMtime) {
		t.Errorf("restored symlink own mtime = %v, want original %v", got, wantMtime)
	}
}

func TestScopedRestoreOnlyTouchesGivenPaths(t *testing.T) {
	e, home := newEngine(t)
	a := filepath.Join(home, "a")
	b := filepath.Join(home, "b")
	mustWrite(t, a, []byte("A0"), 0o600)
	mustWrite(t, b, []byte("B0"), 0o600)

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, a, []byte("A1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, b, []byte("B1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := e.ScopedRestore([]string{a}); err != nil {
		t.Fatal(err)
	}
	gotA, _ := os.ReadFile(a)
	gotB, _ := os.ReadFile(b)
	if string(gotA) != "A0" {
		t.Errorf("a = %q, want A0 (restored)", gotA)
	}
	if string(gotB) != "B1" {
		t.Errorf("b = %q, want B1 (untouched by scoped restore)", gotB)
	}
}

// --- pre-restore snapshot reversibility -----------------------------------

func TestRestoreIsItselfReversibleViaSnapshot(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")
	mustWrite(t, target, []byte("ORIGINAL"), 0o600)

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, target, []byte("CURRENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}

	snapID, err := e.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "ORIGINAL" {
		t.Fatalf("after restore = %q, want ORIGINAL", got)
	}
	// Undo the (unwanted) restore from the pre-restore snapshot.
	if err := e.RestoreSnapshot(snapID); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "CURRENT" {
		t.Errorf("after snapshot-undo = %q, want CURRENT", got)
	}
}

// --- lock exclusivity & stale reclaim -------------------------------------

func TestLockIsExclusive(t *testing.T) {
	e, _ := newEngine(t)
	// Plant a lock owned by a DIFFERENT, live pid so re-acquire cannot reclaim
	// it as our own (re-acquire of this process's own lock is intentionally
	// allowed for crash recovery).
	otherPID := os.Getpid() + 1
	planted, _ := jsonMarshalLockInfo(otherPID)
	if err := os.WriteFile(e.lockPath, planted, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.lockWithAlive(func(pid int) bool { return pid == otherPID })
	if err == nil {
		t.Fatal("second Lock succeeded while held by a live owner")
	}
	var held *ErrLockHeld
	if !asErrLockHeld(err, &held) {
		t.Fatalf("err = %v, want *ErrLockHeld", err)
	}
	if held.OwnerPID != otherPID {
		t.Errorf("ErrLockHeld.OwnerPID = %d, want %d", held.OwnerPID, otherPID)
	}

	// Once the holder "goes away" (now reported dead), acquiring succeeds.
	if err := os.WriteFile(e.lockPath, planted, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := e.lockWithAlive(func(int) bool { return false })
	if err != nil {
		t.Fatalf("Lock after holder died: %v", err)
	}
	_ = l.Unlock()
}

func TestLockReclaimsStaleLockOfDeadOwner(t *testing.T) {
	e, _ := newEngine(t)
	// Pre-plant a lockfile owned by a "dead" pid.
	if _, err := e.lockWithAlive(func(int) bool { return false }); err != nil {
		// This first acquire writes our own pid; remove and plant a foreign one.
		t.Fatal(err)
	}
	// Now the lock is held by us. Re-acquire with a dead-owner predicate should
	// reclaim regardless (treats non-live owners as stale).
	l, err := e.lockWithAlive(func(int) bool { return false })
	if err != nil {
		t.Fatalf("expected stale reclaim, got %v", err)
	}
	if l == nil {
		t.Fatal("nil lock after reclaim")
	}
	_ = l.Unlock()
}

func TestLockReclaimsCorruptLock(t *testing.T) {
	e, _ := newEngine(t)
	if err := os.WriteFile(e.lockPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := e.Lock()
	if err != nil {
		t.Fatalf("corrupt lock should be reclaimed, got %v", err)
	}
	_ = l.Unlock()
}

// TestLockSyncFailureLeavesNoOrphan asserts the post-creation cleanup: when the
// lockfile is created but the fsync fails, Lock must return an error AND remove
// the just-created lockfile. Otherwise the file is orphaned recording THIS live
// PID, and a later apply would wedge behind a lock no one actually holds (no
// *Lock was returned, so nobody can Unlock it).
func TestLockSyncFailureLeavesNoOrphan(t *testing.T) {
	e, _ := newEngine(t)

	injected := errors.New("injected sync failure")
	orig := syncFile
	syncFile = func(*os.File) error { return injected }
	defer func() { syncFile = orig }()

	l, err := e.Lock()
	if err == nil {
		t.Fatal("Lock succeeded despite injected Sync failure")
	}
	if !errors.Is(err, injected) {
		t.Errorf("err = %v, want injected sync failure", err)
	}
	if l != nil {
		t.Error("Lock returned a non-nil *Lock on the failure path")
	}
	if _, statErr := os.Stat(e.lockPath); !os.IsNotExist(statErr) {
		t.Fatalf("lockfile orphaned after failed Lock: stat err = %v, want not-exist", statErr)
	}

	// And a subsequent Lock (sync restored) must succeed — no wedge behind an orphan.
	syncFile = orig
	l2, err := e.Lock()
	if err != nil {
		t.Fatalf("Lock after cleaned-up failure: %v", err)
	}
	_ = l2.Unlock()
}

// --- resource (plist-domain) backup/restore -------------------------------

// fakeResource is an in-memory Resource: Backup returns its current state,
// Restore overwrites it. When absent is true the resource models a domain that
// does not exist (Backup reports absent; Restore with absent=true deletes it).
// It records how many times each is called.
type fakeResource struct {
	domain   string
	state    []byte
	absent   bool // current "domain does not exist" state
	backups  int
	restores int
	deletes  int // Restore(absent=true) calls — domain removed
}

func (f *fakeResource) Domain() string { return f.domain }
func (f *fakeResource) Backup() ([]byte, bool, error) {
	f.backups++
	if f.absent {
		return nil, true, nil
	}
	out := make([]byte, len(f.state))
	copy(out, f.state)
	return out, false, nil
}
func (f *fakeResource) Restore(blob []byte, absent bool) error {
	f.restores++
	if absent {
		f.deletes++
		f.state = nil
		f.absent = true
		return nil
	}
	f.absent = false
	f.state = make([]byte, len(blob))
	copy(f.state, blob)
	return nil
}

// skipResourceErr is a Resource.Restore error that reports itself as a clean
// resource-restore SKIP (mirroring terminal.ErrITerm2Running when iTerm2 is
// running), so the engine must continue the surrounding restore instead of
// aborting. It satisfies ResourceRestoreSkipper without importing terminal.
type skipResourceErr struct{}

func (skipResourceErr) Error() string                { return "resource declined restore (running)" }
func (skipResourceErr) ResourceRestoreSkipped() bool { return true }

// decliningResource is a Resource whose Restore always DECLINES with a skip error,
// recording how many times it was attempted.
type decliningResource struct {
	domain   string
	attempts int
}

func (d *decliningResource) Domain() string                 { return d.domain }
func (d *decliningResource) Backup() ([]byte, bool, error)  { return []byte("PREFS"), false, nil }
func (d *decliningResource) Restore(_ []byte, _ bool) error { d.attempts++; return skipResourceErr{} }

// TestRestoreSkipsDecliningResourceAndContinues proves the running-guard contract
// at the engine level: a resource that DECLINES its restore (skip error) does NOT
// abort the multi-path restore — every other managed path is still reverted, the
// overall restore returns no error, and the resource restore was attempted.
func TestRestoreSkipsDecliningResourceAndContinues(t *testing.T) {
	e, home := newEngine(t)

	// A registered resource that will decline restore, captured into the baseline.
	res := &decliningResource{domain: "com.googlecode.iterm2"}
	e.Register(res)

	// A managed file whose baseline must still be restored despite the resource skip.
	target := filepath.Join(home, "f")
	mustWrite(t, target, []byte("ORIGINAL"), 0o600)

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BackupResource(r, res.domain); err != nil {
		t.Fatal(err)
	}
	if err := e.BackupAndWrite(r, target, []byte("CHANGED"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}

	// Full restore: the resource declines, but that must be a clean skip.
	if _, err := e.Restore(); err != nil {
		t.Fatalf("Restore aborted on a declining resource instead of skipping it: %v", err)
	}
	if res.attempts < 1 {
		t.Errorf("resource restore was not attempted (attempts=%d)", res.attempts)
	}
	if got, _ := os.ReadFile(target); string(got) != "ORIGINAL" {
		t.Errorf("file baseline not restored past the resource skip: live = %q, want ORIGINAL", got)
	}
}

func TestBackupResourceCapturesAndRestoreReapplies(t *testing.T) {
	e, _ := newEngine(t)
	res := &fakeResource{domain: "com.googlecode.iterm2", state: []byte("ORIGINAL-PREFS")}
	e.Register(res)

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// Capture the resource's current (pre-mutation) state.
	if err := e.BackupResource(r, res.domain); err != nil {
		t.Fatal(err)
	}
	if res.backups != 1 {
		t.Fatalf("Backup called %d times, want 1", res.backups)
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}

	// Simulate a later mutation of the live resource.
	res.state = []byte("MUTATED-PREFS")

	// Full restore must re-apply the captured blob through Resource.Restore.
	if _, err := e.Restore(); err != nil {
		t.Fatal(err)
	}
	if string(res.state) != "ORIGINAL-PREFS" {
		t.Errorf("after restore resource state = %q, want ORIGINAL-PREFS", res.state)
	}
	if res.restores < 1 {
		t.Errorf("Restore called %d times, want >=1", res.restores)
	}
}

// TestBackupResourceAbsentRecordsAbsentBaselineAndRestoreDeletes covers the
// fresh-machine path: a domain that does not exist pre-ferry must NOT abort the
// backup; instead an absent (KindAbsent) baseline is recorded, and a later
// restore REMOVES the domain (delete), returning the machine to pre-ferry.
func TestBackupResourceAbsentRecordsAbsentBaselineAndRestoreDeletes(t *testing.T) {
	e, _ := newEngine(t)
	res := &fakeResource{domain: "com.googlecode.iterm2", absent: true}
	e.Register(res)

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// Absence must NOT be a Backup failure that aborts apply.
	if err := e.BackupResource(r, res.domain); err != nil {
		t.Fatalf("BackupResource aborted on an absent domain: %v", err)
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}

	// The recorded baseline says "this domain did not exist pre-ferry".
	st, ok, err := e.Baseline(ResourcePath(res.domain))
	if err != nil || !ok {
		t.Fatalf("absent resource baseline missing: ok=%v err=%v", ok, err)
	}
	if st.Kind != KindAbsent {
		t.Fatalf("absent resource baseline Kind = %q, want %q", st.Kind, KindAbsent)
	}

	// Simulate apply having created/configured the domain afterwards.
	res.absent = false
	res.state = []byte("FERRY-CONFIGURED-PREFS")

	// Restore must DELETE the domain (back to absent), not import a blob.
	if _, err := e.Restore(); err != nil {
		t.Fatal(err)
	}
	if res.deletes < 1 {
		t.Errorf("restore of an absent baseline made %d deletes, want >=1", res.deletes)
	}
	if !res.absent {
		t.Errorf("after restore domain absent = false, want true (deleted)")
	}
}

func TestBackupResourceBaselineIsWriteOnce(t *testing.T) {
	e, _ := newEngine(t)
	res := &fakeResource{domain: "com.apple.Terminal", state: []byte("FIRST")}
	e.Register(res)

	r, err := e.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BackupResource(r, res.domain); err != nil {
		t.Fatal(err)
	}
	// A later capture, after the resource changed, MUST NOT overwrite the
	// immutable baseline (write-once, mirroring file baselines).
	res.state = []byte("SECOND")
	if err := e.BackupResource(r, res.domain); err != nil {
		t.Fatal(err)
	}
	blob, err := loadBlob(baselineBlobFile(t, e, ResourcePath(res.domain)))
	if err != nil {
		t.Fatal(err)
	}
	if string(blob) != "FIRST" {
		t.Errorf("resource baseline = %q, want FIRST (immutable)", blob)
	}
}

func TestBackupResourceRejectsNilRun(t *testing.T) {
	e, _ := newEngine(t)
	e.Register(&fakeResource{domain: "d", state: []byte("x")})
	if err := e.BackupResource(nil, "d"); !errors.Is(err, ErrNilRun) {
		t.Errorf("BackupResource(nil) err = %v, want ErrNilRun", err)
	}
}

// --- nil-run boundary errors ----------------------------------------------

func TestMutatorsRejectNilRun(t *testing.T) {
	e, home := newEngine(t)
	target := filepath.Join(home, "f")
	if err := e.BackupAndWrite(nil, target, []byte("x"), 0o600); !errors.Is(err, ErrNilRun) {
		t.Errorf("BackupAndWrite(nil) err = %v, want ErrNilRun", err)
	}
	if err := e.BackupAndRemove(nil, target); !errors.Is(err, ErrNilRun) {
		t.Errorf("BackupAndRemove(nil) err = %v, want ErrNilRun", err)
	}
}

// asErrLockHeld is a tiny errors.As helper kept local to avoid an import just
// for one assertion site.
func asErrLockHeld(err error, target **ErrLockHeld) bool {
	for err != nil {
		if e, ok := err.(*ErrLockHeld); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
