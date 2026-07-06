package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeRepoFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLoadTerminals_zeroWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, SharedManifestName, "[manage]\nterminals = true\n")

	cfg, err := LoadTerminals(dir)
	if err != nil {
		t.Fatalf("LoadTerminals: %v", err)
	}
	if cfg.EnabledSet {
		t.Errorf("EnabledSet = true, want false for a repo with no [terminals] table")
	}
	if len(cfg.Terminal) != 0 {
		t.Errorf("Terminal = %v, want empty", cfg.Terminal)
	}
}

func TestLoadTerminals_selectionAndOverride(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, SharedManifestName, `
[terminals]
enabled = ["alacritty", "foot"]

[terminals.terminal.foot]
source = "foot"
target = ".config/foot"

[terminals.terminal.alacritty]
target = ".config/alacritty-custom"
`)

	cfg, err := LoadTerminals(dir)
	if err != nil {
		t.Fatalf("LoadTerminals: %v", err)
	}
	if !cfg.EnabledSet || !reflect.DeepEqual(cfg.Enabled, []string{"alacritty", "foot"}) {
		t.Errorf("Enabled = %v (set=%v), want [alacritty foot] set", cfg.Enabled, cfg.EnabledSet)
	}
	if got := cfg.Terminal["foot"]; got.Source != "foot" || got.Target != ".config/foot" {
		t.Errorf("foot = %+v, want {foot .config/foot}", got)
	}
	if got := cfg.Terminal["alacritty"]; got.Target != ".config/alacritty-custom" {
		t.Errorf("alacritty override = %+v, want target .config/alacritty-custom", got)
	}
}

func TestLoadTerminals_localWinsPerField(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, SharedManifestName, `
[terminals]
enabled = ["alacritty"]

[terminals.terminal.foot]
source = "foot"
target = ".config/foot"
`)
	writeRepoFile(t, dir, LocalManifestName, `
[terminals]
enabled = ["kitty"]

[terminals.terminal.foot]
target = ".config/foot-local"
`)

	cfg, err := LoadTerminals(dir)
	if err != nil {
		t.Fatalf("LoadTerminals: %v", err)
	}
	if !reflect.DeepEqual(cfg.Enabled, []string{"kitty"}) {
		t.Errorf("Enabled = %v, want [kitty] (local replaces the list)", cfg.Enabled)
	}
	// local overrode only target; the shared source survives (per-field merge).
	if got := cfg.Terminal["foot"]; got.Source != "foot" || got.Target != ".config/foot-local" {
		t.Errorf("foot merged = %+v, want {foot .config/foot-local}", got)
	}
}

func TestLoadTerminals_rejectsBadPaths(t *testing.T) {
	cases := map[string]string{
		"absolute source": "[terminals.terminal.x]\nsource = \"/etc/x\"\ntarget = \".config/x\"\n",
		"escaping target": "[terminals.terminal.x]\nsource = \"x\"\ntarget = \"../x\"\n",
		"unknown key":     "[terminals]\nnope = true\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeRepoFile(t, dir, SharedManifestName, body)
			if _, err := LoadTerminals(dir); err == nil {
				t.Fatalf("LoadTerminals accepted %q, want error", body)
			}
		})
	}
}
