# Deterministic, routable hunk identity in hand-edited dotfiles

Date: 2026-07-08 · Type: explanation / research

This note studies how ferry should identify a change "hunk" in a hand-edited
dotfile so that the identity is stable across machines and across unrelated
edits, routable to shared-vs-local, and friendly to both humans and agents. It
changes no code: it is an approved SOTA study whose recommendation the
maintainer can accept, reject, or defer, family by family.

The headline: ferry's `capture` command identifies hunks via `diffHunks` in
`cmd/capture.go:1225` — a hand-rolled LCS line diff whose hunk identity **is**
its line range (`repoStart`/`repoEnd`) computed against the last-applied
baseline. That identity drifts the moment nearby lines change, because a line
range is a position and positions move. The key reframe is that **this is a
naming problem, not a diffing problem.** Stable identity comes only from
regions that are explicitly *named* (markers) or that *are files* (drop-ins).
No diff algorithm recovers it, because every diff family — line, structural,
content-hash — derives identity from position or content, and both of those
move when the file is edited. The good news: two of the four families below
give ferry a genuinely stable, routable identity for cheap, and the field has
shipped both patterns for years.

## The reframe: identity comes from naming, not diffing

A diff answers "what changed between version A and version B." It does not
answer "which managed region is this, independent of what else changed" — that
is an identity question, and identity for a region of text has only two durable
sources:

- The region **is a file.** A file has a name, and the name is stable across
  machines and across edits to every *other* file. This is drop-in sharding.
- The region **is explicitly named in place.** A sentinel marker pair around
  the region gives it a content-independent, position-independent handle. This
  is the managed-block pattern.

Everything else — line ranges, AST node paths, content hashes — is derived
from position or content, so it is not identity: it drifts. That is the whole
thesis. The families below are ranked by value-for-effort against ferry's
actual constraints: stable, routable, shared-vs-local aware, agent-friendly,
and cheap to implement in a Go CLI.

## Ranked approach families

### 1. Drop-in directory sharding — highest value-for-effort

Each small file **is** the hunk; identity is the filename. That identity is
stable across machines and stable across edits to every other file, which is
exactly the property ferry's line-range identity lacks. The entire
implementation cost is `filepath.Glob` over a directory — no diff, no
matching, no baseline arithmetic. Routing shared-vs-local becomes directory
placement: `zshrc.d/shared/` versus `zshrc.d/local/`, and the shard name is
the routing key.

The pattern is deeply established as the operating-system convention for
composable configuration: `/etc/profile.d`, systemd drop-in directories,
`conf.d` layouts across countless daemons, oh-my-zsh's `custom/` directory,
and the community `~/.zshrc.d/NN-name.zsh` modularization pattern. chezmoi's
own guidance for machine-to-machine differences leans on splitting config into
separately-managed pieces rather than diffing one monolith.

Cost and caveat: it requires a one-time restructure of a user's monolithic
`.zshrc`/`.bashrc` into a directory the shell sources, and it only works for
files that *can* source a directory. Files that cannot (a single-file config
with no include mechanism) fall back to family 2.

### 2. Sentinel / marker-delimited managed blocks — best for in-place monoliths

When the file must stay a single file, a named marker pair gives the region a
content-independent, position-independent identity:

```
# >>> ferry:<id> >>>
...managed content...
# <<< ferry:<id> <<<
```

The identity is the `<id>`, not the line range, so it survives unrelated edits
above or below. Detection is a cheap regex scan in Go, and the operation is
idempotent: a re-run replaces only the bytes between the two markers, touching
nothing the user wrote outside them.

The reference design is Ansible's `blockinfile` module: markers are *required*
for idempotency, and unique markers let multiple managed blocks coexist in one
file. The same pattern is what every shell-init installer already writes into
your rc files — conda `init`, nvm, rbenv, pyenv, and Homebrew's shellenv block
all delimit their managed region with sentinels.

The failure modes are well documented and must be designed out:

- **User edits inside a block.** ferry must detect this (the content between
  markers no longer matches the last-applied managed content) and
  refuse-and-report or re-route — never silently overwrite a user's in-block
  edit.
- **Marker collisions.** Ansible modules-extras issue #1968 is the cautionary
  case: identical markers make blocks clobber each other. ferry must generate
  stable, unique IDs it owns, not reuse a human-chosen string.
- **Crude enclosing that comments across user code.** conda issue #11030 shows
  an init routine that commented across a region the user had opened, breaking
  it. ferry must never comment across a block boundary a user may have opened,
  and must treat the managed region as bytes-between-markers only.

### 3. The `.ferry/` "agent-ready description" — a composition layer, adopt but not as a hunking mechanism

A sidecar `.ferry/` contract that declares how a file is structured and how an
agent should write into it is SOTA-supported as a *form*, and worth adopting —
but it has **zero hunking power on its own**. It is a composition layer over
families 1 and 2, not a substitute for them.

The precedents are for the form, not for segmentation:

- `.editorconfig` — a sidecar that declares how a file is *treated*, consumed
  by tooling rather than the runtime, centralizing per-file policy instead of
  scattering modelines. This is the shape of a formatting contract.
- `AGENTS.md` — a machine-read contract that instructs an agent how to
  structure its work. Open spec since August 2025, moved under the Linux
  Foundation in December 2025, adopted across 60k+ repositories. This is the
  precedent for a prescriptive, agent-read instruction file.

Verdict: the contract is SOTA-supported **and does not stand alone.** Its
payload must *mandate* markers (family 2) and/or sharding (family 1). A
contract that merely says "write tidy sections" buys nothing deterministic,
because free-form agent output is no more segmentable than free-form human
output — tidiness is not identity.

This is the one place to be honest about the evidence. **CONTESTED /
weak-evidence:** there is no published study showing that a formatting contract
measurably improves segmentation *reliability*. The `.editorconfig` and
`AGENTS.md` precedents are evidence for *adoption and consistency*, not for
*parse-reliability*. So the `.ferry/` contract should be presented as a
reasoned design **bet**, not an evidence-backed certainty. Also flag the
framing: "AGENTS.md as a governance/telemetry contract" is marketing gloss; the
durable, real part is "agent-read prescriptive instructions that tell the agent
to emit marked or sharded regions."

### 4. Positional line-diff (histogram + indent heuristic) — keep only as fallback

Keep the line diff strictly as the fallback path for un-contracted, legacy, or
un-restructured files, and pin the algorithm rather than leaving it
hand-rolled. Its identity is positional and therefore drifts — this family
does not solve the problem, it only degrades gracefully when families 1–3 do
not apply.

The evidence for *how* to pin it:

- **Histogram over Myers.** Nugroho et al. (arXiv 1902.02467) find Myers and
  Histogram produce identical output for over 95% of commits and diverge on
  1.7–8.2%; where they diverge, histogram tends to place cleaner hunk
  boundaries. Prefer histogram-style matching.
- **Indent heuristic.** git's indentation heuristic (git commit 433860f)
  reduces boundary-placement errors to roughly 1/30 of the default. But note
  precisely what it fixes: it stabilizes *where* a boundary lands within one
  diff, not *what* a hunk is across versions. It improves the fallback; it does
  not confer identity.
- **`git rerere` as the cautionary tale.** rerere identifies conflict
  resolutions by content, depends on markers, and deliberately refuses to
  auto-finalize because the surrounding context may have changed. That is
  directly relevant to ferry's auto-routing: when identity is uncertain, prefer
  refuse-and-report over silent auto-merge.

## Rejected — investigated, not worth adopting

- **AST / structural diff for shell** (difftastic, tree-sitter, GumTree). Shell
  barely parses; difftastic itself falls back to line diff on parse errors, so
  for the exact hand-edited rc files ferry targets it degrades to family 4 with
  more machinery. GumTree-style move detection is NP-hard and heuristic
  (arXiv 2403.05939) and cannot reason across files, which is where ferry's
  shared-vs-local routing actually lives. High complexity, no identity gain.
- **augeas lenses.** There is no general shell lens — only `Shellvars.lns` for
  `KEY=value` assignments — and augeas refuses to serialize trees that do not
  match its lens, so arbitrary hand-edited shell is out of scope by
  construction.
- **Content-hash / rerere-style identity as the *primary* scheme.** Fine only
  as a *tiebreaker* to re-locate a marker that moved; useless as the primary
  identity because editing the content changes the hash, which is the drift we
  are trying to escape.

## Recommendation for ferry, in order

1. **Ship the `.ferry/` contract whose payload is marker blocks with
   ferry-owned stable IDs** as the baseline mechanism, and prefer `*.d/`
   sharding as the target wherever the file can source a directory. The
   contract is the composition layer; markers and shards are what make it
   deterministic.
2. **Make the marker/shard name the routing key** for shared-vs-local,
   replacing today's line-range identity in `diffHunks`.
3. **Keep the LCS line-diff strictly as fallback** for un-contracted files,
   switch its intent to histogram-style matching, and treat an edit *inside* a
   managed block as refuse-and-report, never silent overwrite.
4. **Explicitly reject AST / augeas / difftastic** for the core identity path;
   allow content-hash only as a marker-relocation tiebreaker.

## Scope note

This is a capture-engine redesign with a real failure surface — in-block user
edits, marker collisions, one-time monolith restructuring, and an auto-routing
decision that can silently corrupt a hand-edited file if it guesses wrong. It
warrants its own gated design round and is **not** part of the v0.8.0
CLI-surface tidy. Keep the two efforts separate.

## Sources

All tier-1 (official docs, primary specs, peer-reviewed or archival papers,
canonical commits):

- [Ansible `blockinfile` module](https://docs.ansible.com/ansible/latest/collections/ansible/builtin/blockinfile_module.html)
- [Ansible modules-extras issue #1968 (marker collisions)](https://github.com/ansible/ansible-modules-extras/issues/1968)
- [conda issue #11030 (init comments across user code)](https://github.com/conda/conda/issues/11030)
- [conda issue #10249 (init block management)](https://github.com/conda/conda/issues/10249)
- [difftastic](https://github.com/Wilfred/difftastic) and its [Structural Diffs wiki](https://github.com/Wilfred/difftastic/wiki/Structural-Diffs)
- [Augeas Lenses documentation](https://augeas.net/docs/references/lenses.html) and [FAQ](https://augeas.net/faq.html)
- [Nugroho et al., "How Different Are Different diff Algorithms in Git?" (arXiv 1902.02467)](https://arxiv.org/abs/1902.02467)
- [git commit 433860f (indent heuristic)](https://github.com/git/git/commit/433860f3d0beb0c6f205290bd16cda413148f098) and [git-diff docs](https://git-scm.com/docs/git-diff)
- [git-rerere docs](https://git-scm.com/docs/git-rerere)
- [Falleri et al. / GumTree AST-diff benchmark (arXiv 2403.05939)](https://arxiv.org/abs/2403.05939)
- [EditorConfig specification](https://spec.editorconfig.org/)
- [AGENTS.md guide and standard](https://agents.md/)
- [chezmoi: manage machine-to-machine differences](https://www.chezmoi.io/user-guide/manage-machine-to-machine-differences/)
- The zsh drop-in modularization pattern (`~/.zshrc.d/NN-name.zsh`; oh-my-zsh `custom/`; `/etc/profile.d`)
