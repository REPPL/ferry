// Package dotfile is ferry's generic dotfile deployment domain: it materializes
// any declared dotfile (.zshrc, .gitconfig, ...) onto a machine by COPYING the
// repo content to the home target — never by symlinking into the repo, and
// never via GNU Stow.
//
// Why copy, not symlink (the safety argument): a symlink from ~/.zshrc into
// dotfiles/zshrc would mean editing the live file rewrites the committed repo
// file directly, bypassing the capture gate, shared/local routing, AND the
// mandatory secret scan. Copying keeps the repo content and the live file as
// two DISTINCT states, so capture (with its scan + routing) is the only path by
// which content flows back into the repo. That is what makes ferry's
// bidirectional model safe.
//
// Three-way state. To avoid clobbering uncaptured local edits, the domain
// records a per-target LAST-APPLIED content hash in ferry's state dir. On apply
// it compares three things — the live file, the last-applied hash, and the repo
// content — and decides:
//
//   - live == last-applied (no local edits) AND repo changed -> UPDATE
//     (copy repo -> home via the Backuper so the write is backed up + atomic).
//   - live != last-applied (locally edited, not captured)    -> CONFLICT
//     (do NOT overwrite; caller reports "run capture, or apply --force").
//   - live == repo                                           -> NO-OP.
//   - target exists but is not ferry-managed (no last-applied) and would be
//     overwritten                                            -> CONFLICT.
//   - --force                                                -> overwrite
//     anyway (still backed up first).
//
// Status/diff reuses the same three-way comparison to classify each target as
// clean / locally-drifted (a capture candidate) / repo-ahead / conflict.
//
// After a capture, UpdateLastApplied advances a target's last-applied hash ONLY
// when the resulting managed output reproduces the live file; a partial capture
// (live still differs) leaves last-applied put so status keeps reporting the
// remaining drift.
//
// ~/.ssh hands-off contract (the enforcement point). ferry's top security
// invariant is that it NEVER reads, copies, captures, or modifies anything under
// ~/.ssh/. TargetFor is the single boundary that makes this impossible to
// violate: apply, capture, and status all obtain their Target through TargetFor,
// and TargetFor REFUSES any declared name whose resolved home target is ~/.ssh
// itself or under it (ErrForbiddenSSHPath), as well as any name that does not
// resolve strictly within $HOME (ErrPathEscapesHome — an absolute path or a `..`
// climb). A manifest declaring ".ssh/config" or ".ssh/id_ed25519" therefore
// never yields a Target, so no read/back-up/write of ~/.ssh can occur.
//
// Per-domain `.local` overlay (PLAN.md "Per-domain overlay strategy"). A
// Target's OverlayMode tells the apply command how the per-machine overlay
// composes: OverlayIncludeSidecar for an include-style domain (zsh: shared
// ~/.zshrc sources ~/.zshrc.local) where the overlay is a separate sidecar file;
// OverlayWholeFileReplace (the TargetFor default) for a generic dotfile with no
// include point (e.g. .gitconfig) where the per-machine copy in local/<domain>/
// is deployed INSTEAD OF the shared content. ApplyWholeFileOverlayDeferred
// implements that whole-file path here; sidecar materialization stays the
// command's job.
//
// This package is pure logic: only the standard library, plus a small Backuper
// interface (so the real backup engine is wired in Wave 2). It writes nothing
// to the repo and never edits a cobra command.
package dotfile
