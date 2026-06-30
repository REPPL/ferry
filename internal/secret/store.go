package secret

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/REPPL/ferry/internal/paths"
)

// placeholderRe matches a ferry secret placeholder: {{ferry.secret "domain.key"}}.
// Whitespace inside the braces is tolerated. The captured group is the
// dotted reference "domain.key". The key part may itself contain dots; only the
// FIRST segment is the domain (see splitRef).
var placeholderRe = regexp.MustCompile(`\{\{\s*ferry\.secret\s+"([^"]+)"\s*\}\}`)

// Placeholder renders the canonical placeholder string for a domain.key
// reference: {{ferry.secret "domain.key"}}. This is what a managed file commits
// to the repo in place of the real value.
func Placeholder(ref string) string {
	return fmt.Sprintf("{{ferry.secret %q}}", ref)
}

// DetectPlaceholders returns every distinct secret reference ("domain.key")
// found in content, in first-seen order. Empty if none.
func DetectPlaceholders(content string) []string {
	matches := placeholderRe.FindAllStringSubmatch(content, -1)
	var refs []string
	seen := make(map[string]bool)
	for _, m := range matches {
		ref := m[1]
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	return refs
}

// splitRef splits a "domain.key" reference into its domain (first segment) and
// key (the remainder, which may contain further dots). It errors on a malformed
// reference (no dot, or an empty domain/key).
func splitRef(ref string) (domain, key string, err error) {
	i := strings.IndexByte(ref, '.')
	if i <= 0 || i >= len(ref)-1 {
		return "", "", fmt.Errorf("secret reference %q must be of the form domain.key", ref)
	}
	return ref[:i], ref[i+1:], nil
}

// Store is the out-of-repo secret store rooted at a directory (the real store is
// rooted at paths.SecretsDir(); tests root it at a t.TempDir()). Each domain is a
// single TOML file <root>/<domain>.toml whose top-level keys are the secret
// keys. The directory is created 0700 and files 0600 on write; nothing here is
// ever placed under the repo.
type Store struct {
	root string
}

// Open returns a Store rooted at the real out-of-repo secrets directory
// (~/.config/ferry/secrets-local, resolved via internal/paths). The directory is
// NOT created here; Put creates it (0700) lazily on first write.
func Open() (*Store, error) {
	root, err := paths.SecretsDir()
	if err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

// OpenAt returns a Store rooted at an explicit directory. This is the testable
// constructor: tests pass a t.TempDir() so they never touch the real ~.
func OpenAt(root string) *Store {
	return &Store{root: root}
}

// domainFile is the path to a domain's TOML file inside the store.
func (s *Store) domainFile(domain string) string {
	return filepath.Join(s.root, domain+".toml")
}

// Put stores value under domain.key (parsed from ref), creating the store
// directory 0700 and the domain file 0600 if needed, and preserving any other
// keys already in the domain file. The value lives ONLY here, never in the repo.
func (s *Store) Put(ref, value string) error {
	domain, key, err := splitRef(ref)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("create secret store dir %s: %w", s.root, err)
	}
	// Defence in depth: if the dir already existed with looser perms, tighten it.
	_ = os.Chmod(s.root, 0o700)

	m, err := s.readDomain(domain)
	if err != nil {
		return err
	}
	m[key] = value
	return s.writeDomain(domain, m)
}

// Get returns the value stored under domain.key. The boolean is false if the
// domain file or the key is absent (a MISSING secret — the caller skips the
// target rather than treating absence as an error).
func (s *Store) Get(ref string) (string, bool, error) {
	domain, key, err := splitRef(ref)
	if err != nil {
		return "", false, err
	}
	m, err := s.readDomain(domain)
	if err != nil {
		return "", false, err
	}
	v, ok := m[key]
	return v, ok, nil
}

// readDomain loads a domain file into a flat key->value map. A missing file
// yields an empty map (not an error): an absent domain simply has no secrets.
func (s *Store) readDomain(domain string) (map[string]string, error) {
	path := s.domainFile(domain)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read secret domain %s: %w", path, err)
	}
	m := map[string]string{}
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse secret domain %s: %w", path, err)
	}
	return m, nil
}

// writeDomain writes a domain map back to its TOML file with mode 0600,
// deterministically (keys sorted) so the file is stable across writes. The store
// is treated as secret-bearing: perms are never loosened.
func (s *Store) writeDomain(domain string, m map[string]string) error {
	path := s.domainFile(domain)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	enc := toml.NewEncoder(&b)
	ordered := make(map[string]string, len(m))
	for _, k := range keys {
		ordered[k] = m[k]
	}
	if err := enc.Encode(ordered); err != nil {
		return fmt.Errorf("encode secret domain %s: %w", path, err)
	}
	// Write atomically: a temp file in the SAME directory, fsync'd, then renamed
	// over the target. A crash mid-write can never leave a truncated/half-written
	// secret store — readers see either the old file or the fully new one. Mode is
	// 0600 throughout (CreateTemp makes 0600; an explicit Chmod keeps it 0600 even
	// if umask or a pre-existing file differed). The temp is cleaned up on error.
	tmp, err := os.CreateTemp(s.root, ".ferry-secret-*")
	if err != nil {
		return fmt.Errorf("create secret domain temp in %s: %w", s.root, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return fmt.Errorf("write secret domain %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync secret domain %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close secret domain temp %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod secret domain temp %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename secret domain into place %s: %w", path, err)
	}
	return nil
}

// RenderResult is the outcome of rendering placeholders in a file's content.
type RenderResult struct {
	// Rendered is the content with every placeholder replaced. It is only valid
	// (safe to write) when Skip is false.
	Rendered string
	// Missing lists the references whose secret was absent from the store, in
	// first-seen order. Non-empty exactly when Skip is true.
	Missing []string
	// Skip is true when at least one referenced secret was MISSING. The apply
	// path MUST then skip writing this target entirely (leaving any existing live
	// config intact) — writing content with an unrendered placeholder would
	// clobber a working config with an invalid value.
	Skip bool
}

// RenderPlaceholders replaces every {{ferry.secret "domain.key"}} in content
// with its real value from the store. If ANY referenced secret is missing, it
// returns Skip == true with the missing references and does NOT produce
// rendered output for writing — the caller skips the whole target. Content with
// no placeholders renders to itself with Skip == false.
//
// This never emits a file containing an unrendered placeholder by default; only
// an explicit force/debug path in a later wave would do that.
func (s *Store) RenderPlaceholders(content string) (RenderResult, error) {
	refs := DetectPlaceholders(content)
	if len(refs) == 0 {
		return RenderResult{Rendered: content}, nil
	}

	values := make(map[string]string, len(refs))
	var missing []string
	for _, ref := range refs {
		v, ok, err := s.Get(ref)
		if err != nil {
			return RenderResult{}, err
		}
		if !ok {
			missing = append(missing, ref)
			continue
		}
		values[ref] = v
	}
	if len(missing) > 0 {
		// Signal skip; deliberately return NO rendered content so a caller that
		// ignores Skip cannot accidentally write a half-rendered file.
		return RenderResult{Missing: missing, Skip: true}, nil
	}

	rendered := placeholderRe.ReplaceAllStringFunc(content, func(match string) string {
		m := placeholderRe.FindStringSubmatch(match)
		return values[m[1]]
	})
	return RenderResult{Rendered: rendered}, nil
}
