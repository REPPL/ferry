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

// AgentsConfig is the parsed, merged `[agents]` table of ferry.toml overlaid
// with ferry.local.toml (local wins per key; the harness map merges per name).
// Everything is optional: the zero value means "all built-in harnesses, no
// devtree" — the domain itself is still gated behind `[manage] agents = true`.
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

	cfg := agentsFileConfig{harness: map[string]AgentsHarness{}}
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
		default:
			return agentsFileConfig{}, fmt.Errorf("agents.%s is not a recognised setting (expected devtree, harnesses, or harness.<name>)", key)
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

// mergeAgents overlays local on shared: devtree and harnesses replace per key
// when the local file explicitly sets them; harness declarations merge per
// name with local entries winning.
func mergeAgents(shared, local agentsFileConfig) AgentsConfig {
	out := AgentsConfig{Harness: map[string]AgentsHarness{}}

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

	for name, h := range shared.harness {
		out.Harness[name] = h
	}
	for name, h := range local.harness {
		out.Harness[name] = h
	}
	return out
}
