package cmd

// Round-3 sync regressions (all internal, driving the real snapshot/scan code against
// real git repos):
//   - CRITICAL: a pre-existing ferry-sync-snapshot stash + a CLEAN tracked tree must NOT
//     be mistaken for this run's snapshot (hasStash=false) — never applying stale work.
//   - MAJOR: a secret-shaped PATH component blocks both the pre-commit worktree scan and
//     the push-range commit scan (parity with export / init --github), not just content.
//   - MAJOR: backupOutOfBand fails CLOSED when a file it must back up cannot be read.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// r3Repo returns an initialised git repo with one commit on `main`.
func r3Repo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	testGit(t, repo, "init", "-q", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", "-A")
	testGit(t, repo, "commit", "-qm", "base")
	return repo
}

// CRITICAL: a stale ferry-sync-snapshot stash left by a prior failed rollback, plus a
// CLEAN tracked tree this run, must yield hasStash=false — takeSnapshot identifies OUR
// stash by the refs/stash sha DELTA, not by the message at the top of the stash list.
// If this regressed, a re-run could apply/commit/push the OLD stale tracked work.
func TestTakeSnapshotIgnoresStaleStashOnCleanTree(t *testing.T) {
	repo := r3Repo(t)

	// Simulate a prior failed rollback: a ferry-sync-snapshot stash left behind, and the
	// worktree now CLEAN (the tracked change was captured into the stale stash).
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\nstale-work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "stash", "push", "-m", "ferry-sync-snapshot")
	staleSHA := testGit(t, repo, "rev-parse", "refs/stash")
	// Tracked tree is now clean; the stale stash sits at the top of the stash list.

	snap, err := takeSnapshot(repo)
	if err != nil {
		t.Fatalf("takeSnapshot on a clean tree errored: %v", err)
	}
	defer snap.cleanup()

	if snap.hasStash {
		t.Fatalf("takeSnapshot mistook the STALE ferry-sync-snapshot stash for this run's snapshot (hasStash=true, stashSHA=%s) — a re-run could apply STALE work", snap.stashSHA)
	}
	if snap.stashSHA != "" {
		t.Errorf("stashSHA should be empty on a clean-tree run; got %q", snap.stashSHA)
	}
	// The stale stash must be untouched: cleanup() must not drop a stash this run did not make.
	snap.cleanup()
	if got := testGit(t, repo, "rev-parse", "refs/stash"); got != staleSHA {
		t.Errorf("cleanup() disturbed the stale stash: %s -> %s (must be left intact)", staleSHA, got)
	}
}

// Positive control: when this run DOES stash tracked work, hasStash=true and the sha is
// the new top-of-stash — proving the delta identification still catches a real snapshot.
func TestTakeSnapshotIdentifiesOwnStash(t *testing.T) {
	repo := r3Repo(t)
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\nmine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := takeSnapshot(repo)
	if err != nil {
		t.Fatalf("takeSnapshot errored: %v", err)
	}
	defer snap.cleanup()
	if !snap.hasStash {
		t.Fatalf("takeSnapshot did not record this run's tracked stash (hasStash=false)")
	}
	if snap.stashSHA != testGit(t, repo, "rev-parse", "refs/stash") {
		t.Errorf("stashSHA %q is not the new top-of-stash", snap.stashSHA)
	}
}

// MAJOR: a WORKTREE file whose PATH is secret-shaped blocks the pre-commit scan, exactly
// like secret content — even though the file's CONTENT is clean.
func TestScanWorktreeBlocksSecretShapedPath(t *testing.T) {
	repo := r3Repo(t)
	// Clean content, but the filename component is a secret-shaped token.
	if err := os.WriteFile(filepath.Join(repo, secretShapedName), []byte("nothing secret in here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, found, err := scanWorktreeForSecret(repo)
	if err != nil {
		t.Fatalf("scanWorktreeForSecret errored: %v", err)
	}
	if !found {
		t.Fatalf("secret-shaped PATH %q was NOT blocked by the worktree scan", secretShapedName)
	}
	if path != secretShapedName {
		t.Errorf("blocked path = %q, want %q", path, secretShapedName)
	}
}

// MAJOR: a COMMITTED file whose PATH is secret-shaped blocks the push-range scan.
func TestScanCommitRangeBlocksSecretShapedPath(t *testing.T) {
	repo := r3Repo(t)
	if err := os.WriteFile(filepath.Join(repo, secretShapedName), []byte("clean content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", "-A")
	testGit(t, repo, "commit", "-qm", "add secret-shaped path")
	commitSHA := testGit(t, repo, "rev-parse", "HEAD")

	commit, file, found, err := scanCommitRangeForSecret(repo, []string{commitSHA})
	if err != nil {
		t.Fatalf("scanCommitRangeForSecret errored: %v", err)
	}
	if !found {
		t.Fatalf("secret-shaped PATH %q in a committed tree was NOT blocked by the push-range scan", secretShapedName)
	}
	if file != secretShapedName || commit != commitSHA {
		t.Errorf("blocked (commit,file) = (%s,%s), want (%s,%s)", commit, file, commitSHA, secretShapedName)
	}
}

// MAJOR: backupOutOfBand must FAIL CLOSED when a file it must back up cannot be read —
// the snapshot aborts rather than proceeding with an incomplete (un-restorable) backup.
func TestBackupOutOfBandFailsClosedOnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny read")
	}
	repo := r3Repo(t)
	// An UNTRACKED file that git will enumerate but we cannot read (perm denied).
	secretFile := filepath.Join(repo, "unreadable.txt")
	if err := os.WriteFile(secretFile, []byte("private\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secretFile, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(secretFile, 0o644) })

	_, err := takeSnapshot(repo)
	if err == nil {
		t.Fatalf("takeSnapshot did NOT fail closed on an un-backupable untracked file (must abort the snapshot)")
	}
	if !strings.Contains(err.Error(), "back up") && !strings.Contains(err.Error(), "backup") {
		t.Errorf("error did not mention the backup failure: %v", err)
	}
}

// MAJOR: rollback's recovery message must NOT surface a raw home path — redactPath
// collapses the home prefix to `~` (and still applies the token scrub). A path under the
// user's home is echoed as `~/...`, never `/Users/<name>/...` or `/home/<name>/...`.
func TestRedactPathCollapsesHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	got := redactPath(filepath.Join(home, "projects", "cfg"))
	if strings.HasPrefix(got, home) {
		t.Errorf("redactPath leaked the raw home prefix: %q", got)
	}
	if !strings.HasPrefix(got, "~") {
		t.Errorf("redactPath did not collapse home to ~: %q", got)
	}
	// A path NOT under home is returned as-is (token scrub still applies).
	if outside := redactPath("/opt/data/cfg"); outside != "/opt/data/cfg" {
		t.Errorf("redactPath altered a non-home path: %q", outside)
	}
}

// The full rollback message, when the backup dir lives under home, must not contain the
// raw home prefix (defense against a home path leaking into surfaced errors).
func TestRollbackMessageRedactsBackupDirHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	repo, s, _ := newStashRepo(t) // bogus stashSHA → restore fails → recovery message built
	s.backupDir = filepath.Join(home, ".ferry-test-backup-xyz")
	msg := rollback(s, repo, errCause("boom")).Error()
	if strings.Contains(msg, home) {
		t.Errorf("rollback message leaked the raw home path %q:\n%s", home, msg)
	}
	if !strings.Contains(msg, "~/.ferry-test-backup-xyz") {
		t.Errorf("rollback message did not surface the redacted backup dir:\n%s", msg)
	}
}

// TestRedactSecretPathMasksSecretComponent asserts a secret-shaped path component is
// masked to <redacted> in a block message (so a token-shaped filename can't leak), while
// safe components stay visible for the user to locate the file.
func TestRedactSecretPathMasksSecretComponent(t *testing.T) {
	// A high-entropy, secret-shaped component secretInPath flags.
	secretComp := "AKIAIOSFODNN7EXAMPLEXYZ0123456789abcd"
	in := "dir/" + secretComp + "/notes.txt"
	got := redactSecretPath(in)
	if strings.Contains(got, secretComp) {
		t.Fatalf("redactSecretPath leaked the secret-shaped component: %q", got)
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("redactSecretPath did not mask the secret component: %q", got)
	}
	// Safe components remain for locatability.
	if !strings.Contains(got, "dir/") || !strings.Contains(got, "notes.txt") {
		t.Fatalf("redactSecretPath over-redacted safe components: %q", got)
	}
	// A fully-benign path is unchanged.
	if got := redactSecretPath("a/b/c.txt"); got != "a/b/c.txt" {
		t.Fatalf("redactSecretPath altered a benign path: %q", got)
	}
}
