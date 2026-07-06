// Package termcfg models the config-file terminal emulators ferry carries like
// dotfiles: Alacritty (~/.config/alacritty), kitty (~/.config/kitty), and
// WezTerm (~/.wezterm.lua), plus any user-declared terminal. Each terminal's
// plain-text config — a single file or a whole directory — is deployed from the
// config repo's terminals/ area to its home destination as regular-file copies
// reconciled by hash, exactly as the dotfile and agents domains deploy.
//
// Discovery is a BUILT-IN REGISTRY (Builtins) of known terminals mapped to
// their config paths, mirroring the agents harness/asset registries: the set is
// DATA, trimmed by the manifest's `enabled` list and extended or overridden by
// [terminals.terminal.<name>] declarations, never a code change. A built-in
// whose source is absent from the repo deploys nothing, so enabling the ones
// you use is a matter of committing their config.
//
// The .local layer applies per file: a per-machine override at
// local/terminals/<source>/<relpath> wins over the shared repo copy — the
// natural home for a per-machine colour scheme.
//
// GNOME Terminal is intentionally out of scope: it stores its settings in
// dconf, not a config file, so it needs a dump/load bridge rather than a copy
// and is deferred to a later release.
package termcfg
