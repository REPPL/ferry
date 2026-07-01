package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Filenames of the two scope files inside the repo clone.
const (
	SharedManifestName = "ferry.toml"
	LocalManifestName  = "ferry.local.toml"
)

// Manifest is the parsed `[manage]` table of one scope file (shared or local).
//
// Scalar domains (brew, iterm2, fonts, and any future bool) are kept in a map
// so the model is extensible without a struct field per domain — a new domain
// in the TOML is honoured without code changes. The dotfiles list is its own
// field because it is a list, not a bool.
//
// present records which scalar keys actually appeared in this file's source, so
// the merge can distinguish "local set iterm2=false" from "local didn't mention
// iterm2" — only explicitly-set local keys override shared.
type Manifest struct {
	Dotfiles    []string
	domains     map[string]bool
	dotfilesSet bool
	present     map[string]bool
}

// rawManifest mirrors the on-disk TOML shape from docs/configuration.md:
//
//	[manage]
//	dotfiles = [".zshrc", ".gitconfig"]
//	brew     = true
//	iterm2   = true
//	fonts    = false
//
// dotfiles is the one list domain; every other key under [manage] is a bool.
type rawManifest struct {
	Manage map[string]toml.Primitive `toml:"manage"`
}

// parseManifest decodes one scope file's bytes into a Manifest, validating at
// the boundary. Unknown bool domains are accepted (extensible); a value of the
// wrong type (e.g. brew = "yes") is a clear error, not a panic.
func parseManifest(data []byte) (Manifest, error) {
	m := Manifest{
		domains: map[string]bool{},
		present: map[string]bool{},
	}
	var raw rawManifest
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	for key, prim := range raw.Manage {
		if key == "dotfiles" {
			var list []string
			if err := md.PrimitiveDecode(prim, &list); err != nil {
				return Manifest{}, fmt.Errorf("manage.dotfiles must be a list of strings: %w", err)
			}
			m.Dotfiles = list
			m.dotfilesSet = true
			continue
		}
		var b bool
		if err := md.PrimitiveDecode(prim, &b); err != nil {
			return Manifest{}, fmt.Errorf("manage.%s must be a boolean: %w", key, err)
		}
		m.domains[key] = b
		m.present[key] = true
	}
	return m, nil
}

// loadManifestFile reads and parses one scope file. A missing file yields a
// zero Manifest and reports os.ErrNotExist (the caller treats absence as
// "nothing declared"), so a repo with no ferry.local.toml is normal, not an
// error.
func loadManifestFile(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{domains: map[string]bool{}, present: map[string]bool{}}, err
		}
		return Manifest{}, fmt.Errorf("read manifest %s: %w", path, err)
	}
	m, err := parseManifest(data)
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return m, nil
}

// Scope is the effective, merged manifest: ferry.toml overlaid with
// ferry.local.toml (local wins). It is the SINGLE source of truth for which
// domains ferry manages on THIS machine, used IDENTICALLY by apply and capture
// so a domain disabled here is neither applied nor captured.
type Scope struct {
	dotfiles []string
	domains  map[string]bool
}

// LoadScope loads ferry.toml and ferry.local.toml from repoPath and returns
// their merge. ferry.toml is required (it is the committed shared baseline);
// ferry.local.toml is optional (absent = no per-machine overrides).
func LoadScope(repoPath string) (Scope, error) {
	// Guard each repo-side scope file at the LOWEST read layer: a ferry.toml or
	// ferry.local.toml that is a symlink (or sits under a symlinked component)
	// resolving out of the repo / under ~/.ssh would otherwise be os.ReadFile'd
	// through. ferry only writes regular files here, so a symlink is illegitimate
	// and refused BEFORE the read. This protects every command that loadContext
	// drives (apply/diff/status/capture/restore).
	sharedPath, err := safeRepoRead(repoPath, SharedManifestName)
	if err != nil {
		return Scope{}, err
	}
	shared, err := loadManifestFile(sharedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Scope{}, fmt.Errorf("no ferry manifest (%s) in the config repo %s — run `ferry init` to create one, or add %s to the repo", SharedManifestName, repoPath, SharedManifestName)
		}
		return Scope{}, err
	}

	localPath, err := safeRepoRead(repoPath, LocalManifestName)
	if err != nil {
		return Scope{}, err
	}
	local, err := loadManifestFile(localPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Scope{}, err
	}
	// os.ErrNotExist -> local is the zero (empty) Manifest, which overrides nothing.

	return mergeScope(shared, local), nil
}

// mergeScope overlays local on top of shared: for scalar domains, an explicitly
// set local key wins (so shared iterm2=true + local iterm2=false => false);
// unset local keys leave the shared value. dotfiles is whole-list replacement —
// if local declares a dotfiles list it replaces shared's; otherwise shared's
// list stands (a list has no meaningful per-entry merge here).
func mergeScope(shared, local Manifest) Scope {
	s := Scope{domains: map[string]bool{}}

	for k, v := range shared.domains {
		s.domains[k] = v
	}
	for k, v := range local.domains {
		if local.present[k] {
			s.domains[k] = v
		}
	}

	if local.dotfilesSet {
		s.dotfiles = append([]string(nil), local.Dotfiles...)
	} else {
		s.dotfiles = append([]string(nil), shared.Dotfiles...)
	}
	return s
}

// IsManaged reports whether the named domain is in scope on this machine. It is
// the one predicate BOTH apply and capture consult, so the directions can never
// diverge on what is managed.
//
// The dotfiles domain is managed iff at least one dotfile is declared; every
// other domain is managed iff its bool is present and true.
func (s Scope) IsManaged(domain string) bool {
	if domain == "dotfiles" {
		return len(s.dotfiles) > 0
	}
	return s.domains[domain]
}

// DeclaredDotfiles returns the effective list of dotfiles in scope (e.g.
// ".zshrc", ".gitconfig"). The slice is a copy; callers may mutate it freely.
func (s Scope) DeclaredDotfiles() []string {
	return append([]string(nil), s.dotfiles...)
}

// Domains returns the names of all scalar (non-dotfiles) domains that are in
// scope (present and true). Order is unspecified.
func (s Scope) Domains() []string {
	out := make([]string, 0, len(s.domains))
	for k, v := range s.domains {
		if v {
			out = append(out, k)
		}
	}
	return out
}
