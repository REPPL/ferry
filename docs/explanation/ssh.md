# Why ferry stays out of `~/.ssh/`

**ferry does not touch `~/.ssh/` at all**: it never reads, copies, captures, commits,
or modifies your private keys, public keys, `config`, or `known_hosts`.

This is deliberate. Moving SSH material is your job, done out-of-band — see
[Move SSH keys to a new machine](../how-to/move-ssh-keys.md) for the safe recipes.
This page explains why ferry stays hands-off.

## The reasoning

- **Private keys must never enter a git repo.** Once committed, a key is compromised:
  history makes deletion ineffective. An allowlist that ingests only `config` + `*.pub`
  would still be one mistake away from a leak.
- **`~/.ssh/config` is itself semi-sensitive.** It exposes internal hostnames,
  usernames, jump hosts, IP addresses, and `ProxyJump`/`ProxyCommand` (which can embed
  inline secrets). In a public repo, that hands an attacker your network map.
- **`known_hosts` enables reconnaissance.** Even hashed entries are crackable on a GPU
  in minutes, revealing every host you connect to. Never commit it.
- **`Include` directives** in `config` pull in sibling files a naive sync would miss,
  silently breaking your setup on the next machine.

Staying hands-off removes this entire class of risk rather than trying to mitigate it.

## The invariant

`~/.ssh/` is untouchable across every ferry operation: `apply`, `capture`, `status`,
`diff`, and `restore` all skip it. A write whose target resolves under `~/.ssh` — even
through a symlinked parent directory — is refused by the containment check, so no ferry
path can ever land a file there. `ferry doctor` can *report* (read-only) when key or
directory permissions look wrong, but it never changes them.

## What about syncing config across machines?

Tools like chezmoi and yadm can sync SSH material by **encrypting** it (age/GPG) before
committing. ferry deliberately does not: it keeps zero crypto dependencies and zero
key-management burden. If you want encrypted SSH sync, use one of those tools alongside
ferry, or keep your `~/.ssh/config` in a separate private, encrypted store.

## Related documentation

- [Move SSH keys to a new machine](../how-to/move-ssh-keys.md): the out-of-band recipes and correct permissions.
