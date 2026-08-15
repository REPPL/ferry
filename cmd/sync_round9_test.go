package cmd

// Round-9 regressions in the pre-commit worktree secret gate (scanWorktreeForSecret),
// all driving the real scan against real git repos:
//   - CRITICAL: the single-file read followed SYMLINKS, so an untracked repo symlink
//     pointing at ~/.ssh/id_ed25519 made sync READ the private key — a breach of the
//     "~/.ssh is untouchable" boundary. Git commits a symlink as its link TEXT, never
//     the target's bytes, so reading the target is both wrong and unnecessary.
//   - MAJOR: a directory-shaped entry emitted WITHOUT a trailing slash (a dirty
//     submodule ` M sub`, an untracked symlink-to-directory `?? link`) EISDIRed the
//     same read. EISDIR is neither a not-exist nor a deletion, so the gate failed
//     closed and wedged every sync with advice a directory can never satisfy.
//   - MAJOR: the `?? dir/` branch walked the WHOLE directory, scanning files
//     `git add -A` would never stage — gitignored paths (ferry's own init writes an
//     unanchored `local/` pattern) and a nested repository's `.git/**` — so an
//     ignored token or a nested repo's credential-bearing remote URL false-blocked
//     the sync.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// r9Repo is r3Repo plus the git-config neutralisation the collapsed-directory
// entries depend on: a developer's status.showUntrackedFiles=all would list
// per-file entries and never produce the `?? dir/` shape these tests pin.
func r9Repo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return r3Repo(t)
}

// CRITICAL: an untracked symlink whose TARGET lives outside the repo (the
// ~/.ssh/id_ed25519 shape) must not be dereferenced by the gate. Git stages the
// link text, so the target's bytes never enter a commit and the push-range blob
// scan already covers what does; following it would read a file the boundary
// declares untouchable — and, as here, block the sync on content that is never
// going anywhere.
func TestScanWorktreeDoesNotFollowUntrackedSymlink(t *testing.T) {
	repo := r9Repo(t)
	// A private-key-shaped file OUTSIDE the repo, standing in for ~/.ssh/id_ed25519.
	outside := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(outside, []byte(fakeRound9Key), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "notes")); err != nil {
		t.Fatal(err)
	}

	path, found, err := scanWorktreeForSecret(repo)
	if err != nil {
		t.Fatalf("scanWorktreeForSecret errored on an untracked symlink: %v", err)
	}
	if found {
		t.Fatalf("the gate FOLLOWED a symlink out of the repo and blocked on its target (path %q) — sync read a file it must never open", path)
	}
}

// A symlink to a DIRECTORY is a plain `?? link` entry — directory-shaped with no
// trailing slash — so reading it as a file returns EISDIR, which is neither a
// not-exist nor a deletion. Before the lstat gate that wedged every sync.
func TestScanWorktreeSkipsUntrackedSymlinkToDirectory(t *testing.T) {
	repo := r9Repo(t)
	target := filepath.Join(repo, "realdir")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "a.txt"), []byte("harmless\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(repo, "dlink")); err != nil {
		t.Fatal(err)
	}

	path, found, err := scanWorktreeForSecret(repo)
	if err != nil {
		t.Fatalf("an untracked symlink-to-directory wedged the gate: %v", err)
	}
	if found {
		t.Fatalf("no secret was seeded, but the scan blocked on %q", path)
	}
}

// A dirty gitlink (embedded repository whose HEAD moved) is reported as ` M sub`
// — again directory-shaped without a trailing slash. A gitlink's CONTENT is never
// committed (ls-tree records it as type `commit`, which the push-range scan
// already skips), so skipping it loses no coverage.
func TestScanWorktreeSkipsDirtyGitlinkEntry(t *testing.T) {
	repo := r9Repo(t)
	sub := filepath.Join(repo, "sub")
	testGit(t, repo, "init", "-q", "-b", "main", "sub")
	if err := os.WriteFile(filepath.Join(sub, "x.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, sub, "add", "-A")
	testGit(t, sub, "commit", "-qm", "sub one")
	testGit(t, repo, "add", "sub") // records a gitlink, not the contents
	testGit(t, repo, "commit", "-qm", "add gitlink")
	// Move the embedded repo's HEAD so the parent reports ` M sub`.
	if err := os.WriteFile(filepath.Join(sub, "y.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, sub, "add", "-A")
	testGit(t, sub, "commit", "-qm", "sub two")

	path, found, err := scanWorktreeForSecret(repo)
	if err != nil {
		t.Fatalf("a dirty gitlink wedged the gate: %v", err)
	}
	if found {
		t.Fatalf("no secret was seeded, but the scan blocked on %q", path)
	}
}

// MAJOR: the untracked-directory branch must scan exactly what `git add -A` would
// stage. A gitignored file inside the directory is never committed, so scanning it
// blocks the sync on content that cannot leak — and ferry's own init writes an
// unanchored `local/` pattern, so this fires on ordinary local scratch space.
// The positive control in the same directory proves the branch still gates a file
// that WOULD be staged.
func TestScanWorktreeSkipsIgnoredFilesInUntrackedDirectory(t *testing.T) {
	repo := r9Repo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("local/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", ".gitignore")
	testGit(t, repo, "commit", "-qm", "ignore local/")

	// A wholly-untracked directory: one stageable clean file, one IGNORED file
	// carrying a fake credential.
	if err := os.MkdirAll(filepath.Join(repo, "stuff", "local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "stuff", "clean.txt"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "stuff", "local", "env.sh"), []byte(fakeRound9AWSKey), 0o644); err != nil {
		t.Fatal(err)
	}

	if path, found, err := scanWorktreeForSecret(repo); err != nil {
		t.Fatalf("scanWorktreeForSecret errored on an untracked directory: %v", err)
	} else if found {
		t.Fatalf("an IGNORED file (%q) that `git add -A` would never stage blocked the sync", path)
	}

	// Positive control: the same secret in a STAGEABLE file under the same
	// directory must still block.
	if err := os.WriteFile(filepath.Join(repo, "stuff", "creds.sh"), []byte(fakeRound9AWSKey), 0o644); err != nil {
		t.Fatal(err)
	}
	path, found, err := scanWorktreeForSecret(repo)
	if err != nil {
		t.Fatalf("scanWorktreeForSecret errored: %v", err)
	}
	if !found {
		t.Fatal("a secret in a stageable file inside an untracked directory was NOT commit-gated")
	}
	if filepath.ToSlash(path) != "stuff/creds.sh" {
		t.Errorf("blocked path = %q, want stuff/creds.sh", path)
	}
}

// MAJOR: git records a nested repository as a gitlink, so its `.git/**` never
// enters a commit. Walking into it made a nested repo's credential-bearing remote
// URL block the sync with advice the user cannot satisfy (the file is not theirs
// to clean). `git ls-files -o` reports the nested repo as a single directory
// entry, which the branch skips.
func TestScanWorktreeSkipsNestedRepoUnderUntrackedDirectory(t *testing.T) {
	repo := r9Repo(t)
	if err := os.MkdirAll(filepath.Join(repo, "stuff"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "stuff", "clean.txt"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(repo, "stuff", "nested")
	testGit(t, repo, "init", "-q", "-b", "main", filepath.Join("stuff", "nested"))
	// A remote URL with an embedded token: high-confidence content in a file that
	// is never committed by the outer repo.
	testGit(t, nested, "remote", "add", "origin", fakeRound9TokenURL)

	path, found, err := scanWorktreeForSecret(repo)
	if err != nil {
		t.Fatalf("scanWorktreeForSecret errored on an untracked directory holding a nested repo: %v", err)
	}
	if found {
		t.Fatalf("a nested repository's own %q was scanned and blocked the sync — its contents are never committed", path)
	}
}

// The enumeration itself must FAIL CLOSED: a directory whose contents git cannot
// list is a directory we cannot scan, so sync must refuse rather than commit it
// unscanned. The `git` shim answers `ls-files` with a failure and execs the real
// git for everything else (the technique cmd/sync_stderr_test.go uses).
func TestScanWorktreeFailsClosedWhenUntrackedDirectoryCannotBeListed(t *testing.T) {
	repo := r9Repo(t)
	if err := os.MkdirAll(filepath.Join(repo, "stuff"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "stuff", "clean.txt"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = \"ls-files\" ]; then echo 'fatal: cannot list' >&2; exit 128; fi\ndone\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, _, err := scanWorktreeForSecret(repo); err == nil {
		t.Fatal("a failed untracked-directory enumeration was accepted; the gate must fail closed so sync refuses rather than committing unscanned files")
	}
}

// fakeRound9Key is a NON-FUNCTIONAL private-key header — the shape
// internal/secret flags High. NOT a real key.
const fakeRound9Key = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n" +
	"-----END OPENSSH PRIVATE KEY-----\n"

// fakeRound9AWSKey is a fake AWS access-key line, matching the fixture style the
// round-3 scan tests use. NOT a real credential.
const fakeRound9AWSKey = "export AWS_ACCESS_KEY_ID=AKIA1234567890ABCDEF\n"

// fakeRound9TokenURL is a fake GitHub-token-bearing remote URL. NOT a real token.
const fakeRound9TokenURL = "https://user:ghp_0123456789abcdefghijABCDEFGHIJ012345@github.com/owner/repo.git"
