package deps

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/REPPL/ferry/internal/sshguard"
)

// safeRepoManifest validates a REPO-SIDE manifest path BEFORE it is stat'd, read,
// or handed to `brew bundle --file`, and returns the cleaned absolute path when
// it is safe. repoRoot is the REPO ROOT (the deps/ directory's PARENT); abs is the
// candidate manifest path under <repoRoot>/deps/ (e.g. deps/Brewfile.darwin,
// deps/Brewfile.darwin.local, deps/apt.txt).
//
// CRITICAL: the walk starts at repoRoot and Lstats EVERY component from the repo
// root DOWN to the manifest file — INCLUDING the `deps` directory component. So a
// symlinked deps/ directory (e.g. `<repo>/deps -> ~/.ssh`), an intermediate
// symlink, or a symlinked manifest file are ALL caught. Walking from the deps dir
// would never Lstat the deps component itself, letting a symlinked deps/ reach
// os.ReadFile / `brew bundle --file` and leak/clobber ~/.ssh.
//
// ferry only ever writes REGULAR files under deps/, so a SYMLINK on ANY component
// from repoRoot down to the manifest is illegitimate: a repo
// `deps/Brewfile.darwin -> ~/.ssh/config` (or a symlinked deps/) would otherwise
// be os.Stat'd (follows symlinks), os.ReadFile'd, or handed to brew bundle,
// leaking/clobbering ~/.ssh. Any such symlink (or one resolving outside the repo /
// under ~/.ssh) is REFUSED and the path is never stat'd, read, or passed to brew.
//
// An ABSENT manifest is fine — there is no symlink to traverse — so the cleaned
// path is returned and the caller skips/treats it as empty. The check Lstats only
// repo-side components (strictly under repoRoot); when a symlink resolves it reads
// the link TEXT with os.Readlink and resolves it LEXICALLY (never EvalSymlinks,
// which would stat the whole chain and could descend INTO ~/.ssh), then compares
// the target against ~/.ssh by PURE STRING arithmetic, NEVER stating, reading,
// EvalSymlink-ing, or enumerating ~/.ssh itself.
func safeRepoManifest(repoRoot, abs string) (string, error) {
	root := filepath.Clean(repoRoot)
	target := filepath.Clean(abs)

	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("deps: refusing repo path %s: escapes repo root %s", target, root)
	}
	if rel == "." {
		return target, nil
	}

	cur := root
	for _, seg := range strings.Split(rel, string(os.PathSeparator)) {
		cur = filepath.Join(cur, seg)
		fi, lerr := os.Lstat(cur)
		if lerr != nil {
			// Absent component => absent manifest: no symlink to traverse, safe. The
			// caller treats absence as an empty manifest.
			if os.IsNotExist(lerr) {
				return target, nil
			}
			return "", fmt.Errorf("deps: refusing repo path %s: lstat %s: %w", target, cur, lerr)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		// A symlink component: ferry never symlinks managed manifests, so refuse.
		// Read the link TEXT (os.Readlink, NO follow) and resolve it LEXICALLY
		// (relative target anchored at the link's dir, then Clean — never
		// EvalSymlinks, which would stat the chain and could descend INTO ~/.ssh) so
		// we can flag an ~/.ssh escape by string compare only; ~/.ssh itself is never
		// opened.
		if linkTarget, rerr := os.Readlink(cur); rerr == nil {
			if !filepath.IsAbs(linkTarget) {
				linkTarget = filepath.Join(filepath.Dir(cur), linkTarget)
			}
			if under, uerr := sshguard.UnderHomeSSHExact(filepath.Clean(linkTarget)); uerr == nil && under {
				return "", fmt.Errorf("deps: refusing repo path %s: symlink resolves under ~/.ssh", target)
			}
		}
		return "", fmt.Errorf("deps: refusing repo path %s: symlink not allowed", target)
	}
	return target, nil
}
