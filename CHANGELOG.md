# Changelog

All notable changes to ferry are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and ferry
uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html) with a
leading `v`.

Before v1.0.0, minor releases may make breaking changes; each one is
called out in a **Breaking** section. See
`docs/reference/compatibility.md` for the pre-1.0 compatibility rules.

## [Unreleased]

## [0.5.2] - 2026-07-06

### Security

- **`restore --packages` can no longer be steered into running attacker-chosen
  `apt-get` options as root through a tampered installed-package record.** The
  uninstall rail is now symmetric with the install rail: every entry read from
  `~/.local/state/ferry/deps-installed.txt` is re-validated as a plain apt
  package name before it reaches `apt-get remove` (an entry starting with `-`,
  such as `-oDPkg::Pre-Invoke::=touch /tmp/x`, or ending in `-`, apt's REMOVE
  modifier, aborts the whole uninstall with the record left intact), and both
  the `apt-get remove` and `brew uninstall` invocations now place `--` before
  the package list so nothing after it can be read as an option. This runs as
  root under `sudo ferry restore --packages`; the state file is `0700` and
  symlink-hardened, so exploitation requires tampering with local ferry state,
  but the boundary is now closed either way. Legitimate recorded names are
  unaffected.
- **`restore --packages` also rejects a trailing `+` on a recorded apt entry,
  apt's INSTALL modifier.** Like the trailing `-` REMOVE modifier, a trailing
  `+` is applied during package resolution, so `--` gives no protection: a
  tampered entry such as `openssh-server+` would have run `apt-get remove -y --
  openssh-server+` and INSTALLED the package as root instead of removing it. The
  uninstall rail now refuses a trailing `+` and aborts with the record intact.
  The `+` reject is remove-rail-only — `g++` stays installable on the install
  rail; the trade-off is that `g++` cannot be uninstalled through `restore
  --packages` and must be removed manually.
- **A malicious `apt.txt` in a cloned config repo can no longer inject
  `apt-get` options that run as root under `sudo ferry apply --deps`.** The apt
  manifest parser now validates every entry as a package name: an entry that
  starts with `-` (which `apt-get` would read as an option, e.g.
  `-oDPkg::Pre-Invoke::=touch /tmp/x`) or carries any character outside the apt
  name charset (letters, digits, `+ - . : ~`) is refused with an error naming
  the offending line, and the install invocation now places `--` before the
  package list so anything after it is treated as a package, never an option.
  Legitimate names, versioned/epoch forms, and inline comments are unaffected.
  The `brew` path is not susceptible to this class: `brew bundle` reads a
  manifest FILE and package names never reach the command line.
- **A malicious `apt.txt` can no longer smuggle an `apt-get` package-specifier
  modifier that removes or pattern-matches packages as root.** `apt-get` applies
  a trailing `-` on a positional argument as a REMOVE directive during package
  resolution — so `--` (which ends only OPTION parsing) gives no protection, and
  an entry such as `ufw-` or `apparmor-` would have run `apt-get install -y --
  ufw-` and removed that package under `sudo ferry apply --deps`. A leading `~`
  or `.` likewise let `apt` do pattern/regex matching and select unintended
  packages. The apt manifest parser now anchors every entry to the Debian
  package-name shape: it must start with an ASCII letter or digit and must not
  end with `-`. Legitimate names such as `g++`, `python3.11`, `foo:amd64`,
  `libfoo-dev`, and `zsh` are unaffected.
- **The secret scanner now flags a credential keyword anywhere in a
  separator-bounded key, not only as its first token.** Keys such as
  `DB_PASSWORD`, `DATABASE_PASSWORD`, `MY_API_KEY`, `REDIS_PASSWORD` and
  `GITHUB_TOKEN` are matched (with `WORD_` prefixes and `_WORD` suffixes allowed
  around the keyword), so a short or low-entropy value that previously slipped
  past both the name match and the entropy backstop is now caught before it can
  reach the config repo. The keyword must still begin at line-start or after
  `export `, so prose such as `# password rotation notes` is not flagged.
- **The secret scanner now detects a password embedded in a URL's userinfo.** A
  value like `DATABASE_URL=postgres://user:pass@host` or
  `REDIS_URL=redis://:pw@host` is flagged on the captured userinfo password,
  which the credential-name and entropy heuristics both missed. The password is
  gated only by the shared empty/placeholder/interpolation exclusions, with no
  minimum-length floor — the `scheme://user:pass@host` structure is itself the
  signal that the value is a credential, so even a short DB/cache password
  (`redis://:pw@host`) is caught. An ordinary URL with no userinfo password —
  `HOMEPAGE_URL=https://example.com` — is not flagged.

- **A symlinked intermediate parent can no longer redirect a managed write
  outside `$HOME` or into `~/.ssh`.** The deploy-target boundary now runs the
  same symlink-resolving containment check for a flat or nested dotfile name
  that it already ran for other nested targets: a manifest entry such as
  `.config/foo` whose `~/.config` is a symlink escaping `$HOME` (or resolving
  into `~/.ssh`) is refused before any back-up or write, closing a gap where the
  lexical-only check would have written through the link.
- **`ferry restore` re-validates each path's parent chain before it reads or
  writes it.** If an intermediate parent was swapped to a symlink after a
  baseline was captured (for example between `apply` and `restore`), restore now
  refuses that entry — before the pre-restore snapshot reads it, and again before
  the write-back — instead of reading through the redirected path (which could
  capture a `~/.ssh` key or an out-of-`$HOME` file into the snapshot store) or
  deleting and rewriting through it. The refusal is surfaced and the rest of the
  restore still proceeds, so restore is now no weaker than `apply`, which already
  guards before its baseline read.
- **A symlinked intermediate parent can no longer redirect a managed delete
  outside `$HOME` or into `~/.ssh`.** `BackupAndRemove` — reached in production
  via `ferry agents adopt` — now runs the same symlink-resolving containment
  check before it reads the prior state or deletes. A same-user process swapping
  an intermediate parent to a symlink into `~/.ssh` (or outside `$HOME`) after
  plan time can no longer make ferry read a key into the baseline and then unlink
  it through the link; the delete is refused, closing the last open mutation
  boundary.
- **A secret-routed dotfile's rendered plaintext is no longer staged to a
  `$TMPDIR` temp file during `ferry apply`.** Deploying a dotfile whose source
  referenced the secret store — whether an include-style file (e.g. `.zshrc`) or
  a whole-file-replace dotfile (e.g. `.gitconfig` carrying a `{{ferry.secret}}`
  token) — previously wrote the already-substituted plaintext to a `/tmp` file so
  the file-based apply path could read it back; a crash between staging and
  cleanup left the secret at rest in `/tmp`. Every target's effective bytes are
  now applied in memory via the same shared apply core the agents domain uses, so
  the rendered secret never touches disk outside its intended `$HOME`
  destination. Behaviour is otherwise unchanged (the same effective content, the
  same crash-safe deferred last-applied ordering).

### Fixed

- **`~/.local/state/ferry/deps-installed.txt` is now written atomically.** The
  cumulative record of packages ferry installed — which `restore --packages`
  reads to know exactly what to uninstall — was rewritten with a plain
  truncating write; a crash mid-write could leave a partial record, silently
  dropping earlier packages from a later uninstall. It is now written via a
  same-directory temp file and an atomic rename, so a crash leaves the prior
  record fully intact.
- **`ferry capture` writes every repo file atomically.** The single write path
  all capture routes share used a plain truncating write; a crash mid-write could
  truncate a gitignored `local/` overlay that git cannot restore. It now uses the
  same temp-file-plus-rename as the rest of the codebase, so a crash leaves the
  previous file untouched.

- **The transactional writer re-checks its resolved parent chain before any
  back-up read or delete.** `BackupAndWrite` now re-validates containment first —
  before capturing the baseline and recording the journal entry — closing a
  narrow window in which a same-user process swapping a parent to a symlink
  between plan time and write time could have redirected the write. Guarding
  first also means a refused write ingests nothing into the immutable baseline
  and records no journal entry that rollback could never replay. Behaviour is
  unchanged for the ordinary path (an in-`$HOME` real or symlinked-in-`$HOME`
  parent still succeeds).

## [0.5.1] - 2026-07-06

### Security

- **Defence in depth: a secret's plaintext can no longer reach the last-applied
  state file by any code path.** The dotfile apply core now records the deployed
  byte snapshot exclusively through `CommitLastApplied`, which already withholds
  the bytes for a secret-routed target (recording the content hash only). A pair
  of unused eager-persist entry points that snapshotted deployed content without
  that secret-routed gate has been removed, closing a latent path down which a
  future caller could have persisted secret plaintext into the non-secret
  bookkeeping file.

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
