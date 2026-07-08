# Working memory (committed)

The shared, committed tier of ferry's working memory — the standing facts the
next session needs before it can act, kept in the repo so every clone and every
agent starts from the same orientation. Mirrors the sibling `abcd-cli`'s
`.abcd/work/`.

| File | What it holds |
|---|---|
| [`CONTEXT.md`](CONTEXT.md) | Curated, load-first summary of ferry's standing facts and invariants. Read it first. |
| [`DECISIONS.md`](DECISIONS.md) | One-line-per-entry log of session decisions — rejected alternatives and chosen tradeoffs a future session would otherwise re-litigate. |

Because this tier is committed, its contents follow the repository's privacy
rules: no local absolute paths, secrets, or live data. Local, ephemeral working
memory — the `NEXT.md` handover, scratch output, and logs — lives in the
git-ignored [`../.work.local/`](../.work.local/) instead, hidden via
`.git/info/exclude` and never committed. See
[ADR 0002](../development/decisions/0002-work-memory-public-private-split.md)
for the public/private split.
