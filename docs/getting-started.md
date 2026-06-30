# Getting started

`ferry` has two starting points, mirroring git's own `init` / `clone` duality:

- **Fresh** — you have a machine whose setup you want to capture into a new repo.
- **Existing** — you already have a ferry repo and want to set up another machine.

## Prerequisites

`ferry` itself is a single self-contained binary, but it leans on a few host tools for
the work it deliberately does **not** reimplement:

| Prerequisite | Why ferry needs it | When |
|---|---|---|
| **macOS** | Terminal configuration (iTerm2, Apple Terminal) uses macOS-native preference mechanisms. The cross-platform core (dotfiles, dependencies, dev-tree, backup/restore) is built for Linux too — **Linux support is coming soon**. | always (Linux: soon) |
| **`git`** | ferry does not embed git. It shells out to clone your config repo, and you commit/push your captured changes with git yourself. ferry preflights it and tells you how to install it if missing. | `init`, `capture` |
| **A package manager** (Homebrew on macOS) | Only for installing declared dependencies via `ferry apply --deps`. ferry never installs the package manager for you — it uses whatever is present and tells you if none is. | `apply --deps` only |

You do **not** need admin/root, and you do not need to pre-install anything ferry
manages — that's ferry's job. The above are the host tools ferry stands on.

> **Linux is coming soon.** The core is cross-platform; Linux terminal-emulator support
> is still in progress.

---

## Install ferry

```bash
curl -fsSL https://raw.githubusercontent.com/REPPL/ferry/main/install.sh | bash
```

This installs **only** the `ferry` binary to `~/.local/bin` — **no admin rights
required**, so it works on any account, including locked-down or managed machines. If
`~/.local/bin` isn't already on your PATH, the installer prints the one line to add to
your shell config (it never edits your shell itself). It does not install Homebrew or
run anything else.

To build from source instead:

```bash
git clone https://github.com/REPPL/ferry.git && cd ferry
make build
mkdir -p ~/.local/bin
cp bin/ferry-$(uname -s | tr A-Z a-z)-* ~/.local/bin/ferry
# If ~/.local/bin isn't on your PATH, add this to your shell config:
#   export PATH="$HOME/.local/bin:$PATH"
```

---

## Fresh: capture this machine

```bash
ferry init                # first-run setup; asks before scaffolding ~/ABCDevelopment
ferry capture             # review your config; approve each change, route shared/local
git -C <your-ferry-repo> commit -am "Initial capture"
git -C <your-ferry-repo> push
```

`ferry capture` is interactive and selective: it shows you each change and lets you
route it **shared** (synced to every machine) or **local** (this machine only). Things
outside the manifest's scope — a one-off font, an experimental colour scheme — are
never captured.

---

## Existing: set up another machine

```bash
ferry init                # clones your ferry repo over HTTPS, writes ferry's config
ferry diff                # preview what will change on this machine (optional)
ferry apply               # reconcile this machine to the repo
ferry apply --deps        # install dependencies (separate, explicit step)
```

`ferry apply` is idempotent and safe to re-run — run it after every `git pull`. It
never overwrites local edits you haven't captured: if a managed file has uncaptured
changes, `apply` reports a conflict instead of clobbering it.

---

## Day to day

```bash
ferry status              # what has drifted on this machine?
ferry capture             # pull chosen local changes back into the repo
ferry apply               # pull repo changes onto this machine
ferry doctor              # is this machine set up correctly?
```

## Undoing ferry

```bash
ferry restore             # return managed files to their pre-ferry state
```

Every change ferry makes is backed up first, so `restore` returns the machine to
exactly how it was before ferry touched it.

## Next

- [Configuration](configuration.md) — scope, the manifest, and the `.local` layer
- [SSH](ssh.md) — ferry is hands-off with `~/.ssh/`; here's how to move keys yourself
