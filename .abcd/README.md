# .abcd/

ferry's development namespace. Everything here is developer-facing material — it
stays in the repo (transparent) but never ships to a ferry user. User-facing
documentation lives under [`../docs/`](../docs/), which is the only dev-adjacent
tree written for the reader of the CLI.

- **[`development/`](development/)** — the durable record (committed): plans,
  research, and architecture decisions. The specification the build works from.
- **[`work/`](work/)** — working memory, committed shared orientation:
  `CONTEXT.md` (load-first standing facts) and `DECISIONS.md` (one-line decision
  log).

Local, ephemeral working memory — the `NEXT.md` handover, scratch output, and
logs — lives in the git-ignored `.work.local/` alongside these, hidden via
`.git/info/exclude` and never committed. See [`../AGENTS.md`](../AGENTS.md) for
the working-memory split.
