## ferry export

Write a portable, secret-scanned bundle of the config repo

### Synopsis

Write a portable bundle of the config repo for an offline move.

export bundles ONLY git-tracked shared files (never untracked/ignored junk),
secret-scans every text file's content AND every path, and refuses ~/.ssh and
symlink entries. A tracked binary is scanned for embedded private-key markers
and withheld if any are found, otherwise bundled. The result is a self-contained
.zip you move to another account or
machine and ingest with "ferry import". Secrets and the per-machine local layer
are never included unless you pass --include-local. export prints the bundle's
reproducible SHA256 — exporting the same tracked sources always yields the same
digest — so you can verify the move with "ferry import --expect-sha256".

```
ferry export [flags]
```

### Options

```
  -h, --help            help for export
      --include-local   also bundle the per-machine local layer (local/**, ferry.local.toml)
      --out string      path to write the bundle (default ./ferry-bundle.zip; must be OUTSIDE the repo)
```

### SEE ALSO

* [ferry](ferry.md)	 - Carries your terminal, dotfiles, and dependencies across machines

