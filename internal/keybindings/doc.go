// Package keybindings carries the macOS Cocoa key-bindings file
// (~/Library/KeyBindings/DefaultKeyBinding.dict) across machines as a single
// repo-authoritative, whole-file target.
//
// The file is an old-style (NeXT/ASCII) property-list dict the Cocoa text
// system reads once at app launch; the OS never rewrites it, so a hash
// reconcile shows drift only when the user actually edits it. Modelled on
// internal/termcfg's file-tree pattern but degenerate to one fixed source ->
// one fixed nested target: keybindings/DefaultKeyBinding.dict in the config repo
// deploys to ~/Library/KeyBindings/DefaultKeyBinding.dict via
// dotfile.NestedTarget (inside $HOME, so it passes containment) and the standard
// dotfile.ApplyContentDeferred write path.
//
// The domain is repo-authoritative (Captures() == false, like termcfg): edit the
// config-repo copy and `apply` deploys it; a live edit shows as drift and apply
// skips it. There is no `.local` overlay — keyboard behaviour is machine-agnostic
// and the old-style dict format has no include/merge mechanism, so a whole-file
// swap would earn nothing.
//
// Format hygiene keeps the file the readable text form: a bplist00 binary-plist
// header is refused, a UTF-8 BOM is refused, non-UTF-8 bytes are refused, and on
// macOS the source is validated with `plutil -lint` before the copy lands. ferry
// never runs `plutil -convert` on it (convert would destroy the readable
// old-style diff).
package keybindings
