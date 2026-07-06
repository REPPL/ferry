# Adopt an existing agent config

## Goal

Migrate an existing symlink-based instruction directory (the `sync.sh` era) into ferry,
reversibly, without losing the ability to `ferry restore` back to the old symlinks. For
the model behind the agents domain, see [The agents domain](../explanation/agents.md).

## Command

```bash
ferry agents adopt <dir>
```

Migrates an existing symlink-based instruction directory (the `sync.sh` era)
into ferry. Requires the domain to be enabled. It is non-destructive: `<dir>`
is only ever read.

## Steps

1. **Find the bridges**: every symlink at a managed location — harness
   targets, the devtree file, `~/.claude/{skills,agents,hooks}` plus their
   immediate entries, and any symlinked **ancestor** of those — that resolves
   into `<dir>` is identified. A **directory-level** bridge (a symlinked
   `~/.claude` itself, a whole-directory `hooks` or skill link) aborts the
   adopt loudly before anything is touched: replacing it would leave a real
   directory where the backup baseline recorded a symlink, a transition the
   backup engine cannot snapshot, so the swap would not be reversible — and
   ferry never writes *through* such a link either. The error lists the exact
   `rm` for each (the adopted directory keeps the real files); remove them
   and re-run. File-level bridges proceed.
2. **Import**: copies `<dir>`'s `general.md`, `coding.md`, `templates/`,
   `skills/`, `agents/`, and `hooks/` into the config repo's `agents/` area.
   An identical repo file is a quiet no-op; a differing one is skipped with a
   message (reconcile manually) — a re-run cannot clobber repo edits. A
   generated `combined.md` and the old `bin/` scripts are not imported. Every
   destination passes the same symlink-refusing repo guard as any other repo
   write. The bridge list is also written to a timestamped record under
   ferry's state directory.
3. **The swap, as one transaction**: within a single journalled backup-engine
   run, each bridge symlink is removed through the backup machinery (its link
   target is captured in the baseline and the journal) and the managed
   regular-file copies are written in its place. If anything fails, the whole
   run rolls back — the symlinks come back and every written copy is
   reverted, so a half-migrated machine is not a reachable state. After
   success, `ferry restore` (full or `ferry restore agents`) recreates the
   original symlinks from the baseline.

## Result

Afterwards, ferry prints what to delete by hand (the old sync script) and
reminds you that other domains reconcile as usual with `ferry apply`. Set
`devtree` in `[agents]` **before** adopting if the old setup linked a
workspace-level `CLAUDE.md`; otherwise that one bridge is left for you to
remove.

## Related documentation

- [The agents domain](../explanation/agents.md): the repo-authoritative model and apply/restore semantics.
- [Scaffold a project repo](scaffold-a-repo.md): set a fresh project repo up for the pipeline.
