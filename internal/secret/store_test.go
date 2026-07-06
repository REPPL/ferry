package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStore_RoundTripAndPerms(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets-local") // fake store; never real ~
	s := OpenAt(root)

	if err := s.Put("wireguard.private_key", "FAKEvalue1234567890=="); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := s.Get("wireguard.private_key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || got != "FAKEvalue1234567890==" {
		t.Fatalf("round-trip mismatch: ok=%v got=%q", ok, got)
	}

	// Dir 0700.
	di, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("store dir perm = %o, want 0700", perm)
	}

	// File 0600.
	fi, err := os.Stat(filepath.Join(root, "wireguard.toml"))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("domain file perm = %o, want 0600", perm)
	}
}

// TestStore_AtomicRewrite checks the store is written via temp+rename: after a
// second Put rewrites the domain, the file stays 0600 and NO partial temp file
// is left behind in the store directory.
func TestStore_AtomicRewrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets-local")
	s := OpenAt(root)
	if err := s.Put("app.token", "FAKEtok1234567890"); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	// Rewrite the same domain (the O_TRUNC hazard the atomic write replaces).
	if err := s.Put("app.token", "FAKEtokREWRITTEN09"); err != nil {
		t.Fatalf("rewrite Put: %v", err)
	}

	if v, ok, _ := s.Get("app.token"); !ok || v != "FAKEtokREWRITTEN09" {
		t.Errorf("rewrite not durable: ok=%v v=%q", ok, v)
	}

	fi, err := os.Stat(filepath.Join(root, "app.toml"))
	if err != nil {
		t.Fatalf("stat domain file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("domain file perm after rewrite = %o, want 0600", perm)
	}

	// No leftover temp files — only app.toml should remain.
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read store dir: %v", err)
	}
	for _, e := range ents {
		if e.Name() != "app.toml" {
			t.Errorf("unexpected leftover in store dir: %q", e.Name())
		}
	}
}

func TestStore_PreservesOtherKeys(t *testing.T) {
	s := OpenAt(t.TempDir())
	if err := s.Put("app.token", "tokA"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("app.password", "pwB"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.Get("app.token"); !ok || v != "tokA" {
		t.Errorf("token clobbered by second Put: ok=%v v=%q", ok, v)
	}
	if v, ok, _ := s.Get("app.password"); !ok || v != "pwB" {
		t.Errorf("password not stored: ok=%v v=%q", ok, v)
	}
}

func TestStore_MissingSecret(t *testing.T) {
	s := OpenAt(t.TempDir())
	_, ok, err := s.Get("nope.absent")
	if err != nil {
		t.Fatalf("Get of missing should not error, got %v", err)
	}
	if ok {
		t.Errorf("expected ok=false for missing secret")
	}
}

func TestStore_BadRef(t *testing.T) {
	s := OpenAt(t.TempDir())
	if err := s.Put("nodot", "x"); err == nil {
		t.Errorf("expected error for ref without a dot")
	}
}

func TestDetectPlaceholders(t *testing.T) {
	content := `key = {{ferry.secret "wg.private_key"}}
psk = {{ ferry.secret "wg.preshared" }}
dup = {{ferry.secret "wg.private_key"}}
plain = value`
	refs := DetectPlaceholders(content)
	if len(refs) != 2 {
		t.Fatalf("expected 2 distinct refs, got %v", refs)
	}
	if refs[0] != "wg.private_key" || refs[1] != "wg.preshared" {
		t.Errorf("unexpected refs / order: %v", refs)
	}
}

func TestPlaceholderHelper(t *testing.T) {
	want := `{{ferry.secret "wg.key"}}`
	if got := Placeholder("wg.key"); got != want {
		t.Errorf("Placeholder = %q, want %q", got, want)
	}
}

func TestRenderPlaceholders_AllPresent(t *testing.T) {
	s := OpenAt(t.TempDir())
	mustPut(t, s, "wg.private_key", "PRIVFAKE==")
	mustPut(t, s, "wg.preshared", "PSKFAKE==")

	content := "[Interface]\nPrivateKey = {{ferry.secret \"wg.private_key\"}}\nPresharedKey = {{ferry.secret \"wg.preshared\"}}\n"
	res, err := s.RenderPlaceholders(content)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skip {
		t.Fatalf("should not skip when all secrets present: %+v", res)
	}
	if strings.Contains(res.Rendered, "{{") {
		t.Errorf("rendered output still has a placeholder: %q", res.Rendered)
	}
	if !strings.Contains(res.Rendered, "PRIVFAKE==") || !strings.Contains(res.Rendered, "PSKFAKE==") {
		t.Errorf("values not substituted: %q", res.Rendered)
	}
}

// TestRenderPlaceholders_MissingSkips is the load-bearing apply-side guarantee:
// a missing secret yields Skip with NO rendered content, so apply never writes a
// file containing an unrendered placeholder.
func TestRenderPlaceholders_MissingSkips(t *testing.T) {
	s := OpenAt(t.TempDir())
	mustPut(t, s, "wg.private_key", "PRIVFAKE==")
	// wg.preshared intentionally NOT stored.

	content := "PrivateKey = {{ferry.secret \"wg.private_key\"}}\nPresharedKey = {{ferry.secret \"wg.preshared\"}}\n"
	res, err := s.RenderPlaceholders(content)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skip {
		t.Fatalf("expected Skip when a secret is missing, got %+v", res)
	}
	if res.Rendered != "" {
		t.Errorf("missing-secret render must emit NO content, got %q", res.Rendered)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "wg.preshared" {
		t.Errorf("expected missing=[wg.preshared], got %v", res.Missing)
	}
}

func TestRenderPlaceholders_NoPlaceholders(t *testing.T) {
	s := OpenAt(t.TempDir())
	content := "export EDITOR=nvim\n"
	res, err := s.RenderPlaceholders(content)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skip || res.Rendered != content {
		t.Errorf("content without placeholders should render to itself, got %+v", res)
	}
}

func mustPut(t *testing.T, s *Store, ref, val string) {
	t.Helper()
	if err := s.Put(ref, val); err != nil {
		t.Fatalf("Put(%q): %v", ref, err)
	}
}

// TestSplitRef_TraversalRejected covers F6: a domain/key charset guard
// (^[A-Za-z0-9_-]+$) must reject a path-traversal reference so the store can
// never read or write a file outside its flat root.
func TestSplitRef_TraversalRejected(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets-local")
	s := OpenAt(root)

	// A traversal ref must error on both Put and Get, and must NOT create any
	// file outside the store root.
	traversals := []string{
		`aa/../../../etc/x.key`,
		`../../etc.passwd`,
		`ok./nested`,
		`sp ace.key`,
	}
	for _, ref := range traversals {
		if err := s.Put(ref, "FAKEsecretvalue"); err == nil {
			t.Errorf("Put(%q): expected charset/traversal rejection, got nil", ref)
		}
		if _, _, err := s.Get(ref); err == nil {
			t.Errorf("Get(%q): expected charset/traversal rejection, got nil", ref)
		}
	}

	// The parent of the store root must be untouched (no escaped file written).
	parent := filepath.Dir(root)
	ents, _ := os.ReadDir(parent)
	for _, e := range ents {
		if e.Name() != "secrets-local" {
			t.Errorf("traversal escaped the store root: unexpected %q in %s", e.Name(), parent)
		}
	}
}

// TestSplitRef_DottedKeyRoundTrips covers the documented feature that a key may
// itself contain dots (only the first segment is the domain). A reference like
// "aws.access.key" must Put/Get cleanly — the key is a flat map index, never a
// path component, so dots in it are legal and must not be rejected by the
// path-traversal charset guard (which applies to the domain only).
func TestSplitRef_DottedKeyRoundTrips(t *testing.T) {
	s := OpenAt(t.TempDir())
	const ref, val = "aws.access.key", "FAKEsecretvalue"
	if err := s.Put(ref, val); err != nil {
		t.Fatalf("Put(%q): unexpected error: %v", ref, err)
	}
	got, ok, err := s.Get(ref)
	if err != nil {
		t.Fatalf("Get(%q): unexpected error: %v", ref, err)
	}
	if !ok || got != val {
		t.Errorf("Get(%q) = (%q, %v); want (%q, true)", ref, got, ok, val)
	}
}

// TestStore_RejectsNonUTF8 covers the store-hardening item: Put must reject a
// non-UTF-8 value BEFORE it reaches the TOML encoder (which would otherwise error
// opaquely or corrupt the store).
func TestStore_RejectsNonUTF8(t *testing.T) {
	s := OpenAt(t.TempDir())
	if err := s.Put("app.token", "valid\xffinvalid"); err == nil {
		t.Errorf("expected non-UTF-8 value to be rejected")
	}
	// A valid value under the same domain still works afterwards.
	if err := s.Put("app.token", "goodvalue"); err != nil {
		t.Errorf("valid Put after rejected one failed: %v", err)
	}
}
