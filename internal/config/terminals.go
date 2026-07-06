package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// TerminalSpec is one user-defined (or overridden) config-file terminal
// declaration from a `[terminals.terminal.<name>]` table: where the terminal's
// config lives in the repo (a file or directory under terminals/) and where it
// deploys under $HOME. Both fields are optional when overriding a built-in
// terminal (only the set fields override); a NEW terminal must carry both. The
// zero value means "nothing declared".
type TerminalSpec struct {
	// Source is the repo-side path under terminals/ that holds the config — a
	// single file (e.g. "wezterm.lua") or a directory (e.g. "alacritty").
	Source string
	// Target is the destination under $HOME — a file (e.g. ".wezterm.lua") or a
	// directory (e.g. ".config/alacritty"), matching the source's shape.
	Target string
}

// TerminalsConfig is the parsed, merged `[terminals]` table of ferry.toml
// overlaid with ferry.local.toml (local wins: the enabled list replaces
// wholesale; per-terminal declarations merge per field, local winning).
// Everything is optional: the zero value means "every built-in terminal, no
// overrides" — the domain itself is still gated behind `[manage] terminals =
// true`.
type TerminalsConfig struct {
	// Enabled selects which terminals deploy. Nil (not declared) means every
	// known terminal — all built-ins plus every user-defined one. A declared
	// list restricts (and orders) deploys to exactly the named terminals.
	Enabled []string
	// EnabledSet records whether an `enabled` list appeared in either scope
	// file, so an explicitly-empty list ([]) is distinguishable from "not set".
	EnabledSet bool
	// Terminal holds the user-defined terminal declarations by name.
	Terminal map[string]TerminalSpec
}

// LoadTerminals loads and merges the `[terminals]` tables of ferry.toml and
// ferry.local.toml under repoPath (local wins: the enabled list replaces when
// the local file sets it; terminal declarations merge per field with local
// values overriding shared ones). Both files are read through the same
// symlink-refusing guard as the manifest. A repo with no `[terminals]` table
// at all yields the zero TerminalsConfig, not an error.
func LoadTerminals(repoPath string) (TerminalsConfig, error) {
	sharedPath, err := safeRepoRead(repoPath, SharedManifestName)
	if err != nil {
		return TerminalsConfig{}, err
	}
	shared, err := loadTerminalsFile(sharedPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return TerminalsConfig{}, err
	}

	localPath, err := safeRepoRead(repoPath, LocalManifestName)
	if err != nil {
		return TerminalsConfig{}, err
	}
	local, err := loadTerminalsFile(localPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return TerminalsConfig{}, err
	}

	return mergeTerminals(shared, local), nil
}

// rawTerminalsFile mirrors the on-disk `[terminals]` table shape. Keys are
// decoded individually (via toml.Primitive) so an unknown key is a clear error
// and a wrong-typed value fails with the key named.
type rawTerminalsFile struct {
	Terminals map[string]toml.Primitive `toml:"terminals"`
}

// terminalsFileConfig is one scope file's parsed `[terminals]` table plus a
// presence flag so the merge can tell "local set enabled=[]" apart from "local
// didn't mention enabled".
type terminalsFileConfig struct {
	enabled    []string
	enabledSet bool
	terminal   map[string]TerminalSpec
}

// loadTerminalsFile reads and parses one scope file's `[terminals]` table. A
// missing file yields a zero config and reports os.ErrNotExist (absence is
// normal); a present file with no `[terminals]` table yields a zero config and
// nil error.
func loadTerminalsFile(path string) (terminalsFileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return terminalsFileConfig{}, err
		}
		return terminalsFileConfig{}, fmt.Errorf("read manifest %s: %w", path, err)
	}
	cfg, err := parseTerminals(data)
	if err != nil {
		return terminalsFileConfig{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return cfg, nil
}

// parseTerminals decodes one scope file's bytes into its `[terminals]` table,
// validating at the boundary: unknown keys, wrong-typed values, and an
// absolute or $HOME/repo-escaping source/target are all clear errors here
// rather than surprises at apply time.
func parseTerminals(data []byte) (terminalsFileConfig, error) {
	var raw rawTerminalsFile
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return terminalsFileConfig{}, fmt.Errorf("parse manifest: %w", err)
	}

	cfg := terminalsFileConfig{terminal: map[string]TerminalSpec{}}
	for key, prim := range raw.Terminals {
		switch key {
		case "enabled":
			var list []string
			if err := md.PrimitiveDecode(prim, &list); err != nil {
				return terminalsFileConfig{}, fmt.Errorf("terminals.enabled must be a list of strings: %w", err)
			}
			cfg.enabled = list
			cfg.enabledSet = true
		case "terminal":
			var m map[string]TerminalSpec
			if err := md.PrimitiveDecode(prim, &m); err != nil {
				return terminalsFileConfig{}, fmt.Errorf("terminals.terminal.<name> must be a table with `source` and/or `target` strings: %w", err)
			}
			for name, spec := range m {
				if err := validateTerminalSpec(name, spec); err != nil {
					return terminalsFileConfig{}, err
				}
				cfg.terminal[name] = spec
			}
		default:
			return terminalsFileConfig{}, fmt.Errorf("terminals.%s is not a recognised setting (expected enabled or terminal.<name>)", key)
		}
	}

	// Catch typo'd fields inside the terminal sub-tables (e.g.
	// terminals.terminal.x.tpyo): the top-level switch only sees direct children
	// of [terminals], so an unknown deeper key would otherwise be silently
	// discarded. md tracks every key not consumed by a PrimitiveDecode above;
	// filter to the terminals tree so the other domains' tables are not mistaken
	// for unknown keys.
	for _, k := range md.Undecoded() {
		if len(k) > 0 && k[0] == "terminals" {
			return terminalsFileConfig{}, fmt.Errorf("%s is not a recognised setting", k.String())
		}
	}
	return cfg, nil
}

// validateTerminalSpec applies the per-declaration checks a terminal table must
// pass: a relative, repo-contained source and a relative, $HOME-contained
// target (when set). Missing fields are allowed here because an override of a
// built-in terminal may set only one; the registry resolution enforces that a
// NEW terminal carries both.
func validateTerminalSpec(name string, spec TerminalSpec) error {
	if spec.Source != "" {
		if err := requireRelativeContained(spec.Source, fmt.Sprintf("terminals.terminal.%s.source", name), "the repo's terminals/ area"); err != nil {
			return err
		}
	}
	if spec.Target != "" {
		if err := requireRelativeContained(spec.Target, fmt.Sprintf("terminals.terminal.%s.target", name), "$HOME"); err != nil {
			return err
		}
	}
	return nil
}

// requireRelativeContained refuses a path that could not be a home/repo-relative
// location: an absolute path or one that climbs out via "..". field names the
// offending setting; within names the containing area for the message.
func requireRelativeContained(p, field, within string) error {
	if filepath.IsAbs(p) {
		return fmt.Errorf("%s must be relative to %s, got the absolute path %q", field, within, p)
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%s must stay within %s, got %q", field, within, p)
	}
	return nil
}

// mergeTerminals overlays local on shared: the enabled list replaces per key
// when the local file explicitly sets it; terminal declarations merge per FIELD,
// with a local entry's set fields winning. A documented partial override (local
// sets only one field) keeps the shared entry's other field rather than blanking
// it — a blanked required field would make Resolve hard-error the whole domain
// for a non-built-in entry.
func mergeTerminals(shared, local terminalsFileConfig) TerminalsConfig {
	out := TerminalsConfig{Terminal: map[string]TerminalSpec{}}

	if local.enabledSet {
		out.Enabled = append([]string(nil), local.enabled...)
		out.EnabledSet = true
	} else if shared.enabledSet {
		out.Enabled = append([]string(nil), shared.enabled...)
		out.EnabledSet = true
	}

	for name, spec := range shared.terminal {
		out.Terminal[name] = spec
	}
	for name, spec := range local.terminal {
		merged := out.Terminal[name]
		if spec.Source != "" {
			merged.Source = spec.Source
		}
		if spec.Target != "" {
			merged.Target = spec.Target
		}
		out.Terminal[name] = merged
	}
	return out
}
