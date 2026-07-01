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
dotfiles = [".zshrc", ".gitconfig"]
brew     = true
iterm2   = true
fonts    = false        # never sync fonts
```

```toml
# ferry.local.toml (this machine only, gitignored)
[manage]
iterm2 = false          # this is a headless box; skip terminal-app config here
```

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
