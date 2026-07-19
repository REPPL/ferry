// Package work implements work-state ferrying: carrying a project's in-flight
// working context — the session handover note, the run journal, the coding
// agent's per-project memory, and the redacted transcript store — between user
// accounts as an explicit baton pass.
//
// Work state is not configuration, so this is NOT a reconcile domain: it has
// an owner (whichever account is actively working), changes every session, and
// must never silently merge. The verbs are modelled on bundle export/import
// with handover semantics: pack refuses on a dirty secret scan and writes one
// cargo bundle to the store; receive lands the latest cargo behind guarded
// overwrite / union-merge policies, backup-first; claims are advisory
// per-account files, never locks.
//
// See .abcd/development/plans/2026-07-18-work-state-ferrying.md for the
// approved design.
package work
