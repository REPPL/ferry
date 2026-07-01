# Getting started

`ferry` has two starting points, mirroring git's own `init` / `clone` duality:

- **Fresh**: you have a machine whose setup you want to capture into a new repo.
- **Existing**: you already have a ferry repo and want to set up another machine.

## Prerequisites

`ferry` itself is a single self-contained binary, but it leans on a few host tools for
the work it deliberately does **not** reimplement:

| Prerequisite | Why ferry needs it | When |
|---|---|---|
| **macOS** | Terminal configuration (iTerm2, Apple Terminal) uses macOS-native preference mechanisms. The cross-platform core (dotfiles, dependencies, backup/restore) is built for Linux too: **Linux support is coming soon**. | always (Linux: soon) |
| **`git`** | ferry does not embed git. It shells out to clone your config repo, and you commit/push your captured changes with git yourself. ferry preflights it and tells you how to install it if missing. | `init`, `capture` |
| **A package manager** (Homebrew on macOS) | Only for installing declared dependencies via `ferry apply --deps`. ferry never installs the package manager for you: it uses whatever is present and tells you if none is. | `apply --deps` only |

You do **not** need admin/root, and you do not need to pre-install anything ferry
manages: that's ferry's job. The above are the host tools ferry stands on.

> **Linux is coming soon.** The core is cross-platform; Linux terminal-emulator support
> is still in progress.

---

## Install ferry

```bash
curl -fsSL https://raw.githubusercontent.com/REPPL/ferry/main/install.sh | bash
```

> **Note:** the `curl … | bash` installer verifies each binary against a pinned checksum
> and is enabled per release. Building from source (below) works today; see
> [RELEASE.md](RELEASE.md) for how releases are cut.

This installs **only** the `ferry` binary to `~/.local/bin`: **no admin rights
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
ferry init                # first-run setup; starts a new config repo at ~/.config/ferry/repo
ferry capture             # review your config; approve each change, route shared/local
git -C <your-ferry-repo> commit -am "Initial capture"
git -C <your-ferry-repo> push
```

A bare `ferry init` creates the repo at ferry's own default location,
`~/.config/ferry/repo`: you do not need to pick a path. To place it somewhere
else, pass a directory: `ferry init --fresh ~/somewhere`.

`ferry capture` is interactive and selective: it shows you each change and lets you
route it **shared** (synced to every machine) or **local** (this machine only). Things
outside the manifest's scope—a one-off font, an experimental colour scheme—are
never captured.

---

## Existing: set up another machine

Point `ferry init` at your existing ferry repo as a positional argument: an HTTPS URL or
a local/`file://` path. ferry clones it into its own space (`~/.config/ferry/repo` by
default), then writes ferry's config. (A bare `ferry init`, with no source, takes the
**Fresh** path above and sets up a new repo at the same default location.)

```bash
ferry init https://github.com/REPPL/ferry.git   # clone your ferry repo over HTTPS, write ferry's config
ferry diff                # preview what will change on this machine (optional)
ferry apply               # reconcile this machine to the repo
ferry apply --deps        # install dependencies (separate, explicit step)
```

`ferry apply` is idempotent and safe to re-run: run it after every `git pull`. It
never overwrites local edits you haven't captured: if a managed file has uncaptured
changes, `apply` reports a conflict instead of clobbering it.

## Move to another account or machine offline

When you can't clone the repo on the destination: a second user account on the same Mac,
or a machine with no network path to your repo: move a self-contained bundle instead.
`ferry export` writes a secret-scanned `.zip` of the repo's tracked shared files;
`ferry import` ingests it into a fresh config repo on the other side.

```bash
# On the source machine/account:
ferry export --out /Users/Shared/ferry-bundle.zip   # prints the bundle path + SHA256

# On the destination machine/account, after moving the zip there:
ferry import --expect-sha256 <sha256> /Users/Shared/ferry-bundle.zip
ferry apply                                          # reconcile the destination
```

**Same-Mac, two accounts:** account B usually cannot read account A's home directory, so
write the bundle somewhere both accounts can reach: `--out /Users/Shared/ferry-bundle.zip`,
or move it via a USB drive or AirDrop. Convey the SHA256 that `export` printed
separately (a message, not the same channel as the file) and pass it to
`import --expect-sha256` so a tampered or corrupt bundle is refused. `import` writes into
`~/.config/ferry/repo` by default and refuses a non-empty target.

The bundle never contains secrets, anything under `~/.ssh/`, or the per-machine local
layer (unless you pass `--include-local` on **both** export and import). SSH keys stay a
manual copy you make yourself: see [SSH](ssh.md).

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

- [Configuration](configuration.md): scope, the manifest, and the `.local` layer
- [SSH](ssh.md): ferry is hands-off with `~/.ssh/`; here's how to move keys yourself
