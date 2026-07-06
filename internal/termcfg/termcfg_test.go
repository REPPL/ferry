package termcfg

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/REPPL/ferry/internal/config"
)

func TestResolve_defaultsAndSelection(t *testing.T) {
	got, err := Resolve(config.TerminalsConfig{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(got, Builtins()) {
		t.Errorf("zero config = %v, want the built-in registry order", got)
	}

	got, err = Resolve(config.TerminalsConfig{Enabled: []string{"wezterm", "kitty"}, EnabledSet: true})
	if err != nil {
		t.Fatalf("Resolve selection: %v", err)
	}
	want := []Terminal{
		{Name: "wezterm", Source: "wezterm.lua", Target: ".wezterm.lua"},
		{Name: "kitty", Source: "kitty", Target: ".config/kitty"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selection = %v, want %v", got, want)
	}
}

func TestResolve_overrideAndAddition(t *testing.T) {
	got, err := Resolve(config.TerminalsConfig{
		Terminal: map[string]config.TerminalSpec{
			"alacritty": {Target: ".config/alacritty-custom"},
			"foot":      {Source: "foot", Target: ".config/foot"},
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	byName := map[string]Terminal{}
	for _, term := range got {
		byName[term.Name] = term
	}
	if byName["alacritty"].Target != ".config/alacritty-custom" || byName["alacritty"].Source != "alacritty" {
		t.Errorf("alacritty override = %+v", byName["alacritty"])
	}
	if byName["foot"].Source != "foot" || byName["foot"].Target != ".config/foot" {
		t.Errorf("foot addition = %+v", byName["foot"])
	}
}

func TestResolve_errors(t *testing.T) {
	if _, err := Resolve(config.TerminalsConfig{
		Terminal: map[string]config.TerminalSpec{"foot": {Source: "foot"}},
	}); err == nil {
		t.Error("new terminal missing target: want error")
	}
	if _, err := Resolve(config.TerminalsConfig{
		Enabled: []string{"ghostty"}, EnabledSet: true,
	}); err == nil {
		t.Error("selection naming unknown terminal: want error")
	}
}

// mkfile writes body to repoRoot/rel, creating parents.
func mkfile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func planKeys(items []Item) []string {
	keys := make([]string, len(items))
	for i, it := range items {
		keys[i] = it.Key
	}
	sort.Strings(keys)
	return keys
}

func TestPlan_directoryAndSingleFile(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkfile(t, repo, "terminals/alacritty/alacritty.toml", "shared-alacritty")
	mkfile(t, repo, "terminals/alacritty/themes/dark.toml", "shared-dark")
	mkfile(t, repo, "terminals/wezterm.lua", "shared-wezterm")

	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: home, Config: config.TerminalsConfig{}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}

	got := planKeys(items)
	want := []string{
		"terminals/alacritty/alacritty.toml",
		"terminals/alacritty/themes/dark.toml",
		"terminals/wezterm",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v (kitty has no repo source, so deploys nothing)", got, want)
	}

	byKey := map[string]Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	// The single-file terminal lands at its target directly.
	if h := byKey["terminals/wezterm"].Target.Home; h != filepath.Join(home, ".wezterm.lua") {
		t.Errorf("wezterm home = %s, want ~/.wezterm.lua", h)
	}
	// A directory file lands under its target directory, preserving the relpath.
	if h := byKey["terminals/alacritty/themes/dark.toml"].Target.Home; h != filepath.Join(home, ".config/alacritty/themes/dark.toml") {
		t.Errorf("dark.toml home = %s", h)
	}
	if string(byKey["terminals/wezterm"].Content) != "shared-wezterm" {
		t.Errorf("wezterm content = %q", byKey["terminals/wezterm"].Content)
	}
}

func TestPlan_localOverlayWins(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkfile(t, repo, "terminals/alacritty/alacritty.toml", "shared")
	mkfile(t, repo, "terminals/alacritty/themes/dark.toml", "shared-theme")
	// Per-machine override of just the theme file.
	mkfile(t, repo, "local/terminals/alacritty/themes/dark.toml", "MACHINE-theme")
	// Per-machine override of a single-file terminal.
	mkfile(t, repo, "terminals/wezterm.lua", "shared-wez")
	mkfile(t, repo, "local/terminals/wezterm.lua", "MACHINE-wez")

	items, _, err := Plan(PlanInput{RepoRoot: repo, Home: home, Config: config.TerminalsConfig{}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	byKey := map[string]Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	if got := string(byKey["terminals/alacritty/themes/dark.toml"].Content); got != "MACHINE-theme" {
		t.Errorf("theme content = %q, want the local overlay to win", got)
	}
	if got := string(byKey["terminals/alacritty/alacritty.toml"].Content); got != "shared" {
		t.Errorf("non-overridden file = %q, want shared", got)
	}
	if got := string(byKey["terminals/wezterm"].Content); got != "MACHINE-wez" {
		t.Errorf("wezterm content = %q, want the local overlay to win", got)
	}
}

func TestPlan_absentSourceDeploysNothing(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	// No terminals/ dir at all.
	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: home, Config: config.TerminalsConfig{}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 0 || len(warnings) != 0 {
		t.Errorf("items=%v warnings=%v, want both empty for a repo with no terminal configs", items, warnings)
	}
}

func TestPlan_refusesSSHTarget(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkfile(t, repo, "terminals/evil/config", "x")

	items, warnings, err := Plan(PlanInput{
		RepoRoot: repo, Home: home,
		Config: config.TerminalsConfig{
			Terminal: map[string]config.TerminalSpec{"evil": {Source: "evil", Target: ".ssh/config"}},
			Enabled:  []string{"evil"}, EnabledSet: true,
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("items = %v, want none: a ~/.ssh target must be refused, never deployed", items)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one refusal", warnings)
	}
}

func TestPlan_enabledRestricts(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkfile(t, repo, "terminals/alacritty/alacritty.toml", "a")
	mkfile(t, repo, "terminals/kitty/kitty.conf", "k")

	items, _, err := Plan(PlanInput{
		RepoRoot: repo, Home: home,
		Config: config.TerminalsConfig{Enabled: []string{"kitty"}, EnabledSet: true},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := planKeys(items); !reflect.DeepEqual(got, []string{"terminals/kitty/kitty.conf"}) {
		t.Errorf("keys = %v, want only kitty (alacritty not enabled)", got)
	}
}
