package cmd

// Data-loss guard (CRITICAL-1): if a rollback's `stash apply` FAILS, the tracked work
// must be neither applied NOR dropped — it stays PRESERVED in the snapshot stash, and
// the surfaced error must name the stash sha + a recovery command. These are internal
// tests that drive the real snapshot.restore / cleanup / rollback against a real git
// repo, forcing the stash-apply step to fail so the preservation path is exercised.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = gitIsolatedEnv("GIT_PAGER=cat")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newStashRepo returns a git repo with a REAL held ferry-sync-snapshot stash and a
// snapshot whose headSHA is valid but stashSHA is BOGUS, so restore's stash-apply step
// fails while the reset succeeds — modelling a rollback that could not re-apply.
func newStashRepo(t *testing.T) (repo string, s *snapshot, realStashSHA string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo = t.TempDir()
	testGit(t, repo, "init", "-q", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", "-A")
	testGit(t, repo, "commit", "-qm", "base")
	head := testGit(t, repo, "rev-parse", "HEAD")

	// Dirty the tree, then stash it as ferry does (message-tagged), holding the sha.
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("base\nlocal-work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "stash", "push", "-m", "ferry-sync-snapshot")
	realStashSHA = testGit(t, repo, "rev-parse", "refs/stash")

	s = &snapshot{
		repo:     repo,
		headSHA:  head,
		hasStash: true,
		// BOGUS ref: not a valid reference, so `stash apply --index <ref>` fails hard
		// during restore (git: "is not a valid reference", exit 1) — modelling a
		// rollback that could not re-apply the tracked work.
		stashSHA:  "refs/ferry/definitely-not-a-real-stash",
		untracked: map[string]bool{}, backups: map[string]string{},
		modes: map[string]os.FileMode{}, symlinks: map[string]string{},
	}
	return repo, s, realStashSHA
}

func stashCount(t *testing.T, repo string) int {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "stash", "list")
	cmd.Env = gitIsolatedEnv("GIT_PAGER=cat")
	out, _ := cmd.CombinedOutput()
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// A failed stash-apply during restore must leave stashApplied=false and return an error.
func TestRestoreFailedStashApplyReportsError(t *testing.T) {
	repo, s, _ := newStashRepo(t)
	err := s.restore(repo)
	if err == nil {
		t.Fatalf("restore with a bogus stashSHA returned nil (must error)")
	}
	if s.stashApplied {
		t.Errorf("stashApplied is true after a FAILED stash apply (must stay false)")
	}
}

// cleanup MUST NOT drop the snapshot stash when it was never applied — the stash is the
// user's only copy of the tracked work.
func TestCleanupKeepsStashWhenRestoreFailed(t *testing.T) {
	repo, s, _ := newStashRepo(t)
	before := stashCount(t, repo)
	if before < 1 {
		t.Fatalf("setup: expected a held stash, got %d", before)
	}
	_ = s.restore(repo) // fails at stash apply; stashApplied stays false
	s.cleanup()
	if after := stashCount(t, repo); after != before {
		t.Errorf("cleanup DROPPED the snapshot stash after a failed restore: %d -> %d (DATA LOSS)", before, after)
	}
}

// rollback must surface the stash sha + a recovery command when restore fails.
func TestRollbackReportsStashShaOnFailedRestore(t *testing.T) {
	repo, s, realSHA := newStashRepo(t)
	err := rollback(s, repo, errCause("original failure"))
	if err == nil {
		t.Fatalf("rollback returned nil on a failed restore (must return a data-safety error)")
	}
	_ = realSHA // the physical stash still lives at refs/stash (checked by count below)
	msg := err.Error()
	// The message must name the snapshot's stashSHA — on a real run this IS the held
	// stash's sha (captured from refs/stash in takeSnapshot), so the user can recover it.
	if !strings.Contains(msg, s.stashSHA) {
		t.Errorf("rollback error did not name the PRESERVED stash sha %s\n%s", s.stashSHA, msg)
	}
	if !strings.Contains(msg, "stash apply") {
		t.Errorf("rollback error did not give a `git stash apply` recovery command\n%s", msg)
	}
	if !strings.Contains(msg, "original failure") {
		t.Errorf("rollback error dropped the original cause\n%s", msg)
	}
	// Even after cleanup runs (deferred on every real run), the stash is STILL there
	// to recover from — cleanup must not drop an un-applied stash.
	s.cleanup()
	if n := stashCount(t, repo); n < 1 {
		t.Errorf("the preserved stash was lost (count=%d) — recovery is impossible", n)
	}
}

// On a CLEAN restore (valid stashSHA), rollback returns the original cause AND cleanup
// drops the now-redundant stash — proving the guard only KEEPS the stash on failure.
func TestRollbackCleanRestoreDropsStash(t *testing.T) {
	repo, s, realSHA := newStashRepo(t)
	s.stashSHA = realSHA // valid: restore's stash apply will succeed
	err := rollback(s, repo, errCause("boom"))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("rollback on a clean restore must return the original cause; got %v", err)
	}
	if !s.stashApplied {
		t.Errorf("stashApplied is false after a SUCCESSFUL restore")
	}
	s.cleanup()
	if n := stashCount(t, repo); n != 0 {
		t.Errorf("cleanup did not drop the redundant snapshot stash after a clean restore (count=%d)", n)
	}
}

type errCause string

func (e errCause) Error() string { return string(e) }
