package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/REPPL/ferry/internal/paths"
)

// MachineConfig is the content of ~/.config/ferry/config.toml: this machine's
// identity and the path to its repo clone. It answers "where am I, which clone
// is mine" — it never declares scope (that is ferry.toml/ferry.local.toml).
//
// The TOML keys (`hostname`, `repo`) are the persisted contract; downstream
// tooling and the eval harness key off these spellings.
type MachineConfig struct {
	// Hostname identifies this machine (typically os.Hostname()).
	Hostname string `toml:"hostname"`
	// Repo is the absolute path to this machine's clone of the config repo.
	Repo string `toml:"repo"`
	// Managed reports whether ferry created and owns this repo's GitHub remote
	// (route 2, `ferry init --github`). A route-1 repo leaves it false. It is
	// `omitempty` so an unmanaged config writes no `managed` key, and an OLD
	// config (hostname+repo only, no `managed` key) still loads with managed=false.
	Managed bool `toml:"managed,omitempty"`
	// Work is the optional [work] table: this machine's cargo-store location
	// and retention for the work verbs. It is machine-specific (a shared or
	// removable path that differs per machine), so it lives here and never in
	// the repo-side manifests. A nil Work means the table is absent and the
	// work verbs refuse with setup guidance.
	Work *WorkConfig `toml:"work,omitempty"`
}

// WorkConfig is the [work] table's content.
type WorkConfig struct {
	// Store is the cargo-store directory (shared or portable media).
	Store string `toml:"store,omitempty"`
	// Keep is the keep-last-N retention pack applies per project (0 means the
	// default).
	Keep int `toml:"keep,omitempty"`
	// AllowSyncRoot accepts a store under a known cloud-sync directory —
	// an explicit, persisted opt-in to the loud warning.
	AllowSyncRoot bool `toml:"allow_sync_root,omitempty"`
}

// LoadMachineConfig reads and parses ~/.config/ferry/config.toml (resolved via
// internal/paths). A missing file is reported as os.ErrNotExist (callers can
// use errors.Is to detect first-run); a malformed file yields a clear error.
func LoadMachineConfig() (MachineConfig, error) {
	path, err := paths.ConfigFile()
	if err != nil {
		return MachineConfig{}, err
	}
	return loadMachineConfigFrom(path)
}

// LoadMachineConfigFrom reads and parses a machine config.toml from an explicit
// path, symlink-hardening its directory first. It is exported so a tolerant
// fallback reader (e.g. cmd/context.go) can re-read ~/.config/ferry/config.toml
// through the SAME hardening as LoadMachineConfig instead of calling
// toml.DecodeFile directly and bypassing the symlink check.
func LoadMachineConfigFrom(path string) (MachineConfig, error) {
	return loadMachineConfigFrom(path)
}

// loadMachineConfigFrom is the testable core: it reads config.toml from an
// explicit path so tests can point at a fake home without touching real ~.
func loadMachineConfigFrom(path string) (MachineConfig, error) {
	// Symlink-harden ~/.config/ferry BEFORE reading config.toml: if the config dir
	// is a symlink into ~/.ssh or a system/admin location, refuse rather than read
	// through it. HardenStoreDir is lexical, creates nothing, never touches ~/.ssh,
	// and is a no-op for a test path whose dir is not under $HOME (t.TempDir()).
	if err := paths.HardenStoreDir(filepath.Dir(path)); err != nil {
		return MachineConfig{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// Propagate os.ErrNotExist verbatim so errors.Is works for first-run.
		if errors.Is(err, os.ErrNotExist) {
			return MachineConfig{}, err
		}
		return MachineConfig{}, fmt.Errorf("read machine config %s: %w", path, err)
	}
	var mc MachineConfig
	if err := toml.Unmarshal(data, &mc); err != nil {
		return MachineConfig{}, fmt.Errorf("parse machine config %s: %w", path, err)
	}
	// A hand-written or corrupted config.toml could parse cleanly yet omit a
	// required field; validate here so an empty repo path never reaches the code
	// that resolves manifests against it.
	if err := mc.validate(); err != nil {
		return MachineConfig{}, fmt.Errorf("machine config %s: %w", path, err)
	}
	return mc, nil
}

// validate enforces that both required fields are present. Load and Save share
// it so the on-disk contract is the same in both directions.
func (mc MachineConfig) validate() error {
	if mc.Hostname == "" {
		return errors.New("hostname (machine identity) is required")
	}
	if mc.Repo == "" {
		return errors.New("repo (clone path) is required")
	}
	return nil
}

// SaveMachineConfig writes the machine config to ~/.config/ferry/config.toml
// (resolved via internal/paths), creating the config dir if needed. Both
// identity and repo path must be present — they are the file's whole purpose.
func SaveMachineConfig(mc MachineConfig) error {
	path, err := paths.ConfigFile()
	if err != nil {
		return err
	}
	return saveMachineConfigTo(path, mc)
}

// saveMachineConfigTo is the testable core: it writes to an explicit path.
func saveMachineConfigTo(path string, mc MachineConfig) error {
	if err := mc.validate(); err != nil {
		return fmt.Errorf("machine config: %w", err)
	}
	// Symlink-harden ~/.config/ferry BEFORE creating/truncating config.toml so a
	// config dir symlinked into ~/.ssh or a system location is refused, never
	// written through. Lexical, no-op for a test temp path. See loadMachineConfigFrom.
	if err := paths.HardenStoreDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open machine config %s: %w", path, err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(mc); err != nil {
		return fmt.Errorf("encode machine config %s: %w", path, err)
	}
	return nil
}
