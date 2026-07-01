// Package bundle is ferry's offline-transport engine: it writes and validates the
// portable `.zip` bundle that `ferry export` produces and `ferry import` ingests.
//
// A bundle is an ordinary zip containing one metadata member — ferry-bundle.json
// (the manifest) — plus a set of PAYLOAD members (the shared config-repo files).
// The manifest records, for every payload member, its canonical relative path,
// uncompressed size, and hex SHA256, along with a format version, the writer's
// ferry version, and whether the local layer was included. The manifest describes
// payload members ONLY; ferry-bundle.json is the sole zip member NOT listed in it.
//
// # Trust split
//
// The manifest lives inside the zip, so it is corruption-detection, NOT
// tamper-evidence: an attacker who rewrites a payload member can rewrite its
// manifest entry too. Real tamper-evidence comes from the OVERALL bundle SHA256
// (returned by Write, printed by export, verified by import --expect-sha256) — an
// out-of-band anchor the user conveys separately.
//
// # Validate-before-extract
//
// Validate reads and checks a bundle WITHOUT writing any payload to disk. It
// enforces resource caps from the zip CENTRAL DIRECTORY (metadata only, no
// decompression) BEFORE reading the manifest body, so a hostile bundle cannot
// exhaust resources ahead of validation. It then parses the size-capped manifest,
// rejects a newer-than-supported format version, requires the payload-member set
// to EXACTLY equal the manifest entry set, and per entry checks the canonical path
// (no traversal/absolute/backslash/empty, no VCS/control path, no case-fold
// duplicate), the regular-file mode, the declared size, the content SHA256, and
// re-runs the secret gate on the content (defense in depth). Only a bundle that
// passes every check yields a Validated description the caller extracts from.
//
// # SSH-guard split
//
// internal/bundle cannot import package cmd, so the ~/.ssh guard is split across
// two layers. The CALLER (cmd/export) performs the repo-side safeRepoPath check
// before handing paths to Write, and cmd/import chooses a target dir that is not
// under ~/.ssh. This package adds a LEXICAL backstop: it rejects any entry path
// with a ".ssh" component (Write and Validate both), and Extract materialises every
// entry into a FRESH, ferry-owned staging dir it creates (0700, empty) — so a
// symlinked parent of the final target cannot be followed — then returns that dir
// for the caller to move into place. The two layers are independent — neither trusts
// the other to have run.
package bundle
