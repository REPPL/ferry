package backup

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrNilRun is returned by the run-scoped mutators when passed a nil run. A
// mutator must record into a live journal run (started with Begin); a nil run
// has no journal to record into, so this is a boundary error rather than a panic.
var ErrNilRun = errors.New("backup: nil run (start one with Begin)")

// BackupAndWrite transactionally replaces the file at absPath with newContent at
// the given mode. The sequence is crash-safe at every step:
//
//  1. record the path's prior state into the immutable baseline (if first
//     touch on this machine) and into this run's journal,
//  2. write newContent to a temp file in the SAME directory,
//  3. atomic rename it into place,
//  4. (the journal entry is marked complete later by run.Commit).
//
// A crash between any two steps leaves a recoverable state: the prior content is
// already in baseline+journal, the live file is either the old bytes (pre-rename)
// or the new bytes (post-rename), never a half-written file, and an unmarked
// journal run is rolled back by the next apply's RollbackIncomplete.
//
// absPath must be absolute; mode is the desired final mode of the live file.
func (e *Engine) BackupAndWrite(r *run, absPath string, newContent []byte, mode os.FileMode) error {
	if r == nil {
		return ErrNilRun
	}
	if !filepath.IsAbs(absPath) {
		return &NotAbsoluteError{Path: absPath}
	}

	// Re-validate the RESOLVED parent chain at the write boundary FIRST, before
	// any baseline capture or journal record, so a same-user process that
	// swapped an intermediate parent to a symlink AFTER plan time cannot redirect
	// the read+write outside $HOME or into ~/.ssh. Guarding first means a refused
	// write ingests nothing into the immutable baseline and records no journal
	// entry (which rollback could never replay through the same refusal). Fails
	// closed on refusal; a not-under-$HOME path (test root) is a no-op.
	if err := guardResolvedContainment(absPath); err != nil {
		return err
	}

	// (1a) Immutable baseline — captured from the CURRENT on-disk state, so it
	// must run before we mutate. No-op if a baseline already exists.
	if err := e.ensureBaseline(absPath); err != nil {
		return err
	}

	// (1b) Per-run journal — record prior state + intended action.
	prior, priorContent, err := captureState(absPath)
	if err != nil {
		return err
	}
	if err := r.record(prior, priorContent, "write"); err != nil {
		return err
	}

	// (2)+(3) temp write + atomic rename into place.
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	return AtomicWrite(absPath, newContent, mode)
}

// BackupAndRemove transactionally removes absPath, recording its prior state so
// restore/rollback can recreate it. Used when apply needs to delete a managed
// path. A no-op (recorded as "remove") if the path is already absent.
func (e *Engine) BackupAndRemove(r *run, absPath string) error {
	if r == nil {
		return ErrNilRun
	}
	if !filepath.IsAbs(absPath) {
		return &NotAbsoluteError{Path: absPath}
	}
	// Re-validate the RESOLVED parent chain BEFORE reading or deleting, symmetric
	// with BackupAndWrite and restoreState: a same-user process that swapped an
	// intermediate parent to a symlink (e.g. into ~/.ssh or outside $HOME) after
	// plan time would otherwise make captureState READ and os.RemoveAll DELETE
	// through the link. Fails closed; a not-under-$HOME path (test root) is a no-op.
	if err := guardResolvedContainment(absPath); err != nil {
		return err
	}
	if err := e.ensureBaseline(absPath); err != nil {
		return err
	}
	prior, priorContent, err := captureState(absPath)
	if err != nil {
		return err
	}
	if err := r.record(prior, priorContent, "remove"); err != nil {
		return err
	}
	if err := os.RemoveAll(absPath); err != nil {
		return err
	}
	return nil
}

// NotAbsoluteError is returned when a managed path is not absolute. The engine
// keys the store by absolute path, so a relative path would be ambiguous.
type NotAbsoluteError struct{ Path string }

func (e *NotAbsoluteError) Error() string {
	return "backup: path must be absolute: " + e.Path
}
