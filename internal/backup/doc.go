// Package backup is ferry's transactional backup/restore engine: the reversible
// core that the apply and restore commands compose in later waves.
//
// It provides four cooperating pieces, all rooted at ferry's XDG state dir
// (~/.local/state/ferry, resolved via internal/paths — never re-derived here):
//
//   - An exclusive apply lock (one apply at a time; stale-lock reclaim).
//   - An immutable per-machine baseline: the TRUE pre-ferry state of every path
//     ferry has ever touched, written ONCE and never overwritten. Full restore
//     reverts to this.
//   - An append-only per-run journal: what each apply changed, with a
//     completion marker so a run that died mid-way is detectable and rollable.
//   - Transactional file writes (backup -> temp -> atomic rename -> mark
//     complete) and restore (baseline / scoped), snapshotting current state
//     before reverting so an unwanted restore is itself reversible.
//
// Security: the state store can hold secrets lifted from existing dotfiles, SSH
// config, or plist values, so every directory it creates is 0700 and every
// stored file is 0600 by default; a stricter original mode is preserved. The
// store is treated as secret-bearing by default and perms are never loosened.
//
// This package is pure logic with no cobra/command dependencies. Preference
// domains (plist / defaults) are NOT files; they plug in via the Resource hook
// (resource.go) and are implemented alongside the macOS domains in a later wave.
package backup
