package cmd

// The pre-commit secret gate parses `git status --porcelain -z`, whose rename and
// copy entries carry the ORIGIN path as a separate NUL field with no status code.
// That field must be consumed with its entry: sliced as if it were a status line,
// an origin of four bytes or more yields a bogus path (`src/dotfiles` reads as
// `/dotfiles`), which either scans an unrelated file or — when the bogus name
// resolves to a directory — fails the read and aborts the whole sync naming a path
// the user never touched.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// renameScanRepo builds a repo whose staged rename has an origin path that, when
// mis-sliced at [3:], names a REAL directory in the same repo: `src/dotfiles`
// becomes `/dotfiles`, and ferry config repos carry a top-level `dotfiles/`.
func renameScanRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: the secret-gate scan needs a real repo")
	}
	repo := t.TempDir()
	testGit(t, repo, "init", "-q", "-b", "main", ".")
	if err := os.MkdirAll(filepath.Join(repo, "dotfiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "dotfiles", "zshrc"), []byte("export A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "dotfiles"), []byte("harmless\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", "-A")
	testGit(t, repo, "commit", "-qm", "base")
	return repo
}

// MAJOR: a staged rename must not abort the sync. Before the origin field was
// consumed, the bogus `/dotfiles` path resolved to the repo's own `dotfiles/`
// directory, whose read returns EISDIR — not os.IsNotExist — so the gate returned
// a hard error and runSync rolled back and refused to commit or push.
func TestScanWorktreeForSecretConsumesRenameOrigin(t *testing.T) {
	repo := renameScanRepo(t)
	testGit(t, repo, "mv", filepath.Join("src", "dotfiles"), filepath.Join("src", "renamed"))

	path, found, err := scanWorktreeForSecret(repo)
	if err != nil {
		t.Fatalf("staged rename made the secret scan fail closed: %v", err)
	}
	if found {
		t.Fatalf("no secret was seeded, but the scan blocked on %q", path)
	}
}

// The renamed file's own content is still scanned: consuming the origin field must
// not skip the entry it belongs to.
func TestScanWorktreeForSecretStillScansRenamedFile(t *testing.T) {
	repo := renameScanRepo(t)
	testGit(t, repo, "mv", filepath.Join("src", "dotfiles"), filepath.Join("src", "renamed"))
	if err := os.WriteFile(filepath.Join(repo, "src", "renamed"), []byte(fakeRenameSecret), 0o644); err != nil {
		t.Fatal(err)
	}

	path, found, err := scanWorktreeForSecret(repo)
	if err != nil {
		t.Fatalf("scan errored: %v", err)
	}
	if !found {
		t.Fatal("a secret in the renamed file was not detected")
	}
	if filepath.ToSlash(path) != "src/renamed" {
		t.Fatalf("blocked on %q, want src/renamed", path)
	}
}

// stubStatusGit puts a `git` shim first on PATH that answers any invocation
// containing the `status` verb with a fixed payload and execs the real git for
// everything else — the same shim technique cmd/sync_stderr_test.go uses to
// drive the porcelain parser with output real git would not produce.
func stubStatusGit(t *testing.T, payload string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	// The payload is written as raw bytes and `cat`ed, not embedded in the script:
	// it contains NUL separators, which no shell quoting or `printf '%s'` would
	// carry through intact.
	fixture := filepath.Join(dir, "status.bin")
	if err := os.WriteFile(fixture, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = \"status\" ]; then cat " + fixture + "; exit 0; fi\ndone\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// CRITICAL: a field that is neither a status entry nor a consumed rename origin
// must FAIL CLOSED. Sync treats a scan error as an incomplete scan and refuses to
// commit or push, which is the correct posture — silently skipping an
// unrecognised field could leave a changed file unscanned.
func TestScanWorktreeForSecretFailsClosedOnMalformedEntry(t *testing.T) {
	repo := renameScanRepo(t)
	// A bare path with no XY status code and no preceding rename entry to own it.
	stubStatusGit(t, "not-a-status-entry.txt\x00")

	_, _, err := scanWorktreeForSecret(repo)
	if err == nil {
		t.Fatal("a malformed status field was accepted; the gate must fail closed so sync refuses rather than scanning a bogus path")
	}
	if !strings.Contains(err.Error(), "unparseable") {
		t.Fatalf("error %q does not name the unparseable entry", err)
	}
}

// A well-formed listing must still pass: the fail-closed guard must not reject
// the ordinary shapes, including an unstaged-only entry whose leading space is
// load-bearing and an untracked entry.
func TestScanWorktreeForSecretAcceptsWellFormedEntries(t *testing.T) {
	repo := renameScanRepo(t)
	stubStatusGit(t, " M tracked.txt\x00?? untracked.txt\x00!! ignored.txt\x00")

	if _, _, err := scanWorktreeForSecret(repo); err != nil {
		t.Fatalf("well-formed porcelain rejected: %v", err)
	}
}

// A rename whose origin is the FINAL field must not run the index past the end.
func TestScanWorktreeForSecretHandlesTrailingRenameOrigin(t *testing.T) {
	repo := renameScanRepo(t)
	stubStatusGit(t, "R  new-name.txt\x00old-name.txt\x00")

	if _, _, err := scanWorktreeForSecret(repo); err != nil {
		t.Fatalf("rename origin as the last field errored: %v", err)
	}
}

// fakeRenameSecret is a NON-FUNCTIONAL private-key header — the shape
// internal/secret flags High. NOT a real key.
const fakeRenameSecret = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n" +
	"-----END OPENSSH PRIVATE KEY-----\n"
