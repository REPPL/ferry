package cmd

// A fresh config repo must be created on the branch `ferry sync` integrates and
// pushes. Bare `git init` honours the user's `init.defaultBranch` (git's built-in
// default is `master`), and ferry's hardened git env deliberately does not
// neutralise user config — so an unpinned HEAD leaves `ferry init --github`
// succeeding and every later `ferry sync` refusing on the branch name.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitConfigWithDefaultBranch points GIT_CONFIG_GLOBAL at a config declaring
// init.defaultBranch, reproducing a machine whose git creates something other
// than ferry's managed branch.
func gitConfigWithDefaultBranch(t *testing.T, branch string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	body := "[init]\n\tdefaultBranch = " + branch + "\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// headBranch reports the branch HEAD points at, unborn commits included.
func headBranch(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "symbolic-ref", "--short", "HEAD")
	cmd.Env = gitIsolatedEnv()
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("symbolic-ref HEAD in %s: %v", repo, err)
	}
	return strings.TrimSpace(out.String())
}

// MAJOR: a fresh repo lands on ferry's managed branch even when the machine's
// git is configured to create a different one.
func TestCreateFreshRepoPinsManagedBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	gitConfigWithDefaultBranch(t, "master")

	dest := filepath.Join(t.TempDir(), "repo")
	if err := createFreshRepo(io_Discard{}, dest); err != nil {
		t.Fatalf("createFreshRepo: %v", err)
	}

	if got := headBranch(t, dest); got != syncBranchName {
		t.Fatalf("fresh repo HEAD is on %q, want %q — `ferry sync` would refuse to run on it", got, syncBranchName)
	}
}

// The pin must not depend on the user's config happening to be absent.
func TestCreateFreshRepoPinsManagedBranchWithNoUserConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	dest := filepath.Join(t.TempDir(), "repo")
	if err := createFreshRepo(io_Discard{}, dest); err != nil {
		t.Fatalf("createFreshRepo: %v", err)
	}
	if got := headBranch(t, dest); got != syncBranchName {
		t.Fatalf("fresh repo HEAD is on %q, want %q", got, syncBranchName)
	}
}

// io_Discard is a minimal io.Writer sink; createFreshRepo prints progress.
type io_Discard struct{}

func (io_Discard) Write(p []byte) (int, error) { return len(p), nil }
