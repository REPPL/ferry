# Reconciling drift and conflicts

ferry keeps a machine and a git config repo in agreement. This page explains how
it decides what to do when they disagree, and — the crux — **where a conflict is
resolved depending on which way config is moving**. It is background reading; for
the commands themselves see the [commands reference](../reference/commands.md).

## The mental model: three inputs per file

For every managed file, ferry looks at three things:

- **live** — the file as it is on your machine right now.
- **repo** — the file in the git config repo, the single source of truth.
- **last-applied** — a hash of what ferry last deployed to this machine. It is
  ferry's memory of the last agreed state, and it is what lets ferry tell a
  repo-side change apart from a local one.

Two commands move config between live and repo, in opposite directions:

- [`apply`](../reference/commands.md) moves **repo → this machine**: it deploys
  the repo's version onto the machine.
- [`capture`](../reference/commands.md) moves **this machine → the repo**: it
  brings your local edits back into the source of truth.

`last-applied` is the fixed point both directions measure against. If live has
moved past it, you have edited the file locally. If repo has moved past it,
someone changed the source of truth (a teammate, another machine, a `git pull`).
If *both* have moved, the two directions want the same file — that is a conflict.

## The four states, and what `apply` does

ferry classifies each file into one state and acts on it. `status` and `diff`
report the state without changing anything; `apply` is the command that acts.

| State | What it means | What `apply` does |
|---|---|---|
| **Clean** | live, repo and last-applied all agree | Nothing — there is nothing to do. |
| **Repo-ahead** | repo moved past last-applied; live has no uncaptured edit (or the file is a first-touch adoption) | Deploys the repo content, **backing up the prior state first** so [`restore`](../reference/commands.md) can reverse it. |
| **Locally drifted** | live moved past last-applied but repo did *not* | Leaves your edit alone. It is a **capture candidate** — there is no repo change to deploy. |
| **Conflict** | *both* live and repo moved past last-applied | **Refuses** to overwrite. You choose the winner (see below). |

A fresh target that does not yet exist on the machine is a fifth case,
*missing* — `apply` simply creates it, like a repo-ahead deploy with nothing to
back up.

Two details worth calling out:

- **First-touch adoption is not a conflict.** The first time `apply` meets an
  in-scope file that ferry has never managed — one with no last-applied record —
  it takes ownership rather than refusing: it backs the live file up to an
  immutable baseline and then deploys the repo content. Because there is no
  last-applied record, there is no uncaptured edit to protect, so this is
  treated as repo-ahead, not conflict. The backup keeps it reversible.
- **A conflict is reserved for the genuine case:** ferry *has* a last-applied
  record for the file, your live copy differs from it (you edited a file ferry
  previously managed, without capturing), *and* the repo has also moved on. Only
  then does `apply` refuse.

## Where conflicts resolve, per direction

This is the important part. The two directions handle disagreement differently,
and deliberately so.

### Inbound (`apply`): refuse, never silently overwrite

When `apply` meets a genuine conflict it stops and leaves the file byte-for-byte
unchanged. It never merges and never silently overwrites uncaptured local work.
It prints the remedy for that file's kind:

- **Dotfiles** — the fix is to pick a winner: `ferry capture` to keep your local
  edit, or `ferry apply --force` to take the repo's copy.
- **Agents and terminal-config targets are repo-authoritative** — there is no
  capture pass for them. The remedy is to update the repo copy (or source) to
  match, or `ferry apply --force` to take the repo's version.

So an inbound conflict is resolved by *you* choosing a direction — keep local
(capture) or take repo (`--force`) — not by ferry guessing.

### Outbound (`capture`): opt in per change

`capture` never sweeps everything up. It is per-change and per-file:

- It offers only files that have actually drifted, and shows you each change.
- You **approve each change** hunk by hunk (`y` / `n`).
- For a file you approve, you **route** it: `shared` (synced everywhere) or
  `local` (this machine only) — or reject it entirely.
- A **secret scan runs before any write**, so a sensitive value is blocked from
  reaching the repo; you can send it to the out-of-repo secret store instead.

Anything you do not want upstream — a machine-specific tweak, a throwaway
experiment — you route `local` or simply skip. With no input, capture writes
nothing. It also never commits or pushes; that stays your decision.

### Why the asymmetry is deliberate

The two directions are intentionally not mirror images:

- **`apply` is conservative:** it never destroys uncaptured local work. On a
  conflict it refuses and hands the decision back to you.
- **`capture` is selective:** it sends nothing you did not approve, and blocks
  secrets automatically.

Neither direction auto-merges, because the only correct resolver of a genuine
conflict is a human who understands what both edits meant. ferry's job is to make
the disagreement legible and reversible, then let you decide.

## Related documentation

- [Commands](../reference/commands.md): every `ferry <command>` and what it does.
- [The agents domain](agents.md): why agent files are repo-authoritative.
- [Compatibility contract](../reference/compatibility.md): which surfaces are stable.
