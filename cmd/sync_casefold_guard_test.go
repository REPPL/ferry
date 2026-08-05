package cmd

// On a case-insensitive filesystem (macOS APFS default), a remote-added path
// that differs from a local untracked/ignored file only in case resolves to
// the SAME file on disk — git treats the local spelling as ignored under
// core.ignorecase=true and silently overwrites it during checkout. The guard
// compared spellings byte-exactly, so the collision was invisible. The guard
// folds case exactly when the repo declares core.ignorecase=true (git sets it
// on init/clone from the filesystem), so case-sensitive repos keep exact
// matching and never abort on a legitimately distinct Notes.md/notes.md pair.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CRITICAL: with core.ignorecase=true, an ignored local file colliding with a
// remote-added path by case-fold alone aborts the sync.
func TestGuardUntrackedClobberCatchesCaseFoldCollision(t *testing.T) {
	repo := upstreamAdding(t, "notes.md", "remote spelling\n", false)
	// The local file exists under a different case; ignore that spelling so
	// git will clobber it without its own untracked-overwrite refusal.
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("link-target-cache\nNotes.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", ".gitignore")
	testGit(t, repo, "commit", "-qm", "ignore Notes.md")
	if err := os.WriteFile(filepath.Join(repo, "Notes.md"), []byte("local spelling\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "config", "core.ignorecase", "true")

	snap, err := takeSnapshot(repo)
	if err != nil {
		t.Fatalf("takeSnapshot: %v", err)
	}
	gerr := guardUntrackedClobber(repo, snap, "up")
	if gerr == nil {
		t.Fatal("guard passed: on a case-insensitive filesystem the merge would overwrite Notes.md via notes.md")
	}
	if !strings.Contains(gerr.Error(), "Notes.md") || !strings.Contains(gerr.Error(), "notes.md") {
		t.Errorf("guard error should name both spellings: %v", gerr)
	}
}

// A case-SENSITIVE repo (core.ignorecase unset/false) keeps exact matching:
// a legitimately distinct Notes.md beside a remote-added notes.md is not a
// collision and must not abort.
func TestGuardUntrackedClobberExactMatchOnCaseSensitiveRepo(t *testing.T) {
	repo := upstreamAdding(t, "notes.md", "remote spelling\n", false)
	if err := os.WriteFile(filepath.Join(repo, "Notes.md"), []byte("local spelling\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := takeSnapshot(repo)
	if err != nil {
		t.Fatalf("takeSnapshot: %v", err)
	}
	if gerr := guardUntrackedClobber(repo, snap, "up"); gerr != nil {
		t.Fatalf("guard aborted on a distinct-case pair in a case-sensitive repo: %v", gerr)
	}
}
