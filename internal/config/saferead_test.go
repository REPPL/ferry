package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadScope_RefusesSymlinkedSharedManifest: a ferry.toml that is a SYMLINK
// (e.g. pointing at a fake ~/.ssh/config) must be REFUSED before its target is
// read. The symlink target is left byte-identical (never read through).
func TestLoadScope_RefusesSymlinkedSharedManifest(t *testing.T) {
	repo := t.TempDir()

	// A FAKE "~/.ssh/config"-like secret under t.TempDir() — never the real ~/.ssh.
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "config")
	const secretBody = "Host evil\n  IdentityFile ~/.ssh/id_rsa\n"
	if err := os.WriteFile(secret, []byte(secretBody), 0o600); err != nil {
		t.Fatalf("write fake ssh config: %v", err)
	}

	// ferry.toml is a symlink to the fake secret.
	if err := os.Symlink(secret, filepath.Join(repo, SharedManifestName)); err != nil {
		t.Fatalf("symlink shared manifest: %v", err)
	}

	if _, err := LoadScope(repo); err == nil {
		t.Fatal("LoadScope with symlinked ferry.toml: want refusal error, got nil")
	}

	// The symlink target must be untouched (we never read or wrote through it).
	got, err := os.ReadFile(secret)
	if err != nil {
		t.Fatalf("re-read fake secret: %v", err)
	}
	if string(got) != secretBody {
		t.Errorf("symlink target was modified: got %q want %q", got, secretBody)
	}
}

// TestLoadScope_RefusesSymlinkedLocalManifest: a ferry.local.toml that is a
// symlink must be refused even though a regular ferry.toml is present.
func TestLoadScope_RefusesSymlinkedLocalManifest(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, SharedManifestName), []byte("[manage]\nbrew = true\n"), 0o644); err != nil {
		t.Fatalf("write shared: %v", err)
	}

	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "config")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write fake secret: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(repo, LocalManifestName)); err != nil {
		t.Fatalf("symlink local manifest: %v", err)
	}

	if _, err := LoadScope(repo); err == nil {
		t.Fatal("LoadScope with symlinked ferry.local.toml: want refusal error, got nil")
	}
}

// TestLoadScope_RegularFilesLoadFine: normal regular ferry.toml + ferry.local.toml
// load without the guard interfering.
func TestLoadScope_RegularFilesLoadFine(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, SharedManifestName), []byte("[manage]\nbrew = true\niterm2 = true\n"), 0o644); err != nil {
		t.Fatalf("write shared: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, LocalManifestName), []byte("[manage]\niterm2 = false\n"), 0o644); err != nil {
		t.Fatalf("write local: %v", err)
	}

	scope, err := LoadScope(repo)
	if err != nil {
		t.Fatalf("LoadScope with regular files: %v", err)
	}
	if !scope.IsManaged("brew") {
		t.Error("brew should be managed")
	}
	if scope.IsManaged("iterm2") {
		t.Error("iterm2 should be overridden to false by local")
	}
}

// TestLoadScope_AbsentLocalFileFine: an absent ferry.local.toml is normal — the
// guard returns the path and the loader treats absence as "no overrides".
func TestLoadScope_AbsentLocalFileFine(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, SharedManifestName), []byte("[manage]\nbrew = true\n"), 0o644); err != nil {
		t.Fatalf("write shared: %v", err)
	}
	// No ferry.local.toml written.
	if _, err := LoadScope(repo); err != nil {
		t.Fatalf("LoadScope with absent local file should succeed: %v", err)
	}
}
