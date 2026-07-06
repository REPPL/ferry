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
| `agents/templates/` | Scaffold templates: `AGENTS.md`, `NEXT.md`, `CONTEXT.md`, `DECISIONS.md`, `ISSUES.md`, `docs-README.md`, `prepare-commit-msg`, `pre-commit-config.yaml` |
| `agents/<source>/` | One directory per asset mapping (see below), deployed recursively to the mapping's target under `$HOME` with executable bits preserved per file. The built-in mappings read `agents/skills/`, `agents/agents/`, and `agents/hooks/` |

There is no committed `combined.md`: the combined content is **derived in
memory** at apply time — a fixed one-line generated header, a blank line, then
`general.md` + newline + `coding.md`, byte for byte. The render is
deterministic (no timestamps), so re-applying an unchanged repo is a no-op.

## Configuration

Everything under `[agents]` is optional. `ferry.local.toml` overrides
`ferry.toml` per key; `[agents.harness.<name>]` and `[agents.asset.<name>]`
tables merge per field (local wins), so a local table that sets only one
field overrides just that field of the shared entry.

```toml
[agents]
devtree   = "Workspace"     # optional workspace directory, relative to $HOME
harnesses = ["claude", "codex", "opencode", "companion", "gemini"]
assets    = ["skills", "agents", "hooks"]

[agents.harness.myharness]  # a user-defined harness: data, not code
target = ".config/myharness/RULES.md"
source = "combined"         # combined | general | coding

[agents.asset.githooks]     # a user-defined asset mapping: data, not code
source = "githooks"         # directory under the repo's agents/ area
target = ".githooks"        # directory under $HOME
```

| Key | Meaning | Default |
|---|---|---|
| `devtree` | Workspace directory relative to `$HOME`. When set, `coding.md` additionally deploys to `<devtree>/CLAUDE.md` — the devtree **root**, where Claude Code's ancestor walk-up finds it. When unset, no workspace file is deployed and coding content reaches flat tools only via `combined`. | unset |
| `harnesses` | Which harnesses deploy. A declared list restricts (and orders) the set; naming an unknown harness is an error. | all known harnesses |
| `harness.<name>.target` | The harness's global instruction file, relative to `$HOME`. Required for a new harness; optional when overriding a built-in. | — |
| `harness.<name>.source` | Which content the target receives: `general`, `coding`, or `combined`. | `combined` for a new harness |
| `assets` | Which asset mappings deploy. A declared list restricts (and orders) the set — leaving a built-in out removes its tree from management; naming an unknown mapping is an error. | all known mappings |
| `asset.<name>.source` | The mapping's source directory under the repo's `agents/` area. Required for a new mapping; optional when overriding a built-in. | — |
| `asset.<name>.target` | The mapping's destination directory relative to `$HOME`. Required for a new mapping; optional when overriding a built-in. | — |

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

### Built-in asset-mapping registry

Asset mappings are data too: each one copies a config-repo directory
recursively to a directory under `$HOME`, per-file executable bits preserved.
Every deployed file gets the full per-target treatment — hash-gated writes,
drift in `status`/`diff`, backup and restore, collision refusal against every
other harness and asset destination.

| Mapping | Source | Target |
|---|---|---|
| `skills` | `agents/skills/` | `~/.claude/skills/` |
| `agents` | `agents/agents/` | `~/.claude/agents/` |
| `hooks` | `agents/hooks/` | `~/.claude/hooks/` |

### Example: ferry-carried global git hooks

A user-defined mapping is all it takes to carry global git hooks across
machines:

```toml
[agents.asset.githooks]
source = "githooks"
target = ".githooks"
```

Dispatcher scripts placed in the config repo's `agents/githooks/` (one per
hook name, e.g. `pre-commit`) then deploy as executables under
`~/.githooks/`. Point git at them once per machine with
`git config --global core.hooksPath ~/.githooks`. Each dispatcher delegates
rather than replaces: it runs the current repo's own `.githooks/<name>` if
present, then the repo's `.git/hooks/<name>` (so the pre-commit framework and
any locally installed hooks keep working), exiting on the first failure. The
dispatcher scripts themselves are config-repo content, like the scaffold
templates — ferry carries and deploys them but does not ship them.

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
- **Capture**: `ferry capture` flows a live edit to a deployed agent file back
  into the config repo through the same approve and route flow as dotfiles (see
  [Capturing agent edits back](#capturing-agent-edits-back) below). It never
  rewrites the deployed file, never commits, and never pushes.
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

## Capturing agent edits back

Deployed agent files are ordinary regular files, so it is natural to edit one
in place — a skill, a hook, or a harness instruction file. `ferry capture`
brings such an edit back into the config repo so it is no longer reported as
drift forever. It is explicit and user-initiated: there is no daemon and no
file-watcher, and it never commits or pushes (you do that yourself).

Capture classifies every deployed agents target against the last-deployed
baseline — the exact bytes ferry last wrote — and acts only on the two
outcomes that call for it:

- **Locally edited** (the deployed file changed; its repo source did not): a
  capture candidate. Capture reviews the change hunk by hunk and, on your
  approval, routes it to the config repo. An **asset** file (a skill, a hook, a
  sub-agent) routes to its shared source under `agents/<source>/` or to the
  per-machine overlay `local/agents/<source>/` (gitignored, and deployed in
  preference to the shared source on the next apply — local wins). A harness
  **instruction** file sourced from a single file (`general.md` or `coding.md`)
  routes to that source; because one source feeds every harness that shares it,
  only the shared route is offered.
- **Diverged** (the deployed file **and** its repo source have both changed
  since ferry last deployed it): capture **refuses** and shows a diff. It never
  auto-merges and never guesses a winner — reconcile the two by hand, then
  `ferry apply`.

A file whose deployed content is the **derived** `general.md` + `coding.md`
combination (a flat harness's `AGENTS.md`) cannot be split back into two
sources automatically, so an edit there is reported and you are pointed at the
two source files rather than having capture guess.

Capture also offers to **adopt** new agent-shaped files: a regular file sitting
under a managed asset mapping's target directory (for example a skill you
dropped into `~/.claude/skills/`) that ferry has never deployed. What counts as
agent-shaped is defined entirely by the asset-mapping registry, so it grows as
the registry grows. On approval the file is brought into the repo under the
mapping's source directory.

Every route runs the same secret gate as the dotfile domain: a high-confidence
secret in a captured change is blocked from the repo entirely. `~/.ssh` is
never touched, and a captured write is refused if its repo destination resolves
through a symlink out of the repo.

## Task recipes

The domain's two one-off setup operations have their own task guides:

- [Scaffold a project repo](../how-to/scaffold-a-repo.md) — `ferry agents scaffold`: stamp a repo with the multi-tool pipeline layout.
- [Adopt an existing agent config](../how-to/adopt-agent-config.md) — `ferry agents adopt`: migrate a symlink-based instruction directory into ferry.
