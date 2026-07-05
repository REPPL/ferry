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
	return resolveRegistry(
		Builtins(),
		func(h Harness) string { return h.Name },
		cfg.Harness,
		func(cur Harness, exists bool, name string, spec config.AgentsHarness) (Harness, error) {
			if !exists {
				cur = Harness{Name: name, Source: SourceCombined}
			}
			if spec.Target != "" {
				cur.Target = spec.Target
			}
			if spec.Source != "" {
				cur.Source = Source(spec.Source)
			}
			if cur.Target == "" {
				return Harness{}, fmt.Errorf("agents.harness.%s: target is required for a harness that is not a built-in", name)
			}
			return cur, nil
		},
		cfg.Harnesses, cfg.HarnessesSet,
		func(name string) error {
			return fmt.Errorf("agents.harnesses names %q, which is neither a built-in harness nor declared as [agents.harness.%s]", name, name)
		},
	)
}

// resolveRegistry is the shared registry resolver for both the harness and the
// asset-mapping registries (which are structurally identical): the built-in
// entries, overlaid with the user's per-name declarations in sorted-name order
// (an existing name is overridden field by field via overlay; a new name is
// appended), then filtered by the selection list when one is declared. Order is
// deterministic: built-ins, then user additions sorted by name — or exactly the
// declared selection order. overlay applies one user spec (and enforces the
// registry's required-field rule); unknown formats the selection-not-found error.
func resolveRegistry[T any, S any](
	builtins []T,
	nameOf func(T) string,
	decls map[string]S,
	overlay func(cur T, exists bool, name string, spec S) (T, error),
	selection []string,
	selectionSet bool,
	unknown func(name string) error,
) ([]T, error) {
	byName := map[string]T{}
	var order []string
	for _, b := range builtins {
		n := nameOf(b)
		byName[n] = b
		order = append(order, n)
	}

	names := make([]string, 0, len(decls))
	for n := range decls {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		cur, exists := byName[n]
		if !exists {
			order = append(order, n)
		}
		merged, err := overlay(cur, exists, n, decls[n])
		if err != nil {
			return nil, err
		}
		byName[n] = merged
	}

	if !selectionSet {
		out := make([]T, 0, len(order))
		for _, n := range order {
			out = append(out, byName[n])
		}
		return out, nil
	}

	// An explicit selection restricts (and orders) the set; naming an unknown
	// entry is a config error, not a silent no-op.
	out := make([]T, 0, len(selection))
	for _, n := range selection {
		e, ok := byName[n]
		if !ok {
			return nil, unknown(n)
		}
		out = append(out, e)
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
	return resolveRegistry(
		BuiltinAssets(),
		func(m AssetMapping) string { return m.Name },
		cfg.Asset,
		func(cur AssetMapping, exists bool, name string, spec config.AgentsAsset) (AssetMapping, error) {
			if !exists {
				cur = AssetMapping{Name: name}
			}
			if spec.Source != "" {
				cur.Source = spec.Source
			}
			if spec.Target != "" {
				cur.Target = spec.Target
			}
			if cur.Source == "" || cur.Target == "" {
				return AssetMapping{}, fmt.Errorf("agents.asset.%s: source and target are both required for a mapping that is not a built-in", name)
			}
			return cur, nil
		},
		cfg.Assets, cfg.AssetsSet,
		func(name string) error {
			return fmt.Errorf("agents.assets names %q, which is neither a built-in mapping nor declared as [agents.asset.%s]", name, name)
		},
	)
}
