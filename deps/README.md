# deps/ — per-platform dependency manifests

One manifest per platform/manager. `ferry` selects the right one on this machine
by `runtime.GOOS` plus the detected package manager (`internal/deps.SelectManifest`),
and the gated `ferry apply --deps` step installs from it. Default file-only
`apply` never touches these — package installs may prompt, need `sudo`, or run
remote scripts, so they are an explicit step.

## Files

| File | Platform / manager | Contents |
|---|---|---|
| `Brewfile.darwin` | macOS, Homebrew | brews **+ casks + fonts** |
| `Brewfile.linux` | Linux, Linuxbrew | brews only (**no casks, no fonts**) |
| `apt.txt` | Debian/Ubuntu without Homebrew | one apt package per line |

Selection: macOS + brew → `Brewfile.darwin`; Linux + brew → `Brewfile.linux`;
Linux + apt-only → `apt.txt`. ferry never touches another OS's or another
manager's manifest.

## Casks and fonts are darwin-only

Homebrew casks (GUI apps) and `cask "font-…"` fonts exist only on macOS Homebrew.
Linuxbrew has no cask support, and Linux font install is a separate later concern —
so casks and fonts live in `Brewfile.darwin` only. `Brewfile.linux` is the
cross-platform brew subset.

## Per-machine overlays (`.local`, gitignored)

`Brewfile.<goos>.local` (e.g. `Brewfile.darwin.local`) holds per-machine additions
and is **gitignored** — it never syncs. `apply --deps` layers it on top of the
shared manifest. Create one by hand on a machine that needs extra local tools;
do not commit it. There is no `.local` overlay for `apt.txt`.

## Regenerating

`ferry capture` re-dumps **the current platform's** manifest for the detected
manager (`brew bundle dump` into `Brewfile.<goos>`), then you trim it by hand to a
sensible shared set. It never regenerates another platform's file. `apt.txt` is
hand-curated only — apt has no clean installed-set dump, so capture treats it as
apply-only.

## What does NOT belong here

npm/curl post-install tools (Claude Code, bun, opencode, codex) are **not** in any
manifest — ferry runs them as named, pinned post-install steps. Manifests hold
native package-manager dependencies only.
