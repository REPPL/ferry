## ferry bundle

Move the config repo offline as a portable bundle

### Synopsis

Companion commands for moving the config repo offline as a portable bundle.

When you can't clone the repo on the destination — a second user account on the
same machine, or a machine with no network path to the repo — a bundle carries
the setup across instead. "bundle export" writes a portable, secret-scanned .zip
of the repo's tracked shared files and prints its reproducible SHA256;
"bundle import <bundle>" validates that .zip fully and ingests it into a fresh
config repo on the other side. Secrets, ~/.ssh, and the per-machine local layer
are never carried unless you opt in with --include-local.

### Options

```
  -h, --help   help for bundle
```

### SEE ALSO

* [ferry](ferry.md)	 - Carries your terminal, dotfiles, and dependencies across machines
* [ferry bundle export](ferry_bundle_export.md)	 - Write a portable, secret-scanned bundle of the config repo
* [ferry bundle import](ferry_bundle_import.md)	 - Ingest a ferry bundle into a fresh config repo

