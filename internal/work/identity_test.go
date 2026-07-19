package work

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var hexRoot = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// gitTest runs git in dir with the same isolation the implementation uses, so
// fixtures can never touch the host repository (an inherited GIT_DIR wins over
// -C — see issue #17).
func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(isolatedGitEnv(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@localhost",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@localhost",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo creates a temp git repo with one commit and returns its path.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitTest(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-q", "-m", "root")
	return dir
}

func TestProjectIdentity_SingleRoot(t *testing.T) {
	dir := newRepo(t)
	want := gitTest(t, dir, "rev-list", "--max-parents=0", "HEAD")

	id, err := ProjectIdentity(dir)
	if err != nil {
		t.Fatalf("ProjectIdentity: %v", err)
	}
	if id.Key != want {
		t.Errorf("Key = %q, want %q", id.Key, want)
	}
	if !hexRoot.MatchString(id.Key) {
		t.Errorf("Key %q is not 40/64-char hex", id.Key)
	}
	if len(id.Roots) != 1 || id.Roots[0] != want {
		t.Errorf("Roots = %v, want [%s]", id.Roots, want)
	}
}

func TestProjectIdentity_MultiRoot(t *testing.T) {
	dir := newRepo(t)
	// Second root: an orphan branch merged with --allow-unrelated-histories.
	first := gitTest(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	gitTest(t, dir, "checkout", "-q", "--orphan", "second")
	gitTest(t, dir, "rm", "-rf", "--cached", ".")
	if err := os.WriteFile(filepath.Join(dir, "g.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "g.txt")
	gitTest(t, dir, "commit", "-q", "-m", "second root")
	// f.txt is untracked on the orphan branch and would block the checkout.
	if err := os.Remove(filepath.Join(dir, "f.txt")); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "checkout", "-q", first)
	gitTest(t, dir, "merge", "-q", "--allow-unrelated-histories", "-m", "join", "second")

	rawFirst := strings.SplitN(gitTest(t, dir, "rev-list", "--max-parents=0", "HEAD"), "\n", 2)[0]

	id, err := ProjectIdentity(dir)
	if err != nil {
		t.Fatalf("ProjectIdentity: %v", err)
	}
	if id.Key != rawFirst {
		t.Errorf("Key = %q, want first rev-list line %q", id.Key, rawFirst)
	}
	if len(id.Roots) != 2 {
		t.Fatalf("Roots = %v, want 2 entries", id.Roots)
	}
	if !sort.StringsAreSorted(id.Roots) {
		t.Errorf("Roots %v not sorted", id.Roots)
	}
}

func TestProjectIdentity_NotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	_, err := ProjectIdentity(t.TempDir())
	var nre *NotARepoError
	if !errors.As(err, &nre) {
		t.Fatalf("err = %v, want *NotARepoError", err)
	}
}

func TestProjectIdentity_InheritedGitDirCannotAnswer(t *testing.T) {
	other := newRepo(t)
	// An inherited GIT_DIR pointing at another repo must NOT let a plain
	// directory resolve to that repo's identity (the issue-#17 leak shape).
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	_, err := ProjectIdentity(t.TempDir())
	var nre *NotARepoError
	if !errors.As(err, &nre) {
		t.Fatalf("err = %v, want *NotARepoError despite inherited GIT_DIR", err)
	}
}

func TestProjectIdentity_ShallowCloneRefused(t *testing.T) {
	src := newRepo(t)
	// A second commit so --depth 1 actually truncates history.
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, src, "commit", "-aqm", "second")

	shallow := filepath.Join(t.TempDir(), "shallow")
	gitTest(t, filepath.Dir(shallow), "clone", "-q", "--depth", "1", "file://"+src, shallow)

	_, err := ProjectIdentity(shallow)
	var sce *ShallowCloneError
	if !errors.As(err, &sce) {
		t.Fatalf("err = %v, want *ShallowCloneError", err)
	}
}

func TestProjectIdentity_LinkedWorktreeRefused(t *testing.T) {
	dir := newRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gitTest(t, dir, "worktree", "add", "-q", wt)

	_, err := ProjectIdentity(wt)
	var lwe *LinkedWorktreeError
	if !errors.As(err, &lwe) {
		t.Fatalf("err = %v, want *LinkedWorktreeError", err)
	}

	// The main worktree of the same repo stays accepted.
	if _, err := ProjectIdentity(dir); err != nil {
		t.Fatalf("main worktree refused: %v", err)
	}
}

func TestProjectIdentity_NoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitTest(t, dir, "init", "-q")
	if _, err := ProjectIdentity(dir); err == nil {
		t.Fatal("ProjectIdentity on a commit-less repo succeeded, want error")
	}
}
