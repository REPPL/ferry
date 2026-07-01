package evals

// Regression evals for the v0.2.3 cmd/sync.go review-finding fixes. These drive the
// REAL ferry binary against a local bare origin (like sync_test.go) and assert
// OBSERVABLE outcomes. They are ADDITIVE to sync_test.go (which stays the contract):
//
//   - in-progress rebase/merge refusal (CRITICAL-4): sync refuses a mid-operation repo;
//   - detached-HEAD / wrong-branch refusal (MAJOR): sync refuses to push the wrong ref;
//   - commit adds untracked captured files (MAJOR): a NEW file is committed + pushed;
//   - scan-error fails closed (MAJOR): an unreadable changed file aborts before push;
//   - hooks neutralized (CRITICAL-3): a pre-push/pre-commit hook that would fail (or
//     touch a tripwire) never runs, so sync still succeeds and ~/.ssh is untouched.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -----------------------------------------------------------------------------
// In-progress git operation refusal (CRITICAL-4).
// -----------------------------------------------------------------------------

func TestSyncRefusesInProgressRebase_regression(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)

	// Fabricate a mid-rebase state the rollback path would otherwise destroy: create
	// the sentinel git leaves during a real rebase. Simplest deterministic way: make a
	// conflicting rebase and leave it in progress (do NOT abort it).
	seedRemoteAhead(t, sr, "shared.txt", "line-1\nREMOTE\nline-3\n", "remote edit")
	s.WriteRepoFile(t, "shared.txt", "line-1\nLOCAL\nline-3\n")
	syncGit(t, sr.clone, "commit", "-aqm", "local conflicting edit")
	syncGit(t, sr.clone, "fetch", "-q", "origin")
	// Start a rebase that WILL conflict; ignore its non-zero exit — we want it left
	// in progress on purpose.
	_, _ = syncGitOK(t, sr.clone, "rebase", "refs/remotes/origin/"+syncBranch)
	if !rebaseInProgress(sr.clone) {
		t.Skip("could not stage an in-progress rebase in this environment")
	}
	tipBefore := originTip(t, sr)

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut
	if code == 0 {
		t.Errorf("in-progress-rebase: sync exited 0 (must refuse a mid-rebase repo)")
	}
	if !containsAnyFold(combined, "in the middle", "in-progress", "abort", "finish") {
		t.Errorf("in-progress-rebase: refusal did not explain the mid-operation state\n%s", combined)
	}
	// The rebase is STILL in progress — sync did NOT touch it (no rebase --abort).
	if !rebaseInProgress(sr.clone) {
		t.Errorf("in-progress-rebase: sync destroyed the user's in-progress rebase")
	}
	if sr.git.invokedSubcommand("push") {
		t.Errorf("in-progress-rebase: a push happened despite the refusal")
	}
	if tipAfter := originTip(t, sr); tipAfter != tipBefore {
		t.Errorf("in-progress-rebase: origin tip moved despite refusal")
	}
	noForcePush(t, sr)
}

func TestSyncRefusesInProgressMerge_regression(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)

	// Plant a MERGE_HEAD sentinel by hand (a merge left mid-conflict). We write the
	// sentinel file directly so the test is deterministic across git versions.
	gitDir := filepath.Join(sr.clone, ".git")
	head := syncGit(t, sr.clone, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatalf("plant MERGE_HEAD: %v", err)
	}
	tipBefore := originTip(t, sr)

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut
	if code == 0 {
		t.Errorf("in-progress-merge: sync exited 0 (must refuse a mid-merge repo)")
	}
	if !containsAnyFold(combined, "merge", "in the middle", "abort", "finish") {
		t.Errorf("in-progress-merge: refusal did not explain the mid-merge state\n%s", combined)
	}
	if sr.git.invokedSubcommand("push") {
		t.Errorf("in-progress-merge: a push happened despite the refusal")
	}
	if tipAfter := originTip(t, sr); tipAfter != tipBefore {
		t.Errorf("in-progress-merge: origin tip moved despite refusal")
	}
}

// -----------------------------------------------------------------------------
// Detached HEAD / wrong branch refusal (MAJOR).
// -----------------------------------------------------------------------------

func TestSyncRefusesDetachedHEAD_regression(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)
	// Detach HEAD at the current commit.
	head := syncGit(t, sr.clone, "rev-parse", "HEAD")
	syncGit(t, sr.clone, "checkout", "-q", head)
	tipBefore := originTip(t, sr)

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut
	if code == 0 {
		t.Errorf("detached-HEAD: sync exited 0 (must refuse a detached HEAD)")
	}
	if !containsAnyFold(combined, "detached", "branch", "check out", "checkout") {
		t.Errorf("detached-HEAD: refusal did not explain the detached HEAD\n%s", combined)
	}
	if sr.git.invokedSubcommand("push") {
		t.Errorf("detached-HEAD: a push happened despite the refusal")
	}
	if tipAfter := originTip(t, sr); tipAfter != tipBefore {
		t.Errorf("detached-HEAD: origin tip moved despite refusal")
	}
	noForcePush(t, sr)
}

func TestSyncRefusesWrongBranch_regression(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)
	// Move to a DIFFERENT branch than the one sync pushes.
	syncGit(t, sr.clone, "checkout", "-q", "-b", "feature")
	tipBefore := originTip(t, sr)

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut
	if code == 0 {
		t.Errorf("wrong-branch: sync exited 0 (must refuse a non-%s branch)", syncBranch)
	}
	if !containsAnyFold(combined, "branch", syncBranch, "check out", "checkout") {
		t.Errorf("wrong-branch: refusal did not name the expected branch\n%s", combined)
	}
	if tipAfter := originTip(t, sr); tipAfter != tipBefore {
		t.Errorf("wrong-branch: origin tip moved despite refusal")
	}
}

// -----------------------------------------------------------------------------
// commit adds untracked captured files (MAJOR) — `git add -A` not `commit -a`.
// -----------------------------------------------------------------------------

func TestSyncCommitAddsUntracked_regression(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)

	// A brand-NEW untracked file (a captured new dotfile). `git commit -a` would ignore
	// it; the fix stages -A so it is committed AND pushed.
	s.WriteRepoFile(t, "brand-new.txt", "captured-new-content\n")

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("commit-adds-untracked: sync exited %d\n%s\ngit: %v", code, combined, sr.git.lines())
	}
	// The new file is now COMMITTED locally (tracked in HEAD's tree).
	if _, ok := syncGitOK(t, sr.clone, "cat-file", "-e", "HEAD:brand-new.txt"); !ok {
		t.Errorf("commit-adds-untracked: brand-new.txt was NOT committed (commit -a would drop it)")
	}
	// And it reached the origin.
	verify := t.TempDir()
	syncGit(t, verify, "clone", "-q", sr.bare, ".")
	if _, err := os.Stat(filepath.Join(verify, "brand-new.txt")); err != nil {
		t.Errorf("commit-adds-untracked: origin missing brand-new.txt (captured new file not pushed)")
	}
	noForcePush(t, sr)
}

// -----------------------------------------------------------------------------
// scan-error fails closed (MAJOR) — an unreadable changed file aborts before push.
// -----------------------------------------------------------------------------

func TestSyncScanErrorFailsClosed_regression(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root: unreadable-file permission check does not hold")
	}
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)

	// A changed (untracked) file the secret scan MUST read, made UNREADABLE so the read
	// fails. Fail-closed => sync aborts before any commit/push, nothing leaves.
	p := s.WriteRepoFile(t, "unreadable.conf", "value = 1\n")
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	tipBefore := originTip(t, sr)

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut
	if code == 0 {
		t.Errorf("scan-fails-closed: sync exited 0 despite an unreadable changed file (must abort)")
	}
	if !containsAnyFold(combined, "could not read", "scan", "refusing", "fail closed") {
		t.Errorf("scan-fails-closed: abort did not explain the failed scan\n%s", combined)
	}
	if tipAfter := originTip(t, sr); tipAfter != tipBefore {
		t.Errorf("scan-fails-closed: origin tip moved (a push happened on an incomplete scan)")
	}
	noForcePush(t, sr)
}

// -----------------------------------------------------------------------------
// Hooks neutralized (CRITICAL-3) — a repo hook that would run arbitrary code (here:
// fail the operation / touch a tripwire) NEVER runs, so sync still succeeds.
// -----------------------------------------------------------------------------

func TestSyncNeutralizesHooks_regression(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SSHTripwire(t)
	sr := newSyncRepo(t, s, true)

	// Install a pre-commit AND pre-push hook that would each (a) create a marker file
	// and (b) exit non-zero (aborting the op). With hooks neutralized (core.hooksPath=
	// /dev/null) neither runs: no marker appears and sync completes.
	hooksDir := filepath.Join(sr.clone, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	marker := filepath.Join(s.Home, "HOOK_RAN")
	hookBody := "#!/bin/sh\ntouch " + shellQuote(marker) + "\nexit 1\n"
	for _, h := range []string{"pre-commit", "pre-push", "post-commit", "pre-rebase"} {
		hp := filepath.Join(hooksDir, h)
		if err := os.WriteFile(hp, []byte(hookBody), 0o755); err != nil {
			t.Fatalf("write hook %s: %v", h, err)
		}
	}

	// Give sync real work to commit + push.
	s.WriteRepoFile(t, "local-only.txt", "from-local\n")

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("hooks-neutralized: sync exited %d — a hook must have run and aborted it\n%s\ngit: %v", code, combined, sr.git.lines())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("hooks-neutralized: a repo hook RAN (marker created) — hooks were not neutralized")
	}
	// The commit + push still happened (hooks did not block).
	if !sr.git.invokedSubcommand("push") {
		t.Errorf("hooks-neutralized: no push recorded (sync did not complete)")
	}
	// EVERY git call carried the hooks-off flag.
	for _, line := range sr.git.lines() {
		if !strings.Contains(line, "core.hooksPath=/dev/null") {
			t.Errorf("hooks-neutralized: a git call ran WITHOUT -c core.hooksPath=/dev/null: %q", line)
		}
	}
	s.AssertSSHUntouched(t)
	noForcePush(t, sr)
}
