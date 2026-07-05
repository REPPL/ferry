package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/config"
)

// writeSST populates a fake config repo's agents/ area. files maps a path
// relative to agents/ to its content; exec marks paths to make executable.
func writeSST(t *testing.T, files map[string]string, exec ...string) string {
	t.Helper()
	repo := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(repo, RepoSubdir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range exec {
		if err := os.Chmod(filepath.Join(repo, RepoSubdir, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// itemByKey indexes a plan by item key.
func itemByKey(items []Item) map[string]Item {
	m := map[string]Item{}
	for _, it := range items {
		m[it.Key] = it
	}
	return m
}

func TestPlanExpandsHarnessesDevtreeAndAssets(t *testing.T) {
	repo := writeSST(t, map[string]string{
		"general.md":           "GENERAL\n",
		"coding.md":            "CODING\n",
		"skills/demo/SKILL.md": "skill\n",
		"hooks/guard.sh":       "#!/bin/sh\n",
		"agents/reviewer.md":   "reviewer\n",
	}, "hooks/guard.sh")
	home := t.TempDir()

	items, warnings, err := Plan(PlanInput{
		RepoRoot: repo,
		Home:     home,
		Config:   config.AgentsConfig{Devtree: "Workspace"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}

	byKey := itemByKey(items)
	combined := string(RenderCombined([]byte("GENERAL\n"), []byte("CODING\n")))
	tests := []struct {
		key     string
		relDest string
		content string
		exec    bool
	}{
		{"agents/claude", ".claude/CLAUDE.md", "GENERAL\n", false},
		{"agents/codex", ".codex/AGENTS.md", combined, false},
		{"agents/opencode", ".config/opencode/AGENTS.md", combined, false},
		{"agents/companion", ".companion/COMPANION.md", combined, false},
		{"agents/gemini", ".gemini/GEMINI.md", combined, false},
		{"agents/devtree", "Workspace/CLAUDE.md", "CODING\n", false},
		{"agents/skills/demo/SKILL.md", ".claude/skills/demo/SKILL.md", "skill\n", false},
		{"agents/hooks/guard.sh", ".claude/hooks/guard.sh", "#!/bin/sh\n", true},
		{"agents/agents/reviewer.md", ".claude/agents/reviewer.md", "reviewer\n", false},
	}
	if len(items) != len(tests) {
		t.Errorf("Plan produced %d items, want %d: %+v", len(items), len(tests), keysOf(byKey))
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			it, ok := byKey[tt.key]
			if !ok {
				t.Fatalf("item %q missing from plan", tt.key)
			}
			if want := filepath.Join(home, tt.relDest); it.Target.Home != want {
				t.Errorf("Target.Home = %q, want %q", it.Target.Home, want)
			}
			if string(it.Content) != tt.content {
				t.Errorf("Content = %q, want %q", it.Content, tt.content)
			}
			if it.Exec != tt.exec {
				t.Errorf("Exec = %v, want %v", it.Exec, tt.exec)
			}
			if it.Target.Name != tt.key {
				t.Errorf("Target.Name = %q, want %q (the last-applied key)", it.Target.Name, tt.key)
			}
		})
	}
}

func keysOf(m map[string]Item) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestPlanWithoutDevtreeDeploysNoWorkspaceFile(t *testing.T) {
	repo := writeSST(t, map[string]string{"general.md": "G\n", "coding.md": "C\n"})
	items, _, err := Plan(PlanInput{RepoRoot: repo, Home: t.TempDir(), Config: config.AgentsConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := itemByKey(items)["agents/devtree"]; ok {
		t.Error("devtree item planned although devtree is unset")
	}
}

func TestPlanMissingSourcesWarnsAndSkips(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		wantKeys  []string
		wantWarn  string
		warnCount int
	}{
		{
			name:      "coding missing: general-only harness still deploys, combined+devtree skip",
			files:     map[string]string{"general.md": "G\n"},
			wantKeys:  []string{"agents/claude"},
			wantWarn:  "agents/coding.md is missing",
			warnCount: 1,
		},
		{
			name:      "general missing: nothing but a warning",
			files:     map[string]string{"coding.md": "C\n"},
			wantKeys:  []string{"agents/devtree"},
			wantWarn:  "agents/general.md is missing",
			warnCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := writeSST(t, tt.files)
			items, warnings, err := Plan(PlanInput{
				RepoRoot: repo, Home: t.TempDir(),
				Config: config.AgentsConfig{Devtree: "Workspace"},
			})
			if err != nil {
				t.Fatal(err)
			}
			byKey := itemByKey(items)
			if len(items) != len(tt.wantKeys) {
				t.Errorf("planned %v, want exactly %v", keysOf(byKey), tt.wantKeys)
			}
			for _, k := range tt.wantKeys {
				if _, ok := byKey[k]; !ok {
					t.Errorf("item %q missing", k)
				}
			}
			if len(warnings) != tt.warnCount {
				t.Errorf("warnings = %v, want %d warning(s)", warnings, tt.warnCount)
			}
			if len(warnings) > 0 && !strings.Contains(warnings[0], tt.wantWarn) {
				t.Errorf("warning %q does not mention %q", warnings[0], tt.wantWarn)
			}
		})
	}
}

func TestPlanRefusesUnsafeTargets(t *testing.T) {
	repo := writeSST(t, map[string]string{"general.md": "G\n", "coding.md": "C\n"})
	home := t.TempDir()

	tests := []struct {
		name     string
		cfg      config.AgentsConfig
		wantWarn string
	}{
		{
			name: "harness target under ~/.ssh",
			cfg: config.AgentsConfig{
				Harnesses:    []string{"evil"},
				HarnessesSet: true,
				Harness:      map[string]config.AgentsHarness{"evil": {Target: ".ssh/RULES.md", Source: "general"}},
			},
			wantWarn: "never manages paths under ~/.ssh",
		},
		{
			name: "harness target escaping $HOME",
			cfg: config.AgentsConfig{
				Harnesses:    []string{"evil"},
				HarnessesSet: true,
				Harness:      map[string]config.AgentsHarness{"evil": {Target: "../outside/RULES.md", Source: "general"}},
			},
			wantWarn: "escapes $HOME",
		},
		{
			name:     "devtree resolving to ~/.ssh",
			cfg:      config.AgentsConfig{Devtree: ".ssh", Harnesses: []string{}, HarnessesSet: true},
			wantWarn: "never manages paths under ~/.ssh",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: home, Config: tt.cfg})
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 0 {
				t.Errorf("unsafe target planned anyway: %+v", items)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], tt.wantWarn) {
				t.Errorf("warnings = %v, want one containing %q", warnings, tt.wantWarn)
			}
		})
	}
}

// TestPlanRefusesCollisions pins the duplicate detection: a plan in which two
// targets share a store key or a destination path is refused with an error
// naming both parties — never deployed with one record/file silently fighting
// the other.
func TestPlanRefusesCollisions(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.AgentsConfig
		wantSub string
	}{
		{
			name: "user harness named devtree collides on the store key",
			cfg: config.AgentsConfig{
				Devtree: "Workspace",
				Harness: map[string]config.AgentsHarness{
					"devtree": {Target: ".devtree/RULES.md", Source: "coding"},
				},
			},
			wantSub: `store key "agents/devtree"`,
		},
		{
			name: "devtree colliding with the claude harness destination",
			cfg: config.AgentsConfig{
				Devtree: ".claude",
			},
			wantSub: "same destination ~/.claude/CLAUDE.md",
		},
		{
			name: "two harnesses sharing one destination",
			cfg: config.AgentsConfig{
				Harness: map[string]config.AgentsHarness{
					"other": {Target: ".codex/AGENTS.md", Source: "general"},
				},
			},
			wantSub: "same destination ~/.codex/AGENTS.md",
		},
		{
			name: "asset file colliding with a harness destination",
			cfg: config.AgentsConfig{
				Harnesses:    []string{"clash"},
				HarnessesSet: true,
				Harness: map[string]config.AgentsHarness{
					"clash": {Target: ".githooks/pre-commit", Source: "general"},
				},
				Asset: map[string]config.AgentsAsset{
					"githooks": {Source: "githooks", Target: ".githooks"},
				},
			},
			wantSub: "same destination ~/.githooks/pre-commit",
		},
		{
			name: "two asset mappings sharing one destination file",
			cfg: config.AgentsConfig{
				Harnesses:    []string{},
				HarnessesSet: true,
				Asset: map[string]config.AgentsAsset{
					"githooks": {Source: "githooks", Target: ".githooks"},
					"twinhook": {Source: "githooks", Target: ".githooks"},
				},
			},
			wantSub: "same destination ~/.githooks/pre-commit",
		},
		{
			// A harness file target ".githooks" and an asset that deploys a file
			// UNDER ".githooks/" cannot both exist: one wants a file where the
			// other needs a directory. Refuse at plan time, not as a mid-apply
			// ENOTDIR.
			name: "harness file target collides with an asset directory prefix",
			cfg: config.AgentsConfig{
				Harnesses:    []string{"clash"},
				HarnessesSet: true,
				Harness: map[string]config.AgentsHarness{
					"clash": {Target: ".githooks", Source: "general"},
				},
				Asset: map[string]config.AgentsAsset{
					"githooks": {Source: "githooks", Target: ".githooks"},
				},
			},
			wantSub: "~/.githooks",
		},
	}
	repo := writeSST(t, map[string]string{
		"general.md":          "G\n",
		"coding.md":           "C\n",
		"githooks/pre-commit": "#!/bin/sh\n",
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Plan(PlanInput{RepoRoot: repo, Home: t.TempDir(), Config: tt.cfg})
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("Plan error = %v, want substring %q", err, tt.wantSub)
			}
		})
	}
}

// TestPlanCustomAssetMapping pins the data-driven asset registry end to end
// at the planner: a user-declared [agents.asset.githooks] mapping deploys the
// repo's agents/githooks/ dispatchers to ~/.githooks/ with their executable
// bits, keyed under the mapping's own namespace, alongside the built-ins.
func TestPlanCustomAssetMapping(t *testing.T) {
	repo := writeSST(t, map[string]string{
		"general.md":            "G\n",
		"coding.md":             "C\n",
		"hooks/guard.sh":        "#!/bin/sh\n",
		"githooks/pre-commit":   "#!/bin/sh\ndispatch pre-commit\n",
		"githooks/commit-msg":   "#!/bin/sh\ndispatch commit-msg\n",
		"githooks/lib/common.d": "shared dispatcher data\n",
	}, "hooks/guard.sh", "githooks/pre-commit", "githooks/commit-msg")
	home := t.TempDir()

	items, warnings, err := Plan(PlanInput{
		RepoRoot: repo, Home: home,
		Config: config.AgentsConfig{
			Harnesses:    []string{},
			HarnessesSet: true,
			Asset: map[string]config.AgentsAsset{
				"githooks": {Source: "githooks", Target: ".githooks"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}

	byKey := itemByKey(items)
	tests := []struct {
		key     string
		relDest string
		exec    bool
	}{
		{"agents/hooks/guard.sh", ".claude/hooks/guard.sh", true},
		{"agents/githooks/pre-commit", ".githooks/pre-commit", true},
		{"agents/githooks/commit-msg", ".githooks/commit-msg", true},
		{"agents/githooks/lib/common.d", ".githooks/lib/common.d", false},
	}
	if len(items) != len(tests) {
		t.Errorf("planned %d items (%v), want %d", len(items), keysOf(byKey), len(tests))
	}
	for _, tt := range tests {
		it, ok := byKey[tt.key]
		if !ok {
			t.Errorf("item %q missing from plan", tt.key)
			continue
		}
		if want := filepath.Join(home, tt.relDest); it.Target.Home != want {
			t.Errorf("%s deploys to %q, want %q", tt.key, it.Target.Home, want)
		}
		if it.Exec != tt.exec {
			t.Errorf("%s Exec = %v, want %v", tt.key, it.Exec, tt.exec)
		}
	}
}

// TestPlanAssetSelectionRemovesBuiltins: the `assets` list removes a built-in
// mapping exactly as `harnesses` removes a harness — its tree stops deploying
// without any code change.
func TestPlanAssetSelectionRemovesBuiltins(t *testing.T) {
	repo := writeSST(t, map[string]string{
		"general.md":           "G\n",
		"coding.md":            "C\n",
		"skills/demo/SKILL.md": "s\n",
		"hooks/guard.sh":       "#!/bin/sh\n",
	}, "hooks/guard.sh")
	items, _, err := Plan(PlanInput{
		RepoRoot: repo, Home: t.TempDir(),
		Config: config.AgentsConfig{
			Harnesses:    []string{},
			HarnessesSet: true,
			Assets:       []string{"hooks"},
			AssetsSet:    true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	byKey := itemByKey(items)
	if _, ok := byKey["agents/hooks/guard.sh"]; !ok {
		t.Error("selected hooks mapping did not deploy")
	}
	if _, ok := byKey["agents/skills/demo/SKILL.md"]; ok {
		t.Error("de-selected skills mapping still deployed")
	}
}

func TestPlanRefusesSymlinkedAssets(t *testing.T) {
	repo := writeSST(t, map[string]string{"general.md": "G\n", "coding.md": "C\n"})
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(repo, RepoSubdir, "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(skills, "linked.md")); err != nil {
		t.Fatal(err)
	}

	items, warnings, err := Plan(PlanInput{
		RepoRoot: repo, Home: t.TempDir(),
		Config: config.AgentsConfig{Harnesses: []string{}, HarnessesSet: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("symlinked asset planned anyway: %+v", items)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "symlink not allowed") {
		t.Errorf("warnings = %v, want one symlink refusal", warnings)
	}
}

func TestPlanHonoursGuard(t *testing.T) {
	repo := writeSST(t, map[string]string{"general.md": "G\n", "coding.md": "C\n"})
	guardErr := map[string]bool{}
	_, warnings, err := Plan(PlanInput{
		RepoRoot: repo, Home: t.TempDir(),
		Config: config.AgentsConfig{},
		Guard: func(cand string) (string, error) {
			guardErr[cand] = true
			return cand, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	for _, want := range []string{
		filepath.Join(repo, RepoSubdir, "general.md"),
		filepath.Join(repo, RepoSubdir, "coding.md"),
	} {
		if !guardErr[want] {
			t.Errorf("guard was not consulted for %s", want)
		}
	}
}

// TestEnumerationIsSingleSourced pins the one-enumerator contract: Plan's
// deployed targets must be exactly the shared enumeration's specs (content
// attached, targets resolved) for the same config — including a user-defined
// harness, a devtree, and asset files — so no consumer can drift onto its own
// private enumeration again.
func TestEnumerationIsSingleSourced(t *testing.T) {
	repo := writeSST(t, map[string]string{
		"general.md":           "G\n",
		"coding.md":            "C\n",
		"skills/demo/SKILL.md": "s\n",
		"hooks/guard.sh":       "#!/bin/sh\n",
	}, "hooks/guard.sh")
	home := t.TempDir()
	cfg := config.AgentsConfig{
		Devtree: "Workspace",
		Harness: map[string]config.AgentsHarness{
			"myharness": {Target: ".config/myharness/RULES.md", Source: "coding"},
		},
	}

	items, _, err := Plan(PlanInput{RepoRoot: repo, Home: home, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	planKeys := map[string]string{} // key -> resolved destination
	for _, it := range items {
		planKeys[it.Key] = it.Target.Home
	}

	specs, _, err := enumerateSpecs(repo, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != len(planKeys) {
		t.Fatalf("enumerateSpecs has %d specs, Plan deploys %d targets", len(specs), len(planKeys))
	}
	for _, spec := range specs {
		dest, ok := planKeys[spec.Key]
		if !ok {
			t.Errorf("spec %s not deployed by Plan", spec.Key)
			continue
		}
		if want := filepath.Join(home, spec.Rel); dest != want {
			t.Errorf("spec %s deploys to %s, want %s", spec.Key, dest, want)
		}
	}
}
