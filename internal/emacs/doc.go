// Package emacs carries a developer's Emacs configuration tree
// (~/.emacs.d/) across machines, modelled on internal/termcfg's config-file
// tree pattern: the config repo's emacs/ area is fanned out file-by-file into
// per-leaf nested targets under ~/.emacs.d/, each deployed as a regular-file
// COPY reconciled by hash and written through the standard
// dotfile.ApplyContentDeferred path (os.Root-confined). Nothing under $HOME is
// ever symlinked — the historical `ln -s repo ~/.emacs.d` habit is replaced by
// the apply cycle (edit the config-repo emacs/ source, then `ferry apply`).
//
// The domain is repo-authoritative (Captures() == false, like termcfg): edit
// the config-repo copy and `apply` deploys it; a live edit shows as drift and
// apply skips it. For a literate config (init.el bootstrapping a tangled
// inits/repp.org) this adds one `apply` step between editing inits/repp.org and
// Emacs re-tangling it — the accepted trade for cross-machine carry, the
// per-machine .local overlay, and secret handling. Bidirectional capture-back
// is a deliberate out-of-scope follow-up.
//
// Carry/exclude. The carry set is everything committed under emacs/ (init.el,
// early-init.el, inits/repp.org, docs/, README, LICENSE, …). Even so, the
// domain defensively EXCLUDES the volatile, machine-generated paths during the
// walk so they are never deployed even if a source tree contains them: package
// stores (elpa/, eln-cache/, *.elc), the tangled inits/repp.el, and session
// state (auto-save-list/, transient/, url/, network-security.data, recentf,
// savehist, saveplace). See exclude.go for the exact predicate.
//
// Per-machine overlay. The domain deploys the UNION of the shared emacs/ tree
// and the per-machine local/emacs/ overlay tree. The termcfg per-file local-wins
// overlay applies: a file at local/emacs/<relpath> in the repo overrides the
// shared emacs/<relpath> on deploy (whole-file, no Emacs-Lisp parsing). A file
// present ONLY under local/emacs/ (no shared counterpart) deploys as a
// machine-only file, exactly like iTerm2's Dynamic Profiles overlay — the
// natural home for a Customize-written inits/custom.el or a hand-authored
// init.local.el that exists on this machine alone. The exclude filter and
// symlink refusal apply to BOTH trees.
//
// Deploy home is ~/.emacs.d (matching the maintainer's model). Note ~/.emacs.d
// shadows the XDG location ~/.config/emacs: with ~/.emacs.d present, Emacs reads
// it and never consults ~/.config/emacs.
package emacs
