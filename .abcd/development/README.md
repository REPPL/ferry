# Development record

ferry's durable design record — the "what / why" the build works from. Kept in
the repo (transparent) but developer-facing, never shipped to a ferry user;
user-facing documentation lives under [`../../docs/`](../../docs/). Organised
flat by artefact type, one canonical home per concept:

| Folder | What it holds | Naming |
|---|---|---|
| [`plans/`](plans) | Dated design / implementation plans — "here's how we will build X". | `YYYY-MM-DD-topic.md` |
| [`research/`](research) | Investigations that inform design without being decisions — SOTA surveys, spikes, prior-art reviews. | `YYYY-MM-DD-topic.md` |
| [`decisions/`](decisions) | Architecture Decision Records (MADR) — settled decisions, the *why* plus rejected alternatives. | `NNNN-title.md` |

**Conventions.** Plans and research notes are date-prefixed (chronological);
ADRs use a sequential `NNNN` prefix (stable cross-reference handles). Present
tense only — history lives in git. A decision extracted from a plan graduates to
an ADR in [`decisions/`](decisions).
