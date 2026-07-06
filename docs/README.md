# Documentation map

This map states where every document in this repository lives and what each
directory holds. Every page is exactly one Diátaxis type — tutorial
(learning), how-to (task), reference (information), or explanation
(understanding) — and no page mixes types.

## Pages

| Page | Type | What it covers |
|---|---|---|
| [Getting started](tutorials/getting-started.md) | Tutorial | Zero to a working setup: install, first capture, another machine |
| [Configuration](reference/configuration.md) | Reference | The manifest, scope, and the `.local` layer |
| [Commands](reference/commands.md) | Reference | Every `ferry <command>` and what it does |
| [Compatibility contract](reference/compatibility.md) | Reference | Which surfaces are stable, the pre-1.0 rule, and on-disk state versioning |
| [The agents domain](explanation/agents.md) | Explanation | The repo-authoritative model: harnesses, asset mappings, apply/restore semantics |
| [Why ferry stays out of `~/.ssh/`](explanation/ssh.md) | Explanation | The rationale and the untouchable-`~/.ssh` invariant |
| [Scaffold a project repo](how-to/scaffold-a-repo.md) | How-to | `ferry agents scaffold`: stamp a repo with the multi-tool pipeline layout |
| [Adopt an existing agent config](how-to/adopt-agent-config.md) | How-to | `ferry agents adopt`: migrate a symlink-based instruction directory into ferry |
| [Move SSH keys to a new machine](how-to/move-ssh-keys.md) | How-to | Carry your SSH setup across machines yourself, out-of-band |
| [Cutting a release](how-to/cutting-a-release.md) | How-to | How releases are built, checksummed, attested, published, and pruned |

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
