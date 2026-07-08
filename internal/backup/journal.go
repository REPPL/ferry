package backup

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/REPPL/ferry/internal/statefile"
)

// journalVersion is the current on-disk schema version of a journal manifest.
// Version 1 is the first versioned form. A manifest with no "version" field is
// the original pre-versioning form and reads as version 1. A manifest a newer
// ferry wrote is refused on read (never rolled back or deleted), so a downgraded
// ferry cannot mis-revert state a newer one owns. See internal/statefile.
const journalVersion = 1

// Journal layout, per run, under journalDir:
//
//	<runID>/manifest.json   the JournalEntry (prior states + actions)
//	<runID>/<key>.blob      prior file content for each affected file path
//	<runID>/COMPLETE        marker written last; its presence == run finished
//
// Baseline = "what it was before ferry ever ran"; a journal = "what one run
// changed". A run that dies mid-way leaves a directory with NO COMPLETE marker;
// RollbackIncomplete detects that and reverts each recorded prior state.

// completeMarker is the filename whose presence marks a run finished.
const completeMarker = "COMPLETE"

// JournalEntry is one apply run's record.
type JournalEntry struct {
	Version   int       `json:"version"`
	RunID     string    `json:"run_id"`
	StartedAt time.Time `json:"started_at"`
	Changes   []Change  `json:"changes"`
}

// Change records, for one path this run touched, its PRIOR state and the action
// ferry took. Prior state is what rollback restores to.
type Change struct {
	Prior  PathState `json:"prior"`
	Action string    `json:"action"` // e.g. "write", "remove"
}

// run is an in-progress, lock-held journal transaction. Created by Begin,
// mutated via record, finalised by Commit (writes COMPLETE).
type run struct {
	dir   string
	entry JournalEntry
}

func (e *Engine) runDir(runID string) string { return filepath.Join(e.journalDir, runID) }

func newRunID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	// Time prefix keeps directories sortable; random suffix avoids collisions.
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405.000000000"), hex.EncodeToString(b[:]))
}

// Begin starts a new journal run. The directory and manifest are created up
// front (manifest empty, no COMPLETE marker) so even a crash before the first
// write leaves a detectable — and harmlessly empty — incomplete entry.
func (e *Engine) Begin() (*run, error) {
	id := newRunID()
	dir := e.runDir(id)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, dirPerm); err != nil {
		return nil, err
	}
	r := &run{
		dir:   dir,
		entry: JournalEntry{Version: journalVersion, RunID: id, StartedAt: time.Now().UTC()},
	}
	if err := r.flush(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *run) blobPath(absPath string) string {
	return filepath.Join(r.dir, keyFor(absPath)+".blob")
}

// record appends a change to the run: it persists the path's prior content (for
// a file) and adds the prior-state metadata to the manifest, then flushes the
// manifest so the record survives a crash on the very next step.
func (r *run) record(prior PathState, content []byte, action string) error {
	if prior.HasBlob {
		if err := writeStoreBlob(r.blobPath(prior.Path), content, prior.Mode); err != nil {
			return err
		}
	}
	r.entry.Changes = append(r.entry.Changes, Change{Prior: prior, Action: action})
	return r.flush()
}

// flush atomically rewrites the run manifest (temp + rename) so a crash during
// the write never leaves a truncated manifest.
func (r *run) flush() error {
	data, err := json.MarshalIndent(r.entry, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(r.dir, "manifest.json"), data, filePerm)
}

// Commit marks the run complete by writing the COMPLETE marker last (after all
// payloads are durably on disk). Once present, RollbackIncomplete leaves the
// run alone.
func (r *run) Commit() error {
	f, err := os.OpenFile(filepath.Join(r.dir, completeMarker), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, filePerm)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// fsync the run directory so the COMPLETE marker's dir entry is durable. Without
	// this a crash could lose the entry, and RollbackIncomplete would then roll back
	// a run that actually committed.
	return syncDir(r.dir)
}

func (r *run) isComplete() bool {
	_, err := os.Stat(filepath.Join(r.dir, completeMarker))
	return err == nil
}

// listRuns returns run directory IDs sorted oldest-first.
func (e *Engine) listRuns() ([]string, error) {
	ents, err := os.ReadDir(e.journalDir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, ent := range ents {
		if ent.IsDir() {
			ids = append(ids, ent.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (e *Engine) loadEntry(runID string) (JournalEntry, error) {
	var entry JournalEntry
	path := filepath.Join(e.runDir(runID), "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return entry, err
	}
	// Refuse a manifest a newer ferry wrote BEFORE parsing it, so a downgraded
	// ferry never rolls back (or deletes) a run it cannot faithfully interpret.
	if _, _, verr := statefile.Resolve(path, data, journalVersion); verr != nil {
		return entry, verr
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		return entry, err
	}
	// A pre-versioning manifest carries no "version" field; it is schema v1.
	if entry.Version == 0 {
		entry.Version = journalVersion
	}
	return entry, nil
}

// RollbackIncomplete reverts any dangling (incomplete) journal run left by an
// apply that died mid-way, then removes its directory. The next apply calls this
// BEFORE computing its plan so it always starts from a consistent state. It is a
// no-op when every run is complete. Changes are undone in reverse order so the
// last mutation is reverted first.
//
// It returns the run IDs that were rolled back (for reporting/tests).
func (e *Engine) RollbackIncomplete() ([]string, error) {
	ids, err := e.listRuns()
	if err != nil {
		return nil, err
	}
	var rolled []string
	for _, id := range ids {
		markerPath := filepath.Join(e.runDir(id), completeMarker)
		if _, err := os.Stat(markerPath); err == nil {
			continue // complete; leave it.
		}
		if err := e.rollbackRun(id); err != nil {
			return rolled, err
		}
		rolled = append(rolled, id)
	}
	return rolled, nil
}

func (e *Engine) rollbackRun(runID string) error {
	entry, err := e.loadEntry(runID)
	if err != nil {
		// A manifest a NEWER ferry wrote must be left completely alone — not
		// rolled back and not dropped — so a downgraded ferry cannot destroy a
		// newer run's record. Surface the refusal instead.
		var fv *statefile.FutureVersionError
		if errors.As(err, &fv) {
			return err
		}
		// Any other unreadable manifest carries no recoverable record; drop it so
		// it stops being flagged as incomplete forever.
		return os.RemoveAll(e.runDir(runID))
	}
	dir := e.runDir(runID)
	// Reverse order: undo the most recent mutation first.
	for i := len(entry.Changes) - 1; i >= 0; i-- {
		ch := entry.Changes[i]
		var blob []byte
		if ch.Prior.HasBlob {
			blob, err = loadBlob(filepath.Join(dir, keyFor(ch.Prior.Path)+".blob"))
			if err != nil {
				return err
			}
		}
		if err := e.applyState(ch.Prior, blob); err != nil {
			// A resource declined its restore (e.g. iTerm2 running): SKIP this change
			// and keep rolling back the rest. A running iTerm2 rewrites its domain on
			// quit anyway, so we cannot re-import it now — and letting this abort would
			// WEDGE the next apply, which runs RollbackIncomplete before it plans.
			if isResourceRestoreSkip(err) {
				continue
			}
			return err
		}
	}
	return os.RemoveAll(dir)
}
