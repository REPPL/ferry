# Context — ferry standing facts

Curated, load-first summary of the invariants that hold across the whole
codebase. Read this before touching ferry's behaviour. (Committed, so no local
paths, secrets, or live data here.)

## What ferry is

A macOS-first Go CLI that carries a terminal setup — dotfiles, terminal
settings, dependencies, and AI-agent instructions — across user accounts and
machines. A git config repo is the single source of truth: `ferry apply`
reconciles a machine to match it, `ferry capture` pulls local changes back, and
every change is backed up first so `ferry restore` reverses it.

## Non-negotiable invariants

- **No symlinks under `$HOME`.** Deployed files are regular-file copies,
  reconciled by content hash — never symlinks.
- **`~/.ssh/` is untouchable.** ferry never reads, writes, or captures anything
  under `~/.ssh` in any operation.
- **Writes are containment-checked.** A target that resolves outside `$HOME` or
  under `~/.ssh` — even through a symlinked parent — is refused, so a symlinked
  intermediate directory can never redirect a write out of bounds.
- **The agents domain is repo-authoritative.** Edit the config repo copy, not
  the deployed target; a live edit shows as drift and `apply` skips it.
- **`capture` is explicit and non-destructive.** It never rewrites a managed
  file without an explicit accept, and never auto-commits or pushes.
