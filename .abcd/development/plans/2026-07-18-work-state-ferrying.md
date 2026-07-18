# Work-state ferrying: carry in-flight project work between accounts

Date: 2026-07-18 · Status: approved, awaiting implementation

## Why

ferry carries a development *setup* across user accounts and machines; it
does not carry *in-flight work*. For a project managed with the abcd layout,
continuing work on another account today loses:

- the session handover note (`<repo>/.abcd/.work.local/NEXT.md`) and the run
  journal (`<repo>/.abcd/.work.local/run-journal.json`) — gitignored by
  design, so they never travel with the project repo;
- the coding agent's per-project memory
  (`~/.claude/projects/<path-key>/memory/`) — outside any repo, keyed by the
  absolute project path, so it neither travels nor survives a home-directory
  change;
- the redacted session-transcript store (`~/.abcd/history/<root-sha>/`) —
  extracted and redacted (fail-closed) at session end by the project
  tooling, but local to the account.

The project-side tooling deliberately refuses this job: its record-export
mechanism (the lifeboat) reads an explicit allowlist that excludes the
ephemeral tier, and its dev-sync draft (itd-13) lists cross-machine sync as
out of scope. Carrying operator-local state between accounts is exactly
ferry's franchise. This plan adds it.

## Shape: explicit baton pass, not a reconcile domain

Work state is not configuration. It has an owner (whichever account is
actively working), changes every session, and must never silently merge.
So this is **not** a new `FileDomain` in the `capture`/`apply` loop —
reconcile semantics (repo-authoritative, drift detection) are wrong here, and
the reconcile engine's `$HOME`-target model does not cover files inside a
project repo's working tree.

Instead: a new verb family modelled on `bundle export` / `bundle import`,
with handover (baton-pass) semantics. Throughout, **Alice** and **Bob** name
two accounts of the same person — the side handing work over and the side
picking it up:

```
ferry work pack <project-dir>     # bundle this project's work state into the store
ferry work receive <project-dir>  # land the latest cargo for this project here
ferry work status [<project-dir>] # cargo, claims, divergence, store size
ferry work prune [<project-dir>]  # apply the retention policy now
```

- `pack` refuses on a dirty scan (see Safety), writes one cargo bundle to
  the store, records a **sidecar** handover marker
  (`.abcd/.work.local/.ferry-handover.json`: bundle id, content hashes of
  what was packed). It never mutates `NEXT.md` or any other cargo file —
  marking the baton must not modify the baton (that would trip the
  divergence guard on every normal cycle and fight the user's editor).
  `work status` surfaces "handed over, not modified since" vs "modified
  after handover".
- `receive` locates the latest cargo for the project (by pack sequence
  number, never by file mtime — clocks differ across machines and exFAT
  timestamps are coarse), backs up every target through the backup engine
  (`BackupAndWrite`: first-touch baseline + run journal), writes, and
  records the claim. Manifest timestamps are display-only. An equal-seq
  tie at the highest sequence (Alice and Bob both packed without an
  intervening receive) is refused, printing both bundles and their claim
  owners — the user prunes or names one explicitly. If the claim history
  shows the newest bundle was taken back (Alice packed, took it back, and
  kept working), Bob's `receive` refuses without `--force` (the bundle has
  been superseded at the source).
- **Reversal:** plain `ferry restore` is a full machine revert to the
  pre-ferry baseline and the baseline is first-touch-only, so it cannot
  precisely undo "the last receive". `receive` therefore takes a
  per-receive snapshot (the existing snapshot machinery,
  `internal/backup/snapshot.go`) and `ferry work restore <project-dir>`
  reverts exactly the last receive from it. `ferry restore work` (domain
  form) reverts all work-verb writes to baseline.
- Baton tracking is advisory, not locking: per-account claim files in the
  store record who packed/received what. One human across their own
  accounts — Alice and Bob, not an adversary — is the threat model;
  divergence is detected by comparing content
  hashes against the recorded pack/receive baseline (never timestamps) and
  surfaced by `work status` and the `receive` guard — not prevented.
- `receive` on the account that packed (Alice reclaiming her own baton
  before Bob picked it up) is a **take-back**: it clears the handover
  marker and updates the claim; it restores nothing unless `--force`.
- `pack` with no `NEXT.md` refuses ("nothing to hand over — write the
  handover note first"); `--allow-empty` permits memory/transcript-only
  cargo. A missing transcript store or memory directory is tolerated and
  noted in the manifest.

## Project identity

A project is identified the way the abcd tooling identifies it — the first
line of `git rev-list --max-parents=0 HEAD` (run with an isolated git
environment so an inherited `GIT_DIR` cannot answer for another repo);
40- or 64-char hex (SHA-1 or SHA-256 repos). Compatibility matters: the same
value locates the account-level transcript store `~/.abcd/history/<root-sha>/`.
Guards, because the categorical "stable across clones" claim has real holes:

- **Shallow clones:** `git rev-parse --is-shallow-repository` true →
  `pack`/`receive` refuse with "unshallow first" guidance (a shallow clone
  reports the graft boundary as its root — a bogus identity nothing else
  will ever match).
- **Linked worktrees:** worktrees share a root SHA but `.abcd/.work.local/`
  is per-worktree by design, so their batons would collide in the store.
  v1 refuses both `pack` and `receive` in a linked worktree
  (`git rev-parse --git-dir` ≠ `--git-common-dir`) with a clear message; a
  worktree discriminator in the cargo key is future work if it's ever
  wanted.
- **Multi-root histories:** the manifest records the full sorted root-SHA
  set; `receive` matches on set membership, so a subtree import that
  reorders `rev-list` output does not orphan existing cargo. (The store
  directory key stays the abcd-compatible first-line value.) Store lookup
  order follows: the computed-key directory first, then a manifest scan of
  the remaining store directories for a root-SHA-set intersection.

## Cargo (v1)

Per project, one cargo bundle contains:

| Item | Source | Receive policy |
|---|---|---|
| Handover note | `<repo>/.abcd/.work.local/NEXT.md` | overwrite; guarded (below) |
| Run journal | `<repo>/.abcd/.work.local/run-journal.json` | overwrite; guarded |
| Agent memory | `~/.claude/projects/<path-key>/memory/` | guarded replace (below) |
| Redacted transcripts | `~/.abcd/history/<root-sha>/` | union-merge by filename |

Receive policies, decided here because an implementer hits them on day one:

- **Guarded overwrite (NEXT.md, run-journal.json, memory/):** refuse when
  the destination's content hash differs from the last pack/receive
  baseline recorded in local state (i.e. it changed since this account last
  held the baton) unless `--force`. A first receive into an already-
  populated destination likewise refuses without `--force`.
- **Union-merge (transcripts):** session files are immutable and uniquely
  named — copy files absent at the destination, skip files already present,
  never delete. This preserves sessions Bob ran locally that Alice's pack
  never saw. `meta.json` is copied only when absent.
  `receive` takes the store's advisory lock (`transcripts/.lock`) while
  writing so it cannot interleave with a live session-end capture. The
  account-level `~/.abcd/history/index.json` is owned by the project
  tooling and never touched; until its registration runs on the new
  account, `work status` notes the store as present-but-unregistered.

**Excluded by default** (regenerable or too hot to travel): the rest of
`.work.local/` — `logs/` (PII-scan, audit, and memory-lint output),
`reviews/`, `scratch/` — and everything raw under `~/.claude/` (transcripts,
tasks, plans). `~/.ssh` is untouchable, as everywhere in ferry.

The source locations are **data, not code**: a builtin registry (modelled on
the agents domain's `Builtins()` harness registry, `internal/agents/registry.go`)
maps each known harness and project layout to its work-state locations. v1
ships the abcd layout and the Claude Code harness; adding another harness is
a registry entry. A per-repo `extra` glob list in the registry override lets
a repo with non-standard `.work.local` members (worklogs, local decision
files — see abcd's iss-91) opt them in.

### The agent-memory path key

Observed convention, not a contract: `~/.claude/projects/` keys are the
absolute project path with non-alphanumeric characters flattened to dashes —
lossy (a dash in a directory name is indistinguishable from a separator) and
collision-prone on case-insensitive APFS. So ferry only ever **recomputes
forward** from the destination project path, never inverts a key. Before
writing, `receive` verifies the target key directory is either absent, empty,
or matching the local last pack/receive baseline; anything else refuses with
a message rather than merging into what may be a different project's memory.
The registry entry owns the encoding rule, so a harness-side scheme change is
a one-entry fix.

## The cargo store

Work state never enters the ferry config repo (declarative, long-lived,
possibly hosted). The store is a plain directory of cargo bundles on shared
or portable media. Its location is machine-specific, so it lives in the
machine config (`~/.config/ferry/config.toml`), not the repo-side manifests:

```toml
[work]
store = "/Users/Shared/ferry-cargo"   # example: same-Mac accounts; equally an
                                       # SSH-mounted path or removable volume
```

Store layout, designed for two accounts writing without shared-write files:

- `<store>/<root-sha>/<seq>-<bundle-hash>.ferrywork` — the bundles. A
  bundle is a **zip** (generalising `internal/bundle`'s writer — its
  path-canonicalisation, mode-sanitising, and hashing helpers reuse
  directly; the manifest member and schema are cargo-specific) whose
  manifest member records: format version, root-SHA set, source repo path
  and `$HOME` (for path rewriting), per-file hashes, per-item
  inclusion/exclusion, and the scan verdict. `<seq>` is allocated by
  creating the file `O_CREAT|O_EXCL` and retrying at seq+1 on collision
  (safe on APFS, SMB, exFAT); `<bundle-hash>` is the bundle content hash.
  Equal-seq forks are surfaced by `work status`.
- `<store>/<root-sha>/claim.<account>@<host>.json` — per-account claim
  files (e.g. `claim.alice@studio.json`, `claim.bob@laptop.json`), each
  written only by its owner, merged on read. No file is ever written by
  two accounts, which sidesteps both the `/Users/Shared` POSIX-permission
  trap (a file Alice creates 0644 is unwritable by Bob) and claim-write
  races, and works unchanged on permissionless exFAT. Directories ferry
  creates in the store are made group/world-writable, and the how-to
  documents the one-time top-level setup.
- On-disk formats carry the standard `version` integer envelope
  (`internal/statefile`) and follow the compatibility policy (forward-only
  migration, newer-version refusal).

**Store-location guards** (mechanical, not just documented): configuring or
using a store that resolves inside any git worktree is refused (this
mechanically catches "inside the ferry config repo"); a store under a known
sync root (iCloud's Mobile Documents, Dropbox, Google Drive) warns loudly
and requires an explicit override flag. Nothing in ferry ever uploads the
store.

**Retention:** pack applies keep-last-N per project (default small,
configurable); `work status` reports store size; `work prune` applies the
policy on demand. Transcript directories can grow large — a v2 note, not
v1: move transcripts to an incremental content-addressed sidecar under
`<store>/<root-sha>/` instead of re-packing them into every bundle.

## Safety

- **Secret gate, fail closed, with explicit exits:** every text byte packed
  passes the existing secret gate (`internal/secret`: `ScanText`, plus
  `HasBinarySecret` for non-text). A high-confidence finding aborts the
  pack, naming the file and finding. This is deliberately *stricter* than
  `bundle export` (which withholds the offending file and continues):
  work-state cargo is small and personal, and a silent gap in a handover is
  worse than a loud stop. Because "fix at source" is not actionable for the
  machine-managed transcript store (a transcript *about* secret-scanning
  code can legitimately contain fixture key headers), two auditable escape
  hatches exist: `pack --exclude <item>` (recorded in the manifest;
  `status` shows the gap) and a per-finding acknowledgement pinned to
  file + content hash in local state (re-aborts if the flagged content
  changes). Transcripts arrive pre-redacted by the project tooling; ferry
  re-scans anyway — defence in depth, and the gate also covers memory and
  NEXT.md, which nothing has scanned before.
- **Containment, stated precisely:** ferry's existing resolved-containment
  guard protects `$HOME`-anchored writes (symlinked parents refused, `~/.ssh`
  untouchable) and deliberately does not judge paths outside `$HOME`. The
  `$HOME` targets (memory, transcript store) go through it unchanged. The
  two in-repo targets are gated separately: the named project directory must
  be a non-shallow git repo whose root-SHA set matches the manifest, the
  write path must resolve under its `.abcd/.work.local/` with no symlinked
  component, and v1 requires the project directory itself to live under
  `$HOME` (refused otherwise, with a clear message — out-of-home repos are
  a documented v1 limitation, not silently unguarded writes).
- **Backup-first:** all receive writes go through `BackupAndWrite`
  (first-touch baseline + journal) plus the per-receive snapshot described
  above, so both `ferry work restore` (last receive, precise) and
  `ferry restore work` (all work writes, to baseline) are real.
- **Never hosted by default:** enforced by the store-location guards above,
  and stated plainly in the docs: the store holds personal working context
  and belongs on local or private media.

## Implementation outline

- `internal/work/` — `registry.go` (harness/layout → locations + receive
  policies, data-driven), `identity.go` (root-SHA set, shallow/worktree
  guards), `pack.go`, `receive.go`, `claim.go` (per-account claim files),
  `state.go` (versioned local state: baselines, acknowledgements),
  `manifest.go`. Reuse: `internal/secret` (gate), `internal/backup`
  (`BackupAndWrite`, snapshots, containment), `internal/statefile`
  (envelopes), `internal/bundle` (zip helpers, generalised writer). Two
  small engine touches: export a per-receive snapshot entry point (the
  snapshot machinery is currently internal to restore flows), and extend
  `cmd/restore.go`'s scoped-restore resolution to enumerate work-written
  paths from local state for the `ferry restore work` domain form.
- `cmd/work.go` — parent noun + `pack`/`receive`/`status`/`prune`/`restore`
  subcommands, self-wired via its own `init()` like `cmd/bundle.go`.
- Config: `[work]` table in the machine config (`~/.config/ferry/config.toml`)
  for `store` and retention; registry overrides (`[work.cargo.<name>]`,
  per-repo `extra` globs) in `ferry.toml`/`ferry.local.toml` with their own
  local-wins merge, modelled on the `[agents]` table
  (`internal/config/agents.go`) — the scope loader's `[manage]`-only parser
  is not touched.
- Docs (Diátaxis): how-to "Hand work over to another account" (including
  the one-time shared-store setup and clone-then-receive bootstrap),
  explanation "The work domain" (why baton-pass, why the store is not the
  config repo), reference entries for the commands and the `[work]` config.
- Tests: unit tests per package; evals driving the real binary through
  pack → receive round trips across two fake homes, covering the
  root-SHA-mismatch, shallow-clone, and worktree refusals, the secret-gate
  abort and both escape hatches, guarded-overwrite and union-merge receive
  policies, take-back, seq-collision retry, and work-restore after receive.
- CHANGELOG entry (user-facing feature).
- Companion ADR: "Work state is carried by explicit handover verbs, not the
  reconcile loop" (cites ADR-0002, the public/private split of the .work
  memory tiers, as precedent for the durable/ephemeral distinction).

## Out of scope (v1)

- Continuous sync or scheduled packs (explicit handover only).
- Merge/reconcile of diverged work state (detected and surfaced, not solved).
- Harnesses beyond Claude Code and layouts beyond abcd (registry-ready).
- Carrying raw session transcripts, task state, or plan files from
  `~/.claude/` (the redacted store is the travel format).
- Out-of-home project repos (refused with a message).
- A worktree discriminator in the cargo key (linked worktrees refused).
- Encryption of cargo at rest (`age`) — worth a flag if the store ever
  leaves trusted media; noted, not built.

## Risks

- **Path-key coupling:** the agent-memory key scheme is an observed
  convention of one tool. Mitigations: forward-recompute only; encoding
  rule owned by one registry entry; refuse-on-mismatch at the destination
  rather than merge.
- **Identity drift:** a history rewrite is a new project identity and
  orphans prior cargo (mirroring the project tooling's own convention);
  `work status` shows store entries with no local match, by manifest repo
  name, so orphans are visible rather than silent.
- **Baton is advisory:** two accounts can both edit before a pack lands.
  Accepted for a single-human threat model; hash-based divergence detection
  in `status` and the receive guard is the mitigation.
- **abcd layout coupling:** cargo paths are registry data with abcd
  defaults; an abcd layout change is a registry edit, not a redesign.
