## ferry sync

Publish captured changes and pull remote ones for a managed repo

### Synopsis

Publish local changes and pull remote ones in one command.

sync is the everyday update for a managed (route-2) repo: it pulls remote work,
commits your locally-captured changes, and pushes them — WITHOUT ever losing
local work or force-pushing. It integrates the remote first from a clean
baseline, gates the whole push range for secrets, and pushes a single explicit
ref. On a conflict it leaves your machine byte-for-byte unchanged and asks you
to resolve with git. It never runs apply — run "ferry apply" afterwards to
deploy the pulled changes.

```
ferry sync [flags]
```

### Options

```
      --allow-unmanaged   sync a repo not marked managed (still HTTPS-only, secret-gated, never force-pushed)
  -h, --help              help for sync
  -m, --message string    commit message for locally-captured changes (default: a generated one)
```

### SEE ALSO

* [ferry](ferry.md)	 - Carries your terminal, dotfiles, and dependencies across machines

