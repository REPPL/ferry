// Package terminal models the macOS terminal preference DOMAINS that ferry
// manages — iTerm2 (com.googlecode.iterm2) and Apple Terminal
// (com.apple.Terminal) — as first-class preference resources, NOT as naive
// file copies.
//
// These domains are mutated through the native macOS preference mechanism
// (`defaults` / cfprefs), per the export-before-mutate / import-to-rollback
// transaction model:
//
//   - Backup captures the CURRENT state via `defaults export <domain> -` so the
//     backup engine can snapshot it to the immutable baseline + per-run journal
//     BEFORE any mutation.
//   - Apply mutates the domain (iTerm2: set PrefsCustomFolder +
//     LoadPrefsFromCustomFolder via `defaults write`; Apple Terminal: import a
//     prepared export via `defaults import`).
//   - Restore re-applies a previously captured blob via
//     `defaults import <domain> -`, returning the domain to that state — this is
//     the engine's rollback path.
//
// Each domain implements backup.Resource so the engine drives its transaction
// symmetrically with file resources.
//
// Platform guard: everything here is darwin-only. The package COMPILES on every
// target (so `go build ./...` works on the Linux cross-compile targets) using a
// runtime platform.IsDarwin() guard — there are no build-tag-excluded files.
// On a non-darwin host, Apply/Backup/Restore return a clear "macOS only;
// skipped on this platform" result and never shell out.
package terminal
