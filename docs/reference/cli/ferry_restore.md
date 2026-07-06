## ferry restore

Reverse ferry's changes, returning to the pre-ferry state from backup

### Synopsis

Reverse ferry's changes.

restore reverts managed files AND registered terminal preference domains to the
pre-ferry baseline from ferry's automatic backup, snapshotting current state
first so the revert is itself reversible. restore <domain> scopes it to one
target. --packages additionally uninstalls only the packages ferry installed.

```
ferry restore [flags]
```

### Options

```
  -h, --help                     help for restore
      --packages                 also uninstall packages ferry recorded as self-installed
      --purge-without-recovery   remove ferry's own config AND the backup store after restore — DESTROYS the ability to undo this restore or re-restore later (irreversible; the default keeps the backup store)
      --yes                      skip the confirmation prompt
```

### SEE ALSO

* [ferry](ferry.md)	 - Carries your terminal, dotfiles, and dependencies across machines

