## ferry apply

Reconcile this machine to the repo (deploy dotfiles, terminal settings)

### Synopsis

Reconcile this machine to the repo.

apply moves config in one direction — repo -> this machine — deploying the
repo's version onto the machine. It writes in-scope dotfiles and terminal
settings, layering the per-machine .local overlay last. It is idempotent and
safe to re-run after every git pull. A run with changes walks a guided,
domain-grouped review that applies safe changes automatically and prompts on
risky ones; non-interactively, risky changes fail closed. A file you have
edited locally is left untouched for "ferry capture" to bring back — apply
never overwrites uncaptured local work. Dependency installs are a separate,
gated step run with --deps.

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

