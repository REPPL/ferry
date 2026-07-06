# Configuration

ferry decides what to manage from a **manifest**, and keeps deliberately-divergent
per-machine settings in a **`.local` layer**. Both directions: `apply` (repo →
machine) and `capture` (machine → repo): respect the same scope.

## Config files

| File | Location | Role |
|---|---|---|
| `config.toml` | `~/.config/ferry/` (not in repo) | This machine's identity + the path to its repo clone. Written by `ferry init`. |
| `ferry.toml` | repo (committed) | **Shared** defaults: the baseline scope that applies to every machine. |
| `ferry.local.toml` | repo (gitignored) | **Per-machine** scope overrides for this machine. |

Effective scope = `ferry.toml` overlaid with `ferry.local.toml` (local wins).

## Declaring scope

The manifest declares which domains ferry manages. Anything not declared is invisible
to ferry: it is never applied and never captured.

```toml
# ferry.toml (shared)
[manage]
dotfiles  = [".zshrc", ".gitconfig"]
brew      = true
iterm2    = true
fonts     = false       # never sync fonts
agents    = true        # AI-agent instruction files; see agents.md
terminals = true        # config-file terminal emulators (Alacritty, kitty, WezTerm)

[agents]
devtree = "Development"           # optional workspace layer, relative to $HOME
# harnesses = ["claude", "codex"] # optional; default is every built-in harness

[terminals]
# enabled = ["alacritty", "wezterm"] # optional; default is every built-in terminal
```

```toml
# ferry.local.toml (this machine only, gitignored)
[manage]
iterm2 = false          # this is a headless box; skip terminal-app config here
```

## Terminal emulators (config-file)

Config-file terminal emulators — those that keep their settings in plain-text
files — are carried like dotfiles. A built-in registry maps each known terminal
to where its config lives in the repo and where it deploys under `$HOME`:

| Terminal | Repo source (`terminals/`) | Home target |
|---|---|---|
| Alacritty | `terminals/alacritty/` | `~/.config/alacritty/` |
| kitty | `terminals/kitty/` | `~/.config/kitty/` |
| WezTerm | `terminals/wezterm.lua` | `~/.wezterm.lua` |

Enable the domain with `terminals = true` under `[manage]`, then commit each
terminal's config under `terminals/` in the repo. A built-in terminal whose
source is not present in the repo deploys nothing, so you enable the ones you
use simply by committing their config. A directory terminal (Alacritty, kitty)
carries its whole config tree file by file; a single-file terminal (WezTerm)
carries its one file.

The registry is data, edited in the manifest — never in code:

```toml
[terminals]
# Restrict (and order) the deployed set; default is every built-in terminal.
enabled = ["alacritty", "wezterm"]

# Override a built-in's paths, or add a terminal the registry does not know:
[terminals.terminal.foot]
source = "foot"          # under terminals/ in the repo
target = ".config/foot"  # relative to $HOME
```

GNOME Terminal is deliberately out of scope: it stores its settings in dconf,
not a config file, so it needs a dump/load bridge rather than a file copy.

The `.local` layer applies per file, which is the natural home for a
per-machine colour scheme: an override at
`local/terminals/<source>/<relpath>` wins over the shared copy on the next
`apply`, leaving every other file shared.

## The `.local` layer

Some settings should differ per machine on purpose: a colour scheme on your laptop, a
machine-specific tool. Those live in the `.local` layer:

- **In the repo**: gitignored, under `local/<domain>/` (e.g. `local/zsh/zshrc.local`).
- **On the machine**: materialised to the real path (e.g. `~/.zshrc.local`, sourced
  last by the shared `~/.zshrc`).

`apply` layers `.local` **on top of** shared, so your deliberate per-machine
differences always win and are never overwritten.

## How capture decides what to ingest

`ferry capture` is selective:

1. **Allowlist**: only declared domains are even considered.
2. **Review**: within scope, you approve each change (hunk by hunk for text).
3. **Route**: each accepted change goes **shared** (→ repo), **local** (→ `.local`,
   this machine only), or is **rejected**.
4. **Secret scan**: if a change looks like a secret (private key, token), it is
   blocked from the repo entirely and only the out-of-band path is offered.

For a repo source that references stored secrets (`{{ferry.secret "domain.key"}}`
placeholders), capture **reverse-renders**: it renders the source in memory, compares
your live file against the rendered content, and splices your edits back around the
placeholders. Stored values never re-enter the repo, an unchanged stored secret never
trips the gate, and only genuinely new secret material is gated: taking its store
route stores only that span under a new ref and patches only that span to a
placeholder, preserving the rest of the file. On a machine whose local store does not
hold a referenced secret, capture falls back to comparing the raw source, notes the
missing refs, and reports any gate block read-only (populate the store, then re-run).

## How apply stays safe

- Only in-scope domains are touched on this machine.
- Dotfiles are **copied** (not symlinked into the repo), so editing a live file never
  silently rewrites the repo: `capture` is the only path back.
- `apply` tracks what it last wrote; if you edited a managed file locally without
  capturing it, `apply` reports a **conflict** instead of overwriting your work.
- Dependencies install only under the explicit `ferry apply --deps` step, never during
  a default unattended `apply`.

## What a bundle contains

`ferry export` produces a portable `.zip` for an [offline move](getting-started.md#move-to-another-account-or-machine-offline).
It carries the **shared layer only**: the repo's git-tracked shared files (never
untracked, ignored, or editor-backup files). It never carries secrets, anything under
`~/.ssh/`, or the per-machine `.local` layer.

The per-machine local layer (`local/**`, `ferry.local.toml`) is **gitignored by
design**, so it is not tracked and does not enter the bundle. `--include-local` adds
back the local-layer files that are actually **tracked** in the repo (the same
`git ls-files` set export collects from) — so it bundles a local-layer file only if you
have force-tracked it. The local layer lands on the other side only when you pass
`--include-local` on **both** export and import (a double opt-in).

Every included text file's content is secret-scanned, and every entry path is scanned
for secret-shaped components; a binary is scanned for embedded private-key markers.
Anything that trips these is withheld and reported, not bundled — symmetrically on
import, which re-runs the same checks.

## Related

- [Getting started](getting-started.md)
- [SSH](ssh.md)
