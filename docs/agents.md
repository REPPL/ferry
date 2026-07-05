# The agents domain

The agents domain carries a single source of truth of agent instructions —
rules for coding CLIs such as Claude Code, Codex CLI, OpenCode, companion, and
Gemini CLI — across machines and users. Two Markdown files in the config repo
hold every rule; ferry deploys them (and any skills, sub-agents, and hooks) to
the path each tool natively reads, as **regular-file copies** reconciled by
hash. Nothing under `$HOME` is ever symlinked.

## Enabling the domain

The domain is **off by default**. Enable it in the manifest:

```toml
# ferry.toml (shared) or ferry.local.toml (this machine only)
[manage]
agents = true
```

With the domain enabled, the ordinary lifecycle applies: `ferry apply`
deploys, `ferry status` and `ferry diff` report per-target drift, and
`ferry restore agents` (or a full `ferry restore`) reverts every deployed file
to its pre-ferry state from the backup store.

## Repo layout

The domain reads from the `agents/` directory of the config repo:

| Path | Role |
|---|---|
| `agents/general.md` | Rules for all tasks, everywhere (edit this) |
| `agents/coding.md` | Rules for coding work (edit this) |
| `agents/templates/` | Scaffold templates: `AGENTS.md`, `NEXT.md`, `DECISIONS.md`, `ISSUES.md`, `pre-commit-config.yaml` |
| `agents/skills/` | Claude Code skills, deployed recursively to `~/.claude/skills/` |
| `agents/agents/` | Claude Code sub-agents, deployed recursively to `~/.claude/agents/` |
| `agents/hooks/` | Hook scripts, deployed recursively to `~/.claude/hooks/` (executable bits preserved) |

There is no committed `combined.md`: the combined content is **derived in
memory** at apply time — a fixed one-line generated header, a blank line, then
`general.md` + newline + `coding.md`, byte for byte. The render is
deterministic (no timestamps), so re-applying an unchanged repo is a no-op.

## Configuration

Everything under `[agents]` is optional. `ferry.local.toml` overrides
`ferry.toml` per key; `[agents.harness.<name>]` tables merge per name (local
wins).

```toml
[agents]
devtree   = "Workspace"     # optional workspace directory, relative to $HOME
harnesses = ["claude", "codex", "opencode", "companion", "gemini"]

[agents.harness.myharness]  # a user-defined harness: data, not code
target = ".config/myharness/RULES.md"
source = "combined"         # combined | general | coding
```

| Key | Meaning | Default |
|---|---|---|
| `devtree` | Workspace directory relative to `$HOME`. When set, `coding.md` additionally deploys to `<devtree>/CLAUDE.md` — the devtree **root**, where Claude Code's ancestor walk-up finds it. When unset, no workspace file is deployed and coding content reaches flat tools only via `combined`. | unset |
| `harnesses` | Which harnesses deploy. A declared list restricts (and orders) the set; naming an unknown harness is an error. | all known harnesses |
| `harness.<name>.target` | The harness's global instruction file, relative to `$HOME`. Required for a new harness; optional when overriding a built-in. | — |
| `harness.<name>.source` | Which content the target receives: `general`, `coding`, or `combined`. | `combined` for a new harness |

### Built-in harness registry

The registry ships as data; adding a harness never requires a code change.

| Harness | Target | Source |
|---|---|---|
| `claude` | `~/.claude/CLAUDE.md` | `general` |
| `codex` | `~/.codex/AGENTS.md` | `combined` |
| `opencode` | `~/.config/opencode/AGENTS.md` | `combined` |
| `companion` | `~/.companion/COMPANION.md` | `combined` |
| `gemini` | `~/.gemini/GEMINI.md` | `combined` |

Claude Code receives the split pair (`general` at user level, `coding` at the
devtree root) because it composes levels through its directory hierarchy; flat
tools receive the pre-merged `combined` content.

## Apply, status, diff, and restore semantics

- **Idempotent**: every target is hash-compared before writing; a clean target
  is never touched.
- **Backed up**: any pre-existing different file is recorded in ferry's backup
  store before it is overwritten, so `ferry restore` reverses the deploy.
- **Symlink refusal**: a target that is currently a symlink (for example a
  bridge left behind by an older symlink-based setup) is skipped with a clear
  message — remove it, or migrate it with `ferry agents adopt <dir>`.
- **Resolved containment**: a target whose parent directory is a symlink is
  refused when the link resolves outside `$HOME` or under `~/.ssh` — a write
  can never land outside `$HOME` through a symlinked intermediate directory.
  A parent symlink that resolves within `$HOME` is allowed.
- **Collision refusal**: a configuration in which two targets share a store
  key (for example a harness named `devtree` alongside a configured devtree)
  or a destination path (for example `devtree = ".claude"` colliding with the
  `claude` harness) is refused with an error naming both parties.
- **Repo-authoritative**: the deployed content is derived from the repo, so
  the repo copy is the place to edit. A live edit to a deployed target is
  reported by `status`/`diff` as drift and **skipped** by `apply` (ferry never
  silently discards an edit); resolve it by updating the repo copy, or
  override with `ferry apply --force` (backed up, reversible).
- **Capture**: `ferry capture` deliberately skips agents targets with one
  informative line. It never modifies user files.
- **De-scoping**: setting `agents = false` (or removing a harness) leaves the
  deployed files in place and warns that they are now unmanaged; revert them
  with `ferry restore agents`.
- **Data-loss guard**: replacing a substantial existing file with an
  empty/near-empty source is refused without `--force`, exactly as for
  dotfiles.

`ferry restore agents` resolves its revert set from a persisted record of
every destination the domain has ever applied on this machine
(`agents-targets.json` under ferry's state directory, updated at each apply).
The record — not the manifest — is authoritative, so a target that was later
de-scoped is still reverted, and no config repo is needed at all: restore
works with the repo deleted or its manifest unreadable. Recorded paths
without a baseline are skipped, so nothing ferry never touched can be
reverted. A full `ferry restore` likewise needs no repo and reverts
everything ferry ever touched.

## `ferry agents scaffold [--private] <repo-dir> [name]`

Sets a **project** repo up for the multi-tool pipeline. `name` defaults to the
directory's base name. Idempotent; never overwrites an existing file.

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
| `.work/NEXT.md`, `.work/DECISIONS.md` | Committed session handoff and decision log (nothing else lives in `.work/`) |
| `.work.local/scratch/`, `.work.local/logs/` | Local-only runtime artefacts, hidden via git `info/exclude` |
| `.pre-commit-config.yaml` | Copied from the template, only when the repo has none |

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

## `ferry agents adopt <dir>`

Migrates an existing symlink-based instruction directory (the `sync.sh` era)
into ferry. Requires the domain to be enabled. It is non-destructive: `<dir>`
is only ever read.

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

Afterwards, ferry prints what to delete by hand (the old sync script) and
reminds you that other domains reconcile as usual with `ferry apply`. Set
`devtree` in `[agents]` **before** adopting if the old setup linked a
workspace-level `CLAUDE.md`; otherwise that one bridge is left for you to
remove.
