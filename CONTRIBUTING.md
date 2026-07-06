# Contributing to ferry

ferry is a macOS-first Go CLI. This page is the how-to for making a change land:
how to build and check it, how to shape the commit and PR, and where each kind of
document and decision belongs.

## Build, test, and checks

Run from the repo root.

```bash
make build          # cross-compiles bin/ferry-<goos>-<arch> (there is no plain bin/ferry)
gofmt -l .          # format gate: any output names a file needing `gofmt -w`
go vet ./...        # static checks
go test ./...       # full unit + eval suite (evals skip when FERRY_BIN is unset)
```

The eval suite drives the real binary, so it needs `FERRY_BIN` pointing at this
host's build:

```bash
export FERRY_BIN="$PWD/bin/ferry-$(go env GOOS)-$(go env GOARCH)"
FERRY_BIN="$FERRY_BIN" go test ./evals/
```

Without `FERRY_BIN` the eval suite skips every behavioural test and passes. A
change is done only when `make build`, `gofmt -l .` (empty), `go vet ./...`, and
`go test ./...` are all clean and the eval suite is green with `FERRY_BIN` set.

## Branches, commits, and PRs

- Work on a branch and open a PR, so review and CI gate the merge. Keep commits
  small and atomic — one purpose per commit; a behaviour-preserving refactor and
  a bug fix are separate commits.
- Write [Conventional Commit](https://www.conventionalcommits.org) subjects with
  no scope: `feat`, `fix`, `chore`, `refactor`, `docs`, or `test`. The body
  explains *why*.
- A PR that closes an issue includes `Closes #N` in its body.
- Code, identifiers, comments, and commit messages are US English; user-facing
  prose (docs, README) is British English.

## Documentation

Every user-facing change keeps the docs and the CHANGELOG in sync.

- Markdown is the single source of truth. Every page is exactly one
  [Diátaxis](https://diataxis.fr) type — tutorial, how-to, reference, or
  explanation — and no page mixes types. The
  [documentation map](docs/README.md) states where each page lives.
- Docs are present tense: what *is*, never what was superseded or is planned.
  History lives in git; intent lives in `docs/plans/`.
- The repository root carries only `README.md`, `AGENTS.md` (with its
  `CLAUDE.md`/`GEMINI.md` bridges), `CHANGELOG.md`, `CONTRIBUTING.md`,
  `SECURITY.md`, and `LICENSE`. Every other Markdown file lives under `docs/`.

## Decisions and the ADR workflow

Architecture decisions are recorded as [MADR](https://adr.github.io/madr/) files
under `docs/decisions/`, named `NNNN-title.md` with a zero-padded sequential
number (see [ADR 0001](docs/decisions/0001-adr-naming-sequential-madr.md)).

- To add an ADR, take the **next free number** on your branch — there is no
  reservation registry.
- If two branches mint the same `NNNN`, a duplicate-`NNNN` lint fails CI and the
  second branch to merge renumbers its ADR. Do not resolve the clash by force.
- One-line session decisions live in `.work/DECISIONS.md`; promote a decision to
  a `docs/decisions/` ADR when it shapes architecture or is expensive to reverse.

## Working memory

Curated standing facts load first from `.work/CONTEXT.md` (committed). Session
handoff, scratch output, and logs live under `.work.local/` (git-ignored via
`.git/info/exclude`, never committed). See
[ADR 0002](docs/decisions/0002-work-memory-public-private-split.md) for the
rationale. Because `.work/` is committed, its contents follow the repository's
privacy rules: no local absolute paths, secrets, or live data.
