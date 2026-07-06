# Changelog

All notable changes to ferry are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and ferry
uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html) with a
leading `v`.

Before v1.0.0, minor releases may make breaking changes; each one is
called out in a **Breaking** section. See
`docs/reference/compatibility.md` for the pre-1.0 compatibility rules.

## [Unreleased]

## [0.5.0] - 2026-07-06

### Breaking

- **Non-interactive `apply` now fails closed on a risky change.** A run with no
  way to confirm (exhausted stdin, or `--skip-wizard`) exits non-zero when a
  *risky* change is pending — overwriting a locally-modified file, adopting a
  pre-existing file ferry never wrote, or deploying a secret — instead of
  applying it silently. Unattended automation that should proceed anyway must
  pass `--force`. The safe subset of the run still applies, and a fresh-machine
  first `apply` (only file creations) is unaffected. See *Changed* below.
- **The last-applied state file is now schema version 2.** A downgraded ferry
  refuses a version-2 file rather than corrupting it, so rolling back after an
  upgrade means restoring the write-once `dotfile-last-applied.json.pre-v1.bak`
  sibling. Upgrading migrates the older file forward losslessly. See *Changed*.

### Added

- **Config-file terminal emulators.** ferry now carries the settings of
  config-file terminal emulators — Alacritty (`~/.config/alacritty`), kitty
  (`~/.config/kitty`), and WezTerm (`~/.wezterm.lua`) — like dotfiles. Enable
  the domain with `terminals = true` under `[manage]`, then commit each
  terminal's config under `terminals/` in the repo; a built-in terminal whose
  config is not in the repo deploys nothing, so you enable the ones you use by
  committing their config. A built-in registry maps each known terminal to its
  paths and is data, edited in the manifest: `[terminals] enabled = [...]`
  restricts the set, and `[terminals.terminal.<name>]` overrides a built-in's
  source/target or adds a terminal the registry does not know. A directory
  terminal carries its whole config tree file by file; a single-file terminal
  carries its one file. Scope gates the domain in both directions, and the
  `.local` layer applies per file (`local/terminals/<source>/<relpath>` wins) —
  the natural home for a per-machine colour scheme. Terminal config deploys
  through the same guided, backup-first apply as dotfiles, so overwriting a
  locally-modified config is risky and refused unattended. GNOME Terminal
  (dconf) is deferred: it needs a dump/load bridge, not a file copy.
- **Capture-back for the agents domain.** `ferry capture` no longer skips agents
  targets. A live edit to a deployed agent file — a skill, a hook, or a harness
  instruction file — now flows back into the config repo through the same
  approve and route (shared vs `.local`) flow as dotfiles: an asset routes to
  its shared source or a gitignored per-machine `local/agents/` overlay (which
  wins on the next apply), and a single-source instruction file (`general.md` or
  `coding.md`) routes to that source. When the deployed file **and** its repo
  source have both changed since ferry last deployed it — a true divergence —
  capture refuses and shows a diff rather than guessing a winner. A derived
  combined `AGENTS.md` cannot be split back automatically, so its drift is
  reported and points at the two sources. Capture also offers to **adopt** new
  agent-shaped files (a regular file under a managed asset mapping's target
  directory that ferry never deployed); what counts as agent-shaped comes from
  the asset-mapping registry, not a fixed list. The secret gate, `~/.ssh`
  hands-off rule, and the never-commit/never-push contract all still hold, and
  capture never rewrites the deployed file itself.
- **Guided `apply` (quiet when safe, stop when risky).** `apply` is no longer a
  terse reconciler. On a run that has changes it walks the pending work grouped
  by domain (dotfiles / agents): a *safe* change — creating a file where none
  exists, or updating a target whose live content still matches what ferry last
  deployed — applies automatically, while a *risky* change halts for
  confirmation. A change is risky when it would overwrite a file that differs
  from the last-deployed baseline, adopt a pre-existing file ferry never wrote,
  or deploy a value from the secret store. In the walkthrough you confirm a
  domain wholesale, drill in to see each change's full diff, apply or skip a
  change this run, or skip it *always* — remembered per machine in the
  gitignored `.local` layer (`local/skip-always.txt`). A clean, in-sync apply
  prints one line. The risk gate reads the per-target last-deployed baseline to
  tell a locally-modified file from an in-sync one.
- **`apply --skip-wizard`.** An expert opt-out from the walkthrough: safe
  changes still auto-apply, but risky changes are refused rather than prompted.
- **`apply --force` now covers the risk gate.** `--force` is an explicit "just
  do it" override: it treats every risky change as confirmed (the downstream
  conflict and empty-over-substantial data-loss guards still apply and warn).
- **Per-target last-deployed baseline.** `apply` now records, alongside each
  managed target's last-applied hash, the exact bytes it deployed — the
  last-deployed baseline. It is the foundation for the guided apply and
  capture-back work: it lets ferry tell a locally-modified file from an
  in-sync one and reconstruct what it last wrote. The baseline lives in the
  `dotfile-last-applied.json` state file, content-addressed by the hash it
  already stores. A secret-routed target (one whose bytes were rendered from
  the secret store) records ONLY its hash, never the deployed bytes, so a
  plaintext secret is never written into this non-secret state file.

### Changed

- **Non-interactive `apply` fails closed on risky changes.** With no way to
  confirm — an empty/exhausted stdin, or `--skip-wizard` — a risky change is
  listed and refused with a non-zero exit; nothing risky is ever applied
  unattended. The safe subset of the same run still applies. Creating a file
  where none exists remains a safe, automatic change, so a fresh-machine first
  `apply` is unaffected; adopting or overwriting a pre-existing file now asks
  first.
- **`init` hands off to `apply` for the reconcile walkthrough.** `init` keeps
  its zshrc-adoption setup step and then points at (or, with `--apply`, runs)
  the single guided flow, which lives in `apply`.
- **Last-applied state schema is now version 2.** The store gains the
  last-deployed baseline. An older file (a version-1 envelope, or a
  pre-versioning `v0.3.x` file) is migrated forward on the next `apply` with
  every recorded hash preserved and its pre-migration bytes kept in a
  write-once `dotfile-last-applied.json.pre-v1.bak` sibling; the baseline
  starts empty and is re-established per target as `apply` redeploys it. A
  downgraded ferry refuses a version-2 file rather than corrupting it.

## [0.4.1] - 2026-07-05

### Changed

- **Installer shows ferry's identity on success.** After a successful install,
  `install.sh` prints ferry's banner (ASCII logo plus next-step hints) by
  running the just-installed binary, instead of a bare next-step line.
- **Plain-language `ferry init` closing hint.** `ferry init` now closes with
  "run `ferry apply` to set up this machine — it backs up anything it changes
  first, so `ferry restore` can undo it", dropping the "reconcile" jargon and
  the nested `--apply --yes` parenthetical (the flags stay in `--help`).

### Fixed

- Harness targets that climb out of `$HOME` via `..` are now rejected when
  the manifest is parsed (matching asset targets), instead of surfacing only
  as a skipped item at apply time.
- Asset targets of `.` or `./` (the `$HOME` root) are rejected at parse.
- The `~/.ssh` hands-off guard now compares case-insensitively across every
  containment check — dotfile and agents deploy targets, the configured repo
  and clone source/destination paths, and the zsh `source` probe — so a path
  such as `.SSH/config`, which the default case-insensitive macOS filesystem
  maps into `~/.ssh`, is refused everywhere rather than only for deploy
  targets. The fold now also covers the `$HOME` parent components, so a
  wrong-case home prefix (e.g. `/Users/ALICE/.ssh/...` for a home of
  `/Users/alice`), which the same case-insensitive filesystem maps into the
  real `~/.ssh`, is recognised and refused too.
- Clearer `agents adopt` diagnostics for directory-level bridges and for
  stale bridges left in place at locations this adopt run does not cover.

## [0.4.0] - 2026-07-05

### Added

- **Agents domain.** A new managed domain carries one source of truth of
  AI-agent instructions across machines and coding CLIs. Two Markdown
  files (`agents/general.md`, `agents/coding.md`) hold every rule; ferry
  derives the combined content in memory and deploys it, plus any skills,
  sub-agents, and hooks, to the path each harness natively reads, as
  regular-file copies reconciled by hash. Nothing under `$HOME` is
  symlinked. The domain is off by default; enable it with `agents = true`
  under `[manage]`. Built-in harnesses ship as data (`claude`, `codex`,
  `opencode`, `companion`, `gemini`), and an optional `devtree` deploys the
  coding rules to a workspace-root `CLAUDE.md`.
- **Agents lifecycle integration.** With the domain enabled, `apply`
  deploys, `status` and `diff` report per-target drift, `capture` skips
  agents targets with one informative line, and `restore agents` (or a
  full `restore`) reverts every deployed file to its pre-ferry state. The
  revert set comes from a persisted target record, so restore works even
  with the config repo deleted and reverts targets that were later
  de-scoped.
- **Asset-mapping registry.** Asset trees are a data-driven mapping
  surface: each `[agents.asset.<name>]` entry copies a config-repo
  directory recursively to a directory under `$HOME` with per-file
  executable bits preserved, under the full per-target treatment
  (hash-gated writes, drift reporting, backup and restore, collision
  refusal). Built-in mappings cover `skills`, `agents`, and `hooks`; a
  user-defined mapping (for example global git hooks under `~/.githooks`)
  needs no code change.
- **`ferry agents scaffold [--private|--attribution] <repo-dir> [name]`.**
  Sets a project repo up for the multi-tool agent pipeline: an `AGENTS.md`
  router with `CLAUDE.md`/`GEMINI.md` relative symlinks, the `docs/`
  Diátaxis hierarchy, and committed `.work/` session memory, while all
  scratch and log output lives in `.work.local/` hidden through the
  checkout-local git `info/exclude` (never `.gitignore`). `--private`
  (a repo you do not own) leaves zero tracked trace. `--attribution`
  (a repo that requires AI disclosure) installs a `prepare-commit-msg`
  hook that appends a kernel-style `Assisted-by:` trailer to
  agent-authored commits only. The two flags are mutually exclusive.
- **`ferry agents adopt <dir>`.** Migrates an existing symlink-based
  instruction directory into ferry. It reads `<dir>` only, imports its
  files into the config repo, then swaps each `$HOME` bridge symlink for a
  ferry-managed copy in a single journalled transaction — any failure
  rolls back and the symlinks return. Directory-level bridges are refused
  with the exact `rm` to run rather than written through.

### Changed

- **The starter zshrc seeded by the wizard is a minimal neutral example.**
  Machine-specific configuration belongs in `~/.zshrc.local`.
- **Releases build from the tag commit, gated by the test suite.** The
  release workflow now verifies (build, vet, full `go test ./...`, and the
  eval suite against a built Linux binary) at the tagged ref before
  building the published binaries from that same commit. A red suite
  aborts the release; a tag on an older commit releases binaries built
  from that commit, not from the branch tip.
- **Build provenance attestations.** Every released binary and the
  `checksums.txt` manifest carry a signed SLSA build-provenance
  attestation, generated in the release workflow and proven by a
  post-release `gh attestation verify` step. Verify a download yourself
  with `gh attestation verify ferry-<goos>-<arch> -R REPPL/ferry`.
- **Release pruning keeps git tags.** Retention still removes superseded
  GitHub Releases and their assets, but never deletes the git tag: tags
  are immutable once pushed, so a pruned version's tag and commit stay
  reachable.

### Breaking

- **Checksums ship as a release asset, not pinned to a branch.** Releases
  now publish a `checksums.txt` alongside the binaries, and `install.sh`
  fetches that file from the release it is installing and verifies each
  binary against it. The previous flow — SHA256 values hand-pinned into
  `install.sh` on a branch and committed back after each release — is
  gone; the release workflow pushes nothing to any branch. Installation
  stays fail-closed: a binary with no matching checksum is refused. Anyone
  who scripted around the in-`install.sh` `sha_*` pins should read the
  checksum from the release's `checksums.txt` instead.

### Fixed

- Ten findings from the post-merge review of the asset-mapping registry:
  - `adopt` now refuses a bridge that resolves outside `$HOME` or under
    `~/.ssh` through a symlinked parent, using the shared resolved-
    containment check rather than a lexical-only test.
  - `adopt`'s import set derives from the resolved asset registry, so a
    custom mapping's tree is imported and deployed instead of its bridges
    being removed with nothing put in their place.
  - Local `[agents.asset.<name>]` tables merge per field (local wins)
    rather than replacing the shared entry wholesale.
  - `adopt` scans the built-in default asset locations regardless of any
    `assets` selection, so a narrowed selection no longer strands stale
    symlinks.
  - Plan-time collision detection catches file-versus-directory prefix
    collisions (for example `.githooks` against `.githooks/pre-commit`)
    instead of failing mid-apply.
  - An asset `source` of `.` or `templates` is rejected, so raw
    instruction sources and templates are never sprayed into `$HOME`.
  - An asset `target` containing a `..` climb is rejected at parse time,
    matching the devtree validation.
  - Unknown keys in agents harness and asset tables are rejected rather
    than silently ignored, so a typo'd field is a loud error.
  - `bridgeCandidate` no longer re-implements the home-target validator;
    one containment validator is shared.
  - `ResolveAssets` and `Resolve` are unified over one registry resolver.

## [0.3.2] - 2026-07-02

- Fail-closed install path: `install.sh` verifies each downloaded binary
  against a pinned SHA256 before use.

## [0.2.0]

- Portable `export`/`import` bundles: a secret-scanned `.zip` of the
  repo's tracked shared files for an offline move, ingested and fully
  validated into a fresh config repo.

## [0.1.0]

- Initial release: declarative reconciliation of dotfiles, terminal
  settings, and dependencies across user accounts and machines, with an
  out-of-repo secret store and a reversible backup path (`restore`).
