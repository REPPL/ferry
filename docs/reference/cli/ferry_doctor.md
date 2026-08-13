## ferry doctor

Report machine/tool health

### Synopsis

Report machine and tool health.

doctor checks the host tools ferry stands on — git (required), zsh and a
package manager (recommended, reported [warn] when absent) — and reports
anything that needs attention, with the recommended next step. It also
reports ~/.ssh permissions (stat only — ferry never reads, writes, or captures
anything under ~/.ssh; this read-only report is doctor's sanctioned exception)
and observes the managed-target invariants: deployed targets are regular-file
copies, never symlinks, resolving inside $HOME and never under ~/.ssh. A
missing required tool or a genuine invariant breach is reported [fail] and
doctor exits non-zero.

```
ferry doctor [flags]
```

### Options

```
  -h, --help   help for doctor
```

### SEE ALSO

* [ferry](ferry.md)	 - Carries your terminal, dotfiles, and dependencies across machines

