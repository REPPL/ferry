# AGENTS.md

ferry is a macOS-first Go CLI that carries a terminal setup — dotfiles,
terminal settings, dependencies, and AI-agent instructions — across user
accounts and machines. A git config repo is the single source of truth:
`ferry apply` reconciles a machine to match it, `ferry capture` pulls local
changes back, and every change is backed up first so `ferry restore` reverses
it.

## Build, test, and checks

Run from the repo root; every command below has been executed here.

```bash
make build          # cross-compiles bin/ferry-<goos>-<arch> (there is no plain bin/ferry)
gofmt -l .          # format gate: any output names a file needing `gofmt -w`
go vet ./...        # static checks
go test ./...       # full unit + eval suite (evals skip when FERRY_BIN is unset)
go test ./internal/agents/                     # a single package
go test -run TestResolve ./internal/agents/    # a single test
```

The evals drive the real binary, so they need `FERRY_BIN` pointing at this
host's build (there is no plain `bin/ferry` — `make build` writes per-arch
binaries):

```bash
export FERRY_BIN="$PWD/bin/ferry-$(go env GOOS)-$(go env GOARCH)"
FERRY_BIN="$FERRY_BIN" go test ./evals/                                              # full eval suite
FERRY_BIN="$FERRY_BIN" go test -run TestApplyIdempotent_AC_apply_idempotent ./evals/ # one eval
```

Without `FERRY_BIN` the eval suite skips every behavioural test and passes.
CI (`.github/workflows/ci.yml`) runs build, vet, `go test ./...`, race tests
on the internal packages, and the full eval suite against a real Linux binary.

Never run git-mutating operations (`git worktree add`, merges, rebases)
against this repository while a `go test` or other tooling run is in flight
against it — concurrent heavy git activity has corrupted the working repo's
state (issue #17). One at a time; this also means no parallel subagents that
touch git.

## Boundaries

- Nothing under `$HOME` is ever symlinked: deployed files are regular-file
  copies reconciled by hash.
- `~/.ssh/` is untouchable — ferry never reads, writes, or captures it.
- Writes are containment-checked: a target resolving outside `$HOME` or under
  `~/.ssh` is refused, so a symlinked parent cannot redirect a write.
- `ferry capture` never rewrites a managed file without an explicit accept,
  and never auto-commits or pushes.
- The agents domain is repo-authoritative: edit the config repo copy, not the
  deployed target; a live edit shows as drift and `apply` skips it.

## Definition of done

- `make build`, `gofmt -l .` (empty), `go vet ./...`, and `go test ./...` are
  all clean, and the full eval suite is green with `FERRY_BIN` set.
- The docs under `docs/` stay in sync with any behaviour change.
- A CHANGELOG entry accompanies any user-facing change.

## Attribution

- **Naming a tool is confined to credit.** User-facing prose (`README.md`,
  `docs/`) stays host-agnostic. The one sanctioned place to name the AI
  assistant is attribution: the "Built with Claude Code" badge in `README.md`.
  Private, unpublished tool names never appear in any committed file.
- **AI-assisted commits carry an `Assisted-by:` trailer**, kernel format
  (`Assisted-by: Claude:claude-fable-5`) — disclosure, not authorship. Never
  `Co-Authored-By:` for AI (it asserts an authorship the tool does not hold and
  inflates the contributor graph). No "Generated with" lines or session links.
  The human is the author of record, responsible for all AI-assisted output —
  its correctness, licensing, and fit for the project.

## Working memory

Load `.abcd/work/CONTEXT.md` first — it is the curated, load-first summary of
ferry's standing facts and invariants. Record session handoff in
`.abcd/.work.local/NEXT.md` (git-ignored, local-only), not in `.abcd/work/`. See
[ADR 0002](.abcd/development/decisions/0002-work-memory-public-private-split.md) for the split.
