package ghcli

import "testing"

// TestRedactMasksCredentials proves the boundary scrub masks GitHub token prefixes,
// Authorization/Bearer header values, and URL userinfo — the shapes that could
// otherwise leak into an error ferry returns or prints if gh/git emit them on
// stderr. All secrets here are FAKE.
func TestRedactMasksCredentials(t *testing.T) {
	in := "fatal: could not read Username for 'https://user:tok@github.com': " +
		"token ghp_deadbeefCAFE0123456789abcdefGHIJKLmnop used with " +
		"Authorization: Bearer sk-secret-value-xyz and also github_pat_11ABCDEF0123_longtail"

	got := redact(in)

	// Nothing that looks like a real credential survives.
	forbidden := []string{
		"ghp_deadbeef",
		"ghp_deadbeefCAFE0123456789abcdefGHIJKLmnop",
		"github_pat_11ABCDEF0123_longtail",
		"sk-secret-value-xyz",
		"user:tok@github.com",
		"tok@github.com",
	}
	for _, f := range forbidden {
		if contains(got, f) {
			t.Errorf("redact leaked %q in output:\n%s", f, got)
		}
	}

	// The masks are present so the shape stays legible.
	if !contains(got, "***") {
		t.Errorf("redact produced no mask marker:\n%s", got)
	}
	if !contains(got, "https://***@github.com") {
		t.Errorf("redact did not mask URL userinfo to https://***@github.com:\n%s", got)
	}
}

// TestRedactCoversAllTokenPrefixes checks every GitHub token prefix is masked.
func TestRedactCoversAllTokenPrefixes(t *testing.T) {
	for _, p := range []string{"ghp_", "gho_", "ghs_", "ghu_", "ghr_", "github_pat_"} {
		tok := p + "AbCdEf0123456789xyz"
		got := redact("prefix " + tok + " suffix")
		if contains(got, tok) {
			t.Errorf("redact did not mask token with prefix %q: %s", p, got)
		}
	}
}

// contains is a tiny local helper to avoid importing strings in a one-liner test.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
