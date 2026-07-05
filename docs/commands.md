# Commands

Every command is run as `ferry <command>` (e.g. `ferry init`).

| Command | What it does |
|---|---|
| `init` | First-run setup: locate/clone the config repo into ferry's own space (`~/.config/ferry/repo` by default), write ferry's config. On a fresh interactive run (stdin and stdout both ttys) an adoption **wizard** scans your existing `~/.zshrc` and lets you keep it as-is, route it per block (shared / local / drop), or start fresh from a portable starter: nothing is written before the preview confirm, the original is kept in a timestamped `~/.zshrc.ferry-<ts>.bak`, and secret-shaped lines are always routed to the out-of-repo secret store or dropped, never seeded. Non-interactively (or with `--yes`/`--no-wizard`) the same adopt happens without prompts: secrets are extracted to the store automatically (refs listed on stderr) and everything else is kept shared verbatim. The plugin set is currently zsh (`~/.zshrc`); more domains come with later releases. |
| `init --yes` | Don't ask anything: skip the wizard (plain adopt with automatic secret extraction) and assume yes for the confirmations init would otherwise ask (the `--github` create-confirm; the closing apply confirm with `--apply`). |
| `init --no-wizard` | Skip the interactive wizard only (same non-interactive adopt-and-extract fallback), without `--yes`'s other confirmation assents. |
| `init --repair` | Opt into the wizard's repair review: hardcoded `/Users/<name>` paths to `$HOME`, duplicate `PATH` exports, dead `source` lines: each fix is accepted or declined individually. Needs the interactive wizard, so it conflicts with `--yes`/`--no-wizard` and with a non-tty run — unless `--wizard-answers` is given, which satisfies the consent requirement and composes with them. |
| `init --wizard-answers <file>` | Drive every wizard decision from a TOML answers file (mode, per-block routes, secret routes, repairs, starter answers) instead of the TUI: same gates, preview, backup, and confirm, no tty needed. |
| `init --github [name]` | Create a **new private** GitHub repo via the `gh` CLI's existing auth and manage it as ferry's HTTPS remote. Needs `gh` authenticated; ferry stores no token. Always private, never reuses an existing repo, and won't push a file that looks like a secret: the wizard (or the non-interactive fallback) extracts detected secrets to the local store first, so only placeholders are committed and pushed. Add `--yes` for non-interactive use. |
| `apply` | Reconcile this machine to the repo (deploy dotfiles, terminal settings). Idempotent; safe to re-run. Dependencies install behind `apply --deps`. |
| `capture` | Pull local changes back into the repo. Interactive: approve each change, route it *shared* (synced everywhere) or *local* (this machine only). For sources that reference stored secrets, capture compares against the rendered content and splices your edits back around the placeholders, so stored values never re-enter the repo and a store-routed secret never blocks its own round-trip. |
| `sync` | Publish captured changes and pull remote ones for a managed repo, in one command. Integrates the remote first, never force-pushes, gates the whole push range for secrets, and leaves your machine unchanged on a conflict. Route-1 repos need `--allow-unmanaged`. Run `ferry apply` after to deploy pulled changes. |
| `status` | Report config drift (what changed on this machine). |
| `doctor` | Report machine/tool health. |
| `diff` | Preview what `apply` would change. |
| `restore` | Reverse ferry's changes, returning the machine to its pre-ferry state from an automatic backup. |
| `export` | Write a portable, secret-scanned `.zip` bundle of the repo's tracked shared files for an offline move. Prints the bundle SHA256. Never includes secrets, `~/.ssh`, or the local layer (unless `--include-local`). |
| `import` | Ingest a bundle into a fresh config repo (`~/.config/ferry/repo` by default), validate it fully, then write ferry's config. Refuses a non-empty target. `--expect-sha256 <hash>` verifies integrity. |
| `agents scaffold` | Set up a project repo for AI-agent work from the templates in the config repo's `agents/` area: an `AGENTS.md` router, `CLAUDE.md`/`GEMINI.md` bridges, and committed `.work/` handoff files. `--private` instead creates a `.work.local/` layer hidden via `.git/info/exclude` — for repos you don't own, it leaves zero tracked trace. `--attribution` (mutually exclusive with `--private`) instead installs a `prepare-commit-msg` hook that appends a kernel-style `Assisted-by:` trailer to agent-authored commits, for repos that require AI disclosure. Idempotent; never overwrites or repoints anything it didn't create. Works in linked worktrees and submodules. |
| `agents adopt` | One-time migration of an existing symlink-based instruction setup into the config repo: imports the source files (never modifying the source directory), then swaps each `$HOME` bridge symlink for a ferry-managed copy in a single journalled transaction — any failure rolls back and the symlinks return. Refuses directory-level bridges with exact instructions rather than writing through them. |
| `version` | Print the version; `--verbose` adds the Go version and platform. |

The agents *domain* itself (which instruction files deploy where, for which coding
CLIs) is enabled with `agents = true` in the manifest and rides the normal
lifecycle: `apply` deploys, `status`/`diff` report drift, `capture` treats the repo
as authoritative, and `restore agents` reverts the domain even when the config
repo is gone. See [Agents](agents.md).
