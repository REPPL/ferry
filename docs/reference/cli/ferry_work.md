## ferry work

Carry in-flight project work between accounts as an explicit handover

### Synopsis

Carry a project's in-flight work between accounts as an explicit baton pass.

A project's work state — the session handover note, the run journal, the coding
agent's per-project memory, and the redacted transcript store — never travels
with the project repo. "work pack" bundles it into a cargo store on shared or
portable media; "work receive" lands the latest cargo on another account,
backup-first and behind divergence guards; "work status" shows cargo, claims,
and drift; "work restore" reverts exactly the last receive. Work state is not
configuration: it has an owner, changes every session, and never merges
silently — so these are handover verbs, not a reconcile domain.

The cargo store is configured per machine in ~/.config/ferry/config.toml:

    [work]
    store = "/Users/Shared/ferry-cargo"

### Options

```
      --allow-sync-root   accept a cargo store under a cloud-synced directory (iCloud/Dropbox/…) for this run
  -h, --help              help for work
```

### SEE ALSO

* [ferry](ferry.md)	 - Carries your terminal, dotfiles, and dependencies across machines
* [ferry work pack](ferry_work_pack.md)	 - Bundle this project's work state into the cargo store
* [ferry work prune](ferry_work_prune.md)	 - Apply the cargo retention policy now
* [ferry work receive](ferry_work_receive.md)	 - Land the latest cargo for this project here
* [ferry work restore](ferry_work_restore.md)	 - Revert exactly the last work receive on this account
* [ferry work status](ferry_work_status.md)	 - Show cargo, claims, divergence, and store size

