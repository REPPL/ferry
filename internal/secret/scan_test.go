package secret

import (
	"strings"
	"testing"
)

// FAKE secret material — never a real key. Constructed so the scanner's
// named-pattern detectors fire without embedding anything sensitive.
const fakePEMKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nFAKEKEYMATERIALabc123NOTREAL\n-----END OPENSSH PRIVATE KEY-----"

func TestScanText_DetectsPEMPrivateKey(t *testing.T) {
	fs := ScanText(fakePEMKey)
	if !fs.HasHigh() {
		t.Fatalf("expected high-confidence finding for PEM key, got %+v", fs)
	}
	if fs[0].Rule != "pem-private-key" {
		t.Errorf("expected pem-private-key rule, got %q", fs[0].Rule)
	}
	if fs[0].Line != 1 {
		t.Errorf("expected line 1, got %d", fs[0].Line)
	}
}

func TestScanText_DetectsWireGuardKeys(t *testing.T) {
	// FAKE base64-shaped WireGuard values.
	content := strings.Join([]string{
		"[Interface]",
		"PrivateKey = aFAKEkey0000000000000000000000000000000000=",
		"Address = 10.0.0.2/32",
		"[Peer]",
		"PresharedKey = bFAKEpsk1111111111111111111111111111111111=",
		"PublicKey = cNOTSECRET2222222222222222222222222222222=",
	}, "\n")
	fs := ScanText(content)
	if !fs.HasHigh() {
		t.Fatalf("expected high findings for WireGuard keys, got %+v", fs)
	}
	var priv, pre bool
	for _, f := range fs {
		if f.Rule == "wireguard-key" && f.Line == 2 {
			priv = true
		}
		if f.Rule == "wireguard-key" && f.Line == 5 {
			pre = true
		}
		if f.Line == 6 {
			t.Errorf("PublicKey (line 6) must NOT be flagged: %+v", f)
		}
	}
	if !priv || !pre {
		t.Errorf("expected both PrivateKey and PresharedKey flagged, got %+v", fs)
	}
}

func TestScanText_DetectsCredentialAssignment(t *testing.T) {
	cases := []string{
		`password = "hunter2hunter2"`,
		`export API_KEY=sk_live_FAKE000111222333abc`,
		`client_secret: superSecretValue123`,
		`token=ghp_FAKE0000aaaa1111bbbb`,
	}
	for _, c := range cases {
		fs := ScanText(c)
		if !fs.HasHigh() {
			t.Errorf("expected high finding for %q, got %+v", c, fs)
		}
	}
}

func TestScanText_DetectsHighEntropyToken(t *testing.T) {
	// A long, mixed-case + digit, base64-shaped opaque token on an otherwise
	// innocuous line. FAKE.
	line := `AWS_THING xQ7vR2pLkM9zNbW4tHcF8jYs3dG6aP1eUiOoZ0X=`
	fs := ScanText(line)
	if !fs.HasHigh() {
		t.Fatalf("expected high-entropy finding, got %+v", fs)
	}
	found := false
	for _, f := range fs {
		if f.Rule == "high-entropy-token" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected high-entropy-token rule, got %+v", fs)
	}
}

// TestScanText_NoFalsePositives is the false-positive discipline gate: ordinary
// dotfile lines must NEVER be flagged.
func TestScanText_NoFalsePositives(t *testing.T) {
	clean := []string{
		`export EDITOR=nvim`,
		`export PATH="$HOME/.local/bin:$PATH"`,
		`alias ll='ls -alh'`,
		`alias gs='git status'`,
		`export LANG=en_US.UTF-8`,
		`# this is a comment about my password manager`,
		`source "$HOME/.zshrc.local"`,
		`PublicKey = cNOTSECRET2222222222222222222222222222222=`,
		`bindkey '^R' history-incremental-search-backward`,
		`export HOMEBREW_PREFIX=/opt/homebrew`,
		`zstyle ':completion:*' menu select`,
		`password = ""`,                            // empty value
		`api_key = "${API_KEY}"`,                   // env interpolation
		`secret = "{{ferry.secret \"x.y\"}}"`,      // ferry placeholder
		`token = changeme`,                         // placeholder word
		`color = "#1a2b3c4d"`,                      // short hex-ish, under length gate
		`path = /usr/local/share/some/deep/dir`,    // path, not a secret
		`PASSWORD=`,                                // empty value, credential-named key
		`# password rotation notes`,                // comment, not an assignment
		`EDITOR=vim`,                               // non-credential name, normal value
		`npm.token = {{ferry.secret "npm.token"}}`, // ferry placeholder value
		`HOMEPAGE_URL=https://example.com`,         // bare URL, no userinfo
	}
	for _, line := range clean {
		fs := ScanText(line)
		if fs.HasHigh() {
			t.Errorf("FALSE POSITIVE: %q flagged: %+v", line, fs)
		}
	}
}

// TestScanText_CredentialKeywordNotFirstToken covers WS2(a): the credential
// keyword may be a separator-bounded segment of the KEY, not only its first
// token. DB_PASSWORD, MY_API_KEY, REDIS_PASSWORD, GITHUB_TOKEN etc. must block.
func TestScanText_CredentialKeywordNotFirstToken(t *testing.T) {
	cases := []string{
		`export DB_PASSWORD=hunter2seasons`,
		`MY_API_KEY=FAKEabc123def456`,
		`REDIS_PASSWORD=myredispass99`,
		`redis_password: myredispass99`,
		`DATABASE_PASSWORD=supersecretvalue`,
		`GITHUB_TOKEN=ghp_FAKE0000aaaa1111bbbb`,
	}
	for _, c := range cases {
		fs := ScanText(c)
		if !fs.HasHigh() {
			t.Errorf("expected high finding for %q, got %+v", c, fs)
		}
	}
}

// TestScanText_URLUserinfoCredential covers WS2(b): a password embedded in a
// URL's userinfo (scheme://[user][:pass]@host) must block even though the
// password is low-entropy and the token is split apart by the entropy
// tokenizer. A URL with no :pass@ userinfo must NOT be flagged.
func TestScanText_URLUserinfoCredential(t *testing.T) {
	block := []string{
		`DATABASE_URL=postgres://user:hunter2password@host:5432/db`,
		`REDIS_URL=redis://:s3cr3tpw@cache:6379/0`,
		// Short userinfo passwords: the scheme://user:X@host structure is itself
		// the signal that X is a credential, so even a 2-char password must block
		// (no minimum-length floor on the URL path). This is the commit's headline
		// example.
		`REDIS_URL=redis://:pw@host`,
		`redis://:pass@host`,
	}
	for _, c := range block {
		fs := ScanText(c)
		if !fs.HasHigh() {
			t.Errorf("expected high finding for URL userinfo credential %q, got %+v", c, fs)
		}
	}
	// A bare URL with no userinfo password must NOT over-block, and its captured
	// password must never leak into the detail.
	pass := []string{
		`HOMEPAGE_URL=https://example.com`,         // no userinfo at all
		`RSYNC_SRC=rsync://mirror@example.com/pkg`, // user, but no password
	}
	for _, c := range pass {
		fs := ScanText(c)
		if fs.HasHigh() {
			t.Errorf("FALSE POSITIVE: URL without userinfo password %q flagged: %+v", c, fs)
		}
	}
	for _, f := range ScanText(`DATABASE_URL=postgres://user:hunter2password@host:5432/db`) {
		if strings.Contains(f.Detail, "hunter2password") {
			t.Errorf("finding detail leaked the URL password: %q", f.Detail)
		}
	}
}

// TestScanText_NpmAuthToken covers the npm `~/.npmrc` auth token shapes: a
// registry-scoped `//<host>/:_authToken=`, `:_auth=`, or `:_password=` line
// whose value is a literal credential must block, even when the value is a short
// base64/UUID token below the entropy floor and the credential keyword is not at
// line-start (so secretAssignment and the entropy heuristic both miss it).
func TestScanText_NpmAuthToken(t *testing.T) {
	block := []string{
		// Automation token (npm_ prefix).
		`//registry.npmjs.org/:_authToken=npm_FAKE0000aaaa1111bbbb2222cccc3333`,
		// Low-entropy authToken value — would slip the entropy path; caught by the
		// line shape, not the value format. Deliberately not a real npm token
		// format (no `npm_` prefix, not a UUID) so provider secret scanners do not
		// flag this fixture, yet it carries no placeholder marker so ferry still
		// blocks it.
		`//registry.npmjs.org/:_authToken=faketoken0000000000000000legacy`,
		// Basic-auth base64 blob (_auth) — short, misses the entropy floor.
		`//registry.npmjs.org/:_auth=aGVsbG86d29ybGRzZWNyZXQ=`,
		// Legacy password (base64).
		`//registry.npmjs.org/:_password=c2VjcmV0cGFzc3dvcmQxMjM=`,
		// A scoped/private registry with a path prefix.
		`//npm.pkg.github.com/:_authToken=ghp_FAKE0000aaaa1111bbbb2222`,
		// Mixed case on the key.
		`//registry.npmjs.org/:_authtoken=npm_FAKE9999zzzz8888yyyy7777dddd`,
	}
	for _, c := range block {
		fs := ScanText(c)
		if !fs.HasHigh() {
			t.Errorf("expected high finding for npm auth line %q, got %+v", c, fs)
		}
	}
	// The token must never appear in the finding detail.
	for _, f := range ScanText(`//registry.npmjs.org/:_authToken=npm_FAKE0000aaaa1111bbbb2222cccc3333`) {
		if strings.Contains(f.Detail, "npm_FAKE") {
			t.Errorf("finding detail leaked the npm token: %q", f.Detail)
		}
	}
	// Negatives: an env-ref (npm expands it at read time), a ferry placeholder,
	// and ordinary non-secret npmrc lines must NOT be flagged.
	pass := []string{
		`//registry.npmjs.org/:_authToken=${NPM_TOKEN}`,               // env-ref, carried verbatim
		`//registry.npmjs.org/:_authToken={{ferry.secret "npm.tok"}}`, // ferry placeholder
		`//registry.npmjs.org/:_authToken=`,                           // empty value
		`//registry.npmjs.org/:username=alex`,                         // username is not a secret
		`//registry.npmjs.org/:email=alex@example.com`,                // email is not a secret
		`registry=https://registry.npmjs.org/`,                        // plain registry line
		`save-exact=true`,                                             // ordinary config flag
		`@myscope:registry=https://npm.pkg.github.com/`,               // scoped registry mapping
	}
	for _, c := range pass {
		fs := ScanText(c)
		if fs.HasHigh() {
			t.Errorf("FALSE POSITIVE: npmrc non-secret line %q flagged: %+v", c, fs)
		}
	}
}

func TestScanText_LineNumbers(t *testing.T) {
	content := "line one\nline two\npassword = realSecretValue99\nline four"
	fs := ScanText(content)
	if !fs.HasHigh() {
		t.Fatalf("expected a finding, got none")
	}
	if fs[0].Line != 3 {
		t.Errorf("expected finding on line 3, got %d", fs[0].Line)
	}
}

func TestScanValue_WholeValue(t *testing.T) {
	// Opaque/plist-style whole value containing a PEM key — Line should be 0.
	fs := ScanValue(fakePEMKey)
	if !fs.HasHigh() {
		t.Fatalf("expected high finding for value scan, got %+v", fs)
	}
	if fs[0].Line != 0 {
		t.Errorf("expected line 0 for value scan, got %d", fs[0].Line)
	}
}

func TestScanValue_CleanValue(t *testing.T) {
	if ScanValue("Solarized Dark").HasHigh() {
		t.Errorf("clean plist value must not be flagged")
	}
}

// TestScanValue_WireGuardKeyOnNonFirstLine checks the (?m) multiline fix: in a
// whole multi-line opaque value (ScanValue, Line 0), a PrivateKey assignment
// that is NOT on the first line must still be flagged.
func TestScanValue_WireGuardKeyOnNonFirstLine(t *testing.T) {
	value := "[Interface]\nAddress = 10.0.0.2/32\nPrivateKey = aFAKEkey0000000000000000000000000000000000=\n"
	fs := ScanValue(value)
	if !fs.HasHigh() {
		t.Fatalf("expected a high finding for a WireGuard key on a non-first line, got %+v", fs)
	}
	found := false
	for _, f := range fs {
		if f.Rule == "wireguard-key" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a wireguard-key finding, got %+v", fs)
	}
}

func TestFindingDetailNeverLeaksValue(t *testing.T) {
	secret := "realSecretValue99887766"
	fs := ScanText(`password = ` + secret)
	for _, f := range fs {
		if strings.Contains(f.Detail, secret) {
			t.Errorf("finding detail leaked the secret value: %q", f.Detail)
		}
	}
}

// TestHasKeyMarker covers the binary-safe key-marker scan used symmetrically by
// bundle export and import. It must catch a private-key header even amid NUL bytes
// (where the line-based text scanners give up) and stay quiet on ordinary binary.
func TestHasKeyMarker(t *testing.T) {
	markers := []string{
		"BEGIN OPENSSH PRIVATE KEY",
		"BEGIN RSA PRIVATE KEY",
		"BEGIN EC PRIVATE KEY",
		"BEGIN DSA PRIVATE KEY",
		"BEGIN PRIVATE KEY",
		"BEGIN PGP PRIVATE KEY",
		// The general PEM header must catch key-type labels not enumerated above,
		// so a new/uncommon label can't slip a private key past export/import.
		"BEGIN ENCRYPTED PRIVATE KEY",
		"BEGIN SSH2 ENCRYPTED PRIVATE KEY",
	}
	for _, m := range markers {
		// Embed the marker in binary content with NUL bytes on both sides — the
		// text scanner would treat this as binary and skip it; HasKeyMarker must not.
		payload := append([]byte{0x00, 0x01, 0xff, 0x00}, []byte("-----"+m+"-----")...)
		payload = append(payload, 0x00, 0x00)
		if !HasKeyMarker(payload) {
			t.Errorf("HasKeyMarker missed %q embedded in binary content", m)
		}
	}

	// Case-insensitive on the marker.
	if !HasKeyMarker([]byte("\x00begin openssh private key\x00")) {
		t.Errorf("HasKeyMarker should fold case on the marker")
	}

	// Clean binary (no marker) must not trip it.
	clean := []byte{0x00, 0x89, 0x50, 0x4e, 0x47, 0x00, 0xde, 0xad, 0xbe, 0xef}
	if HasKeyMarker(clean) {
		t.Errorf("HasKeyMarker fired on clean binary content")
	}
	if HasKeyMarker(nil) || HasKeyMarker([]byte{}) {
		t.Errorf("HasKeyMarker should be false on empty input")
	}
	// Ordinary text without a key marker must not trip it.
	if HasKeyMarker([]byte("export PATH=$HOME/bin\nalias ll='ls -la'\n")) {
		t.Errorf("HasKeyMarker fired on ordinary config text")
	}
}

// TestScanText_QuotedMultiWordCredential is the regression gate for the quoted
// multi-word value bypass: a credential assignment whose QUOTED value contains
// interior whitespace (or a short leading word) must still be flagged. The
// earlier value capture stopped at the first whitespace, so these slipped the
// gate entirely and reached the shared repo in plaintext.
func TestScanText_QuotedMultiWordCredential(t *testing.T) {
	cases := []string{
		`password = "my dog is rex1"`,        // multi-word, short first word
		`password = "my dog is x9"`,          // multi-word, sub-6-char first word
		`password = " paddedSecret123"`,      // leading space inside the quote
		`secret: 'correct horse battery st'`, // single-quoted multi-word
	}
	for _, c := range cases {
		if fs := ScanText(c); !fs.HasHigh() {
			t.Errorf("expected high finding for quoted multi-word credential %q, got %+v", c, fs)
		}
	}
	// Preserve the no-false-positive discipline: empty and placeholder quoted
	// values must still NOT be flagged.
	for _, c := range []string{
		`password = ""`,
		`api_key = "${API_KEY}"`,
		`secret = "{{ferry.secret \"x.y\"}}"`,
		`token = "changeme now please"`, // placeholder word inside a multi-word value
	} {
		if fs := ScanText(c); fs.HasHigh() {
			t.Errorf("FALSE POSITIVE: placeholder/empty quoted value %q flagged: %+v", c, fs)
		}
	}
}

// TestScanValue_MultiLineAssignment is the regression gate for the multi-line
// ScanValue bypass: a credential assignment on a NON-FIRST line of an opaque
// value (e.g. a defaults-export plist blob) must be caught. The ^-anchored
// detectors previously only ever matched line 1 of the whole value.
func TestScanValue_MultiLineAssignment(t *testing.T) {
	value := "line one\nline two\ndb_password = realSecretValue99\nline four"
	if fs := ScanValue(value); !fs.HasHigh() {
		t.Fatalf("expected high finding for credential on a non-first line, got %+v", fs)
	}
	// A single-line value keeps the original whole-value behaviour.
	if fs := ScanValue(`password = realSecretValue99`); !fs.HasHigh() {
		t.Errorf("single-line ScanValue regressed: %+v", fs)
	}
	// An ordinary multi-line value with no secret must not over-block.
	if fs := ScanValue("first line\nsecond line\nthird line"); fs.HasHigh() {
		t.Errorf("FALSE POSITIVE: benign multi-line value flagged: %+v", fs)
	}
}

// TestScanText_ExpandedCredentialKeywords is the F09 regression gate: common
// abbreviated/adjacent credential key names must now block, while their
// path-valued cousins (`*_file`, `*_path`) must NOT (the value is a filesystem
// path, not key material).
func TestScanText_ExpandedCredentialKeywords(t *testing.T) {
	block := []string{
		`DB_PASS=SuperSecret99x`,
		`PASSPHRASE=hunter2hunter2z`,
		`ENCRYPTION_KEY=q7GmL2vTn9wRx4z`,
		`credentials=abcdef123456ghij`,
		`signing_key: myS1gningKeyValue`,
	}
	for _, c := range block {
		if fs := ScanText(c); !fs.HasHigh() {
			t.Errorf("expected high finding for %q, got %+v", c, fs)
		}
	}
	// Path-valued credential-shaped keys must NOT be flagged (F09 guard).
	pass := []string{
		`pwd = /home/alex/project`,
		`credentials_file = ~/.aws/credentials`,
		`signing_key_path = /etc/keys/id_rsa`,
		`ssl_key = ./certs/server.key`,
	}
	for _, c := range pass {
		if fs := ScanText(c); fs.HasHigh() {
			t.Errorf("FALSE POSITIVE: path-valued credential key %q flagged: %+v", c, fs)
		}
	}
}

// TestScanText_LiteralDollarAndAngleSecrets is the F08 regression gate: a literal
// secret that merely starts with `$` or contains an interior `<`/`...` must be
// caught, while genuine interpolation and angle-bracket templates stay exempt.
func TestScanText_LiteralDollarAndAngleSecrets(t *testing.T) {
	block := []string{
		`password=$tr0ngPassw0rd!`,    // leetspeak literal, not a var ref
		`password=$uper$ecretPass99`,  // interior $, not interpolation
		`password=abc<def123ghi`,      // interior < but no template pair
		`password=abc...def123456ghi`, // ellipsis inside a longer literal
	}
	for _, c := range block {
		if fs := ScanText(c); !fs.HasHigh() {
			t.Errorf("expected high finding for literal secret %q, got %+v", c, fs)
		}
	}
	pass := []string{
		`password=${DB_PASSWORD}`,   // ${...} interpolation
		`password=$DB_PASSWORD`,     // whole-value bare var ref
		`api_key=$(op read secret)`, // $(...) command substitution
		`password=<your-password>`,  // angle-bracket template
	}
	for _, c := range pass {
		if fs := ScanText(c); fs.HasHigh() {
			t.Errorf("FALSE POSITIVE: interpolation/template %q flagged: %+v", c, fs)
		}
	}
}

// TestScanText_AuthHeaderBearerToken is the F21 regression gate: an Authorization
// header token in value position must block even when short, while a prose
// mention of "Bearer" (no secret-shaped token) must not.
func TestScanText_AuthHeaderBearerToken(t *testing.T) {
	block := []string{
		`header = "Authorization: Bearer zVbT3q9LmX2"`,
		`Authorization: Basic aGVsbG86d29ybGQ99`,
	}
	for _, c := range block {
		if fs := ScanText(c); !fs.HasHigh() {
			t.Errorf("expected high finding for auth header token %q, got %+v", c, fs)
		}
	}
	pass := []string{
		`# send a Bearer authentication token here`, // prose, no secret-shaped token
		`Authorization: Bearer <YOUR_TOKEN_HERE>`,   // placeholder
		`Authorization: Bearer null`,                // too short / not shaped
	}
	for _, c := range pass {
		if fs := ScanText(c); fs.HasHigh() {
			t.Errorf("FALSE POSITIVE: non-secret Bearer line %q flagged: %+v", c, fs)
		}
	}
}
