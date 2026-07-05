package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeAgentsRepo writes a repo dir holding a ferry.toml (and optionally a
// ferry.local.toml) with the given bodies.
func writeAgentsRepo(t *testing.T, shared, local string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, SharedManifestName), []byte(shared), 0o644); err != nil {
		t.Fatal(err)
	}
	if local != "" {
		if err := os.WriteFile(filepath.Join(repo, LocalManifestName), []byte(local), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func TestLoadAgents(t *testing.T) {
	tests := []struct {
		name   string
		shared string
		local  string
		want   AgentsConfig
	}{
		{
			name:   "no agents table at all",
			shared: "[manage]\nagents = true\n",
			want:   AgentsConfig{Harness: map[string]AgentsHarness{}, Asset: map[string]AgentsAsset{}},
		},
		{
			name:   "devtree and harness selection",
			shared: "[agents]\ndevtree = \"Workspace\"\nharnesses = [\"claude\", \"codex\"]\n",
			want: AgentsConfig{
				Devtree:      "Workspace",
				Harnesses:    []string{"claude", "codex"},
				HarnessesSet: true,
				Harness:      map[string]AgentsHarness{},
				Asset:        map[string]AgentsAsset{},
			},
		},
		{
			name:   "user-defined harness",
			shared: "[agents.harness.myharness]\ntarget = \".config/myharness/RULES.md\"\nsource = \"combined\"\n",
			want: AgentsConfig{
				Harness: map[string]AgentsHarness{
					"myharness": {Target: ".config/myharness/RULES.md", Source: "combined"},
				},
				Asset: map[string]AgentsAsset{},
			},
		},
		{
			name:   "local overrides devtree and merges harnesses per name",
			shared: "[agents]\ndevtree = \"Shared\"\n\n[agents.harness.a]\ntarget = \".a/A.md\"\n\n[agents.harness.b]\ntarget = \".b/B.md\"\n",
			local:  "[agents]\ndevtree = \"Local\"\n\n[agents.harness.b]\ntarget = \".b/LOCAL.md\"\nsource = \"coding\"\n",
			want: AgentsConfig{
				Devtree: "Local",
				Harness: map[string]AgentsHarness{
					"a": {Target: ".a/A.md"},
					"b": {Target: ".b/LOCAL.md", Source: "coding"},
				},
				Asset: map[string]AgentsAsset{},
			},
		},
		{
			name:   "asset mapping and selection",
			shared: "[agents]\nassets = [\"hooks\", \"githooks\"]\n\n[agents.asset.githooks]\nsource = \"githooks\"\ntarget = \".githooks\"\n",
			want: AgentsConfig{
				Harness:   map[string]AgentsHarness{},
				Assets:    []string{"hooks", "githooks"},
				AssetsSet: true,
				Asset: map[string]AgentsAsset{
					"githooks": {Source: "githooks", Target: ".githooks"},
				},
			},
		},
		{
			name:   "local asset declarations merge per name",
			shared: "[agents.asset.githooks]\nsource = \"githooks\"\ntarget = \".githooks\"\n",
			local:  "[agents.asset.githooks]\ntarget = \".hooks-local\"\n\n[agents.asset.extra]\nsource = \"extra\"\ntarget = \".extra\"\n",
			want: AgentsConfig{
				Harness: map[string]AgentsHarness{},
				Asset: map[string]AgentsAsset{
					"githooks": {Target: ".hooks-local"},
					"extra":    {Source: "extra", Target: ".extra"},
				},
			},
		},
		{
			name:   "local explicit empty harness list wins over shared",
			shared: "[agents]\nharnesses = [\"claude\"]\n",
			local:  "[agents]\nharnesses = []\n",
			want: AgentsConfig{
				// The empty selection survives as set-but-empty (nil slice —
				// mergeAgents copies with append, so zero elements stay nil).
				Harnesses:    nil,
				HarnessesSet: true,
				Harness:      map[string]AgentsHarness{},
				Asset:        map[string]AgentsAsset{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := writeAgentsRepo(t, tt.shared, tt.local)
			got, err := LoadAgents(repo)
			if err != nil {
				t.Fatalf("LoadAgents: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LoadAgents = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadAgentsErrors(t *testing.T) {
	tests := []struct {
		name    string
		shared  string
		wantSub string
	}{
		{
			name:    "unknown key",
			shared:  "[agents]\ntypo = true\n",
			wantSub: "not a recognised setting",
		},
		{
			name:    "wrong-typed devtree",
			shared:  "[agents]\ndevtree = 42\n",
			wantSub: "devtree must be a string",
		},
		{
			name:    "absolute devtree",
			shared:  "[agents]\ndevtree = \"/etc\"\n",
			wantSub: "must be relative to $HOME",
		},
		{
			name:    "devtree climbing out of home",
			shared:  "[agents]\ndevtree = \"../elsewhere\"\n",
			wantSub: "must stay within $HOME",
		},
		{
			name:    "absolute harness target",
			shared:  "[agents.harness.x]\ntarget = \"/etc/x\"\n",
			wantSub: "must be relative to $HOME",
		},
		{
			name:    "invalid harness source",
			shared:  "[agents.harness.x]\ntarget = \".x/X.md\"\nsource = \"everything\"\n",
			wantSub: "must be one of general, coding, combined",
		},
		{
			name:    "absolute asset target",
			shared:  "[agents.asset.x]\nsource = \"x\"\ntarget = \"/etc/x\"\n",
			wantSub: "must be relative to $HOME",
		},
		{
			name:    "absolute asset source",
			shared:  "[agents.asset.x]\nsource = \"/etc/x\"\ntarget = \".x\"\n",
			wantSub: "must be a directory relative to the repo's agents/ area",
		},
		{
			name:    "asset source climbing out of the agents area",
			shared:  "[agents.asset.x]\nsource = \"../dotfiles\"\ntarget = \".x\"\n",
			wantSub: "must stay within the repo's agents/ area",
		},
		{
			name:    "wrong-typed assets list",
			shared:  "[agents]\nassets = true\n",
			wantSub: "assets must be a list of strings",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := writeAgentsRepo(t, tt.shared, "")
			_, err := LoadAgents(repo)
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("LoadAgents error = %v, want substring %q", err, tt.wantSub)
			}
		})
	}
}
