## ferry apply

Reconcile this machine to the repo (deploy dotfiles, terminal settings)

### Synopsis

Reconcile this machine to the repo.

apply deploys in-scope dotfiles and terminal settings, layering the per-machine
.local overlay last. It is idempotent and safe to re-run after every git pull.
A run with changes walks a guided, domain-grouped review that applies safe
changes automatically and prompts on risky ones; non-interactively, risky
changes fail closed. Dependency installs are a separate, gated step run with
--deps.

```
ferry apply [flags]
```

### Options

```
      --deps          install declared dependencies (separate, explicit step)
      --dry-run       preview changes without writing (see also: ferry diff)
      --force         overwrite uncaptured local edits on conflict
  -h, --help          help for apply
      --skip-wizard   skip the guided walkthrough (safe changes auto-apply; risky changes are refused, not prompted)
```

### SEE ALSO

* [ferry](ferry.md)	 - Carries your terminal, dotfiles, and dependencies across machines

