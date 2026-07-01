# evals — behavioral eval harness

This package drives the **real `ferry` binary** inside a sandboxed, throwaway
`$HOME` and asserts only **observable outcomes**: files present/absent, mode bits,
exit codes, stdout/stderr content, and absence-of-write tripwires. No eval ever
inspects ferry's source — each is a black-box check against the documented CLI
contract in `README.md`, `docs/getting-started.md`, `docs/configuration.md`, and
`docs/ssh.md`, mapped to the numbered criteria in `.work/ACCEPTANCE.md`.

## Status: RED until the build lands

The implementation does not exist yet. These evals are written **against the
documented contract first** and go red → green as the binary is built. That is the
point: they are the executable acceptance spec.

Every real assertion is gated behind `requireBin(t)`, which `t.Skip`s the test when
`FERRY_BIN` is unset or does not point at an executable. So the package **always
compiles** and `go test ./evals/...` runs **clean (all skipped)** before any binary
exists — no false failures.

## How to run

```bash
# 1. Build the binary (once the implementing wave lands):
make build                       # or: go build -o bin/ferry ./cmd/ferry

# 2. Point the harness at it and run the evals:
FERRY_BIN="$PWD/bin/ferry" go test ./evals/...

# Optional: also exercise the installer evals once install.sh exists:
FERRY_BIN="$PWD/bin/ferry" FERRY_INSTALL_SH="$PWD/install.sh" go test ./evals/...

# Final-conformance run (Wave-3+): a MISSING install.sh is a FAILURE, not a skip,
# for the offline-gating installer ACs — so an impl can't ship with no installer.
FERRY_CONFORMANCE=1 FERRY_BIN="$PWD/bin/ferry" go test ./evals/...
```

With **no** `FERRY_BIN`, `go test ./evals/...` skips every behavioral test and
passes. This is intentional — it lets the harness live in the repo and stay
green in CI before the binary is buildable.

### Installer evals & the conformance guard

The offline installer gates — **AC-install-path**, **AC-install-prints-path-line**,
and the installer-half of **AC-install-no-homebrew** — drive `install.sh`. On a
normal dev run, if `install.sh` is absent they **skip** with
`(Wave-3: install.sh not present)`; once it exists they **run and gate** (only the
network/`curl | bash` leg stays deferred). To stop an implementation from shipping
with **no installer at all**, the final-conformance run sets `FERRY_CONFORMANCE=1`
(or `FERRY_REQUIRE_INSTALL_SH=1`): under that flag a **missing `install.sh` FAILS**
these installer ACs instead of skipping. Dev runs stay green pre-Wave-3; the final
gate cannot silently pass without `install.sh`.

## Design

- **Isolation** — `NewSandbox(t)` builds a `t.TempDir()`-based throwaway `$HOME`
  with the documented XDG subdirs (`.config/ferry`, `.local/state/ferry`,
  `.local/bin`) and a separate fake config-repo dir. Nothing mutates the real
  `$HOME`. Each test gets its own sandbox, so tests are deterministic and
  parallel-safe (`t.Parallel()`).
- **Running the binary** — `Sandbox.Ferry(args...)` runs `FERRY_BIN` with
  `HOME=<sandbox>` and a controlled env, feeds **empty stdin**, and captures
  stdout/stderr + exit code under a **context timeout** so an unexpected
  interactive prompt can never hang the suite. `FerryWithInput` scripts stdin for
  interactive flows; `FerryEnv` appends/overrides env (e.g. `PATH`).
- **SSH tripwire** — `SSHTripwire(t)` seeds `~/.ssh/` with sentinel key/config/
  known_hosts files and snapshots their content + mode;
  `AssertSSHUntouched(t)` proves nothing under `~/.ssh/` was created, deleted, or
  modified. `AssertNoSecretInRepo` proves seeded secret bytes never reached the
  repo.
- **Write tripwires** — `SnapshotFile`/`AssertUnchanged` capture a single path's
  existence, content hash, and mode (including "still absent").
  `AssertNoWritesOutsideHomeAndInstallDir` samples the system roots the docs
  promise ferry never touches (`/etc`, `/usr`, `/usr/local`, `/opt`, `/Library`).
  **Honest limit:** that helper is not a full syscall tracer — a truly exhaustive
  "no write anywhere outside HOME" proof needs an OS-level sandbox (seatbelt /
  landlock), which is not portable across CI hosts. The strong locality guarantee
  here comes from `HOME` being a temp dir and asserting all expected artifacts
  live under it.

## Contract gaps (`TODO(contract)`)

Some doc promises don't pin an exact mechanism (the interactive capture
accept/reject/route keystroke protocol, a non-interactive `init` repo flag, the
repo's internal dotfile layout). Where the contract isn't doc-defined, the eval
asserts the **safe** observable (e.g. *capture with no approval writes nothing*;
*hunk-by-hunk → only the accepted hunk lands in the repo*) and carries a
`// TODO(contract): …` note to tighten once the protocol is fixed. These are
searchable: `grep -rn "TODO(contract)" evals/`.

Two ACs are **observable-as-written but defer their fixture bytes** to the named
**Layer-2 eval-vs-AC validation pass** (after the binary defines its schemas):
`AC-deps-install-attempted` gates the schema-free invocation *differential* (stub
PM invoked ≥1× under `--deps`, 0× under default `apply`); `AC-terminal-config`
gates the *native-preference-domain vs file-copy* differential for iTerm2 and
Apple Terminal (mechanism-agnostic — CFPreferences or `defaults` both valid). The
concrete dependency/terminal fixtures and live-store verification are deferred,
not dropped.

## File map

Command-surface gating is "the command **exists and runs**" (`--help` exits 0 and
the command appears in `ferry --help`); **help-text wording is non-gating** (logged
as a soft signal, never a failure), per the round-3 demotion in `ACCEPTANCE.md`.

| File | ACs covered |
|---|---|
| `harness.go` | shared sandbox, binary runner, SSH (incl. ReSnapshotSSH) + write tripwires, stub helpers (no tests) |
| `commands_test.go` | AC-cmd-* (exists-and-runs gating; help-text wording non-gating), AC-cmd-set-complete, AC-cmd-apply-deps-flag (--deps accepted + bogus rejected), AC-cmd-apply-dryrun-flag (optional, non-gating) |
| `apply_test.go` | apply-idempotent, conflict-refuse, local-wins, local-survives-apply, scope-respected-apply, deps-not-during-default-apply, deps-install-attempted (differential gate), backup-before-change (observable restore only) |
| `capture_test.go` | capture-interactive-route (shared/local/reject differential), capture-hunk-by-hunk, capture-no-autocommit (wrote→dirty + HEAD + bare-remote push tripwire), secret-blocked, scope-respected-capture (in-vs-out differential), ssh-not-captured |
| `scope_test.go` | **scope-bidirectional** (same allowlist governs apply AND capture) |
| `status_diff_test.go` | **status-reports-drift**, **diff-preview-only** (tripwire + predicted-vs-actual) |
| `restore_test.go` | restore-clean, backup-before-change (round-trip) |
| `safety_test.go` | ssh-untouched, ssh-not-read (level-1 floor + enforced fs_usage no-open where usable, else capability-skip), doctor-ssh-readonly (specific flag + correct-perms-not-flagged + tripwire), noadmin (no-sudo + succeeds-without-root), no-shell-edit, no-admin-system-locations |
| `locations_test.go` | loc-config-toml (identity + repo-path), apply-tracks-last-written (observable only), loc-dotfiles-copied-not-symlinked, loc-ferry-local-toml-gitignored, **loc-ferry-toml-in-repo**, **loc-local-overlay-dir**, **loc-local-materialised**, effective-scope-overlay |
| `init_test.go` | init-fresh (default `~/.config/ferry/repo` + `--fresh <dir>` override), init-clone-https (https-scheme accepted + no-SSH-key), **git-preflight** |
| `doctor_test.go` | doctor-reports-host-tools (healthy-vs-missing status) |
| `terminal_test.go` | terminal-config (iTerm2 + Apple Terminal; native-pref-domain vs file-copy differential; macOS-only) |
| `platform_test.go` | platform-macos (gating on darwin), platform-linux-core (non-gating doc note) |
| `install_test.go` | **install-path** (offline ~/.local/bin placement), install-single-binary, install-prints-path-line (exactly one line), install-no-homebrew + "or run anything else", **deps-uses-present-pm** (no-PM report + no-bootstrap tripwire) |

## Related documentation

- [Acceptance criteria](../.work/ACCEPTANCE.md) — the numbered spec these evals trace to
- [Configuration](../docs/configuration.md), [Getting started](../docs/getting-started.md), [SSH](../docs/ssh.md)
