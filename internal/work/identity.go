package work

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Identity is how a project repo is recognised in the cargo store: the way the
// abcd tooling identifies it, so the same value locates the account-level
// transcript store ~/.abcd/history/<root-sha>/.
type Identity struct {
	// Key is the abcd-compatible store key: the FIRST line of
	// `git rev-list --max-parents=0 HEAD`.
	Key string
	// Roots is the full sorted root-SHA set. A subtree import can add roots
	// and reorder rev-list output, so receive matches on set intersection
	// rather than on Key equality alone.
	Roots []string
}

// NotARepoError reports that dir is not inside a git working tree.
type NotARepoError struct {
	Dir    string
	Detail string
}

func (e *NotARepoError) Error() string {
	msg := "work: " + e.Dir + " is not a git repository"
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// ShallowCloneError reports a shallow clone. A shallow clone reports the graft
// boundary as its root — a bogus identity nothing else will ever match — so
// pack and receive refuse it.
type ShallowCloneError struct{ Dir string }

func (e *ShallowCloneError) Error() string {
	return "work: " + e.Dir + " is a shallow clone (its root commit is a graft boundary, not the project's identity) — run `git fetch --unshallow` first"
}

// LinkedWorktreeError reports a linked worktree. Worktrees share a root SHA
// but .abcd/.work.local/ is per-worktree, so their batons would collide in the
// store; v1 refuses both pack and receive there.
type LinkedWorktreeError struct{ Dir string }

func (e *LinkedWorktreeError) Error() string {
	return "work: " + e.Dir + " is a linked git worktree — work verbs run only in the main worktree"
}

// rootSHA matches a full SHA-1 or SHA-256 object name.
var rootSHA = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// ProjectIdentity computes the identity of the project repo at dir, guarding
// against the identities that would poison the store: not a repo, a shallow
// clone, a linked worktree, a commit-less repo.
//
// Every git invocation runs with an isolated environment so an inherited
// GIT_DIR cannot answer for another repo (the issue-#17 leak shape).
func ProjectIdentity(dir string) (Identity, error) {
	inside, err := gitOutput(dir, "rev-parse", "--is-inside-work-tree")
	if err != nil || inside != "true" {
		return Identity{}, &NotARepoError{Dir: dir, Detail: firstLine(err)}
	}

	shallow, err := gitOutput(dir, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return Identity{}, fmt.Errorf("work: probe shallowness of %s: %w", dir, err)
	}
	if shallow == "true" {
		return Identity{}, &ShallowCloneError{Dir: dir}
	}

	linked, err := isLinkedWorktree(dir)
	if err != nil {
		return Identity{}, err
	}
	if linked {
		return Identity{}, &LinkedWorktreeError{Dir: dir}
	}

	out, err := gitOutput(dir, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return Identity{}, fmt.Errorf("work: %s has no commits to identify it by: %w", dir, err)
	}
	lines := strings.Split(out, "\n")
	roots := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if !rootSHA.MatchString(l) {
			return Identity{}, fmt.Errorf("work: unexpected root-commit line %q from git rev-list in %s", l, dir)
		}
		roots = append(roots, l)
	}
	if len(roots) == 0 {
		return Identity{}, fmt.Errorf("work: %s has no commits to identify it by", dir)
	}
	key := roots[0]
	sort.Strings(roots)
	return Identity{Key: key, Roots: roots}, nil
}

// isLinkedWorktree reports whether dir is a linked worktree (its git dir and
// the common git dir differ once both are resolved against dir).
func isLinkedWorktree(dir string) (bool, error) {
	out, err := gitOutput(dir, "rev-parse", "--absolute-git-dir", "--git-common-dir")
	if err != nil {
		return false, fmt.Errorf("work: probe worktree layout of %s: %w", dir, err)
	}
	lines := strings.SplitN(out, "\n", 2)
	if len(lines) != 2 {
		return false, fmt.Errorf("work: unexpected rev-parse output %q in %s", out, dir)
	}
	gitDir := filepath.Clean(strings.TrimSpace(lines[0]))
	common := strings.TrimSpace(lines[1])
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	common = filepath.Clean(common)
	// Resolve symlinks on both sides before comparing: on macOS a repo under
	// /var/folders reports its absolute git dir under /private/var, while the
	// path joined from dir keeps the /var spelling — same directory, two
	// spellings, and a lexical compare would misread the main worktree as
	// linked.
	if r, err := filepath.EvalSymlinks(gitDir); err == nil {
		gitDir = r
	}
	if r, err := filepath.EvalSymlinks(common); err == nil {
		common = r
	}
	return gitDir != common, nil
}

// gitOutput runs git -C dir with the isolated environment and returns trimmed
// stdout. On failure the error carries git's stderr.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = isolatedGitEnv()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// isolatedGitEnv is the environment for every work-package git invocation: all
// inherited GIT_* variables stripped (an explicit GIT_DIR WINS over -C
// directory discovery — the corruption incident behind issue #17), config
// discovery neutralised, prompts off. Read-only invocations need no identity.
func isolatedGitEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	)
}

// firstLine reduces an error to its first line for embedding in a refusal.
func firstLine(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
