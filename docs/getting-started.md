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

If you already have a `~/.zshrc`, a fresh `ferry init` **adopts** it: the repo's
managed source starts as a copy of your current file, so the first `ferry apply`
matches what is already on disk and changes nothing. Your existing shell config is
never zeroed. If you have no `~/.zshrc`, ferry seeds no shell source at all —
`.zshrc` is still in scope, and your first `ferry capture` fills the repo from the
file once you have one.

`ferry capture` is interactive and selective: it shows you each change and lets you
route it **shared** (synced to every machine) or **local** (this machine only). Things
outside the manifest's scope—a one-off font, an experimental colour scheme—are
never captured.

---

## Let ferry manage a private GitHub repo

If you'd rather not create a GitHub repo, add a remote, and push by hand, let ferry do
it. `ferry init --github [name]` creates a **new private** repo through the GitHub CLI's
existing login and wires it as ferry's HTTPS remote, so `ferry capture` can push and
`ferry apply` on another machine can pull.

```bash
gh auth login                       # once: authenticate the GitHub CLI (if you haven't)
ferry init --github                 # creates a private repo named ferry-config
ferry init --github my-dotfiles     # or pick your own name
ferry init --github my-dotfiles --yes   # non-interactive (scripts, CI): skip the confirm
```

What it guarantees:

- **Needs `gh` authenticated.** ferry uses your existing `gh` login and **stores no
  token** — the credential stays in `gh`'s own keyring. Run `gh auth login` (and
  `gh auth setup-git`) first.
- **Always private.** ferry only ever creates a private repo and verifies it is private
  before pushing; it never passes `--public`.
- **Never touches an existing repo.** If a repo with that name already exists, ferry
  aborts and asks you to pass a different name — it never reuses or overwrites one.
- **Won't push a file that looks like a secret.** The same secret scan `capture` uses
  runs before the first commit and again before the push: if your `~/.zshrc` looks like
  it holds a private key or token, ferry refuses and tells you to move it out of band
  (a secret store or `~/.zshrc.local`).
- **HTTPS only.** The remote ferry sets is always `https://…`; it never sets an `ssh://`
  remote and never touches `~/.ssh`.

Non-interactive runs (a script or CI with no terminal) **require `--yes`** so ferry
never silently creates and pushes to an unexpected account.

`ferry sync` publishes your captured changes and pulls remote ones — the everyday
update for a managed repo. It never force-pushes and leaves your machine unchanged on
a conflict. Run `ferry apply` after to deploy pulled changes.

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
changes, `apply` reports a conflict instead of clobbering it. It also refuses to
replace a substantial existing file with an empty or blank repo source — that would
erase your config, so `apply` stops and names the file instead. Pass `--force` to
override (it warns and backs the file up first, so `ferry restore` can recover it),
or run `ferry capture` to save the current file into the repo before applying.

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
