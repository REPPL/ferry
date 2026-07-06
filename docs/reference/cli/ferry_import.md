## ferry import

Ingest a ferry bundle into a fresh config repo

### Synopsis

Ingest a portable ferry bundle into a fresh config repo.

import validates the bundle FULLY before writing anything (integrity, resource
caps, path/symlink/.git rejection, secret re-scan, version), then lays the shared
files down into a fresh repo (default ~/.config/ferry/repo), git-inits it, writes
ferry's machine config, and stops. It REFUSES a non-empty target (no clobber).
Pass --expect-sha256 <hash> to verify the bundle against the SHA256 export
printed. Run "ferry apply" afterwards to reconcile this machine.

```
ferry import <bundle> [flags]
```

### Options

```
      --expect-sha256 string   verify the bundle's overall SHA256 before importing (out-of-band tamper check)
  -h, --help                   help for import
      --include-local          also import the bundle's local layer (only if it was exported --include-local)
      --out string             target repo directory (default ~/.config/ferry/repo; must be empty/absent)
```

### SEE ALSO

* [ferry](ferry.md)	 - Carries your terminal, dotfiles, and dependencies across machines

