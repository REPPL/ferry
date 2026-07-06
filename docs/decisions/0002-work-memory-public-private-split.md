# 0002 — Public/private split of the .work memory tiers

- Status: Accepted
- Owner: REPPL
- Date: 2026-07-07
- Superseded-by:

## Context and problem statement

Agent sessions accumulate two kinds of working memory: durable project facts and
decisions worth committing, and per-session handoff notes plus scratch output
that should never reach git. We need a layout that keeps the two apart by
construction, so a curated standing fact and a throwaway log cannot end up in the
same tier by accident.

## Considered options

- **`.work/` (committed) + `.work.local/` (git-ignored)** — a tracked tier for
  durable memory and a checkout-local tier (hidden via `.git/info/exclude`) for
  session and runtime artefacts.
- **A `.work.public/` tier** — invert the naming so the committed tier is
  explicitly marked public. Rejected: it reads as if `.work/` were private by
  default and adds a third directory name to remember.
- **Name the curated standing-facts file `MEMORY.md`** — rejected: it collides
  with Claude Code's auto-memory `MEMORY.md`, so a tool and a human artefact
  would fight over the same filename.

## Decision outcome

Chosen: **`.work/` committed, `.work.local/` git-ignored**.

- `.work/` (committed) holds `DECISIONS.md` (an append-only decision log) and
  `CONTEXT.md` (curated, load-first standing facts).
- `.work.local/` (git-ignored via `.git/info/exclude`, never `.gitignore`) holds
  `NEXT.md` (session handoff), `scratch/`, and `logs/`.

The curated file is `CONTEXT.md`, not `MEMORY.md`, to avoid the Claude Code
auto-memory collision.

### Consequences

- A committed `.work/` is world-visible the moment the repo is public, so the
  repository's privacy rules apply to its contents: no local absolute paths, no
  secrets, no real hostnames or live data in `CONTEXT.md` or `DECISIONS.md`.
- Session handoff and runtime artefacts stay local by construction — they live
  under `.work.local/`, which no tracked file records and git never sees.
- Agents load `.work/CONTEXT.md` first for standing facts and write session
  handoff to `.work.local/NEXT.md`.
