package agents

import (
	"fmt"
	"sort"

	"github.com/REPPL/ferry/internal/config"
)

// Source names which rendered instruction content a harness target receives.
type Source string

const (
	// SourceGeneral: the always-applicable rules (agents/general.md) alone —
	// for a harness with its own directory hierarchy that layers coding rules
	// separately (Claude Code).
	SourceGeneral Source = "general"
	// SourceCoding: the coding rules (agents/coding.md) alone — the devtree
	// workspace file uses this so hierarchy-aware harnesses compose it on top
	// of general.
	SourceCoding Source = "coding"
	// SourceCombined: the derived general+coding concatenation — for flat
	// harnesses with no directory hierarchy of their own.
	SourceCombined Source = "combined"
)

// Harness is one deploy destination in the registry: a named tool, the global
// instruction file it reads (relative to $HOME), and which source it receives.
// The registry is DATA — adding or overriding a harness is a manifest edit
// ([agents.harness.<name>]), never a code change.
type Harness struct {
	Name   string
	Target string // home-relative instruction file, e.g. ".codex/AGENTS.md"
	Source Source
}

// Builtins returns the default harness registry: the known coding CLIs and
// the instruction file each reads. These are DEFAULTS, not assumptions — the
// manifest's `harnesses` list trims the set, and `[agents.harness.<name>]`
// overrides a built-in's target/source or adds a new harness entirely.
func Builtins() []Harness {
	return []Harness{
		{Name: "claude", Target: ".claude/CLAUDE.md", Source: SourceGeneral},
		{Name: "codex", Target: ".codex/AGENTS.md", Source: SourceCombined},
		{Name: "opencode", Target: ".config/opencode/AGENTS.md", Source: SourceCombined},
		{Name: "companion", Target: ".companion/COMPANION.md", Source: SourceCombined},
		{Name: "gemini", Target: ".gemini/GEMINI.md", Source: SourceCombined},
	}
}

// Resolve computes the effective harness set from the merged [agents] config:
// the built-in registry, overlaid with the user's `[agents.harness.<name>]`
// declarations (an existing name overrides only the fields it sets; a new
// name is appended and defaults to source "combined"), then filtered by the
// `harnesses` selection list when one is declared. Order is deterministic:
// built-ins first, user-defined additions sorted by name — or exactly the
// declared selection order when `harnesses` is set.
//
// Config errors — a new harness without a target, or a selection naming an
// unknown harness — are returned as clear errors, never silently dropped.
func Resolve(cfg config.AgentsConfig) ([]Harness, error) {
	byName := map[string]Harness{}
	var order []string
	for _, b := range Builtins() {
		byName[b.Name] = b
		order = append(order, b.Name)
	}

	// Overlay user declarations in sorted-name order for determinism.
	names := make([]string, 0, len(cfg.Harness))
	for name := range cfg.Harness {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := cfg.Harness[name]
		h, exists := byName[name]
		if !exists {
			h = Harness{Name: name, Source: SourceCombined}
			order = append(order, name)
		}
		if spec.Target != "" {
			h.Target = spec.Target
		}
		if spec.Source != "" {
			h.Source = Source(spec.Source)
		}
		if h.Target == "" {
			return nil, fmt.Errorf("agents.harness.%s: target is required for a harness that is not a built-in", name)
		}
		byName[name] = h
	}

	if !cfg.HarnessesSet {
		out := make([]Harness, 0, len(order))
		for _, name := range order {
			out = append(out, byName[name])
		}
		return out, nil
	}

	// An explicit selection restricts (and orders) the set; naming an unknown
	// harness is a config error, not a silent no-op.
	out := make([]Harness, 0, len(cfg.Harnesses))
	for _, name := range cfg.Harnesses {
		h, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("agents.harnesses names %q, which is neither a built-in harness nor declared as [agents.harness.%s]", name, name)
		}
		out = append(out, h)
	}
	return out, nil
}

// AssetMapping is one asset-tree mapping in the registry: a named,
// recursively deployed copy of a config-repo directory (under agents/) to a
// directory under $HOME. Like the harness registry, the mappings are DATA —
// adding, overriding, or removing one is a manifest edit
// ([agents.asset.<name>] plus the optional `assets` selection list), never a
// code change. Executable bits are preserved per file, so hook scripts and
// dispatchers stay runnable.
type AssetMapping struct {
	Name   string
	Source string // repo-side directory under agents/, e.g. "githooks"
	Target string // home-relative destination directory, e.g. ".githooks"
}

// BuiltinAssets returns the default asset-mapping registry: the Claude Code
// asset trees. These are DEFAULTS, not assumptions — the manifest's `assets`
// list trims the set, and `[agents.asset.<name>]` overrides a built-in's
// source/target or adds a new mapping entirely.
func BuiltinAssets() []AssetMapping {
	return []AssetMapping{
		{Name: "skills", Source: "skills", Target: ".claude/skills"},
		{Name: "agents", Source: "agents", Target: ".claude/agents"},
		{Name: "hooks", Source: "hooks", Target: ".claude/hooks"},
	}
}

// ResolveAssets computes the effective asset-mapping set from the merged
// [agents] config, with exactly the harness registry's semantics: built-ins
// first, overlaid with the user's `[agents.asset.<name>]` declarations (an
// existing name overrides only the fields it sets; a new name is appended),
// then filtered by the `assets` selection list when one is declared. Order is
// deterministic: built-ins, then user-defined additions sorted by name — or
// exactly the declared selection order.
//
// Config errors — a new mapping missing its source or target, or a selection
// naming an unknown mapping — are returned as clear errors, never silently
// dropped.
func ResolveAssets(cfg config.AgentsConfig) ([]AssetMapping, error) {
	byName := map[string]AssetMapping{}
	var order []string
	for _, b := range BuiltinAssets() {
		byName[b.Name] = b
		order = append(order, b.Name)
	}

	names := make([]string, 0, len(cfg.Asset))
	for name := range cfg.Asset {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := cfg.Asset[name]
		m, exists := byName[name]
		if !exists {
			m = AssetMapping{Name: name}
			order = append(order, name)
		}
		if spec.Source != "" {
			m.Source = spec.Source
		}
		if spec.Target != "" {
			m.Target = spec.Target
		}
		if m.Source == "" || m.Target == "" {
			return nil, fmt.Errorf("agents.asset.%s: source and target are both required for a mapping that is not a built-in", name)
		}
		byName[name] = m
	}

	if !cfg.AssetsSet {
		out := make([]AssetMapping, 0, len(order))
		for _, name := range order {
			out = append(out, byName[name])
		}
		return out, nil
	}

	out := make([]AssetMapping, 0, len(cfg.Assets))
	for _, name := range cfg.Assets {
		m, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("agents.assets names %q, which is neither a built-in mapping nor declared as [agents.asset.%s]", name, name)
		}
		out = append(out, m)
	}
	return out, nil
}
