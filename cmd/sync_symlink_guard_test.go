package cmd

// The untracked-clobber overwrite guard must cover SYMLINKS. An ignored local
// symlink is recorded only in s.symlinks (backupOne stores its target, no byte
// backup), and git silently overwrites an ignored path during checkout — so a
// guard that only consults s.untracked/s.backups lets the merge destroy the
// symlink in a run that then SUCCEEDS, meaning rollback never fires and the
// link is unrecoverable. An untracked symlink is likewise unsafe to compare by
// following it: byte-equality with the remote blob would still replace a
// symlink with a regular file.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// upstreamAdding returns a repo (HEAD on main) plus a local branch `up` whose
// tip adds rel as a tracked regular file with content — the guard's upstream.
// When ignoreRel is true, rel is also listed in the committed .gitignore.
func upstreamAdding(t *testing.T, rel, content string, ignoreRel bool) string {
	t.Helper()
	repo := r3Repo(t)
	ignores := "link-target-cache\n"
	if ignoreRel {
		ignores += rel + "\n"
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(ignores), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", ".gitignore")
	testGit(t, repo, "commit", "-qm", "ignore "+rel)
	testGit(t, repo, "checkout", "-qb", "up")
	if err := os.WriteFile(filepath.Join(repo, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", "-f", rel)
	testGit(t, repo, "commit", "-qm", "add "+rel)
	testGit(t, repo, "checkout", "-q", "main")
	return repo
}

// CRITICAL: an IGNORED local symlink at a remote-added path aborts the sync —
// a symlink can never be byte-identical to the remote's regular blob.
func TestGuardUntrackedClobberCatchesIgnoredSymlink(t *testing.T) {
	repo := upstreamAdding(t, "link.txt", "remote content\n", true)
	if err := os.Symlink("/nonexistent/elsewhere", filepath.Join(repo, "link.txt")); err != nil {
		t.Fatal(err)
	}
	snap, err := takeSnapshot(repo)
	if err != nil {
		t.Fatalf("takeSnapshot: %v", err)
	}
	if _, ok := snap.symlinks["link.txt"]; !ok {
		t.Fatalf("fixture: ignored symlink not recorded in s.symlinks (got %v)", snap.symlinks)
	}
	gerr := guardUntrackedClobber(repo, snap, "up")
	if gerr == nil {
		t.Fatal("guard passed: the merge would silently replace the ignored symlink with the remote's file")
	}
	if !strings.Contains(gerr.Error(), "link.txt") {
		t.Errorf("guard error does not name the colliding path: %v", gerr)
	}
}

// MAJOR: an UNTRACKED symlink whose TARGET's bytes equal the remote blob must
// still abort — comparing through the link would swap a symlink for a file.
func TestGuardUntrackedClobberDoesNotFollowUntrackedSymlink(t *testing.T) {
	repo := upstreamAdding(t, "notes.txt", "same bytes\n", false)
	target := filepath.Join(repo, "link-target-cache")
	if err := os.WriteFile(target, []byte("same bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(repo, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	snap, err := takeSnapshot(repo)
	if err != nil {
		t.Fatalf("takeSnapshot: %v", err)
	}
	if gerr := guardUntrackedClobber(repo, snap, "up"); gerr == nil {
		t.Fatal("guard passed on a symlink via target-byte equality: the merge would replace the symlink with a regular file")
	}
}
