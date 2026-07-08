# ferry's CLI surface against the state of the art

Date: 2026-07-08 · Type: explanation / research

This note audits ferry's command-and-flag surface, measures it against the
leading config/dotfile managers, and judges whether the top level carries more
verbs than it needs. It changes no code: it is a recommendation the maintainer
can accept, reject, or defer, verb by verb.

The headline: ferry exposes **twelve top-level commands**. That is at the
crowded end of the field but not an outlier — chezmoi carries far more. The
problem is not the raw count; it is that a handful of those verbs earn their
top-level slot weakly — `export` and `import` are a matched pair the field would
nest under one noun, `diff` duplicates `apply --dry-run`, and `sync` has no
precedent in any surveyed tool. The core lifecycle (`apply`, `capture`,
`restore`, `status`, `init`) and the already-nested `agents` noun are all
correctly shaped. A disciplined pass brings the everyday surface down to a
**five-verb core** without losing any capability.

## Current surface (from the code)

Enumerated from `cmd/*.go` (`rootCmd.AddCommand(...)` registrations in
`cmd/commands.go`, `cmd/agents.go`, `cmd/export.go`, `cmd/import.go`,
`cmd/version.go`), cross-checked against `docs/reference/commands.md` and the
generated `docs/reference/cli/*.md`.

Two files that look like commands are **not** commands: `cmd/context.go` is the
internal `loadContext` helper plus the `~/.ssh` path guard, and `cmd/wizard.go`
is the first-run wizard machinery folded into `init`. Neither registers a
verb. So the true surface is twelve top-level verbs (one of which, `agents`, is
a parent noun with two subcommands).

| Command | Level | Purpose | Flags |
|---|---|---|---|
| `init` | top | First-run setup: locate/clone the config repo, write ferry's config, run the adoption wizard | `--fresh`, `--yes`, `--apply`, `--github`, `--no-wizard`, `--repair`, `--wizard-answers <file>` |
| `apply` | top | Reconcile this machine to the repo (deploy dotfiles, terminal settings) | `--deps`, `--dry-run`, `--force`, `--skip-wizard` |
| `capture` | top | Pull local changes back into the repo (interactive, selective, secret-gated) | *(none)* |
| `sync` | top | Publish captured changes and pull remote ones for a managed repo | `--message/-m <msg>`, `--allow-unmanaged` |
| `status` | top | Report config drift (git-status-like classification) | *(none)* |
| `doctor` | top | Report machine/tool health and check managed-target invariants | *(none)* |
| `diff` | top | Preview what `apply` would change (read-only) | *(none)* |
| `restore` | top | Reverse ferry's changes to the pre-ferry baseline from backup | `--packages`, `--yes`, `--purge-without-recovery` |
| `export` | top | Write a portable, secret-scanned `.zip` bundle of tracked shared files | `--out <path>`, `--include-local` |
| `import <bundle>` | top | Ingest a bundle into a fresh config repo, validate it, write ferry's config | `--out <dir>`, `--expect-sha256 <hash>`, `--include-local` |
| `agents` | top (noun) | Parent for agent-domain companion operations (no direct run) | *(none)* |
| `agents scaffold <repo-dir> [name]` | sub | Set a project repo up for the multi-tool agent pipeline | `--private`, `--attribution` (mutually exclusive) |
| `agents adopt <dir>` | sub | Migrate an existing symlink-based instruction directory into ferry | *(none)* |
| `version` | top | Print the version | `--verbose/-v` |

There are **no persistent/global flags** — every flag is local to its command.
Total distinct flags: 24 across the tree. `init` alone carries 7; `apply` 4.

## SOTA findings

Seven leading tools were surveyed, all against tier-1 sources (official docs and
manuals). They span the full spectrum of surface size, and the smaller cores
converge on a common vocabulary.

**Surface-size spectrum (minimal → maximal):** GNU Stow (one command, three mode
flags) < dotbot (one config-driven runner, no verbs) < mackup (three verbs) <
Nix home-manager (~12) < yadm (~17 own verbs plus all of git) < chezmoi (~50).
ferry's twelve top-level verbs sit between home-manager and yadm — mid-field,
not extreme.

**The recurring core is a capture / apply / reverse triad plus inspect verbs.**
Reduced to essentials, every tool exposes some form of:

| Concept | chezmoi | yadm | Stow | dotbot | home-manager | mackup | ferry |
|---|---|---|---|---|---|---|---|
| deploy / apply | `apply` | `git checkout` | *(default)* | `./install` | `switch` | `restore` | `apply` |
| capture local → source | `add` / `re-add` | `git add` | `--adopt` | *(manual git)* | — | `backup` | `capture` |
| reverse / remove | `forget` / `destroy` | `git rm` | `-D` | `clean` | `uninstall` | `uninstall` | `restore` |
| preview diff | `diff` | `git diff` | `-n` | `--dry-run` | — | — | `diff` |
| status | `status` | `git status` | — | — | `generations` | — | `status` |
| init / onboard | `init` | `init`/`clone` | — | — | `init` | — | `init` |

ferry's `apply` / `capture` / `restore` naming maps almost exactly onto mackup's
`backup` / `restore` / `uninstall` triad while borrowing chezmoi's clearer
intent-based verbs. The two tools closest to ferry's remit (git-backed,
capture + apply + reverse) are **chezmoi** — the rich convention-setter — and
**mackup** — the minimal triad. ferry's core is well within convention.

Key conventions extracted, each with its tier-1 basis:

- **`status` and `diff` stay separate.** Every tool that has both keeps them
  distinct: chezmoi's `status` is terse and scriptable, its `diff` is a full
  unified patch — copied straight from git, which never merged the two. No
  surveyed tool merges them. So ferry's having *both* `status` and `diff` is
  correct and idiomatic; the redundancy to watch is elsewhere.
  (chezmoi command reference; git(1) porcelain-vs-plumbing.)

- **"What would apply change" is either a `diff` verb or a `--dry-run`/`-n`
  flag, never both.** Feature-rich tools give it a verb (`chezmoi diff`);
  minimal tools give it a flag (`stow -n`, `dotbot --dry-run`). ferry ships
  *both* `diff` and `apply --dry-run` for the same product — the one duplication
  the field would not tolerate. (GNU Stow manual; dotbot README; chezmoi
  reference.)

- **`init` absorbs onboarding; no tool has a standalone `wizard`/`setup`
  verb.** chezmoi's `init <repo>` clones, prompts via templates, and can apply
  in one shot; home-manager's `init [--switch]` generates and optionally
  activates. ferry already follows this — its wizard is folded into `init`, not
  a separate verb — which the field endorses. (chezmoi reference; home-manager
  tools appendix.)

- **Sync is delegated to git, not reimplemented.** yadm and dotbot expect the
  user to run `git push`/`git pull` directly; chezmoi's one sanctioned wrapper
  is `update` (= `git pull` + `apply`), named for the user's *intent*, not the
  mechanism. No surveyed tool ships a bespoke `sync`/`export`/`import` verb for
  remote movement. ferry's `sync` (pull + commit + push, deliberately *without*
  apply) is a publish wrapper with no direct field analogue — closest to
  chezmoi `update` but inverted (publish rather than update). (yadm manual;
  chezmoi reference.)

- **Archive/bundle I/O is nested under a noun.** git groups `bundle create` /
  `verify` / `unbundle` under one `git bundle`; chezmoi keeps `archive` / `dump`
  / `import` as archive tooling, and `import` is tarball-only. Top-level flat
  `export`/`import` verbs for portable bundles are not the norm. (git(1);
  chezmoi reference.)

- **"Adopt an existing file" has no settled home** [CONTESTED]: Stow makes it a
  `--adopt` flag, chezmoi folds it into `add`, home-manager refuses in-place
  adoption entirely. The weight of the feature-rich tools is against a dedicated
  top-level `adopt` verb — it belongs on the capture path. ferry already keeps
  file adoption inside `capture`/`init` and reserves `agents adopt` for the
  distinct symlink-migration case, which is consistent with this. (GNU Stow
  manual; chezmoi reference; home-manager manual.)

- **git's porcelain/plumbing split** is the guiding philosophy: keep the top
  level a small set of stable, task-focused verbs; push low-level or scripting
  operations into a separate, independently-stable tier. (git(1).)

Sources (all tier 1): chezmoi command reference
(chezmoi.io/reference/commands); yadm manual (yadm-dev/yadm `yadm.md`) and
yadm.io; GNU Stow manual (gnu.org/software/stow); dotbot README
(github.com/anishathalye/dotbot); home-manager manual tools appendix
(home-manager.dev); mackup README (github.com/lra/mackup); git(1)
porcelain-vs-plumbing (git-scm.com/docs/git).

## Per-command verdicts

Each verdict is one of KEEP / KEEP-BUT-RENAME / FOLD-INTO-X / DEMOTE-TO-FLAG /
REMOVE, with a rationale grounded in the SOTA conventions above and ferry's own
purpose, plus the migration cost (ferry is pre-1.0, so a breaking CLI change is
allowed but must carry a `Breaking` changelog entry per
`docs/reference/compatibility.md`).

### `apply` — KEEP (core)

The irreducible verb: reconcile the machine to the repo. Every tool in the
field has this exact concept under this or a near-identical name (chezmoi
`apply`, home-manager `switch`, mackup `restore`, stow `stow`). No change.

### `capture` — KEEP (core)

The reverse of `apply`: pull live edits back into the source of truth. chezmoi
splits this into `add` (new file) and `re-add` (update tracked files);
ferry folds both into one interactive, secret-gated verb, which is the better
shape for ferry's "review each change and route it shared/local" model. Keep
the single verb. The name `capture` is ferry's own; it reads well against
`apply` and is worth keeping over chezmoi's `add`/`re-add` split.

### `status` — KEEP (core)

git-style drift report. Universal. No change.

### `restore` — KEEP (core)

ferry's differentiator: a backed-up, reversible undo of everything ferry
deployed. No mainstream dotfile manager offers this as a first-class,
baseline-backed operation (mackup's `uninstall` is the nearest, and it is
weaker). This is load-bearing to ferry's "every change is backed up first"
promise. Keep, top-level.

### `init` — KEEP (secondary, justified)

Universal bootstrap verb (chezmoi `init`, yadm `clone`, home-manager
`init`). The concern is not the verb but its **7 flags** — the widest in the
tree — which signal that `init` is doing three jobs: clone-or-create, run the
wizard, and optionally apply. See the flag audit below; the verb stays.

### `diff` — KEEP the verb, DEMOTE the duplicate flag

`diff` is read-only "what would `apply` change" — and ferry **also** ships
`apply --dry-run` with exactly that contract (`cmd/apply.go` reads a `dry-run`
flag whose own help says "preview changes without writing (see also: ferry
diff)"). Two advertised names for one behaviour is the one duplication the field
would not tolerate: tools give the preview *either* a verb (`chezmoi diff`) *or*
a flag (`stow -n`, `dotbot --dry-run`), never both. Keeping the `diff` **verb**
is correct and idiomatic — chezmoi and git both keep a preview verb distinct
from `status`. So the recommendation is to **keep `diff`** as the canonical,
discoverable preview and **demote `apply --dry-run` to an unadvertised alias**
(or drop the flag), rather than the reverse. Migration cost: low — the flag can
keep working through one deprecation window while docs advertise only `diff`.

### `doctor` — KEEP (secondary, justified)

Health/invariant check. Common in mature tools (chezmoi `doctor`, `brew
doctor`, `flutter doctor`). It also verifies ferry's security invariants (no
symlinked targets, nothing under `~/.ssh`, everything inside `$HOME`)
read-only, which is genuinely distinct from `status` (drift) — `doctor` answers
"is the environment and my own footprint sane", `status` answers "what
diverged". Keep.

### `sync` — KEEP-BUT-SCRUTINISE (secondary, weakly justified)

`sync` is a convenience wrapper: pull remote, commit captured changes, push —
without ever running `apply` or force-pushing. It does real work the primitives
do not compose for free (a safe, secret-gated, non-force publish sequence), so
it is not pure sugar. But it sits awkwardly against the field: no surveyed tool
ships a bespoke `sync` verb — yadm and dotbot expect the user to run `git
push`/`git pull` directly, and chezmoi's one sanctioned wrapper is `update`
(= `git pull` + `apply`), named for the user's *intent*. ferry's `sync` is the
inverse of chezmoi `update` (it publishes rather than updates, and pointedly
does *not* apply — its own help tells the user to run `apply` afterwards), so it
has no direct precedent and automates half of a two-step ritual. The verb is
defensible for the git-shy target user, but it is the strongest candidate for
eventual folding into a git-passthrough model. Recommendation: **keep for now**, flag for review
once usage data exists. Do not add more flags to it. Migration cost of removal:
medium (it is the everyday-update verb for managed repos; removing it re-exposes
raw git steps).

### `export` / `import` — FOLD into a `bundle` noun (secondary, justified capability, wrong shape)

These are a matched pair — write a portable, secret-scanned, reproducible-SHA
`.zip` for an offline move, and ingest one — but they occupy two top-level
slots and share the `--include-local` flag, which is the classic signal of a
noun wanting to own its verbs. The field nests this kind of thing: git puts
`bundle create` / `bundle verify` / `bundle unbundle` under one `git bundle`
noun; chezmoi keeps archive I/O under `chezmoi archive` and `dump`.
Recommendation: **fold to `ferry bundle export` / `ferry bundle import`** (and a
future `ferry bundle verify` for the `--expect-sha256` check has an obvious
home). This reclaims two top-level slots for one clearly-scoped noun and reads
better (`export`/`import` at the top level are ambiguous about *what*).
Migration cost: low–medium (two verbs move under a noun; old spellings can alias
through one deprecation window). If the maintainer values the shorter top-level
spelling for a common offline-move workflow, KEEP-as-is is defensible — this is
a genuine judgement call, flagged below.

### `agents` — KEEP as a noun, but confirm the boundary (secondary, justified)

`agents` is already correctly a parent noun with subcommands (`scaffold`,
`adopt`), not a flat verb — this is the shape the field uses for grouped,
domain-specific operations, and it is the right call. The only question is
whether it should sit at the top level or under a broader grouping. It should
**stay top-level**: the agents domain is a first-class ferry concern (it has its
own section in the manifest and rides the normal `apply`/`status`/`diff`/
`restore` lifecycle), and burying it under a generic `config`/`manage` grouping
would hide a headline feature. No change.

### `version` — KEEP (universal)

Standard. No change. (Cobra also provides `--version`; the subcommand is the
conventional redundancy and is fine.)

## Recommended lean surface

The everyday core a user must learn shrinks to **five verbs** —
`init`, `apply`, `capture`, `status`, `restore` — with `diff` and `doctor` as
the two secondary verbs they reach for occasionally, and everything else nested
or aliased.

Before → after (top-level surface):

| Before (12 top-level) | After (proposed) |
|---|---|
| `init` | `init` |
| `apply` | `apply` (`--dry-run` demoted to an unadvertised alias of `diff`) |
| `capture` | `capture` |
| `status` | `status` |
| `restore` | `restore` |
| `doctor` | `doctor` |
| `diff` | `diff` (kept as the one canonical preview verb) |
| `sync` | `sync` (kept, frozen — no new flags; review later) |
| `export` | `bundle export` |
| `import` | `bundle import` |
| `agents` (+scaffold/adopt) | `agents` (+scaffold/adopt) — unchanged |
| `version` | `version` |

Net top-level verbs: **12 → 11** — `export` + `import` collapse into one
`bundle` noun; `diff` is kept as the single canonical preview and the duplicate
`apply --dry-run` flag is demoted, so no verb is lost there. The *core a
newcomer must learn* is 5 (`init`, `apply`, `capture`, `status`, `restore`); the
*full everyday set* is 7 (core + `diff` + `doctor`). That lands ferry
comfortably inside the field's norms while keeping every capability reachable.

### Flag audit

- **`init` carries 7 flags** — the tree's widest. `--yes`, `--no-wizard`, and
  `--wizard-answers` all steer the wizard's interactivity and overlap in intent
  (`--yes` implies `--no-wizard`); `--repair` only functions with an interactive
  wizard or `--wizard-answers`. This is a lot of surface for one verb. No cut is
  urgent, but the cluster is worth a dedicated simplification pass — e.g. a
  single `--wizard=off|interactive|answers:<file>` mode flag could replace three
  booleans. Flagged as a maintainer decision, not a recommendation.
- **`apply --dry-run` vs `diff`** — the redundancy addressed above; collapse to
  one advertised spelling.
- **`--include-local`** appears on both `export` and `import` — expected for a
  matched pair, and it moves cleanly under the `bundle` noun.
- **`restore --purge-without-recovery`** is a long, scary, correctly-named
  irreversible flag — keep exactly as is.
- No redundant or dead flags found elsewhere; `capture`, `status`, `diff`,
  `doctor` are flagless, which is the right restraint.

## Ranked change list (value of simplification ÷ migration cost)

1. **Fold `export` + `import` → `bundle export` / `bundle import`.** *(high
   value ÷ low–medium cost)* Reclaims two top-level slots, groups a matched
   pair, gives `--expect-sha256` a natural `bundle verify` home. Pure rename;
   alias the old spellings for one minor release, `Breaking` changelog entry.
2. **Advertise one preview spelling: keep the `diff` verb, demote `apply
   --dry-run`.** *(high value ÷ low cost)* Removes a genuine
   two-names-one-behaviour redundancy. The field keeps a preview *verb* distinct
   from status (git, chezmoi), so `diff` is the idiomatic canonical form;
   `apply --dry-run` becomes an unadvertised alias (or is dropped) through one
   deprecation window.
3. **Simplify `init`'s wizard flag cluster** (7 → ~4 via a mode flag). *(medium
   value ÷ medium cost)* Not a removal, a consolidation; touches the widest verb.
   Do it as its own pass, well-tested, since `init` is the first thing a new user
   runs.
4. **Freeze `sync`; revisit against usage.** *(low value now ÷ medium cost to
   remove)* Keep it, add no flags, and reconsider folding it into a
   git-passthrough model once there is evidence of how often it is used versus
   users driving git directly.
5. **Leave `agents`, `doctor`, `restore`, `version`, `status`, `capture`,
   `apply`, `init` as-is.** *(no change)* These are correctly shaped and
   correctly placed.

## Open decisions for the maintainer

- **`bundle` noun vs. flat `export`/`import`.** The fold is the field-idiomatic
  shape, but flat top-level verbs are shorter to type for a common offline-move.
  Genuine judgement call — grouping wins on tidiness, flat wins on ergonomics.
- **Whether to keep `apply --dry-run` at all** once `diff` is the advertised
  preview. Retaining it as a silent alias costs nothing; dropping it is a
  cleaner surface. Low-stakes either way.
- **`sync`'s long-term place.** Convenience wrapper for the git-shy user, or a
  crutch that should give way to a git-passthrough? Needs usage evidence.
- **`init`'s flag consolidation.** Whether the wizard-mode flags are worth
  reshaping into one mode flag, given `init` runs once per machine and the
  current flags each map to a documented behaviour.
