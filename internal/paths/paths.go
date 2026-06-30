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
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// appName is the ferry-owned subdirectory under each base.
const appName = "ferry"

// HardenStoreDir verifies that a ferry-owned store directory is a REAL directory
// reachable from $HOME WITHOUT traversing a symlink, refusing if any component
// from $HOME down to dir is a symlink. Ferry always creates its store dirs
// (~/.config/ferry/secrets-local, ~/.local/state/ferry and their subdirs) as
// real directories, so a symlink component there is illegitimate — an attacker
// could symlink ~/.config/ferry/secrets-local INTO the repo worktree (so a
// blocked secret lands in the repo) or ~/.local/state/ferry INTO ~/.ssh or a
// system path (so apply/restore write there). Call this BEFORE creating or
// writing to a store dir.
//
// The check is symlink-aware but PURELY LEXICAL on any symlink target: it Lstats
// only components strictly under $HOME (never under ~/.ssh), and when a component
// is a symlink it reads the link TEXT with os.Readlink (NO follow), resolves it
// LEXICALLY, and refuses — it NEVER EvalSymlinks an untrusted component and NEVER
// stats, reads, or enumerates ~/.ssh, the repo, or a system path. A symlinked
// component is refused outright (ferry never legitimately symlinks a store dir),
// so the resolved target is reported only for a clearer message.
//
// dir MUST be the store dir's canonical path under $HOME (as produced by
// SecretsDir/StateDir). A dir that is NOT under $HOME — e.g. a test's t.TempDir()
// — has no $HOME-anchored chain to validate and is accepted unchanged, so the
// real-path constructors are hardened while the explicit-root test constructors
// keep working.
func HardenStoreDir(dir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	home = filepath.Clean(home)
	abs := filepath.Clean(dir)

	rel, err := filepath.Rel(home, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		// Not under $HOME (e.g. a test temp dir): no $HOME-anchored chain to harden.
		return nil
	}
	if rel == "." {
		return nil
	}

	// Walk each component from $HOME down to dir. Lstat does NOT follow symlinks,
	// so a symlink component is detected before it could be traversed. ferry never
	// symlinks a store dir, so ANY symlink component is refused.
	cur := home
	for _, seg := range strings.Split(rel, string(os.PathSeparator)) {
		cur = filepath.Join(cur, seg)
		fi, lerr := os.Lstat(cur)
		if lerr != nil {
			// An absent component is fine — there is no symlink to traverse, and the
			// caller will create the remaining real dirs. Stop walking.
			if os.IsNotExist(lerr) {
				return nil
			}
			return fmt.Errorf("refusing store dir %s: lstat %s: %w", abs, cur, lerr)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		// A symlink component: read its target TEXT with os.Readlink (NO follow) and
		// resolve it LEXICALLY (relative target anchored at the link's dir, then
		// Clean) — never EvalSymlinks, which would stat the whole chain and could
		// descend INTO ~/.ssh. Any symlink store component is illegitimate; refuse.
		target, rerr := os.Readlink(cur)
		if rerr != nil {
			return fmt.Errorf("refusing store dir %s: symlink component %s does not resolve: %w", abs, cur, rerr)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(cur), target)
		}
		return fmt.Errorf("refusing store dir %s: component %s is a symlink (resolves to %s); ferry store dirs must be real directories", abs, cur, filepath.Clean(target))
	}
	return nil
}

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
