# SSH

**ferry does not touch `~/.ssh/` at all** — it never reads, copies, captures, commits,
or modifies your private keys, public keys, `config`, or `known_hosts`.

This is deliberate. Moving SSH material is your job, done out-of-band. Here's why, and
how to do it safely.

## Why ferry stays out of `~/.ssh/`

- **Private keys must never enter a git repo.** Once committed, a key is compromised —
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

## Moving your SSH setup to a new machine

Pick whichever fits your threat model:

- **Generate a fresh key per machine** (recommended) — then add the new public key to
  the servers/services you use. No private key ever leaves the machine it was made on:
  ```bash
  ssh-keygen -t ed25519 -C "you@machine"
  ssh-copy-id user@host        # or paste the .pub into GitHub/GitLab/etc.
  ```
- **Transfer an existing key out-of-band** — encrypted USB, AirDrop, or a one-shot
  `scp` over an already-trusted connection. Never email it or put it in a repo.
- **Use a secrets manager / SSH agent** — e.g. the 1Password SSH agent, which holds
  keys and serves them to `ssh` without them living as files you copy around.

## Correct permissions (set these yourself)

OpenSSH's `StrictModes` (on by default) refuses keys if permissions are too open. Set:

| Path | Mode | |
|---|---|---|
| `~/.ssh` | `700` | directory, user-only |
| `~/.ssh/config` | `600` | |
| private keys (`id_*`, `*.pem`, …) | `600` | |
| `*.pub` public keys | `644` | public keys need not be secret |
| `~/.ssh/authorized_keys` | `600` | |

```bash
chmod 700 ~/.ssh
chmod 600 ~/.ssh/config ~/.ssh/id_* ~/.ssh/authorized_keys 2>/dev/null
chmod 644 ~/.ssh/*.pub 2>/dev/null
```

`ferry doctor` can *report* (read-only) when these look wrong — it never changes them.

## What about syncing config across machines?

Tools like chezmoi and yadm can sync SSH material by **encrypting** it (age/GPG) before
committing. ferry deliberately does not — it keeps zero crypto dependencies and zero
key-management burden. If you want encrypted SSH sync, use one of those tools alongside
ferry, or keep your `~/.ssh/config` in a separate private, encrypted store.
