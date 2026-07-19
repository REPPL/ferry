## ferry work prune

Apply the cargo retention policy now

```
ferry work prune [<project-dir>] [flags]
```

### Options

```
      --bundle string   remove exactly the bundle with this SHA256 instead of applying keep-last-N
  -h, --help            help for prune
      --keep int        keep the last N bundles (default: [work] keep, else 5)
```

### Options inherited from parent commands

```
      --allow-sync-root   accept a cargo store under a cloud-synced directory (iCloud/Dropbox/…) for this run
```

### SEE ALSO

* [ferry work](ferry_work.md)	 - Carry in-flight project work between accounts as an explicit handover

