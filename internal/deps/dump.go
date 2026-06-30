package deps

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/REPPL/ferry/internal/platform"
)

// ReDumpManifest re-dumps THIS machine's manifest for the DETECTED manager,
// overwriting only the current platform's file — never another OS's or another
// manager's. capture (Wave 2) calls it; here we provide the function + the exec
// hook (runner) and capture wires the rest.
//
//   - brew: `brew bundle dump --force --file=deps/Brewfile.<goos>` (the detected
//     manager's file only).
//   - apt: apt has no clean "dump installed set", so a re-dump is reported as
//     unsupported rather than guessing — the shared apt.txt stays hand-curated.
//   - none: ErrNoPackageManager (the caller reports it; never bootstraps a PM).
//
// Returns the absolute path of the file that was (re)written.
func ReDumpManifest(depsDir string, runner CommandRunner) (string, error) {
	m, err := SelectManifest(depsDir)
	if err != nil {
		return "", err
	}
	return reDump(m, runner)
}

// reDump is the testable core: the manifest is pre-selected, so a test asserts
// the dump targets ONLY the detected manager's file (Brewfile.<goos>) and never
// another OS's.
func reDump(m Manifest, runner CommandRunner) (string, error) {
	if runner == nil {
		return "", fmt.Errorf("deps: nil CommandRunner")
	}
	switch m.Manager {
	case platform.ManagerBrew:
		// Ensure the deps/ directory exists so brew bundle dump can write into it.
		if err := os.MkdirAll(filepath.Dir(m.Shared), 0o755); err != nil {
			return "", fmt.Errorf("deps: create deps dir for %s: %w", m.Shared, err)
		}
		if _, err := runner.Run(brewBin, "bundle", "dump", "--force", "--file="+m.Shared); err != nil {
			return "", fmt.Errorf("deps: brew bundle dump --file=%s: %w", m.Shared, err)
		}
		return m.Shared, nil
	case platform.ManagerApt:
		return "", fmt.Errorf("deps: apt has no clean installed-set dump; %s stays hand-curated (capture is brew-only)", m.Shared)
	default:
		return "", ErrNoPackageManager
	}
}

// fileExists reports whether path exists (any type). Used to skip an absent
// per-machine overlay without treating it as an error.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
