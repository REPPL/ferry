# 0001 — Sequential MADR naming for ADRs

- Status: Accepted
- Owner: REPPL
- Date: 2026-07-07
- Superseded-by:

## Context and problem statement

ferry records architecture decisions as MADR files under `docs/decisions/`. We
need one naming convention that sorts stably, reads unambiguously, and does not
force coordination between branches before a decision can be written down. A
superseded draft proposed a `<date>--<slug>` scheme (for example
`2026-07-07--adr-naming.md`); this ADR settles the convention.

## Considered options

- **Sequential `NNNN-title.md`** — a zero-padded ordinal plus a kebab-case
  title (`0001-adr-naming-sequential-madr.md`). Sorts in decision order; the
  number is a stable, quotable handle ("ADR 0001").
- **Date-prefixed `<date>--<slug>.md`** — encodes when, never collides, but two
  ADRs decided the same day sort arbitrarily and the filename carries no stable
  ordinal to cite.
- **A reservation registry** — a shared file that hands out the next number up
  front. Removes the collision risk but adds a coordination step and a second
  source of truth to keep in sync.

## Decision outcome

Chosen: **sequential `NNNN-title.md`**. It gives decisions a stable, citable
ordinal and sorts in the order they were made, which matters more than encoding
the date (git already records when each file landed). The author takes the next
free number on their own branch — no registry, no up-front reservation.

### Consequences

- Two branches opened around the same time can both mint the same `NNNN`. A
  duplicate-`NNNN` lint fails CI, so the second branch to merge renumbers its
  ADR before it can land — the collision is caught mechanically, not by
  convention alone.
- Citing a decision is stable: "ADR 0001" always names this file regardless of
  when it was written.
- The date lives inside the file (the `Date:` field), not in the filename.
