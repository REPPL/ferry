## ferry agents adopt

Migrate an existing symlink-based instruction directory into ferry

### Synopsis

Migrate an existing symlink-based agent-instruction setup into ferry.

adopt imports <dir>'s source files (general.md, coding.md, templates/,
skills/, agents/, hooks/) into the config repo's agents/ area, then replaces
each $HOME bridge symlink that points into <dir> with a ferry-managed
regular-file copy (the removed symlinks are listed in a timestamped record
first). It is non-destructive: <dir> itself is only ever read — delete its old
sync script yourself once you are satisfied.

Requires the agents domain to be enabled ("agents = true" under [manage]).

```
ferry agents adopt <dir> [flags]
```

### Options

```
  -h, --help   help for adopt
```

### SEE ALSO

* [ferry agents](ferry_agents.md)	 - Onboard project repos and migrate agent-instruction setups

