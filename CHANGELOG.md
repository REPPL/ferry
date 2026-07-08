# Changelog

All notable changes to ferry are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and ferry
uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html) with a
leading `v`.

Before v1.0.0, minor releases may make breaking changes; each one is
called out in a **Breaking** section. See
`docs/reference/compatibility.md` for the pre-1.0 compatibility rules.

## [Unreleased]

## [0.7.1] - 2026-07-08

### Changed

- **Developer documentation moves out of the user-facing `docs/` tree into a new
  `.abcd/development/` namespace.** ferry's plans, research notes, and
  architecture decision records (ADRs) now live under
  `.abcd/development/{plans,research,decisions}/`, leaving `docs/` for
  user-facing Diátaxis documentation only. `ferry agents scaffold` stamps the
  same layout: a scaffolded repo gets `docs/` reserved for Diátaxis content and
  `.abcd/development/` for its developer records.

## [0.7.0] - 2026-07-08

### Added

- **git (`~/.gitconfig`) is now a carried dotfile with a native `[include]`
  overlay and a machine-identity firewall.** Declare `".gitconfig"` in the
  `dotfiles` list and ferry carries `dotfiles/gitconfig` to `~/.gitconfig`. Like
  the zsh and tmux files it is an include-sidecar dotfile, but the injected
  directive is git's own two-line `[include]` block (`path = ~/.gitconfig.local`)
  appended last, so git last-wins-merges the machine-local file — a native
  equivalent of ferry's overlay. Machine **identity is never shared**:
  `user.email`, `user.name`, `user.signingkey`, `gpg.program`,
  `credential.helper`, `credential.<host>.username`, and every
  `[includeIf "gitdir:…"]` block are forced to the
  never-shared `~/.gitconfig.local` layer, so one machine's commit identity can
  never reach the shared `~/.gitconfig` or the shared repo — even a source that
  mistakenly commits identity has it stripped on deploy. A literal token embedded
  in a `url.*.insteadOf` value (`https://<TOKEN>@host/`, `https://oauth2:<TOKEN>@…`)
  or after `Bearer`/`Basic` in an `http.extraHeader` is caught on capture: only
  the token is routed to the out-of-repo secret store and replaced with a
  `{{ferry.secret …}}` placeholder — the surrounding URL scheme/host and the
  `Authorization: Bearer ` prefix stay byte-identical — and rendered back on
  deploy. A `${GIT_TOKEN}` env-ref is left in the repo verbatim.
  `credential.helper = store` (which writes plaintext `~/.git-credentials`) is
  warned about and never carried; ferry never reads or writes `~/.git-credentials`.

- **npm's `~/.npmrc` is now a carried dotfile with registry-token redaction.**
  Declare `".npmrc"` in the `dotfiles` list and ferry carries `dotfiles/npmrc`
  to `~/.npmrc` as a plain whole-file dotfile (no include sidecar). Only the
  user-level `~/.npmrc` is carried, never a per-project one. A registry auth line
  — `//host/:_authToken=…`, `:_auth=…`, or `:_password=…` — is recognised by the
  secret scanner as a high-confidence credential, so a literal token is blocked
  from the shared repo on capture: route it to the out-of-repo secret store and a
  `{{ferry.secret …}}` placeholder is committed in its place, which `apply`
  renders back to the real token (deployed `0600`) so npm reads it. The
  recommended pattern is an environment reference (`_authToken=${NPM_TOKEN}`),
  which npm expands at read time and which ferry carries to the shared repo
  verbatim; the token then never lives in the config at all. The token is never
  written as plaintext into the committed repo.

- **tmux is now a carried dotfile with a per-machine `.local` sidecar.** Declare
  `".tmux.conf"` in the `dotfiles` list and ferry carries `dotfiles/tmux.conf`
  to `~/.tmux.conf`. Like the zsh shell files, it is an include-sidecar dotfile:
  when a per-machine overlay exists at `local/tmux/tmux.conf.local`, `apply`
  appends `source-file -q ~/.tmux.conf.local` last (so the sidecar wins) and
  materialises `~/.tmux.conf.local`; `capture` strips that injected directive
  before writing the shared source and routes per-machine edits to the sidecar.
  A secret inside a quoted tmux option (`set -g @token '…'`) is caught on
  capture — only the quoted value is routed to the out-of-repo secret store and
  replaced with a `{{ferry.secret …}}` placeholder, leaving the `set -g @token '…'`
  syntax and quotes byte-identical — and rendered back on deploy. A `${ENV}`
  reference is left in the repo verbatim.

- **macOS key bindings are now a managed domain.** Enable `keybindings = true`
  under `[manage]` and ferry carries `keybindings/DefaultKeyBinding.dict` from the
  config repo to `~/Library/KeyBindings/DefaultKeyBinding.dict` as a regular-file
  copy reconciled by hash. The domain is repo-authoritative (edit the repo copy
  and `apply` deploys it; a live edit shows as drift and `apply` skips it) with no
  per-machine `.local` overlay. The source must stay the readable old-style
  (NeXT/ASCII) text property list: ferry validates it with `plutil -lint` on macOS
  and refuses a binary plist (`bplist00` header), a UTF-8 byte-order mark, or
  non-UTF-8 bytes before deploying, and never runs `plutil -convert` on it.
  Bindings load at app launch, so relaunch an affected app to pick up changes.

- **The Emacs configuration tree is now a managed domain.** Enable `emacs = true`
  under `[manage]` and ferry carries the config repo's `emacs/` tree to
  `~/.emacs.d/`, fanned out file by file as regular-file copies reconciled by hash
  (ferry never symlinks `~/.emacs.d/`). Volatile, machine-generated paths are
  pruned and never deployed even if present: `elpa/`, `eln-cache/`, `*.elc`, the
  tangled `inits/repp.el`, `auto-save-list/`, `transient/`, `url/`,
  `network-security.data`, and session state (`recentf`, `savehist`, `saveplace`).
  The domain is repo-authoritative (edit the repo source and `apply` deploys it; a
  live edit shows as drift and `apply` skips it) with no capture pass. The
  per-machine `.local` overlay applies per file: `local/emacs/<relpath>` wins over
  the shared `emacs/<relpath>`, the home for a Customize-written `inits/custom.el`
  or a hand-authored `init.local.el`. Emacs files participate in the secret store
  like dotfiles. Note `~/.emacs.d` shadows the XDG `~/.config/emacs`.

- **iTerm2 Dynamic Profiles are now a managed domain.** Enable
  `iterm2-profiles = true` under `[manage]` and ferry carries the config repo's
  `iterm2/DynamicProfiles/*.json` to
  `~/Library/Application Support/iTerm2/DynamicProfiles/`, fanned out file by file
  as regular-file copies reconciled by hash (ferry never symlinks them). The
  domain is repo-authoritative (edit the JSON and `apply` deploys it; a live edit
  shows as drift and `apply` skips it) with no capture pass; set
  `"Rewritable": false` in the profile so iTerm2 will not rewrite it either. A
  profile's `"Guid"` is a **frozen identity** — ferry copies the JSON byte-for-byte
  and never generates or rewrites a GUID. Each file is validated (as JSON on every
  platform, and through `plutil` on macOS) before it lands, because one malformed
  file disables all of iTerm2's dynamic profiles; a file that fails is warned about
  and skipped. The per-machine `.local` overlay applies per file:
  `local/iterm2-profiles/<name>.json` wins over the shared copy, and a file present
  only there deploys as a machine-only profile (e.g. a child profile referencing a
  shared parent's GUID). Profiles participate in the secret store like dotfiles.

- **`ferry doctor` now observes ferry's managed-target invariants** (read-only):
  it reports whether any deployed target is a symlink (ferry deploys regular-file
  copies, never symlinks), whether any managed target resolves under `~/.ssh`,
  and whether every managed target resolves inside `$HOME`. A genuine breach —
  a symlink where a copy belongs, an escape from `$HOME`, or a target under
  `~/.ssh` — is reported `[fail]` and makes doctor exit non-zero; a machine with
  nothing managed yet is advisory. The check is stat/lstat-only and never reads
  file contents.

- **npm globals are a new managed deps domain.** Enable `npm-globals = true`
  under `[manage]` and ferry carries the machine's globally-installed npm package
  **names** (never versions) in `deps/npm-globals.txt`. `ferry capture` re-dumps
  the list from `npm ls -g` (sorted, names-only, npm itself excluded);
  `ferry apply --deps` reconciles it with `npm i -g`; `ferry status` reports drift
  (packages installed but unrecorded, or recorded but not installed). npm globals
  are **install/reconcile-only** — ferry never uninstalls them and `restore
  --packages` leaves them intact. npm is detected independently of the OS package
  manager, so it runs **alongside** Homebrew/apt, not instead of it; when npm is
  absent the domain skips cleanly. Entries are validated as plain package names,
  so a git-URL, tarball-URL, local-path, or version-tagged spec in the list is
  refused before `npm i -g` runs. The `~/.npmrc` config file and its registry
  auth token are carried separately, as a dotfile (see below).

- A "Built with Claude Code" attribution badge in the README, and an
  **Attribution** policy in `AGENTS.md`: the badge is the one sanctioned place
  to name the AI assistant, user-facing prose stays host-agnostic, and commits
  carry no AI trailers.
- README badges for the CI workflow status and the Go toolchain version.

### Changed

- **`ferry status` now reports Homebrew Brewfile drift.** When `brew = true` is
  managed, status compares the live `brew bundle dump` against the repo Brewfile
  (shared plus per-machine overlay) and reports how many entries would be captured
  or installed — READ-ONLY: it never installs a package and never rewrites the
  Brewfile. The comparison is by package identity (directive plus name), so a
  benign option or version difference does not read as drift. This reconciles the
  deps managed surface: `status` and `capture` both key on `[manage] brew`, while
  `apply --deps` remains the explicit opt-in that performs the install. Homebrew
  stays install/reconcile-only — no `brew bundle cleanup` path exists.

- **`ferry export` labels its bundle SHA256 as reproducible.** The digest is a
  deterministic function of the tracked sources — exporting the same sources
  always yields the same SHA (no timestamps or randomness are bundled) — so a
  recipient can recompute it and confirm a bundle was produced from the same
  inputs. The output line and `--help` text now say so; `import --expect-sha256`
  remains the verification path.

- **The iTerm2 global preferences (`iterm2 = true`) now use an import-blob model
  with an allowlist.** `capture` exports the live `com.googlecode.iterm2` domain
  and keeps **only** an allowlisted set of stable, machine-agnostic global keys
  (quit/close prompts, tab and window chrome behaviour, dimming, clipboard
  behaviour, the auto-update preference, the default-profile pointer), dropping
  everything else — so volatile machine state (window geometry, one-shot `NoSync…`
  flags) can never reach the repo. `apply` imports the committed filtered plist
  (`iterm2/com.googlecode.iterm2.plist`) via `defaults import`. Because a running
  iTerm2 rewrites the domain on quit, `apply` **refuses the import while iTerm2 is
  running** and asks you to quit it and re-run; after a successful import ferry runs
  `killall cfprefsd` so the daemon does not serve a stale cache (relaunch iTerm2 to
  see the change). The `.local` layer applies whole-domain
  (`local/iterm2/com.googlecode.iterm2.plist`).

- The default built-in harness registry ships a trimmed set of coding CLIs;
  any additional harness is declared via `[agents.harness.<name>]` — data, not
  a code change.

### Breaking

- **iTerm2's custom-prefs-folder mechanism is retired** (v0.7.0 D4). Earlier
  releases deployed `iterm2 = true` by pointing iTerm2's `PrefsCustomFolder` at the
  repo folder via `defaults write`; ferry now imports an allowlist-filtered plist
  with `defaults import` instead, and `apply` refuses while iTerm2 is running. An
  existing committed `iterm2/com.googlecode.iterm2.plist` still applies, but it is
  now imported into the live domain (quit iTerm2 first) rather than loaded from a
  folder, and the next `capture` reduces it to the allowlisted global keys. Re-run
  `ferry capture` (with iTerm2 quit for a subsequent `apply`) to record the filtered
  plist. Profiles now belong in the separate `iterm2-profiles` domain as Dynamic
  Profiles JSON.

## [0.6.0] - 2026-07-07

### Security

- **The backup write boundary now closes the leaf-swap TOCTOU.** `apply` and
  `restore` already re-checked the resolved parent chain at the write boundary
  (`guardResolvedContainment`), which defeats a swapped intermediate parent. That
  check does not stop a same-user process from swapping the *final* path
  component to a symlink into `~/.ssh` or outside `$HOME` in the window between
  the check and the write. Every leaf-level mutation now runs through an `os.Root`
  opened on the target's parent directory, operating on the basename only;
  `os.Root` refuses to traverse a final-component symlink that escapes the parent,
  so a raced leaf swap can no longer redirect the write or remove. The parent-chain
  guard is unchanged — this is a second, additive defence.

### Added

- **A generated CLI reference under `docs/reference/cli/`.** `make gen-docs` walks
  the Cobra command tree and writes one Markdown page per command (deterministic —
  no timestamp footer). The tree is committed so it renders on GitHub, and a CI
  currency check clean-regenerates it and fails on any added, changed, or removed
  command page.
- **A `consistency-lint` gate** (`scripts/consistency-lint.sh`, wired into CI):
  no two ADRs share an `NNNN` prefix, every ADR is named `NNNN-title.md`, and no
  prose or config file points session handoff at `.work/NEXT.md`.
- **Root `CONTRIBUTING.md` and `SECURITY.md`.**
- **The abcd information-architecture documentation tree.** Docs are reorganised
  into Diátaxis types (`tutorials/`, `how-to/`, `reference/`, `explanation/`),
  with the decision record under `docs/decisions/` (sequential MADR ADRs; 0001
  records the naming decision, 0002 the working-memory split).
- **A committed `.work/` working-memory tier** holding `DECISIONS.md` (append-only
  log) and `CONTEXT.md` (curated load-first standing facts); `ferry agents scaffold`
  now emits `.work/CONTEXT.md` for scaffolded repos.

### Breaking

- **`NEXT.md` moves to the private `.work.local/` layer.** Session handoff is no
  longer committed. `ferry agents scaffold` now emits `NEXT.md` to
  `.work.local/NEXT.md` (both modes) and adds `.work/CONTEXT.md`. Tracked-mode
  scaffold therefore requires a new `agents/templates/CONTEXT.md` in the config
  repo — scaffold errors clearly if it is missing. **Migration for an existing
  config repo:** add `agents/templates/CONTEXT.md`; for a repo you already
  scaffolded, `mv .work/NEXT.md .work.local/NEXT.md` and add `.work/CONTEXT.md`
  (scaffold never overwrites, so it will not do this for you).
- **Documentation pages moved to Diátaxis paths.** External links to the old flat
  `docs/*.md` pages (`docs/getting-started.md`, `docs/configuration.md`,
  `docs/commands.md`, `docs/agents.md`, `docs/ssh.md`, `docs/RELEASE.md`) break;
  the new homes are under `docs/{tutorials,how-to,reference,explanation}/` per the
  map in `docs/README.md`.

### Dependencies

- `github.com/cpuguy83/go-md2man/v2` and `github.com/russross/blackfriday/v2` are
  now indirect requires, pulled in by `github.com/spf13/cobra/doc` for the CLI
  reference generator. The generator lives in a standalone `tools/gendocs` main
  package that the ferry binary never imports, so neither module is linked into
  the shipped `ferry` binary.

## [0.5.3] - 2026-07-06

### Security

- **The capture secret-gate now blocks several credential shapes that previously
  reached the shared repo.** Indented credential assignments are scanned (the
  gate no longer requires the key at column zero); named provider tokens are
  recognised by their prefix regardless of the surrounding key name (AWS access
  keys, GitHub tokens and fine-grained PATs, Google API keys, Slack tokens,
  OpenAI `sk-`/`sk-proj-` keys, and Stripe live keys); an AWS-style secret whose
  base64 body contains slashes is no longer mistaken for a filesystem path;
  structural credential fields (`private_key_id`, `pat`, `bearer`) and the
  quoted-JSON key form (`"api_key": "…"`) are caught; a credential value carried
  on continuation lines (a YAML block scalar or a heredoc body) is associated
  with its key; and a long hexadecimal value is flagged when its key names a
  credential. Ordinary content that merely resembles a secret — a git SHA, an
  MD5 or SHA-256 digest, a dash-stripped UUID, a `sk-button` CSS class, and
  filesystem paths — is deliberately left unblocked.
- **The out-of-repo secret store rejects a path-traversal reference.** A secret
  reference's domain and key must now match `[A-Za-z0-9_-]+`, so a crafted
  placeholder can no longer read or write a file outside the flat store root, and
  a non-UTF-8 secret value is refused before it can corrupt a domain file.
- **A private-key span extracted from shell config no longer swallows a ferry
  placeholder.** The PEM-span widener that both the capture and the zsh plugin
  paths use now stops before a `{{ferry.secret …}}` line, so a placeholder can
  never be stored inside a secret value and left literal in the deployed file.
- **A cloned or wired config repo is now treated as untrusted git input.** Every
  git command ferry runs against it — the clone, the working-tree probe, and the
  fetch/rebase/push of `ferry sync` — disables repository hooks and the
  filesystem monitor, denies the `ext` transport, and restricts the `file`
  transport to explicit user actions. A hostile `.git/config` can therefore no
  longer run a command through a hook, through `core.fsmonitor`, or through a
  `url.<…>.insteadOf = ext::…` URL rewrite. The clone additionally refuses a
  source beginning with `-` and passes the source after `--` so it can never be
  read as a git option, `ferry sync` re-checks the URL it would actually push to
  after any `insteadOf`/`pushInsteadOf` rewrite and still refuses anything but
  HTTPS, and its fetch disables submodule and tag fanout. The GitHub push keeps
  its own credential-helper path.
- **A Brewfile from a cloned repo is gated before Homebrew runs it.**
  `ferry apply --deps` now refuses a `Brewfile` directive outside a strict
  allow-list (`brew`, `cask`, `mas`, `tap`, `vscode`, `whalebrew` with plain
  name-shaped arguments), rejecting a URL or custom-tap argument, a local-path
  formula, a Ruby string-interpolation marker (`#{`, `#@`, or `#$`) that Homebrew
  would evaluate, and an `args:`/postflight block that could run install-time code. The
  gate is fail-closed: a hand-edited Brewfile that uses an inline comment or a
  single-quoted directive is rejected, but ferry's own `brew bundle dump` output
  is unaffected — the allow-list is a superset of what `brew bundle dump` emits,
  so capturing and re-applying ferry's own manifest still round-trips.
- **The apt package rail resolves its binaries by trusted absolute path.**
  `apt-get` and `dpkg-query`, which run as root under `sudo ferry apply --deps`,
  are now resolved through a sanitised system path rather than the inherited
  `PATH`, so a `sudo` configured without `secure_path` cannot let a poisoned
  `PATH` entry hijack them; the `dpkg-query` probe also carries a `--`
  end-of-options separator.
- **Terminal configs render `{{ferry.secret …}}` placeholders like dotfiles, and
  a secret-bearing deployed file is no longer world-readable.** A terminal config
  under `terminals/` now goes through the same render-or-skip pipeline as a
  dotfile: a referenced secret that is present is substituted before the file is
  deployed, and a referenced secret that is missing skips the whole target rather
  than writing a literal placeholder into the live terminal config. Any deployed
  file whose bytes were rendered from the secret store — a dotfile or a terminal
  config — has group and other access stripped from its mode (`0600`, or `0700`
  for an executable), so the plaintext credential is not readable by other
  accounts on the machine. This holds whether the file is being created OR an
  existing one is being updated in place: adopting a world-readable `0644`
  `~/.wezterm.lua` or `~/.gitconfig` whose repo source is secret-routed tightens
  it rather than preserving the readable mode. The clamp is forward-only — it
  applies on the next write of an already-deployed file, not retroactively to a
  file ferry does not rewrite this run. The apply core is the single
  enforcement point for this and for keeping the plaintext out of ferry's
  last-applied snapshot, so no caller can bypass either. A real secret committed
  under `terminals/` is caught by the same push gate as any other managed file.
- **`ferry restore` now serialises against a concurrent `ferry apply`.** A revert
  (file/domain restore, `--packages` uninstall, or `--purge-without-recovery`)
  takes the same exclusive apply lock that `apply` does, held for the whole
  operation and released on every exit path. A restore attempted while an apply is
  in progress now fails closed with a clear message instead of racing it and
  interleaving writes to the same managed paths. A genuine no-op restore (nothing
  recorded, no `--packages`/`--purge`) still reports "nothing to restore" without
  taking the lock or creating an empty state store.

### Changed

- Internal, no output change: the `agents scaffold` file layout is now a single
  declarative table (`scaffoldLayout`) that both scaffold modes walk, replacing
  the duplicated hardcoded `put(...)` sequences. A golden characterization test
  locks the scaffold output byte-for-byte across tracked, private, and
  attribution modes.

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
  root under `sudo ferry restore --packages`; the state directory is `0700`
  (the record file itself `0600`) and symlink-hardened, so exploitation requires
  tampering with local ferry state,
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
  `opencode`, `gemini`), and an optional `devtree` deploys the
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
