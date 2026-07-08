package emacs

import (
	"path/filepath"
	"strings"
)

// excludedDirs are directory names whose whole subtree is volatile,
// machine-generated state that must never be carried: package stores and
// installed bytecode (elpa/, eln-cache/), and per-machine session/network
// caches (auto-save-list/, transient/, url/). Matched on ANY path component so
// a nested occurrence (e.g. inits/elpa/) is pruned too.
var excludedDirs = map[string]bool{
	"elpa":           true,
	"eln-cache":      true,
	"auto-save-list": true,
	"transient":      true,
	"url":            true,
}

// excludedFiles are exact base names of volatile session-state files Emacs
// rewrites at runtime: the network-security cache and the recentf/savehist/
// saveplace persistence files. Carrying them would churn on every session and
// leak one machine's history to another.
var excludedFiles = map[string]bool{
	"network-security.data": true,
	"recentf":               true,
	"savehist":              true,
	"saveplace":             true,
}

// excludedRelPaths are exact slash-relative paths excluded regardless of their
// base name: the tangled Emacs-Lisp output inits/repp.el, which is regenerated
// from the literate inits/repp.org at load time and must not be carried.
var excludedRelPaths = map[string]bool{
	"inits/repp.el": true,
}

// excluded reports whether a tree entry at the slash-or-OS relative path rel
// (relative to the emacs/ source root) is a volatile path the domain must never
// deploy. It is deterministic and side-effect-free, so the walk can call it on
// every entry — a directory match prunes the whole subtree, a file match skips
// the one file. The rules are: any path component that names an excluded
// directory; an exact excluded relative path; an exact excluded base name; or a
// compiled-bytecode file (a .elc extension).
func excluded(rel string) bool {
	rel = filepath.ToSlash(rel)
	if excludedRelPaths[rel] {
		return true
	}
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if excludedDirs[p] {
			return true
		}
	}
	base := parts[len(parts)-1]
	if excludedFiles[base] {
		return true
	}
	if strings.HasSuffix(base, ".elc") {
		return true
	}
	return false
}
