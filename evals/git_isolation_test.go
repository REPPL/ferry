package evals

// Regression guard for the git-environment-bleed repo-corruption incident.
//
// The eval suite is routinely run from inside a linked git worktree (agents work
// in worktrees). In that context the process inherits GIT_DIR / GIT_WORK_TREE /
// GIT_CONFIG* pointing at the HOST repository. If an eval-side `git` helper builds
// its environment with `append(os.Environ(), …)` (or sets no env at all), that
// inherited GIT_DIR WINS over the `-C <dir>` the helper passes, so:
//   - a WRITE helper's `git init --bare` (gitAddBareRemote) reinitialises the HOST
//     repo as bare (writing core.bare=true into its .git/config), and `git commit`
//     (gitCommitAll / InitGitRepo) layers stray commits onto the checked-out branch;
//   - a READ helper's `git status` (gitStatusIgnored) refreshes the HOST index's
//     stat cache — a silent write that also makes the helper's assertions read the
//     HOST repo, so they can pass regardless (a false green).
//
// The two subtests below poison the ambient git environment to point at a throwaway
// "host" repo, drive the real eval helpers against a sandbox, and prove the host
// repo's HEAD, reflog, core.bare AND index are all untouched. No FERRY_BIN needed —
// this exercises the harness git helpers directly.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvalGitHelpersCannotTouchHostRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	// newHost builds a throwaway "host" repo with one commit and returns its path
	// plus a reader that always uses the isolated env (so the ambient poison set by a
	// subtest cannot redirect these host reads; `-C host` alone selects the host).
	newHost := func(t *testing.T) (host string, hostGit func(args ...string) string) {
		host = t.TempDir()
		hostGit = func(args ...string) string {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", host}, args...)...)
			cmd.Env = gitIsolatedEnv()
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("host git %v: %v\n%s", args, err, out)
			}
			return strings.TrimSpace(string(out))
		}
		hostGit("init", "-q")
		hostGit("config", "user.email", "host@localhost")
		hostGit("config", "user.name", "host")
		if err := os.WriteFile(filepath.Join(host, "keep.txt"), []byte("host content\n"), 0o644); err != nil {
			t.Fatalf("seed host file: %v", err)
		}
		hostGit("add", "-A")
		hostGit("commit", "-q", "-m", "host baseline")
		return host, hostGit
	}

	// newSandboxRepo builds a clean committed sandbox git repo via the real seeding
	// helpers. Called BEFORE poisoning so the helpers (once isolated) have a genuine
	// repo to target.
	newSandboxRepo := func(t *testing.T) *Sandbox {
		s := NewSandbox(t)
		s.InitGitRepo(t)
		s.WriteRepoFile(t, "ferry.toml", "# sandbox\n")
		gitCommitAll(t, s.Repo, "seed export repo")
		return s
	}

	// t.Setenv forbids t.Parallel; every git helper now strips GIT_*, so poisoning the
	// process env here cannot leak into concurrently-running (already-isolated) tests.

	t.Run("write-helpers-cannot-commit-or-rebare-host", func(t *testing.T) {
		host, hostGit := newHost(t)
		indexPath := filepath.Join(host, ".git", "index")
		headBefore := hostGit("rev-parse", "HEAD")
		reflogBefore := hostGit("reflog", "--format=%H %gs")
		bareBefore := hostGit("config", "--get", "core.bare")
		indexBefore := hashFile(t, indexPath)

		// GIT_DIR only: the default work tree is the process cwd, so a buggy helper's
		// `git commit` stages the SANDBOX files into the HOST index and moves HOST HEAD.
		t.Setenv("GIT_DIR", filepath.Join(host, ".git"))

		s := NewSandbox(t)
		s.InitGitRepo(t)                                // git init + config
		s.WriteRepoFile(t, "ferry.toml", "# sandbox\n") // a change to stage
		gitCommitAll(t, s.Repo, "seed export repo")     // add -A + commit

		// Assert the COMMIT-leak vector immediately (Errorf, not Fatalf, so the later
		// bare-remote step still runs). These are the "stray commits" from the incident.
		if got := hostGit("rev-parse", "HEAD"); got != headBefore {
			t.Errorf("HOST REPO CORRUPTED: HEAD moved %s -> %s (an eval git helper committed to the host repo)", headBefore, got)
		}
		if got := hostGit("reflog", "--format=%H %gs"); got != reflogBefore {
			t.Errorf("HOST REPO CORRUPTED: reflog changed (an eval git helper wrote to the host repo)\nbefore:\n%s\nafter:\n%s", reflogBefore, got)
		}

		// The bare-remote vector: `git init --bare` must land in a temp dir, never flip
		// the host repo's core.bare (the config write that broke every work-tree op).
		_ = gitAddBareRemote(t, s.Repo) // git init --bare + remote add + push

		if got := hostGit("config", "--get", "core.bare"); got != bareBefore {
			t.Errorf("HOST REPO CORRUPTED: core.bare %q -> %q (an eval `git init --bare` reinitialised the host repo as bare)", bareBefore, got)
		}
		if got := hostGit("rev-parse", "--is-bare-repository"); got != "false" {
			t.Errorf("HOST REPO CORRUPTED: host is now a bare repository (%s)", got)
		}
		if got := hashFile(t, indexPath); got != indexBefore {
			t.Errorf("HOST REPO CORRUPTED: .git/index changed (an eval git helper wrote the host index)")
		}

		// Positive control: the helpers must actually have built the SANDBOX repo.
		if sandboxHead := gitRevParse(t, s.Repo, "HEAD"); sandboxHead == headBefore {
			t.Errorf("sandbox HEAD equals host HEAD (%s): the sandbox commit likely landed on the host repo", headBefore)
		}
	})

	t.Run("read-helpers-cannot-write-host-index", func(t *testing.T) {
		host, hostGit := newHost(t)
		indexPath := filepath.Join(host, ".git", "index")

		// A real sandbox repo the (isolated) read helpers can correctly target.
		s := newSandboxRepo(t)

		// Settle, then racily-dirty the host's committed file (same bytes, new mtime) so
		// a `git status` that resolves to the host WILL rewrite its stat-cache index —
		// a deterministic reproduction of the silent index write.
		hostGit("status", "--porcelain")
		time.Sleep(1100 * time.Millisecond)
		if err := os.WriteFile(filepath.Join(host, "keep.txt"), []byte("host content\n"), 0o644); err != nil {
			t.Fatalf("re-touch host file: %v", err)
		}
		headBefore := hostGit("rev-parse", "HEAD")
		indexBefore := hashFile(t, indexPath)

		// Inherit BOTH GIT_DIR and GIT_WORK_TREE (the linked-worktree shape) so a buggy
		// read helper's work tree matches the host index and its status refresh writes it.
		t.Setenv("GIT_DIR", filepath.Join(host, ".git"))
		t.Setenv("GIT_WORK_TREE", host)

		// Drive the read-only helpers against the SANDBOX repo. A buggy (env-inheriting)
		// helper would instead resolve to the host and refresh its index.
		_ = gitStatusIgnored(t, s.Repo)                   // git status --porcelain --ignored
		_ = originURLIn(s.Repo)                           // git remote get-url origin
		_, _ = runGitIn(s.Repo, "rev-parse", "--git-dir") // git rev-parse --git-dir

		if got := hashFile(t, indexPath); got != indexBefore {
			t.Errorf("HOST REPO CORRUPTED: .git/index rewritten by an eval READ helper (a `git status` refreshed the host index — a silent write, and its assertions read the host repo)")
		}
		if got := hostGit("rev-parse", "HEAD"); got != headBefore {
			t.Errorf("HOST REPO CORRUPTED: HEAD moved %s -> %s via a read helper", headBefore, got)
		}
	})
}
