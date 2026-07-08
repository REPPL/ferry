# Scaffold a project repo

## Goal

Set a **project** repo up for the multi-tool agent pipeline — the AGENTS.md router, the
`CLAUDE.md`/`GEMINI.md` bridges, the committed `.work/` memory, the `docs/` map, and the
local-only runtime layout — in one idempotent command. For the model behind the agents
domain, see [The agents domain](../explanation/agents.md).

## Command

```bash
ferry agents scaffold [--private|--attribution] <repo-dir> [name]
```

Sets a **project** repo up for the multi-tool pipeline. `name` defaults to the
directory's base name. Idempotent; never overwrites an existing file.

## What it creates

The layout separates **committed project memory** from **local-only runtime
artefacts**: everything under `.work/` is meant to be committed, while
scratch output and logs always live in `.work.local/` and never reach git.
Both modes create `.work.local/scratch/` and `.work.local/logs/` and hide the
whole `.work.local/` directory via the checkout-local git `info/exclude` —
`.gitignore` is never touched.

Default (tracked) mode, for a repo you own:

| Item | Role |
|---|---|
| `AGENTS.md` | Router stamped from `agents/templates/AGENTS.md`, with `{{PROJECT}}` and `{{DATE}}` substituted |
| `CLAUDE.md`, `GEMINI.md` | Relative symlinks to `AGENTS.md` **inside the project repo** (project-tracked content — ferry does not deploy these to `$HOME`) |
| `docs/README.md` | The user-facing documentation map, stamped from `agents/templates/docs-README.md`: the four Diátaxis directories and the root-markdown allowlist |
| `.abcd/development/plans/`, `.abcd/development/research/`, `.abcd/development/decisions/` | Developer-record directories, created up front — dated plans and research (`YYYY-MM-DD-topic.md`) and ADRs (MADR, `NNNN-title.md`); `docs/` stays user-facing Diátaxis, whose content directories are created on first use |
| `.work/DECISIONS.md`, `.work/CONTEXT.md` | Committed decision log and curated standing facts (the load-first summary) |
| `.work.local/NEXT.md`, `.work.local/scratch/`, `.work.local/logs/` | Local-only session handoff and runtime artefacts, hidden via git `info/exclude` |
| `.pre-commit-config.yaml` | Copied from the template, only when the repo has none |

`--attribution` (tracked mode only) marks a repo that **requires AI
disclosure** — a research project, say — overriding the workspace
no-attribution default:

| Item | Role |
|---|---|
| `.githooks/prepare-commit-msg` | Stamped from `agents/templates/prepare-commit-msg` and made executable: appends a kernel-style `Assisted-by:` trailer to agent-authored commits only; human commits are untouched. Never `Co-Authored-By` — the human is always the author, the tool is disclosed |
| `core.hooksPath = .githooks` | Set on the current clone when the target is a git repo; it is per-clone configuration, so every fresh clone re-runs `git config core.hooksPath .githooks` (the output says so) |
| `## AI attribution` section | Appended to `AGENTS.md` when the heading is absent, stating the policy |

`--attribution` and `--private` are mutually exclusive and the combination is
refused: a repo you do not own is not yours to set attribution policy in.

`--private` mode, for a repo you do not own — zero tracked trace:

| Item | Role |
|---|---|
| `.work.local/NEXT.md`, `DECISIONS.md`, `ISSUES.md` | The same logs plus a private observation list, alongside the `scratch/` and `logs/` dirs |
| git `info/exclude` entry | Hides `.work.local/` locally; never committed or pushed |

Anything already sitting where a bridge symlink would go is left untouched: a
real file is skipped with a message (merge it into `AGENTS.md` first), and a
symlink pointing anywhere other than `AGENTS.md` is your own wiring — it is
reported and skipped, never repointed.

All three git layouts are recognised: a plain `.git` directory, and a `.git`
file (a linked worktree or a submodule), whose `gitdir:` pointer is followed.
In a linked worktree the exclude entry is written to the **shared** common git
directory's `info/exclude`, which is where git reads it.

## Result

The repo carries the full agent-pipeline scaffold, committable in one go (bar the
git-ignored `.work.local/`). Re-running the command is safe: it only fills in what is
missing and never overwrites an existing file.

## Related documentation

- [The agents domain](../explanation/agents.md): the repo-authoritative model behind these files.
- [Adopt an existing agent config](adopt-agent-config.md): migrate a symlink-based instruction directory into ferry.
