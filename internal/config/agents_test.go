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
			want:   AgentsConfig{Harness: map[string]AgentsHarness{}},
		},
		{
			name:   "devtree and harness selection",
			shared: "[agents]\ndevtree = \"Workspace\"\nharnesses = [\"claude\", \"codex\"]\n",
			want: AgentsConfig{
				Devtree:      "Workspace",
				Harnesses:    []string{"claude", "codex"},
				HarnessesSet: true,
				Harness:      map[string]AgentsHarness{},
			},
		},
		{
			name:   "user-defined harness",
			shared: "[agents.harness.myharness]\ntarget = \".config/myharness/RULES.md\"\nsource = \"combined\"\n",
			want: AgentsConfig{
				Harness: map[string]AgentsHarness{
					"myharness": {Target: ".config/myharness/RULES.md", Source: "combined"},
				},
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
