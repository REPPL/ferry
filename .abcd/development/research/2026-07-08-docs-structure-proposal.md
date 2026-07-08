# Unified repository documentation structure — proposal

Date: 2026-07-08 · Status: proposal (not adopted) · Type: research

> This note lives in ferry's current internal research home, `.work/research/`.
> If the proposal below is adopted, this file moves to
> `.dev/development/research/` (or the chosen namespace) along with the rest of
> the durable record.

The maintainer's goal: **`docs/` carries user-facing text only; developer-facing
text — plans, research, decisions, roadmap, working memory — lives elsewhere.**
The sibling `abcd-cli` repo already does this through a single `.abcd/`
namespace. This note maps both repos, contrasts them, and proposes a concrete
unified structure for ferry plus the exact migration ripple. It changes nothing;
it recommends.

---

## 1. abcd-cli — the target model

abcd-cli keeps **all** development material under one top-level namespace,
`.abcd/`, and reserves `docs/` for user-facing Diátaxis pages only. `.abcd/`
stays in-tree (transparent) but is excluded from the release artifact.

### 1.1 The three tiers (durability × sharing)

Documented in `.abcd/README.md` and `AGENTS.md`:

| Tier | Path | Committed? | Holds |
|---|---|---|---|
| **Durable record** | `.abcd/development/` | yes | brief, intents, decisions/adrs, plans, research, roadmap, principles — the specification the build works from |
| **Shared working** | `.abcd/work/` | yes | `CONTEXT.md` (current orientation), `DECISIONS.md` (append-only log), `issues/`, `reviews/` |
| **Local ephemeral** | `.abcd/.work.local/` | gitignored | `NEXT.md` handover, `scratch/`, `logs/` — per-worktree, never merge-conflicts |

### 1.2 Where each dev-doc category lives, and why

Each `development/` subdirectory carries a README that states what it is for.
Organised **flat by artefact type**, one canonical home per concept:

| Category | abcd-cli path | Naming | Rationale (from the READMEs) |
|---|---|---|---|
| Plans | `.abcd/development/plans/` | `YYYY-MM-DD-topic.md` | Dated because chronological; "how we will build X". A decision extracted from a plan graduates to an ADR. |
| Research | `.abcd/development/research/` | `notes/` dated + `prompting/`, `spikes/` | Investigations that inform design without being decisions. May seed an intent or ADR. Exempt from record-lint present-tense rules. |
| Decisions (ADRs) | `.abcd/development/decisions/adrs/` | `NNNN-title.md` (MADR) | Retrospective, settled decisions. Stable sequential handles for cross-reference. Plus `notes/`. |
| RFCs | `.abcd/development/roadmap/rfcs/` | — | Prospective, contested decisions; an accepted RFC produces an ADR. |
| Roadmap | `.abcd/development/roadmap/phases/` | `phase-N-slug.md` | Sequencing axis (replaces version language). |
| Intents | `.abcd/development/intents/{drafts,planned,shipped,disciplines,superseded}/` | `itd-N-slug.md` | Press-release-shaped user-facing "why"; lifecycle encoded by directory. |
| Brief | `.abcd/development/brief/` | numbered folders | The living canvas — what abcd IS. |
| Principles | `.abcd/development/principles/` | one per file | Distilled cross-cutting design rules. |
| Working memory | `.abcd/work/` + `.abcd/.work.local/` | `CONTEXT.md`, `DECISIONS.md` / `NEXT.md` | Durable-vs-working-vs-ephemeral split. |

`docs/` (abcd-cli) is purely the four Diátaxis directories plus `assets/`; its
`docs/README.md` explicitly points development records at `../.abcd/development/`.

### 1.3 How the split is enforced by tooling

- **`.abcd/docs-lint.json`** — `"roots": ["docs", "README.md"]`. Bans
  change-narration tense and specific harness names in user-facing prose;
  enforces `links_resolve` and a `stray_root_docs` allowlist. Runs in CI as
  `abcd docs lint` (blocking).
- **`.abcd/record-lint.json`** — `"roots": [".abcd/development"]`. A separate
  lint over the *record* (no git metadata, links resolve, lifecycle rules);
  `exempt_paths` includes `.abcd/development/research/`. Runs as
  `go run ./cmd/record-lint` (currently non-blocking drift gate).
- **`scripts/check-reviews.sh`** — deterministic gate over
  `.abcd/work/reviews/`.
- **`.gitignore`** — only `.abcd/.work.local/` is ignored; `development/` and
  `work/` are committed.
- **`.githooks/pre-commit`** reads its banlist from
  `.abcd/.work.local/private-names.txt`; `pre-push` runs `make preflight`.

The key move: **two lint roots**, one pointed at `docs/` (user-facing tone) and
one pointed at `.abcd/development/` (record integrity). The path boundary *is*
the enforcement boundary.

---

## 2. ferry — the current state

ferry mixes user-facing and developer-facing material inside `docs/`, and keeps
working memory at the repo root.

### 2.1 Current layout

```
docs/
├── tutorials/ how-to/ reference/ explanation/   # user-facing Diátaxis
├── assets/
├── decisions/     # ADRs, NNNN-title.md          ← developer-facing
├── research/      # YYYY-MM-DD-topic.md           ← developer-facing
├── plans/         # YYYY-MM-DD-topic.md           ← developer-facing
└── README.md      # the map (rows for decisions/research/plans at lines 28-30)

.work/             # committed shared working memory
├── CONTEXT.md   DECISIONS.md   research/2026-07-08-cli-surface-vs-sota.md
.work.local/       # gitignored: NEXT.md, scratch/, logs/, backups/, history/
```

So ferry already has abcd's `work/` + `.work.local/` split — just at the repo
root, not nested under a namespace — and already keeps `.work/research/`. What
it lacks is a **durable-record home separate from `docs/`**: plans, research,
and ADRs currently sit *inside* `docs/`, contradicting the stated goal.

### 2.2 Tooling wired to the current `docs/plans` + `docs/research` (+ `docs/decisions`) paths

Every reference that a migration must update (verified by
`git grep "docs/plans\|docs/research\|docs/decisions"`):

| # | File | Line(s) | What it does with the path |
|---|---|---|---|
| 1 | `internal/agents/scaffold.go` | 212 | `scaffoldDocsDirs = []string{"decisions", "research", "plans"}` — **the scaffold generator creates these under `docs/` in every repo it stamps** |
| 2 | `internal/agents/scaffold.go` | 206 | `docs-README.md` template (embedded) — the map it writes into scaffolded repos |
| 3 | `internal/agents/scaffold_golden_test.go` | 135-137, 203-205 | golden expectations asserting `docs/decisions`, `docs/plans`, `docs/research` are created |
| 4 | `internal/agents/scaffold_test.go` | 293 | asserts `docs/decisions`, `docs/research`, `docs/plans` exist |
| 5 | `scripts/check-plan-shipped.sh` | 50 | globs `"$ROOT"/docs/plans/*"$VERSION".md` for the release plan-shipped gate |
| 6 | `scripts/consistency-lint.sh` | 21, 47 | `decisions=docs/decisions` (ADR NNNN-uniqueness); exempts `docs/plans/` from the stale-`.work/NEXT.md` sweep |
| 7 | `.github/workflows/release.yml` | 70 | comment describing the `docs/plans/*<tag>.md` plan-status backstop (calls script #5) |
| 8 | `docs/README.md` | 28-30 | map rows declaring `decisions/`, `research/`, `plans/` under `docs/` |
| 9 | `CONTRIBUTING.md` | 51, 59-60, 67, 74 | "intent lives in `docs/plans/`"; ADRs "under `docs/decisions/`"; links to ADR 0001/0002 |
| 10 | `docs/how-to/cutting-a-release.md` | 36 | "a plan under `docs/plans/` marked shipped in vX.Y.Z" |
| 11 | `docs/how-to/scaffold-a-repo.md` | 35 | table row: `docs/decisions/`, `docs/research/`, `docs/plans/` created up front |
| 12 | `AGENTS.md` | 72 | link to `docs/decisions/0002-...md` |
| 13 | `.work/DECISIONS.md`, `CHANGELOG.md` | — | prose references to `docs/decisions/` (lower priority; text only) |
| 14 | **cross-repo** `../CLAUDE.md` (ABCDevelopment) | docs section | "`research/` and `plans/` under `docs/`; `decisions/` (ADRs)" — the shared convention that ferry's scaffold **propagates** |

The load-bearing point: item 1/2/3/4 mean **ferry's convention is not a private
folder choice — the scaffold tool stamps `docs/{decisions,research,plans}` into
every new repo**, and item 14 codifies it across all ABCDevelopment repos.
Changing ferry's layout is therefore a cross-repo convention change.

---

## 3. Compare and contrast

| Axis | abcd-cli | ferry (today) | Divergence / cost |
|---|---|---|---|
| **What counts as `docs/`** | user-facing Diátaxis only | Diátaxis **plus** decisions/research/plans | ferry leaks dev docs into the shipped/user tree — the exact thing the goal forbids |
| **Durable record home** | `.abcd/development/{plans,research,decisions,roadmap,intents,brief,principles}` | none — scattered inside `docs/` | ferry has no single "the spec the build works from" root |
| **Shared working memory** | `.abcd/work/` (CONTEXT, DECISIONS) | `.work/` (CONTEXT, DECISIONS) | same split, different root — already aligned in spirit |
| **Local ephemeral** | `.abcd/.work.local/` | `.work.local/` | same split, different root — already aligned |
| **ADRs** | under `development/decisions/adrs/` (dev-facing) | under `docs/decisions/` (in the user tree) | ferry treats ADRs as user-facing; abcd treats them as record |
| **Namespace** | one umbrella `.abcd/` | two roots: `docs/` (mixed) + `.work*/` | ferry has no umbrella; dev material spans `docs/` and root |
| **Lint enforcement** | two roots: `docs-lint.json` → `docs/`, `record-lint.json` → `.abcd/development/` | one docs-oriented consistency-lint; ADR/plan gates hard-code `docs/...` paths | the path boundary is not an enforcement boundary in ferry |
| **Propagation** | abcd stamps its own `.abcd/` (it is the brand) | **ferry's scaffold stamps `docs/{decisions,research,plans}` into arbitrary third-party repos** | ferry's choice ripples to every scaffolded repo — the biggest cost of divergence |

---

## 4. Recommended unified structure for ferry

Adopt abcd's model: a single development-namespace umbrella, `docs/` reserved for
user-facing Diátaxis, the durable record moved out of `docs/`, and a second lint
root pointed at the record.

### 4.1 Target tree

```
docs/                         # USER-FACING Diátaxis only (unchanged content)
├── tutorials/ how-to/ reference/ explanation/ assets/
└── README.md                 # map with the decisions/research/plans rows removed

<NS>/                         # NEW umbrella — namespace = open decision (§7.1)
├── README.md                 # "abcd-style" explainer: three tiers, what ships
├── development/              # DURABLE RECORD (committed, excluded from release)
│   ├── README.md             # flat-by-artefact map (mirror abcd's)
│   ├── plans/                #  ← docs/plans/            YYYY-MM-DD-topic.md
│   ├── research/             #  ← docs/research/ + .work/research/
│   ├── decisions/            #  ← docs/decisions/  (ADRs; see §4.3)  NNNN-title.md
│   └── roadmap/              #  (optional; only if ferry wants a phases/rfcs axis)
├── work/                    # SHARED WORKING (committed)  ← .work/
│   ├── CONTEXT.md   DECISIONS.md
└── .work.local/             # LOCAL EPHEMERAL (gitignored)  ← .work.local/
    ├── NEXT.md   scratch/   logs/   history/   backups/
```

Location-by-location mapping:

| Current | Target |
|---|---|
| `docs/plans/` | `<NS>/development/plans/` |
| `docs/research/` | `<NS>/development/research/` |
| `.work/research/` | `<NS>/development/research/` (merge — same category) |
| `docs/decisions/` | `<NS>/development/decisions/` (recommended; see §4.3) |
| `.work/CONTEXT.md`, `.work/DECISIONS.md` | `<NS>/work/` |
| `.work.local/**` | `<NS>/.work.local/` |
| `docs/{tutorials,how-to,reference,explanation,assets}` | unchanged |

### 4.2 Working-memory recommendation

ferry's public/private split (`.work/` committed, `.work.local/` gitignored)
**already matches abcd** — only the root differs. Two options:

- **(A) Fold under the umbrella** — `.work/` → `<NS>/work/`, `.work.local/` →
  `<NS>/.work.local/`. Full abcd parity; one coherent namespace; the scaffold
  generator already centralises these paths so doing it in the same pass is
  cheap. Costs: touches ADR 0002, the `.git/info/exclude` logic in `scaffold.go`,
  and `consistency-lint.sh`'s `.work.local/NEXT.md` rule.
- **(B) Keep at root** — leave `.work/` and `.work.local/` where they are; only
  move the durable record out of `docs/`. Less ripple, and it already satisfies
  the stated goal (working memory is *not* in `docs/` today).

**Recommendation: (A) for the final target, staged.** Do the mandatory durable-
record move first (Phase 1 below); fold working memory into the umbrella as a
follow-up (Phase 2) so the two changes review independently. If the maintainer
wants a single PR, (A) in one pass is coherent — just larger.

### 4.3 ADR location — recommendation: **move to the development namespace**

abcd puts decisions under `development/decisions/`. The maintainer's own
principle is decisive: **ADRs record architecture rationale and rejected
alternatives — they are developer-facing, not user-facing.** They fail the
"does a ferry *user* read this?" test. Keeping them in `docs/` is the same
category error as plans and research. **Recommend moving `docs/decisions/` →
`<NS>/development/decisions/`.** Cost: update the ADR-uniqueness path in
`consistency-lint.sh` (line 21) and the ADR links in `CONTRIBUTING.md` and
`AGENTS.md`. (If the maintainer prefers ADRs remain semi-public, they can stay
in `docs/reference/decisions/` — but that reintroduces dev material into the
shipped tree and is not recommended.)

### 4.4 Scope note — keep ferry lean

abcd's `development/` is rich (brief, intents, roadmap/phases/rfcs, principles)
because abcd is an intent-driven product. **ferry need not adopt that whole
surface.** Start with `development/{plans,research,decisions}` — the three
categories ferry actually has — and add `roadmap/` only if ferry later wants a
phases axis. Do not import `intents/`, `brief/`, or `principles/` speculatively.

---

## 5. Migration ripple — ordered checklist

Ordering rule: **the release gate must never see a half-moved state.** Because
ferry's release is tag-triggered on `main` and PRs gate `main`, do the moves and
the tooling updates in **one PR** so `main` is always internally consistent. The
plan-shipped gate fails *safe* (a missing plan glob → "ok", exit 0), so a
mismatch would silently stop gating rather than go red — another reason to keep
the script update in the same commit as the `git mv`.

**Phase 0 — decisions (STOP gate; maintainer sign-off before any move):**
1. Choose the namespace name (§7.1).
2. Decide ADR location (§4.3 recommends move).
3. Decide fold-vs-keep for working memory (§4.2 recommends fold, staged).
4. Decide cross-repo CLAUDE.md scope (§7.4).
5. Decide whether *scaffolded* repos get the new namespace or keep `docs/` dev
   dirs (§7.5) — this gates the scaffold-generator change.

**Phase 1 — the durable-record move (mandatory core):**
6. Create `<NS>/` skeleton with READMEs (mirror abcd's `.abcd/README.md`,
   `development/README.md`, and per-subdir READMEs). No moves yet.
7. `git mv docs/plans/* <NS>/development/plans/`,
   `docs/research/* <NS>/development/research/`,
   `.work/research/* <NS>/development/research/`, and (if §4.3 accepted)
   `docs/decisions/* <NS>/development/decisions/`.
8. **Scaffold generator** — `scaffold.go` line 212 `scaffoldDocsDirs` and the
   dir-creation at ~line 261; the embedded `docs-README.md` template; then update
   goldens: `scaffold_golden_test.go` (135-137, 203-205) and `scaffold_test.go`
   (293). Tests fail loudly until goldens match — that is the safety net.
9. **Scripts** — `check-plan-shipped.sh` line 50 glob → new plans path;
   `consistency-lint.sh` line 21 `decisions=` → new decisions path, line 47
   exempt `docs/plans/` → new plans path; `release.yml` line 70 comment.
10. **User-facing docs** — `docs/README.md` remove rows 28-30 and add the
    "development records live under `<NS>/development/`" pointer (mirror abcd's
    `docs/README.md`); `docs/how-to/cutting-a-release.md` line 36;
    `docs/how-to/scaffold-a-repo.md` line 35.
11. **Agent/contributor docs** — `CONTRIBUTING.md` lines 51, 59-60, 67, 74;
    `AGENTS.md` line 72 (ADR link) and its working-memory section; `.work/DECISIONS.md`
    and `CHANGELOG.md` prose references.
12. **`.gitignore` / git-exclude** — if working memory folds now, update the
    `.work.local/` exclude line and the `excludeWorkLocal` logic in `scaffold.go`;
    otherwise leave for Phase 2.
13. **CHANGELOG** — add a user-facing entry (docs layout change).
14. **Validate** — `make build`, `gofmt -l .` (empty), `go vet ./...`,
    `go test ./...`, then evals with `FERRY_BIN` set;
    `scripts/consistency-lint.sh`; render/link-check the docs. All green = done.

**Phase 2 — fold working memory (optional, if §4.2-A staged):**
15. `git mv .work/* <NS>/work/`, `.work.local/* <NS>/.work.local/`; update
    ADR 0002, `consistency-lint.sh` NEXT.md rule, scaffold `.work*` paths + goldens,
    AGENTS.md working-memory section, the `.git/info/exclude` line.

**Phase 3 — cross-repo convention (maintainer decision, §7.4):**
16. Update `../CLAUDE.md` (ABCDevelopment) docs section — either scope the
    `docs/{plans,research}` rule to "ferry-and-abcd use `<NS>/development/`" or
    flip it for all repos. STOP for explicit maintainer choice.

---

## 6. Risks, rollback, effort

**Risks**
- **Broken internal links** — ADR references from `CONTRIBUTING.md`/`AGENTS.md`
  and any doc cross-links. Caught by `consistency-lint.sh` links + a render pass.
- **Scaffold golden drift** — if goldens are not updated in lockstep the scaffold
  tests fail. This is a *feature* (loud failure), not a hazard, provided step 8
  is atomic with the move.
- **Cross-repo contradiction** — if ferry moves but `../CLAUDE.md` still says
  `docs/plans`, ferry's own scaffold output would violate the shared rule. Phase 3
  must land or be explicitly scoped.
- **Namespace collision** — a scaffolded third-party repo that already uses the
  chosen namespace dir. Neutral naming (§7.1) and the §7.5 decision mitigate.

**Rollback** — the whole change is `git mv` plus text edits on a branch. No data
migration, no irreversible step, no release cutover. Rollback = revert the PR (or
delete the branch pre-merge). Rehearse by running the full validation (step 14)
on the branch before requesting merge.

**Effort: M.** Mechanically simple (moves + find/replace), but the spread —
scaffold generator + two golden test files + four scripts/workflows + six doc
files + the cross-repo convention — puts it above S. No logic changes; no new
dependencies.

---

## 7. Open decisions for the maintainer

1. **Namespace name.** abcd uses `.abcd/` because that *is* its brand. ferry
   scaffolds **arbitrary** repos, and a `.ferry/` dir stamped into a random user
   project reads as "ferry-owned" (misleading) and collides conceptually with the
   runtime `~/.config/ferry/`. Options: `.ferry/` (direct mirror), or a
   tool-neutral umbrella like `.dev/` / `.project/` (better for scaffolded
   third-party repos). **Recommendation: a neutral umbrella**, precisely because
   the scaffold propagates it — but this is the maintainer's brand call.
2. **ADR location.** Move `docs/decisions/` → `<NS>/development/decisions/`
   (recommended, §4.3) or keep ADRs semi-public in `docs/`.
3. **Working-memory fold.** Fold `.work/` + `.work.local/` under the umbrella for
   full parity (recommended, staged as Phase 2) or keep them at the repo root
   (already satisfies the goal; less ripple).
4. **Cross-repo CLAUDE.md scope.** Update the shared
   `../CLAUDE.md` convention for **ferry only** (scope the rule) or for **all
   ABCDevelopment repos**. Flag: whichever way, ferry's scaffold output must match
   the shared rule.
5. **Scaffolded-repo policy.** Should repos ferry *stamps* receive the new
   namespace, or keep `docs/{decisions,research,plans}`? This is the highest-
   leverage decision — it determines whether the scaffold-generator change (step
   8) happens at all, and whether every future ferry-scaffolded repo carries the
   new layout. A scaffolded repo may not want ferry's opinionated umbrella.
6. **Roadmap surface.** Adopt `<NS>/development/roadmap/` now, or defer until
   ferry actually needs a phases/rfcs axis (recommended: defer — keep lean, §4.4).
