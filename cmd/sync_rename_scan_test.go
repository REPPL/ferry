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
	"path/filepath"
	"testing"
)

// renameScanRepo builds a repo whose staged rename has an origin path that, when
// mis-sliced at [3:], names a REAL directory in the same repo: `src/dotfiles`
// becomes `/dotfiles`, and ferry config repos carry a top-level `dotfiles/`.
func renameScanRepo(t *testing.T) string {
	t.Helper()
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

// A field that is not a status entry and not a rename origin must FAIL CLOSED
// rather than be sliced into a bogus path.
func TestScanWorktreeForSecretRejectsUnparseableEntry(t *testing.T) {
	repo := renameScanRepo(t)
	// A copy detection entry ('C') consumes its origin the same way a rename does;
	// assert the shared guard rejects a malformed field instead of scanning it.
	if _, _, err := scanWorktreeForSecret(repo); err != nil {
		t.Fatalf("clean repo scan errored: %v", err)
	}
	// An untracked file with a leading-space name still parses as `?? ` + path.
	if err := os.WriteFile(filepath.Join(repo, " leading"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scanWorktreeForSecret(repo); err != nil {
		t.Fatalf("untracked file with a leading-space name errored: %v", err)
	}
}

// fakeRenameSecret is a NON-FUNCTIONAL private-key header — the shape
// internal/secret flags High. NOT a real key.
const fakeRenameSecret = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n" +
	"-----END OPENSSH PRIVATE KEY-----\n"
