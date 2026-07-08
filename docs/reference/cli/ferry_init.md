## ferry init

First-run setup: locate/clone the config repo and write ferry's config

### Synopsis

First-run, once-per-machine setup.

init locates or clones your config repo (over HTTPS — no SSH key needed) into
ferry's own space (~/.config/ferry/repo by default), writes ferry's config file
(~/.config/ferry/config.toml), and creates or confirms this machine's
ferry.local.toml manifest before any mutation. A bare "ferry init" starts a
fresh repo at that default location; "ferry init --fresh <dir>" places it
elsewhere. It then SHOWS the apply plan and stops; pass --apply (with --yes to
skip the prompt) to actually reconcile this machine.

```
ferry init [flags]
```

### Options

```
      --apply           run apply at the end of init (default: show the plan and stop)
      --fresh           set up a NEW config repo (capture this machine) instead of cloning
      --github          create a NEW private GitHub repo via the gh CLI and manage it as ferry's remote
  -h, --help            help for init
      --repair          review opt-in repairs (hardcoded home paths, duplicate PATH exports, dead source lines) in the wizard
      --wizard string   first-run wizard mode: off | interactive | answers:<file> (default: interactive on a real tty, else off)
      --yes             assume yes for init's confirmations (the --github create-confirm and the closing --apply confirm); does NOT skip the wizard — use --wizard=off
```

### SEE ALSO

* [ferry](ferry.md)	 - Carries your terminal, dotfiles, and dependencies across machines

