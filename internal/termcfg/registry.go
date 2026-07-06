package termcfg

import (
	"fmt"

	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/registry"
)

// RepoSubdir is the config-repo subdirectory that holds the terminal domain's
// sources: one file or directory per terminal (alacritty/, kitty/,
// wezterm.lua, and any user-declared ones).
const RepoSubdir = "terminals"

// LocalSubdir is the gitignored per-machine overlay root inside the repo. A
// terminal's per-machine override lives at local/terminals/<source>/<relpath>
// (or local/terminals/<source> for a single-file terminal), mirroring the
// dotfile and agents-asset .local layers — the natural home for a per-machine
// colour scheme.
const LocalSubdir = "local"

// Terminal is one config-file terminal in the registry: a named tool, the
// repo-side path its config lives at (a file or directory under terminals/),
// and the home-relative destination it deploys to. The registry is DATA —
// adding, overriding, or restricting a terminal is a manifest edit
// ([terminals.terminal.<name>] plus the optional `enabled` list), never a code
// change.
type Terminal struct {
	Name   string
	Source string // repo-side path under terminals/, e.g. "alacritty" or "wezterm.lua"
	Target string // home-relative destination, e.g. ".config/alacritty" or ".wezterm.lua"
}

// Builtins returns the default terminal registry: the known config-file
// terminals and where each keeps its config. These are DEFAULTS, not
// assumptions — a terminal whose Source does not exist in the repo deploys
// nothing (so enabling all built-ins costs nothing until you add a config),
// the manifest's `enabled` list trims the set, and
// [terminals.terminal.<name>] overrides a built-in's source/target or adds a
// terminal entirely. GNOME Terminal is intentionally absent: it stores config
// in dconf, not a file, and needs a dump/load bridge rather than a copy.
func Builtins() []Terminal {
	return []Terminal{
		{Name: "alacritty", Source: "alacritty", Target: ".config/alacritty"},
		{Name: "kitty", Source: "kitty", Target: ".config/kitty"},
		{Name: "wezterm", Source: "wezterm.lua", Target: ".wezterm.lua"},
	}
}

// Resolve computes the effective terminal set from the merged [terminals]
// config, with the shared registry semantics: the built-in registry, overlaid
// with the user's [terminals.terminal.<name>] declarations (an existing name
// overrides only the fields it sets; a new name is appended), then filtered by
// the `enabled` selection list when one is declared. Order is deterministic:
// built-ins, then user-defined additions sorted by name — or exactly the
// declared selection order.
//
// Config errors — a new terminal missing its source or target, or a selection
// naming an unknown terminal — are returned as clear errors, never silently
// dropped.
func Resolve(cfg config.TerminalsConfig) ([]Terminal, error) {
	return registry.Resolve(
		Builtins(),
		func(t Terminal) string { return t.Name },
		cfg.Terminal,
		func(cur Terminal, exists bool, name string, spec config.TerminalSpec) (Terminal, error) {
			if !exists {
				cur = Terminal{Name: name}
			}
			if spec.Source != "" {
				cur.Source = spec.Source
			}
			if spec.Target != "" {
				cur.Target = spec.Target
			}
			if cur.Source == "" || cur.Target == "" {
				return Terminal{}, fmt.Errorf("terminals.terminal.%s: source and target are both required for a terminal that is not a built-in", name)
			}
			return cur, nil
		},
		cfg.Enabled, cfg.EnabledSet,
		func(name string) error {
			return fmt.Errorf("terminals.enabled names %q, which is neither a built-in terminal nor declared as [terminals.terminal.%s]", name, name)
		},
	)
}
