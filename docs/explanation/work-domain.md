# The work domain

This page explains why `ferry work` is a family of explicit handover verbs —
a baton pass — rather than another domain in the `apply`/`capture` reconcile
loop, and why its cargo store is a plain directory rather than the config
repo.

## Work state is not configuration

ferry's reconcile loop is built for configuration: declarative, long-lived
state with a single source of truth (the config repo) that every machine
converges towards. A project's in-flight work state is the opposite on every
axis:

- **It has an owner.** Whichever account is actively working holds the baton;
  the other side's copy is stale by definition. Configuration has no owner —
  the repo does.
- **It changes every session.** The handover note and agent memory move with
  every working day; drift is the normal state, not a signal.
- **It must never silently merge.** Two accounts' diverged handover notes
  cannot be reconciled by a machine; picking either silently loses work.

Reconcile semantics — repo-authoritative, drift detection, converge on apply
— are wrong for all three. So the work verbs model a **baton pass**: `pack`
hands over, `receive` picks up, and divergence is *detected and surfaced*
(by content hash, never timestamps), not resolved.

## Why the store is not the config repo

The config repo is declarative, long-lived, and possibly hosted. Work-state
cargo is none of those: it is personal working context — session notes,
agent memory, redacted transcripts — that belongs on local or private media
and expires quickly (retention keeps the last few bundles). Mixing the two
would push personal context into a repo that may be pushed to a host.

The store is therefore a plain directory of cargo bundles, and the guards
are mechanical, not just documented: a store that resolves inside any git
worktree is refused outright (which also catches "inside the config repo"),
and a store under a known cloud-sync root (iCloud, Dropbox, provider mounts)
requires an explicit opt-in. Nothing in ferry ever uploads the store.

## Two accounts, no shared writes

The store is designed so that no file is ever written by two accounts:

- Bundles are created once (`O_CREAT|O_EXCL`) under an increasing sequence
  number; a same-sequence collision from two concurrent packs leaves both
  files in place as a visible fork that `receive` refuses until one is named
  or pruned.
- Each account writes only its own claim file
  (`claim.<user>@<host>.json`), merged on read.

This sidesteps POSIX-permission traps on `/Users/Shared` (a file Alice
creates is not writable by Bob) and works unchanged on permissionless
filesystems like exFAT. Ordering is always by sequence number — clocks
differ across machines, so timestamps are display-only everywhere.

## The baton is advisory

Claims record who packed and received what; they are not locks. The threat
model is one human across their own accounts, not an adversary — so ferry
detects divergence (hash against the last-held baseline) and refuses with a
clear message rather than preventing concurrent edits. `--force` is the
explicit override, and every forced write is still backed up first and
snapshotted, so `ferry work restore` reverts it precisely.

## Project identity

A project is identified by its git root-commit SHA — the same value the
project tooling uses for the account-level transcript store, so one identity
locates both. The full sorted root-SHA *set* travels in every bundle's
manifest, and `receive` matches on set intersection, so a subtree import
that reorders the roots does not orphan existing cargo. Shallow clones are
refused (their root is a graft boundary, not an identity); a history rewrite
is a new identity, and `work status` keeps orphaned store entries visible by
their manifest repo name.

## Related

- [Hand work over to another account](../how-to/hand-over-work.md) — the task
  itself.
- [Commands](../reference/commands.md) — the `work` verb reference.
- ADR 0003 (in the repository's development records) records this decision
  and its rejected alternative.
