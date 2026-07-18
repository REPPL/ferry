package work

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/paths"
)

// ProjectStatus is one project's work-state picture: cargo, claims,
// handover-marker freshness, divergence, and store size.
type ProjectStatus struct {
	Key      string
	StoreDir string
	Bundles  []BundleRef
	// TopTie holds every bundle at the highest sequence when there is more
	// than one — an unresolved equal-seq fork.
	TopTie []BundleRef
	Claims []Claim
	Marker *HandoverMarker
	// MarkerDirty lists "item/rel" entries modified (or gone missing) since
	// the handover marker was recorded.
	MarkerDirty []string
	Baseline    *Baseline
	// Diverged lists guarded "item/rel" entries whose current content differs
	// from this account's last pack/receive baseline.
	Diverged []string
	// StoreBytes is the total size of the whole cargo store.
	StoreBytes int64
	// OtherProjects names other projects present in the store, by their
	// newest manifest's repo basename.
	OtherProjects []string
}

// Status assembles the project's work-state picture. It is read-only: no
// store, state, or project file is mutated.
func Status(st *Store, lc Locator, id Identity, state *State) (*ProjectStatus, error) {
	key, refs, err := st.LocateProject(id)
	if err != nil {
		return nil, err
	}
	claims, err := st.Claims(key)
	if err != nil {
		return nil, err
	}
	s := &ProjectStatus{
		Key:      key,
		StoreDir: st.ProjectDir(key),
		Bundles:  refs,
		Claims:   claims,
		Baseline: state.Baseline,
	}
	if n := len(refs); n > 0 {
		top := refs[n-1].Seq
		for _, r := range refs {
			if r.Seq == top {
				s.TopTie = append(s.TopTie, r)
			}
		}
		if len(s.TopTie) == 1 {
			s.TopTie = nil
		}
	}

	current, err := currentItemHashes(lc)
	if err != nil {
		return nil, err
	}
	marker, ok, err := ReadHandoverMarker(lc.ProjectDir)
	if err != nil {
		return nil, err
	}
	if ok {
		s.Marker = marker
		s.MarkerDirty = deltas(marker.Files, current)
	}
	if state.Baseline != nil {
		s.Diverged = deltas(guardedOnly(state.Baseline.Files), current)
	}

	s.StoreBytes, err = dirBytes(st.Root())
	if err != nil {
		return nil, err
	}
	s.OtherProjects, err = st.otherProjects(key, id)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// currentItemHashes hashes every registry item's current on-disk content,
// keyed item -> rel -> sha.
func currentItemHashes(lc Locator) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for _, it := range BuiltinItems() {
		root, err := it.Locate(lc)
		if err != nil {
			return nil, err
		}
		switch it.Kind {
		case KindFile:
			hash, exists, err := hashFileIfExists(root)
			if err != nil {
				return nil, err
			}
			if exists {
				out[it.Name] = map[string]string{filepath.Base(root): hash}
			}
		case KindDir:
			hashes, err := hashDir(root)
			if err != nil {
				return nil, err
			}
			if len(hashes) > 0 {
				out[it.Name] = hashes
			}
		}
	}
	return out, nil
}

// guardedOnly filters an item->rel->sha map down to guarded-overwrite items —
// union-merge items are never divergence.
func guardedOnly(files map[string]map[string]string) map[string]map[string]string {
	guarded := map[string]bool{}
	for _, it := range BuiltinItems() {
		if it.Policy == PolicyGuardedOverwrite {
			guarded[it.Name] = true
		}
	}
	out := map[string]map[string]string{}
	for item, rels := range files {
		if guarded[item] {
			out[item] = rels
		}
	}
	return out
}

// deltas lists "item/rel" entries whose recorded hash no longer matches the
// current content ("(missing)" when the file is gone), sorted.
func deltas(recorded, current map[string]map[string]string) []string {
	var out []string
	for item, rels := range recorded {
		for rel, sha := range rels {
			cur, ok := current[item][rel]
			switch {
			case !ok:
				out = append(out, item+"/"+rel+" (missing)")
			case cur != sha:
				out = append(out, item+"/"+rel)
			}
		}
	}
	sort.Strings(out)
	return out
}

// dirBytes sums the regular-file sizes under root; a missing root is 0.
func dirBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}

// otherProjects names the store's other project directories by their newest
// manifest's repo basename (or the directory key when unreadable) — orphaned
// or foreign cargo stays visible rather than silent.
func (st *Store) otherProjects(currentKey string, id Identity) ([]string, error) {
	entries, err := os.ReadDir(st.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == currentKey || !rootSHA.MatchString(e.Name()) {
			continue
		}
		refs, err := st.Bundles(e.Name())
		if err != nil || len(refs) == 0 {
			continue
		}
		newest := refs[len(refs)-1]
		label := e.Name()[:12]
		if m, _, err := readBundle(newest.Path, newest.SHA256); err == nil {
			if intersects(m.Roots, id.Roots) {
				continue // same project under another root key, already counted
			}
			label = filepath.Base(m.RepoPath) + " (" + label + ")"
		}
		out = append(out, label)
	}
	sort.Strings(out)
	return out, nil
}

// AllWrittenPaths unions the Written sets of every project's work state under
// ferry's state dir — the enumeration `ferry restore work` reverts.
func AllWrittenPaths() ([]string, error) {
	root, err := paths.StateDir()
	if err != nil {
		return nil, err
	}
	return AllWrittenPathsAt(root)
}

// AllWrittenPathsAt is the testable core of AllWrittenPaths. A missing state
// directory is an empty union.
func AllWrittenPathsAt(stateRoot string) ([]string, error) {
	dir := filepath.Join(stateRoot, "work")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		key := strings.TrimSuffix(name, ".json")
		if !rootSHA.MatchString(key) {
			continue
		}
		s, err := LoadStateAt(stateRoot, key)
		if err != nil {
			return nil, fmt.Errorf("work: reading state for %s: %w", key, err)
		}
		for _, p := range s.Written {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}
