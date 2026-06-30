package config

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeRepoScope writes ferry.toml (always) and ferry.local.toml (when non-empty)
// into a fresh fake repo dir and returns its path. Never touches real ~.
func writeRepoScope(t *testing.T, shared, local string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, SharedManifestName), []byte(shared), 0o644); err != nil {
		t.Fatalf("write shared: %v", err)
	}
	if local != "" {
		if err := os.WriteFile(filepath.Join(repo, LocalManifestName), []byte(local), 0o644); err != nil {
			t.Fatalf("write local: %v", err)
		}
	}
	return repo
}

func TestLoadScope_SharedOnly(t *testing.T) {
	t.Parallel()
	repo := writeRepoScope(t, `[manage]
dotfiles = [".zshrc", ".gitconfig"]
brew = true
iterm2 = true
fonts = false
`, "")

	sc, err := LoadScope(repo)
	if err != nil {
		t.Fatalf("LoadScope: %v", err)
	}
	if !sc.IsManaged("dotfiles") {
		t.Error("dotfiles should be managed")
	}
	if !sc.IsManaged("brew") {
		t.Error("brew should be managed")
	}
	if !sc.IsManaged("iterm2") {
		t.Error("iterm2 should be managed")
	}
	if sc.IsManaged("fonts") {
		t.Error("fonts=false should not be managed")
	}
	if sc.IsManaged("unknown") {
		t.Error("undeclared domain should not be managed")
	}

	got := sc.DeclaredDotfiles()
	sort.Strings(got)
	want := []string{".gitconfig", ".zshrc"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("DeclaredDotfiles: got %v want %v", got, want)
	}
}

func TestLoadScope_LocalOverridesShared(t *testing.T) {
	t.Parallel()
	// shared iterm2=true; local iterm2=false => effective false.
	// brew stays true (local does not mention it).
	repo := writeRepoScope(t, `[manage]
dotfiles = [".zshrc"]
brew = true
iterm2 = true
fonts = false
`, `[manage]
iterm2 = false
fonts = true
`)

	sc, err := LoadScope(repo)
	if err != nil {
		t.Fatalf("LoadScope: %v", err)
	}
	if sc.IsManaged("iterm2") {
		t.Error("local iterm2=false must override shared iterm2=true")
	}
	if !sc.IsManaged("fonts") {
		t.Error("local fonts=true must override shared fonts=false")
	}
	if !sc.IsManaged("brew") {
		t.Error("brew unset locally must keep shared true")
	}
}

func TestLoadScope_LocalDotfilesReplace(t *testing.T) {
	t.Parallel()
	repo := writeRepoScope(t, `[manage]
dotfiles = [".zshrc", ".gitconfig"]
`, `[manage]
dotfiles = [".vimrc"]
`)
	sc, err := LoadScope(repo)
	if err != nil {
		t.Fatalf("LoadScope: %v", err)
	}
	got := sc.DeclaredDotfiles()
	if len(got) != 1 || got[0] != ".vimrc" {
		t.Errorf("local dotfiles list should replace shared; got %v", got)
	}
}

func TestLoadScope_LocalUnsetDotfilesKeepsShared(t *testing.T) {
	t.Parallel()
	repo := writeRepoScope(t, `[manage]
dotfiles = [".zshrc"]
iterm2 = true
`, `[manage]
iterm2 = false
`)
	sc, err := LoadScope(repo)
	if err != nil {
		t.Fatalf("LoadScope: %v", err)
	}
	got := sc.DeclaredDotfiles()
	if len(got) != 1 || got[0] != ".zshrc" {
		t.Errorf("shared dotfiles should stand when local omits them; got %v", got)
	}
}

func TestLoadScope_NoLocalFile(t *testing.T) {
	t.Parallel()
	repo := writeRepoScope(t, `[manage]
dotfiles = [".zshrc"]
brew = true
`, "")
	sc, err := LoadScope(repo)
	if err != nil {
		t.Fatalf("LoadScope with no local file should succeed: %v", err)
	}
	if !sc.IsManaged("brew") {
		t.Error("brew should remain managed without a local file")
	}
}

func TestLoadScope_MissingSharedErrors(t *testing.T) {
	t.Parallel()
	repo := t.TempDir() // no ferry.toml
	if _, err := LoadScope(repo); err == nil {
		t.Error("missing ferry.toml should be a clear error")
	}
}

func TestLoadScope_MalformedSharedErrors(t *testing.T) {
	t.Parallel()
	repo := writeRepoScope(t, "[manage\nbrew = true\n", "")
	if _, err := LoadScope(repo); err == nil {
		t.Error("malformed ferry.toml should error, not panic")
	}
}

func TestLoadScope_WrongTypeErrors(t *testing.T) {
	t.Parallel()
	// brew must be a bool; a string is a boundary error.
	repo := writeRepoScope(t, `[manage]
brew = "yes"
`, "")
	if _, err := LoadScope(repo); err == nil {
		t.Error("brew = \"yes\" (non-bool) should error")
	}
}

func TestLoadScope_DotfilesWrongTypeErrors(t *testing.T) {
	t.Parallel()
	repo := writeRepoScope(t, `[manage]
dotfiles = true
`, "")
	if _, err := LoadScope(repo); err == nil {
		t.Error("dotfiles = true (non-list) should error")
	}
}

func TestScope_IsManagedBidirectional(t *testing.T) {
	t.Parallel()
	// One Scope; the same IsManaged answer must serve apply AND capture.
	repo := writeRepoScope(t, `[manage]
dotfiles = [".zshrc"]
fonts = false
`, "")
	sc, err := LoadScope(repo)
	if err != nil {
		t.Fatalf("LoadScope: %v", err)
	}
	// Simulate both directions consulting the one predicate.
	for _, direction := range []string{"apply", "capture"} {
		if !sc.IsManaged("dotfiles") {
			t.Errorf("[%s] dotfiles must be managed", direction)
		}
		if sc.IsManaged("fonts") {
			t.Errorf("[%s] fonts=false must be out of scope", direction)
		}
	}
}

func TestScope_Domains(t *testing.T) {
	t.Parallel()
	repo := writeRepoScope(t, `[manage]
dotfiles = [".zshrc"]
brew = true
iterm2 = true
fonts = false
`, "")
	sc, err := LoadScope(repo)
	if err != nil {
		t.Fatalf("LoadScope: %v", err)
	}
	got := sc.Domains()
	sort.Strings(got)
	want := []string{"brew", "iterm2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Domains: got %v want %v (only present-and-true scalars)", got, want)
	}
}
