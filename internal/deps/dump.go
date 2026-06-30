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
		// CRITICAL guard: `brew bundle dump --force` truncates+writes the target file,
		// so it would write THROUGH a symlink. If the target Brewfile (or any parent
		// component up to the deps dir's parent) is a symlink, REFUSE — never create or
		// follow it, never invoke brew. A repo `deps/Brewfile.darwin -> ~/.ssh/config`
		// must NOT let brew overwrite ~/.ssh/config. ferry only ever writes regular
		// files here, so a symlink component is always illegitimate. This Lstats the
		// deps-side path only; it never reads ~/.ssh.
		if err := refuseSymlinkTarget(m.Shared); err != nil {
			return "", err
		}
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

// refuseSymlinkTarget refuses if the manifest target path OR any of its parent
// directory components (from the repo root DOWN) is a symlink. It delegates to
// safeRepoManifest, whose walk starts at the repo root (the deps/ directory's
// PARENT) and Lstats EVERY component from the root down to the target IN ORDER —
// PARENTS BEFORE the target. This is the critical ordering: a single
// os.Lstat(<repo>/deps/Brewfile.<goos>) on the FULL target path would first
// TRAVERSE a symlinked `deps/` parent (e.g. `<repo>/deps -> ~/.ssh`) into ~/.ssh
// before any symlink check ran. Walking root-down Lstats `deps` itself BEFORE the
// Brewfile, so a symlinked deps/ (or any parent, or the target leaf) is caught
// before any traversal through it. A symlink at ANY level → refuse, so even a
// caller that forgot the cmd-side safeRepoPath is protected by the library.
//
// `brew bundle dump --force` truncate+writes the target, so a symlink component
// would write THROUGH it onto whatever it points at (e.g. ~/.ssh/config); any
// symlink → hard refuse. An absent target (or absent parent yet to be created by
// MkdirAll) is fine: there is no symlink to traverse. This Lstats only deps-side
// components (strictly under the repo root) — it never reads, stats, or enumerates
// the symlink's target, so it never touches ~/.ssh.
func refuseSymlinkTarget(target string) error {
	target = filepath.Clean(target)
	// repoRoot is the deps/ directory's PARENT (target is <repoRoot>/deps/<file>).
	repoRoot := filepath.Dir(filepath.Dir(target))
	if _, err := safeRepoManifest(repoRoot, target); err != nil {
		return fmt.Errorf("deps: refusing to dump manifest %s: %w (brew bundle dump would write through it)", target, err)
	}
	return nil
}

// fileExists reports whether path exists (any type). Used to skip an absent
// per-machine overlay without treating it as an error.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
