package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Snapshot layout, per pre-restore snapshot, under snapshotDir:
//
//	<snapID>/manifest.json   []PathState of the paths AS THEY WERE before restore
//	<snapID>/<key>.blob      current file content for each affected file path
//
// A snapshot is taken BEFORE a restore mutates anything, so an unwanted restore
// is itself reversible: re-applying the snapshot returns the machine to its
// pre-restore state. Unlike the baseline this is NOT immutable — each restore
// makes a fresh one.

type snapshot struct {
	States []PathState `json:"states"`
}

// snapshotCurrent captures the current state of each path into a new snapshot
// and returns its ID. Empty input yields an (empty) snapshot — restore always
// produces a recoverable point even when there is nothing to revert.
func (e *Engine) snapshotCurrent(absPaths []string) (string, error) {
	id := newRunID()
	dir := filepath.Join(e.snapshotDir, id)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, dirPerm); err != nil {
		return "", err
	}

	var snap snapshot
	for _, p := range absPaths {
		state, content, err := e.captureForSnapshot(p)
		if err != nil {
			return "", err
		}
		if state.HasBlob {
			if err := writeStoreBlob(filepath.Join(dir, keyFor(p)+".blob"), content, state.Mode); err != nil {
				return "", err
			}
		}
		snap.States = append(snap.States, state)
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", err
	}
	if err := AtomicWrite(filepath.Join(dir, "manifest.json"), data, filePerm); err != nil {
		return "", err
	}
	return id, nil
}

// Snapshot captures the current state of absPaths into a new snapshot and
// returns its ID — the exported entry point for callers OUTSIDE a restore
// flow that need their mutation to be precisely reversible (the work domain
// takes one per receive, so `ferry work restore` can revert exactly that
// receive). RestoreSnapshot re-applies it.
func (e *Engine) Snapshot(absPaths []string) (string, error) {
	return e.snapshotCurrent(absPaths)
}

// RestoreSnapshot re-applies a previously taken pre-restore snapshot, undoing a
// restore. It returns the machine's affected paths to their pre-restore state.
func (e *Engine) RestoreSnapshot(snapID string) error {
	dir := filepath.Join(e.snapshotDir, snapID)
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return err
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return err
	}
	for _, state := range snap.States {
		var blob []byte
		if state.HasBlob {
			blob, err = loadBlob(filepath.Join(dir, keyFor(state.Path)+".blob"))
			if err != nil {
				return err
			}
		}
		if err := e.applyState(state, blob); err != nil {
			// A resource declined its restore (e.g. iTerm2 running): SKIP this entry
			// and continue undoing the rest rather than aborting the whole snapshot
			// re-apply. A running iTerm2 would silently drop the re-import anyway.
			if isResourceRestoreSkip(err) {
				continue
			}
			return err
		}
	}
	return nil
}

// captureForSnapshot captures the current state of p for a pre-restore snapshot.
// A resource path is captured via its Resource.Backup hook (so undoing a restore
// returns the resource to its pre-restore state); a real path uses captureState.
func (e *Engine) captureForSnapshot(p string) (PathState, []byte, error) {
	if isResourcePath(p) {
		res, ok := e.resources[domainForResourcePath(p)]
		if !ok {
			return PathState{}, nil, fmt.Errorf("backup: no resource registered for domain %q", domainForResourcePath(p))
		}
		blob, absent, err := res.Backup()
		if err != nil {
			return PathState{}, nil, err
		}
		// Mirror the baseline: a currently-absent domain snapshots as KindAbsent so
		// undoing a restore returns it to absent (delete), not an empty import.
		if absent {
			return PathState{Path: p, Kind: KindAbsent}, nil, nil
		}
		return PathState{Path: p, Kind: KindFile, Mode: filePerm, HasBlob: true}, blob, nil
	}
	return captureState(p)
}
