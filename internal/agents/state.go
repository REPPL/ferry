package agents

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/REPPL/ferry/internal/paths"
)

// targetsFileName is the persisted record of every $HOME destination the
// agents domain has applied on this machine (absolute path -> store key),
// kept under ferry's state dir. It exists for RESTORE: `ferry restore agents`
// must revert what was ACTUALLY applied — including targets the manifest has
// since dropped (a removed harness, a changed devtree) — and must work with
// the config repo deleted or unreadable. Resolving from the live manifest
// would miss exactly those, so the record is CUMULATIVE and keyed by PATH:
// apply unions the current plan's destinations in and never removes an entry
// — a key whose destination moved keeps BOTH paths restorable (the engine
// skips recorded paths that have no baseline).
const targetsFileName = "agents-targets.json"

// statePerm mirrors ferry's owner-only state-store posture.
const (
	stateDirPerm  os.FileMode = 0o700
	stateFilePerm os.FileMode = 0o600
)

// RecordTargets unions targets (store key -> absolute $HOME destination) into
// the persisted record under stateDir, creating it if absent. The record is
// keyed by PATH, so existing entries are always kept — a key whose
// destination moved (a renamed devtree) contributes a NEW entry while the old
// destination stays restorable. The write is atomic (temp + rename) and the
// state dir is symlink-hardened first, like every other ferry state write.
func RecordTargets(stateDir string, targets map[string]string) error {
	if len(targets) == 0 {
		return nil
	}
	if err := paths.HardenStoreDir(stateDir); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, stateDirPerm); err != nil {
		return err
	}

	record, err := readTargetRecord(stateDir)
	if err != nil {
		return err
	}
	for key, path := range targets {
		record[path] = key
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, targetsFileName)
	tmp, err := os.CreateTemp(stateDir, ".agents-targets-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed away
	if err := tmp.Chmod(stateFilePerm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// RecordedTargetPaths returns the absolute $HOME destinations the agents
// domain has ever applied on this machine, sorted, from the persisted record.
// An absent record (the domain was never applied, or a pre-record machine)
// yields an empty slice, not an error. It is a pure read: no state dir is
// created, so the read-only restore path stays write-free — and it needs NO
// config repo, which is what keeps `ferry restore agents` working when the
// repo is deleted or its manifest unreadable.
func RecordedTargetPaths(stateDir string) ([]string, error) {
	if err := paths.HardenStoreDir(stateDir); err != nil {
		return nil, err
	}
	record, err := readTargetRecord(stateDir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(record))
	for p := range record {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// readTargetRecord loads the record map; an absent file is an empty map.
func readTargetRecord(stateDir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, targetsFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	record := map[string]string{}
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	return record, nil
}
