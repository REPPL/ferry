<div align="center">

  <img src="docs/assets/img/logo.png" alt="ferry Logo" width="150">

  <h1>ferry</h1>

  <p>Carries your terminal, dotfiles, and dependencies across user accounts and machines.</p>

  <a href="https://github.com/REPPL/ferry/releases"><img src="https://img.shields.io/github/v/release/REPPL/ferry?cacheSeconds=300" alt="Release"></a>
  <a href="https://github.com/REPPL/ferry/blob/main/LICENSE"><img src="https://img.shields.io/github/license/REPPL/ferry?cacheSeconds=300" alt="License"></a>
  <img src="https://img.shields.io/github/last-commit/REPPL/ferry?cacheSeconds=300" alt="Last commit">
  <br />
  <img src="https://img.shields.io/badge/macOS-000000?logo=apple&logoColor=white" alt="macOS">
  <img src="https://img.shields.io/badge/Linux-coming%20soon-FCC624?logo=linux&logoColor=black" alt="Linux: coming soon">

</div>

---

`ferry` carries your terminal setup across user accounts and machines. Define your
configuration once in a git repo; `ferry` reconciles any machine to match it, and
pulls local changes back when you want to harmonise them everywhere.

> `ferry` is a research project for my personal use. Not ready for production as things
> may break.

## Documentation

- [Getting started](docs/getting-started.md): zero to a working setup
- [Configuration](docs/configuration.md): the manifest, scope, and the `.local` layer
- [SSH](docs/ssh.md): how ferry treats SSH (hands-off) and how to move keys yourself

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/REPPL/ferry/main/install.sh | bash
```

> **Note:** the `curl … | bash` installer verifies each binary against a pinned checksum
> and is enabled per release. Building from source works today; see
> [RELEASE.md](docs/RELEASE.md) for how releases are cut.

Installs the single `ferry` binary to `~/.local/bin`. If that's not on your PATH, the
installer prints the one line to add. It does not install Homebrew or run anything else.
(No `sudo`, no shell edits: see [Principles](#principles).)

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

## Commands

Every command is run as `ferry <command>` (e.g. `ferry init`).

| Command | What it does |
|---|---|
| `init` | First-run setup: locate/clone the config repo into ferry's own space (`~/.config/ferry/repo` by default), write ferry's config. |
| `init --github [name]` | Create a **new private** GitHub repo via the `gh` CLI's existing auth and manage it as ferry's HTTPS remote. Needs `gh` authenticated; ferry stores no token. Always private, never reuses an existing repo, and won't push a file that looks like a secret. Add `--yes` for non-interactive use. |
| `apply` | Reconcile this machine to the repo (deploy dotfiles, terminal settings). Idempotent; safe to re-run. Dependencies install behind `apply --deps`. |
| `capture` | Pull local changes back into the repo. Interactive: approve each change, route it *shared* (synced everywhere) or *local* (this machine only). |
| `status` | Report config drift (what changed on this machine). |
| `doctor` | Report machine/tool health. |
| `diff` | Preview what `apply` would change. |
| `restore` | Reverse ferry's changes, returning the machine to its pre-ferry state from an automatic backup. |
| `export` | Write a portable, secret-scanned `.zip` bundle of the repo's tracked shared files for an offline move. Prints the bundle SHA256. Never includes secrets, `~/.ssh`, or the local layer (unless `--include-local`). |
| `import` | Ingest a bundle into a fresh config repo (`~/.config/ferry/repo` by default), validate it fully, then write ferry's config. Refuses a non-empty target. `--expect-sha256 <hash>` verifies integrity. |
| `version` | Print the version; `--verbose` adds the Go version and platform. |

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
