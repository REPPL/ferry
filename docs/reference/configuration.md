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
iterm2    = true        # the iTerm2 global preferences; see iTerm2 below
fonts     = false       # never sync fonts
agents          = true  # AI-agent instruction files; see agents.md
terminals       = true  # config-file terminal emulators (Alacritty, kitty, WezTerm)
keybindings     = true  # the macOS Cocoa key-bindings dict; see below
emacs           = true  # the Emacs configuration tree (~/.emacs.d/); see below
iterm2-profiles = true  # iTerm2 Dynamic Profiles (JSON); see iTerm2 below
npm-globals     = true  # globally-installed npm packages; see Dependencies below

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

Terminal configs participate in the secret store like dotfiles: a
`{{ferry.secret …}}` placeholder is rendered on apply, a real secret is
commit-gated on capture, and a secret-routed file is deployed `0600`.

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

## iTerm2

iTerm2 is carried as **two** artefacts, because its profiles and its app-wide
preferences want different representations:

| Artefact | `[manage]` key | Repo source | Home target | Mechanism |
|---|---|---|---|---|
| Dynamic Profiles | `iterm2-profiles` | `iterm2/DynamicProfiles/*.json` | `~/Library/Application Support/iTerm2/DynamicProfiles/` | file copy |
| Global preferences | `iterm2` | `iterm2/com.googlecode.iterm2.plist` | the `com.googlecode.iterm2` domain | `defaults import` |

### Profiles — Dynamic Profiles JSON

iTerm2 reads any `*.json` file in its `DynamicProfiles/` folder and live-reloads
it, so profiles are carried as plain, reviewable, mergeable JSON — a config-file
domain like the terminal emulators above. Enable it with `iterm2-profiles = true`
and commit one JSON file per profile set under `iterm2/DynamicProfiles/`. Each
file deploys as a regular-file copy reconciled by hash; ferry never symlinks it.

The domain is **repo-authoritative**: edit the JSON in the repo and `apply`
deploys it. A live edit to a deployed file shows as drift, and `apply` skips it
rather than overwriting your change — set `"Rewritable": false` in the profile so
iTerm2 itself will not rewrite it either. There is no capture pass.

A profile's `"Guid"` is its **frozen identity**: ferry copies the JSON
byte-for-byte and never generates or rewrites a GUID (a changed GUID would orphan
the deployed profile). Each file is validated before it lands — as JSON on every
platform, and additionally through `plutil` on macOS — because **one malformed
file disables all of iTerm2's dynamic profiles**; a file that fails validation is
warned about and skipped, never deployed.

**Per-machine divergence.** The `.local` layer applies per file: a file at
`local/iterm2-profiles/<name>.json` wins over the shared
`iterm2/DynamicProfiles/<name>.json`, and a file present *only* under
`local/iterm2-profiles/` deploys as a machine-only profile — the natural home for
a child Dynamic Profile that sets `"Dynamic Profile Parent GUID"` to a shared
parent's GUID and overrides only, say, the font size. ferry performs no JSON
surgery; the overlay is purely file-level.

### Global preferences — filtered plist

App-wide iTerm2 settings that live outside any profile are carried as an
**allowlisted** `defaults` plist. Enable it with `iterm2 = true`. On `capture`,
ferry exports the live `com.googlecode.iterm2` domain and keeps **only** an
allowlisted set of stable, machine-agnostic global keys (quit/close prompts, tab
and window chrome behaviour, dimming, clipboard behaviour, the auto-update
preference, the default-profile pointer). Everything else is dropped, so volatile
machine state — window geometry, one-shot `NoSync…` dialog flags — can never reach
the repo. The filtered plist is committed at `iterm2/com.googlecode.iterm2.plist`.
(The allowlist is a curated starting point; extend it in your own repo review.)

On `apply`, ferry imports that committed plist into the domain with
`defaults import`, which **replaces** the whole `com.googlecode.iterm2` domain
with the carried set — so any global key you have not allowlisted is reset. The
dropped keys are the volatile ones (window geometry regenerates, `NoSync…` dialog
flags are one-shot), but if you rely on a global setting the starter allowlist
omits, add it to the allowlist so it is carried and preserved.

**Quit iTerm2 first.** A running iTerm2 keeps its preferences in memory and
rewrites the domain on quit, so mutating it while it runs is silently lost — both
`apply` and `restore` therefore *refuse* to touch the global-preferences domain
while iTerm2 is running and tell you to quit it and re-run. `restore` skips only
that one domain (reverting every other managed path) and prints how to finish, so
it never aborts the wider revert. After a successful import ferry runs
`killall cfprefsd` so the preferences daemon does not serve a stale cached copy;
relaunch iTerm2 for the change to take effect. (The profiles artefact above has no
such constraint — it is a plain file copy iTerm2 live-reloads.)

The `.local` layer applies whole-domain: a committed
`local/iterm2/com.googlecode.iterm2.plist` is imported instead of the shared copy
on machines that need a wholesale-divergent global set.

## tmux

tmux is carried as a dotfile: declare `".tmux.conf"` in the `dotfiles` list and
commit your shared config at `dotfiles/tmux.conf`. Like the zsh shell files,
`~/.tmux.conf` is an **include-sidecar** dotfile — it has a real include point,
so its per-machine `.local` layer is a separate sourced file rather than a
whole-file swap. When a per-machine overlay exists at `local/tmux/tmux.conf.local`,
`apply` deploys the shared `~/.tmux.conf` with ferry's directive appended **last**
so the sidecar wins:

```tmux
# ferry: per-machine overlay, sourced last so it wins
source-file -q ~/.tmux.conf.local
```

`source-file -q` sources the per-machine file when it exists and stays quiet when
it does not — the tmux analogue of the shell `[ -f … ] && source …` guard. On
`capture`, ferry strips that injected directive before writing the shared source,
so the committed `dotfiles/tmux.conf` never carries ferry's own boilerplate, and
per-machine edits route to `local/tmux/tmux.conf.local`.

**Tokens in options.** A secret set in a tmux option — for example
`set -g @token 'ghp_…'` — is caught on capture: only the value is routed to the
out-of-repo secret store and replaced with a `{{ferry.secret …}}` placeholder,
leaving the surrounding syntax byte-for-byte intact. The recogniser covers all
four option-setting commands (`set`, `set-option`, `setw`, `set-window-option`),
single- or double-quoted **and** bare unquoted values, and tolerates a trailing
`# comment` after the value — for instance `set -g @token 'ghp_…' # CI token` keeps
both the quotes and the comment untouched. `apply` renders the placeholder back to
the real value (deployed `0600`). An environment reference such as
`set -g @token '${TMUX_TOKEN}'` (or unquoted `$TMUX_TOKEN`) is **not** a literal
secret (tmux expands it at read time), so it is carried to the shared repo
verbatim. Any option shape the recogniser cannot cleanly isolate — an unbalanced
quote, or a value trailed by something other than whitespace or a comment — is
never partially rewritten: capture blocks the whole file instead.

## git

git is carried as a dotfile: declare `".gitconfig"` in the `dotfiles` list and
commit your shared config at `dotfiles/gitconfig`. Like the zsh and tmux files,
`~/.gitconfig` is an **include-sidecar** dotfile, but the injected directive is
git's own native two-line `[include]` block, appended **last** so git
last-wins-merges the machine-local file:

```ini
# ferry: per-machine overlay, sourced last so it wins
[include]
	path = ~/.gitconfig.local
```

git applies includes inline as it reads the file, so an `[include]` placed after
every existing `[include]`/`[includeIf]` block gives `~/.gitconfig.local` the
final say — a native equivalent of ferry's overlay. On `capture`, ferry strips
that injected block (header and `path` line) before writing the shared source, so
the committed `dotfiles/gitconfig` never carries ferry's own boilerplate.

**Identity is never shared.** A machine's commit identity must never travel to
another machine's `~/.gitconfig`, so ferry forces these keys — and every
`[includeIf "gitdir:…"]` block (per-directory identity) — into the never-shared
`~/.gitconfig.local` layer, dropping them from the shared `~/.gitconfig` and the
shared repo:

- `user.email`, `user.name` — commit authorship;
- `user.signingkey` — the signing key id or public-key path (identity, not a
  secret: it stays as plaintext in the local file and is never routed to the
  secret store);
- `gpg.program` — the local signing binary;
- `credential.helper` — the local credential backend;
- `credential.<host>.username` — the account login name (account identity, kept
  as plaintext in the local file and never routed to the secret store).

The firewall runs on both `apply` and `capture`: even a `dotfiles/gitconfig` that
mistakenly commits an identity key has it stripped before it can deploy into the
shared `~/.gitconfig`. Put your identity in `local/git/gitconfig.local` (which
`apply` materialises to `~/.gitconfig.local`), where each machine keeps its own.

**`credential.helper` must be `osxkeychain`.** ferry warns about
`credential.helper = store`, which writes credentials as **plaintext** into
`~/.git-credentials`, and never carries it. ferry never reads or writes
`~/.git-credentials` (it is treated like `~/.ssh` — untouchable).

**Tokens in URLs and headers.** A literal token embedded in a `url.*.insteadOf`
value (`https://<TOKEN>@host/` or `https://oauth2:<TOKEN>@host/`) or after
`Bearer`/`Basic` in an `http.extraHeader` value is caught on capture: only the
token is routed to the out-of-repo secret store and replaced with a
`{{ferry.secret …}}` placeholder — the surrounding URL scheme and host, the
`user:` prefix, and the `Authorization: Bearer ` prefix stay byte-for-byte
intact. `apply` renders the placeholder back to the real value (deployed `0600`).
An environment reference such as `https://${GIT_TOKEN}@host/` is **not** a literal
secret (git expands it at read time), so it is carried to the shared repo
verbatim.

## npm registry auth (`~/.npmrc`)

npm's user config is carried as a whole-file dotfile: declare `".npmrc"` in the
`dotfiles` list and commit your shared config at `dotfiles/npmrc`. Unlike the
zsh, tmux, and git files, `~/.npmrc` is a plain **whole-file** dotfile — it has
no include point, so it gains no ferry directive and is reconciled by hash like
any other dotfile. Only the **user-level** `~/.npmrc` is carried; ferry never
manages a per-project (repo-level) `.npmrc`, which belongs with its project and
routinely holds project-specific registry pins.

**The registry auth token.** A registry auth line — `//registry.example.com/:_authToken=…`,
`:_auth=…`, or `:_password=…` — carries a credential that must never reach the
shared repo. Two patterns keep it out:

- **Recommended — an environment reference.** Write the token as
  `//registry.npmjs.org/:_authToken=${NPM_TOKEN}` and export `NPM_TOKEN` on each
  machine. npm expands the environment when it reads the file, so the real token
  never lives in the config at all; ferry recognises `${…}` as a non-secret and
  carries the line to the shared repo verbatim.
- **Alternative — a stored literal.** If a machine writes a literal token into
  `~/.npmrc`, `capture` detects it and blocks the change from the repo entirely
  (both shared and the gitignored `local/` layer). Route it to the out-of-repo
  secret store and ferry leaves a `{{ferry.secret …}}` placeholder in the
  committed file in its place; `apply` renders the placeholder back to the real
  token so npm reads it, deploying the file `0600`.

The token is never written as plaintext into the committed repo — the same
discipline git applies to `~/.git-credentials`, which ferry refuses to carry.

This section covers the `~/.npmrc` config file only; the machine's
globally-installed npm **packages** are a separate managed domain — see
[npm globals](#npm-globals) under Dependencies.

## macOS key bindings

The macOS Cocoa text system reads a single key-bindings file at app launch to
remap keys system-wide (for example, binding <kbd>⌥f</kbd> to move a word
forward in every Cocoa text field). ferry carries that one file:

| Repo source | Home target |
|---|---|
| `keybindings/DefaultKeyBinding.dict` | `~/Library/KeyBindings/DefaultKeyBinding.dict` |

Enable the domain with `keybindings = true` under `[manage]`, then commit your
dict at `keybindings/DefaultKeyBinding.dict`. With the domain enabled but no
source committed, nothing deploys. The file is deployed as a regular-file copy,
reconciled by hash like every other target — ferry never symlinks it.

The domain is **repo-authoritative**: edit the dict in the config repo and
`apply` deploys it. A live edit to the deployed file shows as drift, and `apply`
skips it rather than overwriting your change — update the repo copy (or
`ferry apply --force`) to reconcile. There is no capture pass and no `.local`
overlay: keyboard behaviour is machine-agnostic, and the old-style dict format
has no include or merge layer, so a per-machine variant would be a whole-file
swap that earns nothing. A genuinely divergent machine keeps a separate dict.

**Reloading.** The bindings load when an app launches, so an apply takes effect
on the *next* launch of each app — relaunch the affected app to pick up new
bindings (no logout needed). An app that is still running will keep showing the
old bindings; that is expected, not an apply failure. Note too that many apps
honour only a subset of the dict: Electron apps, Visual Studio Code, and
Terminal.app respect a limited set of actions, which is an app limitation rather
than a ferry fault.

**Format.** The dict must stay the readable old-style (NeXT/ASCII) text property
list — the reviewable, diff-friendly form. ferry validates the source with
`plutil -lint` on macOS before deploying, and refuses a source that is a binary
plist (a `bplist00` header, which an editor can save silently), that carries a
UTF-8 byte-order mark, or that is not valid UTF-8. ferry never runs
`plutil -convert` on the file (convert would rewrite it to XML or binary and
destroy the readable diff). A `.gitattributes` entry marks the dict as `text` so
git normalises and diffs it as text. Do not carry the SIP-protected system
template (`StandardKeyBinding.dict` inside AppKit), and do not confuse this with
per-app `NSUserKeyEquivalents` (a separate, churning `defaults` domain).

## Emacs

ferry carries an Emacs configuration tree across machines like a config-file
terminal: the repo's `emacs/` area is deployed file by file to `~/.emacs.d/`.

| Repo source | Home target |
|---|---|
| `emacs/` | `~/.emacs.d/` |

Enable the domain with `emacs = true` under `[manage]`, then commit your
configuration under `emacs/` in the repo (`init.el`, `early-init.el`, a literate
`inits/repp.org`, `docs/`, `README`, `LICENSE`, …). With the domain enabled but
no `emacs/` tree committed, nothing deploys. Every file is deployed as a
regular-file copy reconciled by hash — ferry never symlinks `~/.emacs.d/`, so the
older `ln -s repo ~/.emacs.d` habit is replaced by the apply cycle. Note that
`~/.emacs.d` shadows the XDG location `~/.config/emacs`: with `~/.emacs.d`
present, Emacs reads it and never consults `~/.config/emacs`.

**Carry and exclude.** The carry set is everything committed under `emacs/`. Even
so, ferry defensively excludes the volatile, machine-generated paths so they are
never deployed even if a source tree contains them: package stores and compiled
bytecode (`elpa/`, `eln-cache/`, `*.elc`), the tangled Emacs-Lisp output
`inits/repp.el` (regenerated from the literate `inits/repp.org` at load), and
session state (`auto-save-list/`, `transient/`, `url/`, `network-security.data`,
`recentf`, `savehist`, `saveplace`).

**Repo-authoritative.** Edit the configuration in the config repo, then
`ferry apply` deploys it. A live edit to a deployed file shows as drift, and
`apply` skips it rather than overwriting your change — update the repo source (or
`ferry apply --force`) to reconcile. For a literate config this adds one `apply`
step between editing `inits/repp.org` in the repo and Emacs re-tangling it on
next load. There is no capture pass: `ferry capture` does not ingest live
`~/.emacs.d/` edits back into the repo.

**Per-machine overlay.** ferry deploys the union of the shared `emacs/` tree and
the per-machine `local/emacs/` overlay tree. The `.local` layer applies per file:
an override at `local/emacs/<relpath>` wins over the shared `emacs/<relpath>` on
the next `apply`, leaving every other file shared. A file present **only** under
`local/emacs/` — with no shared counterpart — deploys as a machine-only file,
exactly like iTerm2's Dynamic Profiles overlay. This is the natural home for a
Customize-written `inits/custom.el` (which `init.el` loads when present) or a
hand-authored `init.local.el` for machine-specific bits (fonts, `exec-path`,
GUI-versus-tty) that lives on one machine alone. The exclude filter and symlink
refusal apply to both trees.

Emacs files participate in the secret store like dotfiles: a `{{ferry.secret …}}`
placeholder is rendered on apply, a real secret is commit-gated on capture, and a
secret-routed file is deployed `0600`.

## Dependencies

ferry carries the packages a machine needs as declarative manifests under
`deps/` in the config repo, one representation per manager. Installing packages
mutates system state, so it happens **only** under the explicit `ferry apply
--deps` step — never during a default unattended `apply`. Every manager here is
**install/reconcile-only**: ferry adds what the manifest declares and never
removes a package the manifest omits.

### Homebrew

Enable `brew = true` under `[manage]`. The git-tracked representation is a
`Brewfile.<os>` (`deps/Brewfile.darwin`, `deps/Brewfile.linux`) plus an optional
per-machine `deps/Brewfile.<os>.local` overlay for casks or Mac App Store apps
that belong to one machine only. `ferry capture` re-dumps the Brewfile from
`brew bundle dump`; `ferry apply --deps` installs it with `brew bundle` (shared
first, then the `.local` overlay).

`ferry status` reports **Brewfile drift**: it compares the live `brew bundle
dump` against the repo Brewfile and reports how many entries would be captured
(installed locally but not recorded) or installed (recorded but not present).
This is read-only — status never installs a package and never rewrites the
Brewfile. Drift is compared by package identity (the directive and name), so a
benign option or version difference is not reported as drift.

A cloned config repo's Brewfile is untrusted input that `brew bundle` evaluates
as Ruby, so ferry gates every directive through a fail-closed allow-list
(`brew`, `cask`, `mas`, `tap`, `vscode`, `whalebrew` only) before any `brew
bundle` runs. There is no `brew bundle cleanup` step: ferry never uninstalls an
undeclared package.

### npm globals

Enable `npm-globals = true` under `[manage]`. ferry carries the **names** of the
globally-installed npm packages — never their versions — as a plain sorted list
at `deps/npm-globals.txt`. `ferry capture` re-dumps the list from `npm ls -g
--json --depth=0` (npm itself is excluded); `ferry apply --deps` reconciles it
with `npm i -g`; `ferry status` reports drift the same way as Homebrew (how many
names would be captured or installed).

npm is detected independently of the OS package manager, so npm globals run
**alongside** Homebrew or apt rather than instead of them — a machine can carry
both a Brewfile and an npm globals list. When npm is not installed, the domain
skips cleanly. Like Homebrew, this is install/reconcile-only: `apply --deps` adds
missing packages but never uninstalls, and `restore --packages` leaves npm
globals intact.

Because the list feeds `npm i -g`, each entry is validated as a plain registry
package name (optionally scoped, e.g. `@angular/cli`). A git URL, a tarball URL,
a local path, a version-tagged spec, or a flag in the list is refused before npm
runs. The `~/.npmrc` config file and its registry auth token are carried
separately, as a dotfile — see [npm registry auth](#npm-registry-auth-npmrc).

## The `.local` layer

Some settings should differ per machine on purpose: a colour scheme on your laptop, a
machine-specific tool. Those live in the `.local` layer:

- **In the repo**: gitignored, under `local/<domain>/` (e.g. `local/zsh/zshrc.local`,
  `local/tmux/tmux.conf.local`, `local/git/gitconfig.local`).
- **On the machine**: materialised to the real path (e.g. `~/.zshrc.local`, sourced
  last by the shared `~/.zshrc`; `~/.tmux.conf.local`, sourced last by the shared
  `~/.tmux.conf` via `source-file -q`; `~/.gitconfig.local`, pulled in last by the
  shared `~/.gitconfig` via a native git `[include]`, and the home of each
  machine's git identity).

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

`ferry export` produces a portable `.zip` for an [offline move](../tutorials/getting-started.md#move-to-another-account-or-machine-offline).
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

- [Getting started](../tutorials/getting-started.md)
- [SSH](../explanation/ssh.md)
