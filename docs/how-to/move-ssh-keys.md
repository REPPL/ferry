# Move SSH keys to a new machine

ferry never touches `~/.ssh/` (and won't carry it for you — see
[Why ferry stays out of `~/.ssh/`](../explanation/ssh.md) for the reasoning). Moving
your SSH setup is a task you do yourself, out-of-band. This page is the recipe.

## Goal

Get working SSH access on a new machine without ever putting a private key into a git
repo or any synced store.

## Move your SSH setup

Pick whichever fits your threat model:

- **Generate a fresh key per machine** (recommended): then add the new public key to
  the servers/services you use. No private key ever leaves the machine it was made on:
  ```bash
  ssh-keygen -t ed25519 -C "you@machine"
  ssh-copy-id user@host        # or paste the .pub into GitHub/GitLab/etc.
  ```
- **Transfer an existing key out-of-band**: encrypted USB, AirDrop, or a one-shot
  `scp` over an already-trusted connection. Never email it or put it in a repo.
- **Use a secrets manager / SSH agent**: e.g. the 1Password SSH agent, which holds
  keys and serves them to `ssh` without them living as files you copy around.

## Set the correct permissions

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

## Result

SSH works on the new machine, no private key ever entered a repo, and permissions
satisfy `StrictModes`. `ferry doctor` can *report* (read-only) when these look wrong; it
never changes them.

## Related documentation

- [Why ferry stays out of `~/.ssh/`](../explanation/ssh.md): the rationale and the untouchable-`~/.ssh` invariant.
