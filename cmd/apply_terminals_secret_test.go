package cmd

// fn-4.5 / F24: the config-file terminal domain must render {{ferry.secret ...}}
// placeholders EXACTLY like dotfiles — substitute the real value when the secret
// is present (and flag the target secret-routed so its plaintext is kept out of
// the last-applied snapshot and written 0600), and render-or-SKIP the whole
// target when the referenced secret is MISSING (never deploy the literal
// placeholder to the terminal's live config).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/secret"
)

// mkTermRepoFile writes body under repo/rel, creating parents.
func mkTermRepoFile(t *testing.T, repo, rel, body string) {
	t.Helper()
	p := filepath.Join(repo, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPlanTerminalsRendersSecretPlaceholder(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkTermRepoFile(t, repo, "terminals/wezterm.lua",
		`return { token = "{{ferry.secret "wezterm.token"}}" }`+"\n")

	store := secret.OpenAt(t.TempDir())
	if err := store.Put("wezterm.token", "REAL-TOKEN-VALUE"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	lastApplied, err := dotfile.OpenStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("open last-applied: %v", err)
	}

	items, _, err := planTerminals(&cmdContext{RepoPath: repo}, home, store, lastApplied)
	if err != nil {
		t.Fatalf("planTerminals: %v", err)
	}

	var wez *planItem
	for i := range items {
		if strings.Contains(items[i].domain, "wezterm") {
			wez = &items[i]
		}
	}
	if wez == nil {
		t.Fatalf("no wezterm terminal item planned; items=%v", items)
	}
	if wez.skip {
		t.Fatalf("wezterm item skipped, want rendered (secret is present)")
	}
	if !wez.secretRouted {
		t.Fatalf("wezterm item secretRouted=false, want true (source referenced the secret store)")
	}
	if strings.Contains(string(wez.content), "{{ferry.secret") {
		t.Fatalf("terminal content still carries the literal placeholder: %q", wez.content)
	}
	if !strings.Contains(string(wez.content), "REAL-TOKEN-VALUE") {
		t.Fatalf("terminal content = %q, want the substituted secret value", wez.content)
	}
}

func TestPlanTerminalsSkipsMissingSecret(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkTermRepoFile(t, repo, "terminals/wezterm.lua",
		`return { token = "{{ferry.secret "wezterm.token"}}" }`+"\n")

	store := secret.OpenAt(t.TempDir()) // empty store: the referenced secret is MISSING
	lastApplied, err := dotfile.OpenStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("open last-applied: %v", err)
	}

	items, _, err := planTerminals(&cmdContext{RepoPath: repo}, home, store, lastApplied)
	if err != nil {
		t.Fatalf("planTerminals: %v", err)
	}

	var wez *planItem
	for i := range items {
		if strings.Contains(items[i].domain, "wezterm") {
			wez = &items[i]
		}
	}
	if wez == nil {
		t.Fatalf("no wezterm terminal item planned; items=%v", items)
	}
	if !wez.skip {
		t.Fatalf("wezterm item skip=false, want true (referenced secret missing -> render-or-SKIP)")
	}
	if len(wez.missing) == 0 || !strings.Contains(strings.Join(wez.missing, ","), "wezterm.token") {
		t.Fatalf("missing refs = %v, want to name wezterm.token", wez.missing)
	}
	if strings.Contains(string(wez.content), "{{ferry.secret") {
		t.Fatalf("skipped terminal must carry no literal-placeholder content, got %q", wez.content)
	}
}
