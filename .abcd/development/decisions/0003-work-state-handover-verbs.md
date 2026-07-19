# Work state is carried by explicit handover verbs, not the reconcile loop

- Status: accepted
- Date: 2026-07-18

## Context and problem statement

ferry carries a development *setup* across accounts and machines; it did not
carry *in-flight work*. For an abcd-layout project, continuing work on
another account loses the session handover note and run journal (gitignored
by design), the coding agent's per-project memory (outside any repo, keyed
by absolute path), and the redacted transcript store (account-local). The
project-side tooling deliberately refuses this job. How should ferry carry
operator-local work state between accounts?

## Considered options

1. A new `FileDomain` in the `capture`/`apply` reconcile loop.
2. A new verb family with explicit handover (baton-pass) semantics and a
   plain-directory cargo store.

## Decision outcome

Chosen option 2, `ferry work pack|receive|status|prune|restore`, because
work state fails every assumption the reconcile loop makes: it has an owner
(whichever account is actively working), it changes every session (drift is
its normal state, not a signal), and it must never silently merge. The
reconcile engine's `$HOME`-target model also does not cover files inside a
project repo's working tree. Divergence is detected by content hash against
the last-held baseline and surfaced, never resolved automatically; ordering
is by pack sequence number, never timestamps.

The cargo store is a plain directory on shared or portable media — never the
config repo (declarative, long-lived, possibly hosted; personal working
context must not ride along), enforced mechanically: a store inside any git
worktree is refused, a store under a known cloud-sync root needs an explicit
opt-in, and no store file is ever written by two accounts (O_EXCL bundles,
per-account claim files merged on read).

This extends the durable/ephemeral distinction of
[ADR 0002](0002-work-memory-public-private-split.md) across the account
boundary: the ephemeral tier travels only by explicit handover, with the
secret gate fail-closed over every byte.

## Consequences

- Good: no silent merges; every landing is backup-first, snapshotted, and
  precisely reversible (`ferry work restore`); the store works on
  permissionless media (exFAT, SMB) and across same-Mac accounts.
- Bad: handover is manual by design — there is no continuous sync, and a
  forgotten `pack` leaves the other account stale (surfaced by
  `work status`, not prevented).
- Neutral: the agent-memory path-key encoding is an observed convention of
  one tool, owned by a single registry entry; a harness-side change is a
  one-entry fix, and `receive` refuses rather than merges when the target
  does not match the recorded baseline.
