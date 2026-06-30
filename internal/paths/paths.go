// Package paths resolves ferry-owned config and state locations.
//
// Ferry standardises on XDG-style locations (~/.config/ferry and
// ~/.local/state/ferry) on BOTH macOS and Linux. All paths are always built
// from os.UserHomeDir() plus explicit joins; there is NO XDG_CONFIG_HOME /
// XDG_STATE_HOME override. This guarantees ferry-owned config and state live in
// one consistent location across OSes, exactly where the docs name them
// (~/.config/ferry). We deliberately do NOT use os.UserConfigDir() either,
// because it resolves to ~/Library/Application Support on macOS.
package paths

import (
	"os"
	"path/filepath"
)

// appName is the ferry-owned subdirectory under each base.
const appName = "ferry"

// ConfigDir returns ferry's config directory (~/.config/ferry).
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", appName), nil
}

// StateDir returns ferry's state directory (~/.local/state/ferry).
func StateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", appName), nil
}

// ConfigFile returns the path to ferry's per-machine config file
// (~/.config/ferry/config.toml).
func ConfigFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// SecretsDir returns the out-of-repo secret store
// (~/.config/ferry/secrets-local). Created mode 0700 by callers; never under
// the repo.
func SecretsDir() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "secrets-local"), nil
}

// LockPath returns the apply transaction lockfile
// (~/.local/state/ferry/lock). It lives outside the repo so moving or
// re-cloning the repo never affects locking.
func LockPath() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lock"), nil
}

// BaselineDir returns the immutable pre-ferry baseline directory
// (~/.local/state/ferry/baseline).
func BaselineDir() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "baseline"), nil
}

// JournalDir returns the append-only per-run journal directory
// (~/.local/state/ferry/journal).
func JournalDir() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "journal"), nil
}
