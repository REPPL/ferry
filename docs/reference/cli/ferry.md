## ferry

Carries your terminal, dotfiles, and dependencies across machines

### Synopsis

ferry carries your terminal setup across user accounts and machines.

Define your configuration once in a git repo; ferry reconciles any machine to
match it, and pulls local changes back when you want to harmonise them
everywhere.

```
ferry [flags]
```

### Options

```
  -h, --help   help for ferry
```

### SEE ALSO

* [ferry agents](ferry_agents.md)	 - Onboard project repos and migrate agent-instruction setups
* [ferry apply](ferry_apply.md)	 - Reconcile this machine to the repo (deploy dotfiles, terminal settings)
* [ferry capture](ferry_capture.md)	 - Pull local changes back into the repo (interactive, selective)
* [ferry diff](ferry_diff.md)	 - Preview what apply would change
* [ferry doctor](ferry_doctor.md)	 - Report machine/tool health
* [ferry export](ferry_export.md)	 - Write a portable, secret-scanned bundle of the config repo
* [ferry import](ferry_import.md)	 - Ingest a ferry bundle into a fresh config repo
* [ferry init](ferry_init.md)	 - First-run setup: locate/clone the config repo and write ferry's config
* [ferry restore](ferry_restore.md)	 - Reverse ferry's changes, returning to the pre-ferry state from backup
* [ferry status](ferry_status.md)	 - Report config drift (what changed on this machine)
* [ferry sync](ferry_sync.md)	 - Publish captured changes and pull remote ones for a managed repo
* [ferry version](ferry_version.md)	 - Print ferry's version (add --verbose for build details)

