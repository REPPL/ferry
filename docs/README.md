# Documentation map

This map states where every document in this repository lives and what each
directory holds. Every page is exactly one Diátaxis type — tutorial
(learning), how-to (task), reference (information), or explanation
(understanding) — and no page mixes types.

## Pages

| Page | What it covers |
|---|---|
| [Getting started](getting-started.md) | Zero to a working setup: install, first capture, another machine |
| [Configuration](configuration.md) | The manifest, scope, and the `.local` layer |
| [Commands](commands.md) | Every `ferry <command>` and what it does |
| [Agents](agents.md) | The agents domain: instruction files, harnesses, asset mappings, `scaffold` and `adopt` |
| [SSH](ssh.md) | Why ferry is hands-off with `~/.ssh/`, and how to move keys yourself |
| [Cutting a release](RELEASE.md) | How releases are built, checksummed, attested, published, and pruned |
| [Compatibility contract](reference/compatibility.md) | Which surfaces are stable, the pre-1.0 rule, and on-disk state versioning |

## Directories

| Directory | Contents | Naming |
|---|---|---|
| `tutorials/`, `how-to/`, `reference/`, `explanation/` | The four Diátaxis types; each directory is created on first use | — |
| `decisions/` | Architecture decision records, MADR format | `NNNN-title.md` |
| `research/` | Dated research notes | `YYYY-MM-DD-topic.md` |
| `plans/` | Dated implementation plans | `YYYY-MM-DD-topic.md` |
| `assets/` | Images and other static assets | — |

## Root-markdown allowlist

The repository root carries only `README.md`, `AGENTS.md` (with its
`CLAUDE.md`/`GEMINI.md` symlink bridges), `CHANGELOG.md`, `CONTRIBUTING.md`,
`SECURITY.md`, and `LICENSE`. Every other Markdown file lives under `docs/`.
