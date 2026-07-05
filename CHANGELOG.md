# Changelog

All notable changes to ferry are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and ferry
uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html) with a
leading `v`.

Before v1.0.0, minor releases may make breaking changes; each one is
called out in a **Breaking** section. See
`docs/reference/compatibility.md` for the pre-1.0 compatibility rules.

## [Unreleased]

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
