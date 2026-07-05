<div align="center">

  <img src="docs/assets/img/logo.png" alt="ferry Logo" width="150">

  <h1>ferry</h1>

  <p>Ferries your development setup across user accounts and machines.</p>

  <a href="https://github.com/REPPL/ferry/releases"><img src="https://img.shields.io/github/v/release/REPPL/ferry?cacheSeconds=300" alt="Release"></a>
  <a href="https://github.com/REPPL/ferry/blob/main/LICENSE"><img src="https://img.shields.io/github/license/REPPL/ferry?cacheSeconds=300" alt="License"></a>
  <img src="https://img.shields.io/github/last-commit/REPPL/ferry?cacheSeconds=300" alt="Last commit">
  <br />
  <img src="https://img.shields.io/badge/macOS-000000?logo=apple&logoColor=white" alt="macOS">
  <img src="https://img.shields.io/badge/Linux-core%20CI--tested-FCC624?logo=linux&logoColor=black" alt="Linux: core CI-tested">
  <br />
  <img src="https://img.shields.io/badge/status-experimental-orange" alt="Status: experimental">

</div>

---

`ferry` carries your development setup (dotfiles, agent setup, and dependencies) across
accounts and machines. Define your configuration once in a git repo; `ferry` reconciles
any machine to match it, and pulls local changes back when you want to harmonise them
everywhere.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/REPPL/ferry/main/install.sh | bash
```

Installs the single `ferry` binary to `~/.local/bin`. If that's not on your PATH, the
installer prints the one line to add.

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
- [Commands](docs/commands.md): every `ferry <command>` and what it does
- [Agents](docs/agents.md): the agents domain — one set of AI-agent instructions (CLAUDE.md, AGENTS.md, skills, hooks) carried across machines and coding CLIs
- [SSH](docs/ssh.md): how ferry treats SSH (hands-off) and how to move keys yourself
- [Compatibility](docs/reference/compatibility.md): which surfaces are stable, the pre-1.0 rule, and how on-disk state is versioned
- [Releases](docs/RELEASE.md): how releases are built, checksummed, attested, and pruned

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
