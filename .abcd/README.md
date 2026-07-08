# .abcd/

ferry's development namespace. Everything here is developer-facing material — it
stays in the repo (transparent) but never ships to a ferry user. User-facing
documentation lives under [`../docs/`](../docs/), which is the only dev-adjacent
tree written for the reader of the CLI.

- **[`development/`](development/)** — the durable record (committed): plans,
  research, and architecture decisions. The specification the build works from.

Working memory (`.work/`, committed shared orientation, and `.work.local/`,
git-ignored local ephemera) currently sits at the repo root; a phase-2 migration
folds it under this namespace (`.abcd/work/`, `.abcd/.work.local/`). Until then,
see [`../AGENTS.md`](../AGENTS.md) for the working-memory split.
