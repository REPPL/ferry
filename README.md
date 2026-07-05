<div align="center">

  <img src="docs/assets/img/logo.png" alt="ferry Logo" width="150">

  <h1>ferry</h1>

  <p>Ferries your dotfiles, agent setup, and dependencies across user accounts and machines.</p>

  <a href="https://github.com/REPPL/ferry/releases"><img src="https://img.shields.io/github/v/release/REPPL/ferry?cacheSeconds=300" alt="Release"></a>
  <a href="https://github.com/REPPL/ferry/blob/main/LICENSE"><img src="https://img.shields.io/github/license/REPPL/ferry?cacheSeconds=300" alt="License"></a>
  <img src="https://img.shields.io/github/last-commit/REPPL/ferry?cacheSeconds=300" alt="Last commit">
  <br />
  <img src="https://img.shields.io/badge/macOS-000000?logo=apple&logoColor=white" alt="macOS">
  <img src="https://img.shields.io/badge/Linux-core%20CI--tested-FCC624?logo=linux&logoColor=black" alt="Linux: core CI-tested">

</div>

---

`ferry` carries your terminal setup across user accounts and machines. Define your
configuration once in a git repo; `ferry` reconciles any machine to match it, and
pulls local changes back when you want to harmonise them everywhere.

> `ferry` is a research project for my personal use. Not ready for production as things
> may break.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/REPPL/ferry/main/install.sh | bash
```

> **Note:** the `curl … | bash` installer fetches the release's `checksums.txt` and
> verifies each binary against it, failing closed if it is absent. Building from source
> works today; see [RELEASE.md](docs/RELEASE.md) for how releases are cut.

Installs the single `ferry` binary to `~/.local/bin`. If that's not on your PATH, the
installer prints the one line to add. It does not install Homebrew or run anything else.
(No `sudo`, no shell edits: see [Principles](#principles).)

### Verifying a download

Every released binary carries a signed build-provenance attestation, so you can confirm
it was built by ferry's release workflow from this repository before you trust it.
Download a binary from the [Releases page](https://github.com/REPPL/ferry/releases), then
verify it with the [GitHub CLI](https://cli.github.com):

```bash
gh attestation verify ferry-darwin-arm64 -R REPPL/ferry
```

A successful check prints the verified provenance; a missing or mismatched attestation
fails the command, so a tampered or unofficial binary never passes.

## Quickstart

```bash
# On a machine whose setup you want to capture:
ferry init            # first-run setup; starts a config repo at ~/.config/ferry/repo
ferry capture         # review local config and pull it into the repo
git -C <repo> push    # share it

# On another machine:
ferry init <repo-url> # clone your config repo over HTTPS, set this machine up
ferry apply           # reconcile this machine to the repo
```

See [Getting started](docs/getting-started.md) for the full happy path.

## Documentation

- [Getting started](docs/getting-started.md): zero to a working setup
- [Configuration](docs/configuration.md): the manifest, scope, and the `.local` layer
- [Agents](docs/agents.md): the agents domain — one set of AI-agent instructions (CLAUDE.md, AGENTS.md, skills, hooks) carried across machines and coding CLIs
- [SSH](docs/ssh.md): how ferry treats SSH (hands-off) and how to move keys yourself
- [Compatibility](docs/reference/compatibility.md): which surfaces are stable, the pre-1.0 rule, and how on-disk state is versioned
- [Releases](docs/RELEASE.md): how releases are built, checksummed, attested, and pruned

## Commands

Every command is run as `ferry <command>` (e.g. `ferry init`).

| Command | What it does |
|---|---|
| `init` | First-run setup: locate/clone the config repo into ferry's own space (`~/.config/ferry/repo` by default), write ferry's config. On a fresh interactive run (stdin and stdout both ttys) an adoption **wizard** scans your existing `~/.zshrc` and lets you keep it as-is, route it per block (shared / local / drop), or start fresh from a portable starter: nothing is written before the preview confirm, the original is kept in a timestamped `~/.zshrc.ferry-<ts>.bak`, and secret-shaped lines are always routed to the out-of-repo secret store or dropped, never seeded. Non-interactively (or with `--yes`/`--no-wizard`) the same adopt happens without prompts: secrets are extracted to the store automatically (refs listed on stderr) and everything else is kept shared verbatim. The plugin set is currently zsh (`~/.zshrc`); more domains come with later releases. |
| `init --yes` | Don't ask anything: skip the wizard (plain adopt with automatic secret extraction) and assume yes for the confirmations init would otherwise ask (the `--github` create-confirm; the closing apply confirm with `--apply`). |
| `init --no-wizard` | Skip the interactive wizard only (same non-interactive adopt-and-extract fallback), without `--yes`'s other confirmation assents. |
| `init --repair` | Opt into the wizard's repair review: hardcoded `/Users/<name>` paths to `$HOME`, duplicate `PATH` exports, dead `source` lines: each fix is accepted or declined individually. Needs the interactive wizard, so it conflicts with `--yes`/`--no-wizard` and with a non-tty run — unless `--wizard-answers` is given, which satisfies the consent requirement and composes with them. |
| `init --wizard-answers <file>` | Drive every wizard decision from a TOML answers file (mode, per-block routes, secret routes, repairs, starter answers) instead of the TUI: same gates, preview, backup, and confirm, no tty needed. |
| `init --github [name]` | Create a **new private** GitHub repo via the `gh` CLI's existing auth and manage it as ferry's HTTPS remote. Needs `gh` authenticated; ferry stores no token. Always private, never reuses an existing repo, and won't push a file that looks like a secret: the wizard (or the non-interactive fallback) extracts detected secrets to the local store first, so only placeholders are committed and pushed. Add `--yes` for non-interactive use. |
| `apply` | Reconcile this machine to the repo (deploy dotfiles, terminal settings). Idempotent; safe to re-run. Dependencies install behind `apply --deps`. |
| `capture` | Pull local changes back into the repo. Interactive: approve each change, route it *shared* (synced everywhere) or *local* (this machine only). For sources that reference stored secrets, capture compares against the rendered content and splices your edits back around the placeholders, so stored values never re-enter the repo and a store-routed secret never blocks its own round-trip. |
| `sync` | Publish captured changes and pull remote ones for a managed repo, in one command. Integrates the remote first, never force-pushes, gates the whole push range for secrets, and leaves your machine unchanged on a conflict. Route-1 repos need `--allow-unmanaged`. Run `ferry apply` after to deploy pulled changes. |
| `status` | Report config drift (what changed on this machine). |
| `doctor` | Report machine/tool health. |
| `diff` | Preview what `apply` would change. |
| `restore` | Reverse ferry's changes, returning the machine to its pre-ferry state from an automatic backup. |
| `export` | Write a portable, secret-scanned `.zip` bundle of the repo's tracked shared files for an offline move. Prints the bundle SHA256. Never includes secrets, `~/.ssh`, or the local layer (unless `--include-local`). |
| `import` | Ingest a bundle into a fresh config repo (`~/.config/ferry/repo` by default), validate it fully, then write ferry's config. Refuses a non-empty target. `--expect-sha256 <hash>` verifies integrity. |
| `agents scaffold` | Set up a project repo for AI-agent work from the templates in the config repo's `agents/` area: an `AGENTS.md` router, `CLAUDE.md`/`GEMINI.md` bridges, and committed `.work/` handoff files. `--private` instead creates a `.work.local/` layer hidden via `.git/info/exclude` — for repos you don't own, it leaves zero tracked trace. `--attribution` (mutually exclusive with `--private`) instead installs a `prepare-commit-msg` hook that appends a kernel-style `Assisted-by:` trailer to agent-authored commits, for repos that require AI disclosure. Idempotent; never overwrites or repoints anything it didn't create. Works in linked worktrees and submodules. |
| `agents adopt` | One-time migration of an existing symlink-based instruction setup into the config repo: imports the source files (never modifying the source directory), then swaps each `$HOME` bridge symlink for a ferry-managed copy in a single journalled transaction — any failure rolls back and the symlinks return. Refuses directory-level bridges with exact instructions rather than writing through them. |
| `version` | Print the version; `--verbose` adds the Go version and platform. |

The agents *domain* itself (which instruction files deploy where, for which coding
CLIs) is enabled with `agents = true` in the manifest and rides the normal
lifecycle: `apply` deploys, `status`/`diff` report drift, `capture` treats the repo
as authoritative, and `restore agents` reverts the domain even when the config
repo is gone. See [Agents](docs/agents.md).

## Principles

- **Declarative**: a dependency manifest and config are the single source of truth;
  ferry reconciles the machine to match.
- **Selective, both ways**: a per-machine manifest declares what ferry manages, so
  off-scope things (a one-off font, an experimental colour scheme) are never synced.
  Machine-specific settings live in a gitignored `.local` layer that always wins, so
  deliberate per-machine differences are never overwritten.
- **Reversible**: every change is backed up first; `ferry restore` returns your managed
  files and terminal settings to their pre-ferry state. It keeps ferry's own backup store
  so the restore is itself reversible; `ferry restore --purge-without-recovery`
  additionally removes that store (irreversible). A fresh `ferry init` adopts your
  existing `~/.zshrc`, and `apply` refuses to replace a substantial file with an empty or
  blank one without `--force`, so your config is never silently erased.
- **No admin assumed**: ferry installs to `~/.local/bin` and never requires `sudo` or
  root, so it works on any account, including locked-down or managed machines. It never
  edits your shell on its own.
- **Safe with secrets**: ferry never touches `~/.ssh/`. SSH keys and other secrets are
  handled out-of-band and never committed. See [docs/ssh.md](docs/ssh.md).
