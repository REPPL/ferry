package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Restore reverts every managed path to its immutable pre-ferry baseline, so
// each path is byte-identical (content), mode-identical, and symlink-identical
// to its original state — and a path that "did not exist" pre-ferry is removed.
//
// BEFORE reverting, it snapshots the CURRENT state of every path it will touch
// (snapshotRestore) so an unwanted restore is itself reversible. The returned
// snapshot ID identifies that pre-restore snapshot.
func (e *Engine) Restore() (snapshotID string, err error) {
	paths, err := e.baselinePaths()
	if err != nil {
		return "", err
	}
	return e.restorePaths(paths)
}

// ScopedRestore reverts only the given paths to their baseline (a partial
// revert, e.g. one domain's files). Like Restore it snapshots current state
// first. A path with no baseline is skipped (nothing pre-ferry to revert to).
// Entries are absolute file paths or registered resource paths
// (ResourcePath(domain)); resource entries restore through their Resource hook.
func (e *Engine) ScopedRestore(absPaths []string) (snapshotID string, err error) {
	var scoped []string
	for _, p := range absPaths {
		if e.HasBaseline(p) {
			scoped = append(scoped, p)
		}
	}
	return e.restorePaths(scoped)
}

// restorePaths snapshots then reverts the given paths to baseline.
func (e *Engine) restorePaths(absPaths []string) (string, error) {
	snapID, err := e.snapshotCurrent(absPaths)
	if err != nil {
		return "", err
	}
	// Deterministic order keeps restore predictable and testable.
	sort.Strings(absPaths)
	for _, p := range absPaths {
		state, ok, err := e.Baseline(p)
		if err != nil {
			return snapID, err
		}
		if !ok {
			continue
		}
		var blob []byte
		if state.HasBlob {
			blob, err = loadBlob(e.baselineBlobPathFor(state.Blob))
			if err != nil {
				return snapID, err
			}
		}
		if err := e.applyState(state, blob); err != nil {
			return snapID, err
		}
	}
	return snapID, nil
}

// applyState re-applies a recorded PathState. A resource entry (synthetic
// "resource://" path) is routed through its registered Resource.Restore hook; a
// real file/symlink/absent entry goes through restoreState. This keeps file and
// resource restore symmetric across Restore/ScopedRestore, snapshot undo, and
// incomplete-run rollback.
func (e *Engine) applyState(state PathState, blob []byte) error {
	if isResourcePath(state.Path) {
		return e.restoreResource(domainForResourcePath(state.Path), blob, state.Kind == KindAbsent)
	}
	return restoreState(state, blob)
}

// restoreResource drives a registered preference-domain Resource's own restore.
// When absent is true the baseline recorded the domain as not existing pre-ferry,
// so the resource REMOVES/clears it (e.g. `defaults delete`); otherwise blob (the
// captured state, stored the same secret-safe way as file blobs) is re-applied.
func (e *Engine) restoreResource(domain string, blob []byte, absent bool) error {
	r, ok := e.resources[domain]
	if !ok {
		return fmt.Errorf("backup: no resource registered for domain %q", domain)
	}
	return r.Restore(blob, absent)
}

// baselinePaths returns every absolute path that has a baseline entry, read from
// the stored metadata (the store key is a hash, so the path lives inside the
// JSON, not the filename).
func (e *Engine) baselinePaths() ([]string, error) {
	ents, err := os.ReadDir(e.baselineDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		st, ok, err := e.baselineByMetaFile(ent.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, st.Path)
		}
	}
	return out, nil
}

// baselineByMetaFile reads one baseline metadata file and applies the SAME
// validation as Baseline, so the full-restore enumeration robustly skips a
// stale/partial/corrupt .json (or one whose referenced blob is missing) instead
// of failing the whole restore. ok=false means "not a usable baseline; skip it".
func (e *Engine) baselineByMetaFile(name string) (PathState, bool, error) {
	data, err := os.ReadFile(filepath.Join(e.baselineDir, name))
	if err != nil {
		return PathState{}, false, err
	}
	return e.parseValidBaseline(data)
}
