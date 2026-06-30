<div align="center">

  <img src="docs/assets/img/logo.png" alt="ferry Logo" width="150">

  <h1>ferry CLI</h1>

  <p>Carries your terminal, dotfiles, and dependencies across user accounts and machines.</p>

  <a href="https://github.com/REPPL/ferry/releases"><img src="https://img.shields.io/github/v/release/REPPL/ferry?cacheSeconds=300" alt="Release"></a>
  <a href="https://github.com/REPPL/ferry/blob/main/LICENSE"><img src="https://img.shields.io/github/license/REPPL/ferry?cacheSeconds=300" alt="License"></a>
  <img src="https://img.shields.io/github/last-commit/REPPL/ferry?cacheSeconds=300" alt="Last commit">
  <br />
  <img src="https://img.shields.io/badge/macOS-000000?logo=apple&logoColor=white" alt="macOS">
  <img src="https://img.shields.io/badge/Linux-FCC624?logo=linux&logoColor=black" alt="Linux">

</div>


---

`ferry` carries your terminal setup across user accounts and machines. Define your configuration once in a git repo; `ferry` reconciles any user account to match it, and pulls local changes back when you want to harmonise them across accounts and machines.

## Commands

- `ferry` `init`: First-run setup on a new machine: clones/links the config repo, writes ferry's config, and (optionally, with your confirmation) scaffolds your development directory tree.
- `ferry` `apply`: Reconciles this machine to the repo: Installs dependencies (`brew bundle` on Mac), deploys dotfiles, and applies terminal settings. Idempotent and unattended, thus safe to run after every `git pull`.
- `ferry` `capture`: Pulls local changes back into the repo. Interactive and selective: You approve each change and route it as shared (synced everywhere) or local (this machine only).
- `ferry` `status` / `ferry` `doctor`: Report config drift and machine health. `ferry` `diff` previews what apply would change.
- `ferry` `restore`: Reverses `ferry` completely, returning the machine to its exact *pre-ferry* state from an automatic backup.

## Principles

- Declarative: A `Brewfile` (Mac) and config manifest are the single source of truth; ferry reconciles the machine to match.
- Selective, both ways: A per-machine manifest declares what ferry manages, so off-scope things (a one-off font, an experimental colour scheme) are never synced. Machine-specific settings live in a gitignored .local layer that always wins, so deliberate per-machine differences are never overwritten.
- Reversible: Every change is backed up first; ferry restore leaves no trace.
- Safe with secrets: SSH keys and other secrets are handled out-of-band and never committed.
