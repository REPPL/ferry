## ferry work pack

Bundle this project's work state into the cargo store

```
ferry work pack <project-dir> [flags]
```

### Options

```
      --acknowledge stringArray   let a secret-flagged file travel as-is, named item/path (repeatable; pinned to its current content)
      --allow-empty               permit a pack without the handover note (memory/transcript-only cargo)
      --exclude stringArray       leave a named item out of the cargo (repeatable; recorded in the manifest)
  -h, --help                      help for pack
```

### Options inherited from parent commands

```
      --allow-sync-root   accept a cargo store under a cloud-synced directory (iCloud/Dropbox/…) for this run
```

### SEE ALSO

* [ferry work](ferry_work.md)	 - Carry in-flight project work between accounts as an explicit handover

