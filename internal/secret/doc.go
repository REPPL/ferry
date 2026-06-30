// Package secret is ferry's secret-scan gate and out-of-repo secret store: the
// security-critical core that keeps private keys, WireGuard configs, and tokens
// out of the config repo (and out of the repo worktree entirely).
//
// It provides three cooperating pieces that the capture and apply commands
// compose in a later wave:
//
//   - A secret scanner (scan.go): ScanText scans content line-by-line and
//     ScanValue scans an opaque whole value (for binary plist domains). Both
//     return structured Findings naming what matched and where. Detection rests
//     on a small set of HIGH-CONFIDENCE named patterns (PEM private keys,
//     WireGuard keys, password/secret/token assignments) plus a deliberately
//     conservative Shannon-entropy heuristic for opaque high-entropy tokens.
//     The named patterns are the trusted core; the entropy heuristic is tuned
//     to err toward precision so that ordinary dotfile lines (EDITOR exports,
//     aliases, PATH entries) are never flagged.
//
//   - A routing gate (gate.go): IsBlockedFromRepo reports whether a candidate
//     change must be kept out of repo routing. A high-confidence secret is
//     blocked from BOTH the shared route AND the local route, because local/
//     lives inside the repo worktree (gitignored, but still plaintext on disk in
//     the repo dir). The only routes for a detected secret are reject or the
//     out-of-repo secret store.
//
//   - An out-of-repo secret store (store.go): real secret values live in
//     ~/.config/ferry/secrets-local/<domain>.toml (dir 0700, files 0600,
//     resolved via internal/paths — NEVER under the repo). A managed file in the
//     repo keeps only a placeholder, {{ferry.secret "domain.key"}}. The store
//     renders placeholders from those real values on apply; if a referenced
//     secret is MISSING, RenderPlaceholders signals that the target must be
//     SKIPPED entirely rather than writing a file with an unrendered placeholder
//     (which would clobber a working local config). Only an explicit force/debug
//     path in a later wave would write a raw placeholder; the default never does.
//
// This package is pure logic with no cobra/command dependencies. Security
// posture: be conservative about what is safe to route to the repo, and never
// weaken a check to reduce false positives — narrow the entropy heuristic
// instead.
package secret
