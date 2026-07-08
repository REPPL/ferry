// Package iterm2profiles models iTerm2's Dynamic Profiles as a ferry FileDomain:
// the config repo's iterm2/DynamicProfiles/ tree of *.json files, deployed
// file-by-file to ~/Library/Application Support/iTerm2/DynamicProfiles/ as
// regular-file COPIES reconciled by hash (never symlinks — ferry's core
// invariant), exactly like the config-file terminal (termcfg) and Emacs domains.
//
// Dynamic Profiles are plain, live-reloaded JSON, so they are the reviewable,
// mergeable representation of iTerm2 profiles (the opaque global plist is a
// separate ResourceDomain). This domain is REPO-AUTHORITATIVE: you edit the JSON
// in the repo and `ferry apply` deploys it; there is no capture pass, so a live
// edit shows as drift and apply skips it (Captures() is false). "Rewritable":
// false in a carried profile documents the same rule to iTerm2 itself.
//
// Two invariants make this safe to carry:
//
//   - FROZEN GUID. A profile's "Guid" is its identity; ferry never generates or
//     rewrites one. There is no capture pass and apply copies bytes verbatim, so
//     a GUID is byte-stable by construction — a changed GUID would orphan the
//     deployed profile.
//   - VALIDATED JSON. One malformed file disables ALL of iTerm2's dynamic
//     profiles, so each file is validated (encoding/json validity on every
//     platform, plus `plutil -convert xml1` on macOS — not `-lint`, which rejects
//     valid JSON) BEFORE it is deployed; a file that
//     fails is warned about and skipped, never deployed.
//
// Per-machine divergence rides the termcfg-style per-file overlay: a file at
// local/iterm2-profiles/<rel> wins over the shared iterm2/DynamicProfiles/<rel>,
// and a file present ONLY under local/iterm2-profiles/ deploys as a machine-only
// profile (e.g. a child Dynamic Profile referencing a shared parent's GUID). ferry
// performs no JSON surgery — the overlay is purely file-level.
package iterm2profiles
