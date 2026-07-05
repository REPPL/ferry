package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// AgentsHarness is one user-defined (or overridden) harness declaration from
// an `[agents.harness.<name>]` table. Both fields are optional when overriding
// a built-in harness (only the set fields override); a NEW harness must carry
// a target. The zero value means "nothing declared".
type AgentsHarness struct {
	// Target is the harness's global instruction file, relative to $HOME
	// (e.g. ".config/myharness/RULES.md").
	Target string
	// Source names which rendered instruction content the target receives:
	// "general", "coding", or "combined". Empty defaults to "combined" for a
	// new harness and leaves a built-in's source unchanged on an override.
	Source string
}

// AgentsAsset is one user-defined (or overridden) asset-tree mapping from an
// `[agents.asset.<name>]` table: a directory of files in the config repo,
// deployed recursively to a directory under $HOME. Both fields are optional
// when overriding a built-in mapping (only the set fields override); a NEW
// mapping must carry both. The zero value means "nothing declared".
type AgentsAsset struct {
	// Source is the repo-side directory under agents/ that holds the files
	// (e.g. "githooks" for agents/githooks/).
	Source string
	// Target is the destination directory relative to $HOME
	// (e.g. ".githooks").
	Target string
}

// AgentsConfig is the parsed, merged `[agents]` table of ferry.toml overlaid
// with ferry.local.toml (local wins per key; the harness and asset maps merge
// per name). Everything is optional: the zero value means "all built-in
// harnesses and asset mappings, no devtree" — the domain itself is still
// gated behind `[manage] agents = true`.
type AgentsConfig struct {
	// Devtree is an OPTIONAL workspace directory relative to $HOME. When set,
	// the coding instruction set additionally deploys to <devtree>/CLAUDE.md
	// (the devtree ROOT); when unset no workspace file is deployed.
	Devtree string
	// Harnesses selects which harnesses deploy. Nil (not declared) means every
	// known harness — all built-ins plus every user-defined one. A declared
	// list restricts deploys to exactly the named harnesses.
	Harnesses []string
	// HarnessesSet records whether a `harnesses` list appeared in either scope
	// file, so an explicitly-empty list ([]) is distinguishable from "not set".
	HarnessesSet bool
	// Harness holds the user-defined harness declarations by name.
	Harness map[string]AgentsHarness
	// Assets selects which asset mappings deploy, exactly as Harnesses does
	// for harnesses: nil means every known mapping; a declared list restricts
	// (and orders) the set.
	Assets []string
	// AssetsSet records whether an `assets` list appeared in either scope file.
	AssetsSet bool
	// Asset holds the user-defined asset-mapping declarations by name.
	Asset map[string]AgentsAsset
}

// agentsSourceValues are the accepted `source` values for a harness.
var agentsSourceValues = map[string]bool{"general": true, "coding": true, "combined": true}

// LoadAgents loads and merges the `[agents]` tables of ferry.toml and
// ferry.local.toml under repoPath (local wins: devtree and harnesses replace
// per key when the local file sets them; harness declarations merge per name
// with local entries overriding shared ones). Both files are read through the
// same symlink-refusing guard as the manifest. A repo with no `[agents]` table
// at all yields the zero AgentsConfig, not an error.
func LoadAgents(repoPath string) (AgentsConfig, error) {
	sharedPath, err := safeRepoRead(repoPath, SharedManifestName)
	if err != nil {
		return AgentsConfig{}, err
	}
	shared, err := loadAgentsFile(sharedPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return AgentsConfig{}, err
	}

	localPath, err := safeRepoRead(repoPath, LocalManifestName)
	if err != nil {
		return AgentsConfig{}, err
	}
	local, err := loadAgentsFile(localPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return AgentsConfig{}, err
	}

	return mergeAgents(shared, local), nil
}

// rawAgentsFile mirrors the on-disk `[agents]` table shape. Keys are decoded
// individually (via toml.Primitive) so an unknown key is a clear error and a
// wrong-typed value fails with the key named.
type rawAgentsFile struct {
	Agents map[string]toml.Primitive `toml:"agents"`
}

// agentsFileConfig is one scope file's parsed `[agents]` table plus presence
// flags so the merge can tell "local set devtree=”" apart from "local didn't
// mention devtree".
type agentsFileConfig struct {
	devtree      string
	devtreeSet   bool
	harnesses    []string
	harnessesSet bool
	harness      map[string]AgentsHarness
	assets       []string
	assetsSet    bool
	asset        map[string]AgentsAsset
}

// loadAgentsFile reads and parses one scope file's `[agents]` table. A missing
// file yields a zero config and reports os.ErrNotExist (absence is normal); a
// present file with no `[agents]` table yields a zero config and nil error.
func loadAgentsFile(path string) (agentsFileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return agentsFileConfig{}, err
		}
		return agentsFileConfig{}, fmt.Errorf("read manifest %s: %w", path, err)
	}
	cfg, err := parseAgents(data)
	if err != nil {
		return agentsFileConfig{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return cfg, nil
}

// parseAgents decodes one scope file's bytes into its `[agents]` table,
// validating at the boundary: unknown keys, wrong-typed values, an absolute or
// $HOME-escaping devtree, and an invalid harness `source` are all clear errors
// here rather than surprises at apply time.
func parseAgents(data []byte) (agentsFileConfig, error) {
	var raw rawAgentsFile
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return agentsFileConfig{}, fmt.Errorf("parse manifest: %w", err)
	}

	cfg := agentsFileConfig{
		harness: map[string]AgentsHarness{},
		asset:   map[string]AgentsAsset{},
	}
	for key, prim := range raw.Agents {
		switch key {
		case "devtree":
			var s string
			if err := md.PrimitiveDecode(prim, &s); err != nil {
				return agentsFileConfig{}, fmt.Errorf("agents.devtree must be a string: %w", err)
			}
			if err := validateDevtree(s); err != nil {
				return agentsFileConfig{}, err
			}
			cfg.devtree = s
			cfg.devtreeSet = true
		case "harnesses":
			var list []string
			if err := md.PrimitiveDecode(prim, &list); err != nil {
				return agentsFileConfig{}, fmt.Errorf("agents.harnesses must be a list of strings: %w", err)
			}
			cfg.harnesses = list
			cfg.harnessesSet = true
		case "harness":
			var m map[string]AgentsHarness
			if err := md.PrimitiveDecode(prim, &m); err != nil {
				return agentsFileConfig{}, fmt.Errorf("agents.harness.<name> must be a table with `target` and/or `source` strings: %w", err)
			}
			for name, h := range m {
				if err := validateHarnessSpec(name, h); err != nil {
					return agentsFileConfig{}, err
				}
				cfg.harness[name] = h
			}
		case "assets":
			var list []string
			if err := md.PrimitiveDecode(prim, &list); err != nil {
				return agentsFileConfig{}, fmt.Errorf("agents.assets must be a list of strings: %w", err)
			}
			cfg.assets = list
			cfg.assetsSet = true
		case "asset":
			var m map[string]AgentsAsset
			if err := md.PrimitiveDecode(prim, &m); err != nil {
				return agentsFileConfig{}, fmt.Errorf("agents.asset.<name> must be a table with `source` and/or `target` strings: %w", err)
			}
			for name, a := range m {
				if err := validateAssetSpec(name, a); err != nil {
					return agentsFileConfig{}, err
				}
				cfg.asset[name] = a
			}
		default:
			return agentsFileConfig{}, fmt.Errorf("agents.%s is not a recognised setting (expected devtree, harnesses, harness.<name>, assets, or asset.<name>)", key)
		}
	}

	// Catch typo'd fields inside the harness/asset sub-tables: the top-level
	// switch only sees the direct children of [agents], so an unknown key such
	// as `agents.asset.x.tpyo` would otherwise be silently discarded. md tracks
	// every key not consumed by a PrimitiveDecode above; filter to the agents
	// tree so the other domains' tables (which this parser does not decode) are
	// not mistaken for unknown keys.
	for _, k := range md.Undecoded() {
		if len(k) > 0 && k[0] == "agents" {
			return agentsFileConfig{}, fmt.Errorf("%s is not a recognised setting", k.String())
		}
	}
	return cfg, nil
}

// validateDevtree refuses a devtree that could not be a home-relative
// workspace directory: an absolute path or one that climbs out via "..". A
// deeper check (the ~/.ssh refusal) happens when the target is built.
func validateDevtree(s string) error {
	if s == "" {
		return nil
	}
	if filepath.IsAbs(s) {
		return fmt.Errorf("agents.devtree must be relative to $HOME, got the absolute path %q", s)
	}
	clean := filepath.Clean(s)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("agents.devtree must stay within $HOME, got %q", s)
	}
	return nil
}

// validateHarnessSpec applies the per-declaration checks a harness table must
// pass: a relative target (when set) and a recognised source (when set). A
// missing target is allowed here because an override of a built-in harness may
// set only the source; the registry resolution enforces that a NEW harness
// carries a target.
func validateHarnessSpec(name string, h AgentsHarness) error {
	if h.Target != "" && filepath.IsAbs(h.Target) {
		return fmt.Errorf("agents.harness.%s.target must be relative to $HOME, got the absolute path %q", name, h.Target)
	}
	if h.Source != "" && !agentsSourceValues[h.Source] {
		return fmt.Errorf("agents.harness.%s.source must be one of general, coding, combined; got %q", name, h.Source)
	}
	return nil
}

// validateAssetSpec applies the per-declaration checks an asset table must
// pass: a relative, repo-contained source directory and a relative target
// directory (when set). Missing fields are allowed here because an override
// of a built-in mapping may set only one; the registry resolution enforces
// that a NEW mapping carries both.
func validateAssetSpec(name string, a AgentsAsset) error {
	if a.Source != "" {
		if filepath.IsAbs(a.Source) {
			return fmt.Errorf("agents.asset.%s.source must be a directory relative to the repo's agents/ area, got the absolute path %q", name, a.Source)
		}
		clean := filepath.Clean(a.Source)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("agents.asset.%s.source must stay within the repo's agents/ area, got %q", name, a.Source)
		}
	}
	if a.Target != "" && filepath.IsAbs(a.Target) {
		return fmt.Errorf("agents.asset.%s.target must be relative to $HOME, got the absolute path %q", name, a.Target)
	}
	return nil
}

// mergeAgents overlays local on shared: devtree and the selection lists
// replace per key when the local file explicitly sets them; harness and asset
// declarations merge per name with local entries winning.
func mergeAgents(shared, local agentsFileConfig) AgentsConfig {
	out := AgentsConfig{
		Harness: map[string]AgentsHarness{},
		Asset:   map[string]AgentsAsset{},
	}

	out.Devtree = shared.devtree
	if local.devtreeSet {
		out.Devtree = local.devtree
	}

	if local.harnessesSet {
		out.Harnesses = append([]string(nil), local.harnesses...)
		out.HarnessesSet = true
	} else if shared.harnessesSet {
		out.Harnesses = append([]string(nil), shared.harnesses...)
		out.HarnessesSet = true
	}
	if local.assetsSet {
		out.Assets = append([]string(nil), local.assets...)
		out.AssetsSet = true
	} else if shared.assetsSet {
		out.Assets = append([]string(nil), shared.assets...)
		out.AssetsSet = true
	}

	for name, h := range shared.harness {
		out.Harness[name] = h
	}
	for name, h := range local.harness {
		out.Harness[name] = h
	}
	for name, a := range shared.asset {
		out.Asset[name] = a
	}
	for name, a := range local.asset {
		out.Asset[name] = a
	}
	return out
}
