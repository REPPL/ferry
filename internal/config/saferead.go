package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/REPPL/ferry/internal/sshguard"
)

// safeRepoRead validates a REPO-SIDE path BEFORE it is opened for reading and
// returns its cleaned absolute path when it is safe. repoRoot is an ALREADY
// ABSOLUTE repo clone path (the caller passes an absolute repo path); relPath is
// the file to read under it (e.g. "ferry.toml"). The returned path is what the
// caller hands to os.ReadFile.
//
// ferry only ever writes REGULAR files into the repo, so a SYMLINK anywhere on
// the path from repoRoot down to the target is illegitimate and a security risk:
// a repo `ferry.toml -> ~/.ssh/config` would otherwise be os.Stat'd (which
// follows symlinks) and os.ReadFile'd, leaking ~/.ssh. The rule is therefore
// strict — the target, and every component under repoRoot leading to it, must
// not be a symlink. A symlink (or a symlink that resolves outside the repo or
// under ~/.ssh) is REFUSED and the path is never opened.
//
// An ABSENT target is fine: there is no symlink to traverse, so the cleaned path
// is returned and the caller handles absence (an absent ferry.local.toml is
// normal). The check Lstats only repo-side components (all strictly under
// repoRoot); when a repo symlink resolves to a target it reads the link TEXT with
// os.Readlink and resolves it LEXICALLY (never EvalSymlinks, which would stat the
// whole chain and could descend INTO ~/.ssh), then compares that target against
// ~/.ssh by PURE STRING arithmetic — it NEVER stats, lstats, reads, EvalSymlinks,
// or enumerates ~/.ssh itself.
func safeRepoRead(repoRoot, relPath string) (string, error) {
	root := filepath.Clean(repoRoot)
	abs := filepath.Clean(filepath.Join(root, relPath))

	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing repo path %s: escapes repo root %s", abs, root)
	}
	if rel == "." {
		return abs, nil
	}

	// Walk each component from repoRoot down to the target. Lstat does NOT follow
	// symlinks, so a symlink component is detected before it could be traversed.
	cur := root
	for _, seg := range strings.Split(rel, string(os.PathSeparator)) {
		cur = filepath.Join(cur, seg)
		fi, lerr := os.Lstat(cur)
		if lerr != nil {
			// An absent component means an absent (or partially-absent) target: there
			// is no symlink to traverse, so the target is safe. The caller treats
			// absence as "nothing declared".
			if os.IsNotExist(lerr) {
				return abs, nil
			}
			return "", fmt.Errorf("refusing repo path %s: lstat %s: %w", abs, cur, lerr)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		// A symlink component: read its target TEXT (os.Readlink, NO follow) and
		// refuse — ferry never symlinks managed content, so any repo-side symlink is
		// refused. Resolving the target LEXICALLY (relative target anchored at the
		// link's dir, then Clean — never EvalSymlinks, which would stat the chain and
		// could descend INTO ~/.ssh) lets us report an escape under ~/.ssh by pure
		// string compare; ~/.ssh itself is never opened.
		if target, rerr := os.Readlink(cur); rerr == nil {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(cur), target)
			}
			if under, uerr := sshguard.UnderHomeSSHExact(filepath.Clean(target)); uerr == nil && under {
				return "", fmt.Errorf("refusing repo path %s: symlink resolves under ~/.ssh", abs)
			}
		}
		return "", fmt.Errorf("refusing repo path %s: symlink not allowed", abs)
	}
	return abs, nil
}
