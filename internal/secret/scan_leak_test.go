package secret

import "testing"

// blocked asserts a line/text IS treated as a high-confidence secret (kept out
// of the repo). leak-class fixtures for fn-4.1/4.2.
func blocked(t *testing.T, label, content string) {
	t.Helper()
	if !IsBlockedFromRepo(content) {
		t.Errorf("%s: expected BLOCKED, but %q was allowed: %+v", label, content, ScanText(content))
	}
}

// notBlocked asserts ordinary content is NOT blocked (false-positive discipline).
func notBlocked(t *testing.T, label, content string) {
	t.Helper()
	if IsBlockedFromRepo(content) {
		t.Errorf("%s: FALSE POSITIVE — %q was blocked: %+v", label, content, ScanText(content))
	}
}

// TestScanText_IndentedAssignment covers F1: a credential assignment that is
// INDENTED (leading whitespace) must still be scanned. Before the ^\s* change
// the anchor `^` failed on any leading space and the secret leaked.
func TestScanText_IndentedAssignment(t *testing.T) {
	blocked(t, "indented password", "    password = realSecretValue99")
	blocked(t, "indented export", "\texport API_KEY=realSecretValue99abc")
	blocked(t, "indented yaml", "  api_key: superSecretValue123")
}

// TestScanText_NamedTokens covers F2: constant-shape named cloud/service tokens
// must be recognised by their prefix even when the KEY name is not a credential
// keyword (so secretAssignment does not fire). FAKE values, prefix-valid only.
func TestScanText_NamedTokens(t *testing.T) {
	cases := map[string]string{
		"aws-access-key": `id = AKIA1234567890ABCDEF`,
		"aws-asia":       `x = ASIAZ8Q7Y6X5W4V3U2T1`,
		"github-pat":     `gh = ghp_0123456789abcdefghijABCD0123456789`,
		"github-fine":    `gh = github_pat_11ABCDE0000aaaa1111bbbb2222cccc`,
		"gcp-api":        `k = AIzaSyD0aB1cD2eF3gH4iJ5kL6mN7oP8qR9sT0uV`,
		"slack-token":    `s = xoxb-0123456789-0123456789-abcdefABCDEF0123`,
		"openai-sk":      `o = sk-0123456789abcdefghijABCDEFGH`,
		"openai-sk-proj": `o = sk-proj-0123456789abcdefghijABCDEFGH`,
		"stripe-sk-live": `p = sk_live_0123456789abcdefABCD`,
		"stripe-rk-live": `p = rk_live_0123456789abcdefABCD`,
	}
	for name, line := range cases {
		blocked(t, name, line)
	}
}

// TestScanText_AWSSecretWithSlash covers the F1 slash case: an AWS-secret-shaped
// high-entropy base64 token that CONTAINS slashes must still be blocked. The old
// `>=2 slashes => path` rule dropped it; the fix is a path-SHAPE exclusion that
// keeps whole-token entropy scoring (NO splitting on '/').
func TestScanText_AWSSecretWithSlash(t *testing.T) {
	blocked(t, "aws secret with slashes", `blob = wJalrXUtnFEMIbPxRf/K7MDENGDsY/CYzFAKE99xY`)
}

// TestScanText_KeywordGatedHex covers F3: a long pure-hex value is a secret ONLY
// when the line's KEY names a credential. A bare 32+ hex string (git SHA / MD5 /
// UUID) must NOT block (see the false-positive test).
func TestScanText_KeywordGatedHex(t *testing.T) {
	blocked(t, "keyword-gated hex (no =)", `git_password 0123456789abcdef0123456789abcdef`)
	blocked(t, "keyword-gated hex assignment", `api_secret_hash = deadbeefdeadbeefdeadbeefdeadbeef`)
}

// TestScanText_BlockScalarAndHeredoc covers F4: a value carried on continuation
// lines (YAML block scalar `key: |`, or a heredoc under a credential key) must be
// associated with its key and blocked.
func TestScanText_BlockScalarAndHeredoc(t *testing.T) {
	blockScalar := "api_key: |\n  plainLowEntropyBodyValue\n"
	blocked(t, "yaml block scalar", blockScalar)

	heredoc := "DB_PASSWORD=$(cat <<'PWEOF'\nplainlowsecretbody\nPWEOF\n)\n"
	blocked(t, "heredoc under credential key", heredoc)
}

// TestScanText_StructuralFields covers F5: structural credential field names that
// are NOT in the base keyword set, plus the quoted-JSON key form.
func TestScanText_StructuralFields(t *testing.T) {
	blocked(t, "private_key_id", `private_key_id = a1b2c3d4e5f6g7h8`)
	blocked(t, "refresh_token", `refresh_token = realRefreshTokenValue99`)
	blocked(t, "pat", `GITHUB_PAT=realPatValue0123456789`)
	blocked(t, "bearer", `bearer = realBearerTokenValue99`)
	blocked(t, "quoted-json api_key", `  "api_key": "realJsonSecretValue99"`)
	blocked(t, "quoted-json private_key_id", `  "private_key_id": "a1b2c3d4e5f6g7h8"`)
}

// TestScanText_LeakFalsePositives is the false-positive discipline gate for the
// new rules: ordinary content that superficially resembles a secret must NEVER
// be blocked. Required fixtures: git SHA, MD5, SHA256, stripped UUID, sk-button,
// filesystem paths, and a base64-with-one-slash non-path.
func TestScanText_LeakFalsePositives(t *testing.T) {
	clean := map[string]string{
		"git-sha":          `revision = da39a3ee5e6b4b0d3255bfef95601890afd80709`,
		"git-sha-comment":  `# pin to commit 5e6b4b0d3255bfef95601890afd80709da39a3ee`,
		"md5":              `checksum = d41d8cd98f00b204e9800998ecf8427e`,
		"sha256":           `sha256 = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`,
		"stripped-uuid":    `id = f47ac10b58cc4372a5670e02b2c3d479`,
		"sk-button-css":    `class = "sk-button"`,
		"abs-path":         `path = /usr/local/share/some/deep/dir`,
		"home-path":        `export PATH="$HOME/.local/bin:$PATH"`,
		"rel-path":         `dir = usr/local/lib/python3/site-packages`,
		"base64-one-slash": `logo = iVBORw0KGgo/AAAA`,
		"editor":           `export EDITOR=nvim`,
		"comment-password": `# this is a comment about my password manager`,
	}
	for name, line := range clean {
		notBlocked(t, name, line)
	}
}
