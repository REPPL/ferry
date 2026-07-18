# Hand work over to another account

This page walks through carrying a project's in-flight work state — the
session handover note, run journal, per-project agent memory, and redacted
transcripts — from one account to another with `ferry work`. Throughout,
**Alice** and **Bob** name two accounts of the same person: the side handing
work over and the side picking it up.

## One-time setup: the cargo store

Both accounts point at one shared directory — the cargo store. It lives on
shared or portable media (never inside a git repository, and never under a
cloud-synced directory unless you explicitly opt in).

1. Create the store once, world-writable so both accounts can use it:

   ```bash
   sudo mkdir -p /Users/Shared/ferry-cargo
   sudo chmod 777 /Users/Shared/ferry-cargo
   ```

2. On **each** account, add the store to `~/.config/ferry/config.toml`:

   ```toml
   [work]
   store = "/Users/Shared/ferry-cargo"
   ```

An SSH-mounted path or a removable volume works the same way; if the volume
is not mounted, the work verbs say so instead of creating a stray directory.

## Hand over (Alice)

1. Write the handover note — `ferry work pack` refuses without one:

   ```bash
   $EDITOR <project>/.abcd/.work.local/NEXT.md
   ```

2. Pack the project's work state into the store:

   ```bash
   ferry work pack <project>
   ```

   Every packed byte passes the secret gate. A high-confidence finding stops
   the whole pack; either fix the content at its source, leave the item out
   (`--exclude <item>`), or let the flagged file travel as-is with
   `--acknowledge <item>/<path>` (pinned to the file's current content — the
   pack re-aborts if that content changes later).

3. Check the baton:

   ```bash
   ferry work status <project>
   ```

   `status` shows the cargo, both accounts' claims, and whether anything
   changed after the handover ("handed over, not modified since" is the
   clean state).

## Pick up (Bob)

1. If the project is not cloned yet, clone it first — full history, not
   shallow (`ferry work` refuses a shallow clone, whose identity is bogus):

   ```bash
   git clone <origin> <project>
   ```

2. Land the latest cargo:

   ```bash
   ferry work receive <project>
   ```

   Every write is backed up first, and the whole receive is snapshotted, so

   ```bash
   ferry work restore <project>
   ```

   reverts exactly that receive.

## When something refuses

- **"changed since this account last held the baton"** — the destination has
  local edits the receive would overwrite. Review them; `--force` replaces
  them (the backup and snapshot still protect the previous content).
- **"bundles tie at the highest sequence"** — both accounts packed without an
  intervening receive. Receive one explicitly with `--bundle <sha256>`, or
  remove one with `ferry work prune --bundle <sha256>`.
- **"taken back by its packer"** — Alice reclaimed the baton
  (`ferry work receive` on her own account) and kept working. Wait for her
  fresh pack, or `--force` to land the superseded bundle anyway.
- **"is a linked git worktree"** — the work verbs run only in a project's
  main worktree.

## Retention

`pack` keeps the last five bundles per project (configurable via `keep` in
the `[work]` table); `ferry work prune` applies the policy on demand.
