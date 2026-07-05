package agents

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/REPPL/ferry/internal/paths"
	"github.com/REPPL/ferry/internal/statefile"
)

// targetsVersion is the current on-disk schema version of the agents target
// record. Version 1 is the first versioned form: a JSON envelope carrying this
// integer plus the path->key targets map. A file with no "version" field is the
// original pre-versioning form (a bare path->key map) and reads as version 1,
// migrated forward on the next mutating write. See internal/statefile for the
// shared version contract.
const targetsVersion = 1

// targetsFile is the versioned on-disk envelope for the agents target record.
type targetsFile struct {
	Version int               `json:"version"`
	Targets map[string]string `json:"targets"`
}

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

	path := filepath.Join(stateDir, targetsFileName)
	record, version, migrate, err := loadTargetRecord(stateDir)
	if err != nil {
		// A future-version record is refused here, before any write, leaving the
		// newer ferry's file untouched.
		return err
	}
	if migrate {
		// Migrate-on-read (mutating path): preserve the pre-migration file before
		// this union rewrites it in the current versioned envelope form. The
		// backup is named after the resolved source version being migrated away
		// from (matching the dotfile store).
		if _, err := statefile.BackupForMigration(path, version); err != nil {
			return err
		}
	}
	for key, dest := range targets {
		record[dest] = key
	}

	data, err := json.MarshalIndent(targetsFile{Version: targetsVersion, Targets: record}, "", "  ")
	if err != nil {
		return err
	}
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
	// A read enumerates the record without migrating it: this is the write-free
	// restore path. A future-version record is still refused.
	record, _, _, err := loadTargetRecord(stateDir)
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

// loadTargetRecord loads the path->key record, resolving its on-disk schema
// version. It returns the record, the resolved on-disk version, whether the file
// must be migrated forward, and any refusal. An absent file is an empty,
// current-version record. migrate is true when the file is the original
// pre-versioning bare-map form, so a mutating caller must back it up before
// rewriting it as an envelope. A file a newer ferry wrote is refused with a
// *statefile.FutureVersionError and the file is left untouched.
func loadTargetRecord(stateDir string) (record map[string]string, version int, migrate bool, err error) {
	path := filepath.Join(stateDir, targetsFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, targetsVersion, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	version, migrate, err = statefile.Resolve(path, data, targetsVersion)
	if err != nil {
		return nil, 0, false, err
	}
	if migrate {
		// The original pre-versioning form is a bare path->key map.
		m := map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, 0, false, err
		}
		return m, version, true, nil
	}
	var env targetsFile
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, 0, false, err
	}
	if env.Targets == nil {
		env.Targets = map[string]string{}
	}
	return env.Targets, version, false, nil
}
