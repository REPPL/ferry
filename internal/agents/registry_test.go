package agents

import (
	"reflect"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/config"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.AgentsConfig
		want    []Harness
		wantErr string
	}{
		{
			name: "zero config yields every built-in in registry order",
			cfg:  config.AgentsConfig{},
			want: Builtins(),
		},
		{
			name: "selection restricts and orders",
			cfg: config.AgentsConfig{
				Harnesses:    []string{"gemini", "claude"},
				HarnessesSet: true,
			},
			want: []Harness{
				{Name: "gemini", Target: ".gemini/GEMINI.md", Source: SourceCombined},
				{Name: "claude", Target: ".claude/CLAUDE.md", Source: SourceGeneral},
			},
		},
		{
			name: "explicit empty selection deploys no harness",
			cfg: config.AgentsConfig{
				Harnesses:    []string{},
				HarnessesSet: true,
			},
			want: []Harness{},
		},
		{
			name: "user-defined harness appends with combined default",
			cfg: config.AgentsConfig{
				Harness: map[string]config.AgentsHarness{
					"myharness": {Target: ".config/myharness/RULES.md"},
				},
			},
			want: append(Builtins(), Harness{
				Name: "myharness", Target: ".config/myharness/RULES.md", Source: SourceCombined,
			}),
		},
		{
			name: "override of a built-in changes only the set fields",
			cfg: config.AgentsConfig{
				Harnesses:    []string{"codex"},
				HarnessesSet: true,
				Harness: map[string]config.AgentsHarness{
					"codex": {Source: "general"},
				},
			},
			want: []Harness{
				{Name: "codex", Target: ".codex/AGENTS.md", Source: SourceGeneral},
			},
		},
		{
			name: "new harness without a target is a config error",
			cfg: config.AgentsConfig{
				Harness: map[string]config.AgentsHarness{
					"broken": {Source: "combined"},
				},
			},
			wantErr: "target is required",
		},
		{
			name: "selection naming an unknown harness is a config error",
			cfg: config.AgentsConfig{
				Harnesses:    []string{"claude", "nonesuch"},
				HarnessesSet: true,
			},
			wantErr: `names "nonesuch"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.cfg)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Resolve error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Resolve = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveAssets(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.AgentsConfig
		want    []AssetMapping
		wantErr string
	}{
		{
			name: "zero config yields every built-in in registry order",
			cfg:  config.AgentsConfig{},
			want: BuiltinAssets(),
		},
		{
			name: "user-defined mapping appends after the built-ins",
			cfg: config.AgentsConfig{
				Asset: map[string]config.AgentsAsset{
					"githooks": {Source: "githooks", Target: ".githooks"},
				},
			},
			want: append(BuiltinAssets(), AssetMapping{
				Name: "githooks", Source: "githooks", Target: ".githooks",
			}),
		},
		{
			name: "selection restricts and orders",
			cfg: config.AgentsConfig{
				Assets:    []string{"hooks", "githooks"},
				AssetsSet: true,
				Asset: map[string]config.AgentsAsset{
					"githooks": {Source: "githooks", Target: ".githooks"},
				},
			},
			want: []AssetMapping{
				{Name: "hooks", Source: "hooks", Target: ".claude/hooks"},
				{Name: "githooks", Source: "githooks", Target: ".githooks"},
			},
		},
		{
			name: "explicit empty selection deploys no assets",
			cfg: config.AgentsConfig{
				Assets:    []string{},
				AssetsSet: true,
			},
			want: []AssetMapping{},
		},
		{
			name: "override of a built-in changes only the set fields",
			cfg: config.AgentsConfig{
				Assets:    []string{"hooks"},
				AssetsSet: true,
				Asset: map[string]config.AgentsAsset{
					"hooks": {Target: ".config/claude/hooks"},
				},
			},
			want: []AssetMapping{
				{Name: "hooks", Source: "hooks", Target: ".config/claude/hooks"},
			},
		},
		{
			name: "new mapping without source and target is a config error",
			cfg: config.AgentsConfig{
				Asset: map[string]config.AgentsAsset{
					"broken": {Source: "broken"},
				},
			},
			wantErr: "source and target are both required",
		},
		{
			name: "selection naming an unknown mapping is a config error",
			cfg: config.AgentsConfig{
				Assets:    []string{"nonesuch"},
				AssetsSet: true,
			},
			wantErr: `names "nonesuch"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveAssets(tt.cfg)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ResolveAssets error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveAssets: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ResolveAssets = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBuiltinAssetsAreValid(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range BuiltinAssets() {
		if b.Name == "" || b.Source == "" || b.Target == "" {
			t.Errorf("built-in asset %+v has an empty field", b)
		}
		if seen[b.Name] {
			t.Errorf("built-in asset name %q is duplicated", b.Name)
		}
		seen[b.Name] = true
	}
}

func TestBuiltinsAreValid(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range Builtins() {
		if b.Name == "" || b.Target == "" {
			t.Errorf("built-in %+v has an empty name or target", b)
		}
		if seen[b.Name] {
			t.Errorf("built-in name %q is duplicated", b.Name)
		}
		seen[b.Name] = true
		switch b.Source {
		case SourceGeneral, SourceCoding, SourceCombined:
		default:
			t.Errorf("built-in %q has invalid source %q", b.Name, b.Source)
		}
	}
}
