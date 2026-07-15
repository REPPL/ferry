package secret

import (
	"bytes"
	"math"
	"regexp"
	"strings"
)

// credKeyword is the shared alternation of credential-naming key words, composed
// once and reused by every keyword-anchored detector (secretAssignment, the
// credential-key predicate that gates the hex rule, and the block-scalar/heredoc
// openers) so the keyword list never forks. It covers the base credential words
// plus the STRUCTURAL field names (F5) that carry secrets under non-obvious keys:
// private_key_id (GCP service-account), pat (personal access token), bearer.
// refresh_token is already covered by the `token` word via the key-prefix chain.
const credKeyword = `(password|passwd|passphrase|pass|pwd|` +
	`secret|secret[_-]?key|api[_-]?key|apikey|` +
	`access[_-]?key|client[_-]?secret|token|auth[_-]?token|` +
	`credentials?|(?:encryption|signing|master)[_-]?key|` +
	`private[_-]?key[_-]?id|bearer|pat)`

// Confidence ranks how sure a finding is. Only High findings block repo routing
// (see gate.go); Medium is reserved for future, less-certain heuristics and is
// not currently emitted by the conservative ruleset.
type Confidence int

const (
	// Medium is a plausible-but-uncertain match. Not currently produced; kept so
	// the gate's High-only policy is explicit rather than implied.
	Medium Confidence = iota
	// High is a near-certain secret (named pattern or a long, high-entropy,
	// secret-shaped token). High findings block both shared and local routing.
	High
)

func (c Confidence) String() string {
	switch c {
	case High:
		return "high"
	default:
		return "medium"
	}
}

// Finding describes one matched secret. Line is 1-based for text scans; it is 0
// for whole-value scans (ScanValue), where there is no line context.
type Finding struct {
	// Rule names the detector that fired (e.g. "pem-private-key").
	Rule string
	// Confidence is the certainty of the match.
	Confidence Confidence
	// Line is the 1-based line number for text scans, or 0 for value scans.
	Line int
	// Detail is a short, human-readable description of what matched. It NEVER
	// contains the secret value itself — only the rule/keyword context — so that
	// findings can be logged or shown without leaking the secret.
	Detail string
}

// Findings is an ordered list of Finding. HasHigh reports whether any
// high-confidence secret was detected — the signal the routing gate keys off.
type Findings []Finding

// HasHigh reports whether any finding is High confidence.
func (fs Findings) HasHigh() bool {
	for _, f := range fs {
		if f.Confidence == High {
			return true
		}
	}
	return false
}

// --- Named, high-confidence patterns -------------------------------------
//
// These are the trusted core of the scanner. A match here is treated as a
// near-certain secret. They are intentionally specific so that ordinary config
// never matches.

var (
	// pemPrivateKey matches a PEM private-key header, e.g.
	// "-----BEGIN OPENSSH PRIVATE KEY-----" or "BEGIN RSA PRIVATE KEY". This is
	// the single highest-confidence signal — a private key must never reach the
	// repo. The header alone is enough; we do not require the full block.
	pemPrivateKey = regexp.MustCompile(`(?i)BEGIN(?:\s+[A-Z0-9]+)*\s+PRIVATE\s+KEY`)

	// wireGuardKey matches WireGuard key assignments: "PrivateKey = ..." and
	// "PresharedKey = ...". These appear in wg .conf files and carry raw key
	// material. (PublicKey is deliberately NOT matched — it is not secret.) The
	// (?m) flag makes ^ match at every line start, so in ScanValue (the whole
	// multi-line value, Line 0) a key on a non-first line is still caught.
	wireGuardKey = regexp.MustCompile(`(?im)^\s*(PrivateKey|PresharedKey)\s*=\s*\S`)

	// secretAssignment matches assignments whose KEY names a credential:
	// password / passwd / secret / secret_key / api_key / apikey / access_key /
	// client_secret / token / auth_token. It accepts shell (KEY=val, export
	// KEY=val), TOML/ini (key = "val"), and YAML-ish (key: val) shapes. The value
	// must be non-trivially long and not an obvious placeholder (see
	// isLikelySecretValue) to avoid flagging empty or templated assignments.
	//
	// The keyword is matched as a SEPARATOR-BOUNDED segment of the key, not only
	// its first token, so DB_PASSWORD, DATABASE_PASSWORD, MY_API_KEY,
	// REDIS_PASSWORD and GITHUB_TOKEN all match: `(?:[A-Za-z0-9]+[._-])*` allows
	// leading `WORD_`/`WORD.`/`WORD-` segments before the keyword and
	// `(?:[._-][A-Za-z0-9]+)*` allows trailing `_WORD` segments after it. The
	// keyword must still start at line-start (leading whitespace tolerated — F1
	// indented assignments) or after `export ` (no OTHER whitespace in the
	// prefix), so prose like `# password rotation notes` never matches. An
	// optional quote on both sides of the key admits the QUOTED-JSON key form
	// (`"api_key": "value"`) so structural JSON secrets are scanned (F5).
	//
	// The value is captured by an alternation, NOT a single `["']?([^"'\s]+)["']?`
	// group: that earlier form stopped the capture at the first whitespace, so a
	// QUOTED multi-word secret (`password = "my dog is rex1"`) yielded only its
	// first word — and if that fragment was under the 6-char floor, or the quote
	// was space-padded, the assignment produced NO finding at all and a plaintext
	// passphrase reached the shared repo. The alternation captures a double-quoted
	// (group 2) or single-quoted (group 3) value WHOLE (interior whitespace and
	// all), falling back to an unquoted run (group 4). scanLine takes the first
	// non-empty of the three and runs the SAME isLikelySecretValue gate on it.
	secretAssignment = regexp.MustCompile(`(?i)(?:^\s*|export\s+)` +
		`["']?` +
		`(?:[A-Za-z0-9]+[._-])*` +
		credKeyword +
		`(?:[._-][A-Za-z0-9]+)*` +
		`["']?` +
		`\s*[:=]\s*` +
		`(?:"([^"]*)"|'([^']*)'|([^"'\s]+))`)

	// credentialKey matches ONLY the KEY portion of a credential assignment
	// (line-start/after-export, optional quotes, credKeyword as a
	// separator-bounded segment) with NO value shape. It is the "does this line's
	// key name a credential?" predicate that gates the keyword-only hex rule and
	// the block-scalar/heredoc association: a bare 32+ hex value or a continuation
	// value counts as a secret only under such a key.
	credentialKey = regexp.MustCompile(`(?i)(?:^\s*|export\s+)` +
		`["']?` +
		`(?:[A-Za-z0-9]+[._-])*` +
		credKeyword +
		`(?:[._-][A-Za-z0-9]+)*` +
		`["']?`)

	// namedToken matches CONSTANT-SHAPE provider tokens by their fixed prefix
	// (F2). A match is a near-certain secret regardless of the key name or
	// entropy, so it anchors the high-confidence gate. Case is SIGNIFICANT
	// (AKIA/AIza/ghp_ differ in case) so there is no (?i). Each alternative pins
	// enough trailing length to clear ordinary identifiers — OpenAI keys require
	// 20+ chars after `sk-`, so a CSS class like `sk-button` never matches.
	namedToken = regexp.MustCompile(
		`(?:AKIA|ASIA|AGPA|AIDA)[A-Z0-9]{16}` + // AWS access-key id
			`|gh[posr]_[A-Za-z0-9]{20,}` + // GitHub token (ghp_/gho_/ghs_/ghr_)
			`|github_pat_[A-Za-z0-9_]{20,}` + // GitHub fine-grained PAT
			`|AIza[0-9A-Za-z_-]{35}` + // GCP API key
			`|xox[baprs]-[A-Za-z0-9-]{10,}` + // Slack token
			`|sk-proj-[A-Za-z0-9_-]{20,}` + // OpenAI project key
			`|sk-[A-Za-z0-9]{20,}` + // OpenAI secret key
			`|sk_live_[A-Za-z0-9]{16,}` + // Stripe live secret key
			`|rk_live_[A-Za-z0-9]{16,}`) // Stripe restricted live key

	// blockScalarOpener matches a credential-keyed YAML block-scalar opener
	// (`api_key: |` / `secret: >-`) whose value lives on the following, more
	// indented lines (F4). Only credential-keyed openers qualify, so a
	// `description: |` prose block never triggers.
	blockScalarOpener = regexp.MustCompile(`(?i)^\s*` +
		`["']?` +
		`(?:[A-Za-z0-9]+[._-])*` +
		credKeyword +
		`(?:[._-][A-Za-z0-9]+)*` +
		`["']?` +
		`\s*:\s*[|>][+-]?[0-9]*\s*$`)

	// heredocMarker captures a heredoc delimiter (`<<EOF`, `<<-'EOF'`, `<<~"EOF"`).
	// Combined with credentialKey it associates the heredoc BODY (up to the
	// delimiter line) with a credential key (F4).
	heredocMarker = regexp.MustCompile(`<<[-~]?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)

	// hexSecret matches a long pure-hexadecimal value. It is a secret ONLY when
	// the line's key names a credential (credentialKey): a BARE 32+ hex string is
	// by design a git SHA / MD5 / stripped UUID (all below the entropy floor), so
	// it must never block on its own (F3, adversarial A-1).
	hexSecret = regexp.MustCompile(`^[0-9a-fA-F]{32,}$`)

	// npmAuthLine matches an npm registry auth assignment whose value is a
	// literal credential — the `~/.npmrc` token shapes: a registry-scoped
	// `//<host>/[path]:_authToken=`, `:_auth=`, or `:_password=` line. These
	// bypass secretAssignment (the credential keyword is NOT at line-start — it
	// sits after `//<host>/:`) and are too short/low-entropy to trip the entropy
	// heuristic (a base64 `_auth`, a UUID legacy `_authToken`), so they need their
	// own detector or a real npm token would leak to the shared repo. Only the
	// captured VALUE (group 1) gates the finding, via the shared
	// placeholder/interpolation rule (isNonPlaceholderSecret) and — like the
	// URL-userinfo path — WITHOUT a length floor: the `:_authToken=` structure is
	// itself the signal that the value is a credential, so a short legacy token
	// still blocks. A `${NPM_TOKEN}` env-ref (npm expands it at read time) or a
	// ferry placeholder is therefore left un-flagged and carried verbatim. The
	// non-secret registry keys (:username, :email, :certfile, :keyfile) and plain
	// config lines (registry=, save-exact=true) are NOT matched. Case is
	// insensitive so `_authToken`/`_authtoken` both match.
	npmAuthLine = regexp.MustCompile(`(?i)^\s*//\S*:_(?:authtoken|auth|password)\s*=\s*(.+)$`)

	// urlCredential matches a password embedded in a URL's userinfo, i.e.
	// `scheme://[user][:pass]@host` (DATABASE_URL=postgres://user:pass@host,
	// REDIS_URL=redis://:pw@host). These bypass secretAssignment (the key name is
	// ..._URL, not a credential keyword) and the entropy heuristic (the tokenizer
	// splits the URL on ':' and the userinfo password is short and low-entropy),
	// so they need their own detector. Only the userinfo PASSWORD is captured
	// (group 1); it is gated by isNonPlaceholderSecret so a plain URL (no `:pass@`),
	// an empty password, a placeholder or an interpolation is NOT flagged. Unlike
	// the assignment path there is NO minimum-length floor: the `scheme://user:X@host`
	// structure is itself the signal that X is a credential, so even a 2-char DB
	// password (redis://:pw@host) blocks. A URL with a user but no password
	// (rsync://mirror@host) captures an empty group and is likewise not flagged.
	urlCredential = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^/@\s:]*(?::([^/@\s]*))?@`)

	// authHeaderToken matches an HTTP Authorization credential in VALUE position:
	// `Authorization: Bearer <tok>` / `Basic <tok>`. credKeyword's `bearer` only
	// fires when it is the assignment KEY (line-start/after-export), so a token
	// carried as a header VALUE inside a generic dotfile (~/.curlrc, ~/.wgetrc, a
	// custom tool config) slipped every gate — the entropy backstop needs 24+
	// chars, and these tokens are often shorter. The captured token is gated by
	// isNonPlaceholderSecret + isSecretShaped + a small length floor so prose like
	// `Bearer authentication` (no digits → not secret-shaped) is not flagged.
	authHeaderToken = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+([^\s"']+)`)
)

// entropyThreshold is the Shannon-entropy floor (bits per character) for the
// opaque high-entropy token heuristic. WHY 4.0: English/config text averages
// ~3.0-3.5 bits/char; base64/hex secret material runs ~4.5-6.0. A 4.0 floor
// (combined with the length and shape gates below) sits clearly above prose and
// ordinary identifiers but below real key material. It is intentionally
// conservative: we would rather miss a borderline token (the named patterns and
// the manual capture review remain) than flag a normal config line. Tune HERE if
// real dotfiles produce false positives — raise the floor or the length gate;
// never lower a named-pattern check to compensate.
const entropyThreshold = 4.0

// minEntropyTokenLen is the minimum candidate length for the entropy heuristic.
// Real tokens/keys are long; short high-entropy strings (a hex colour, a short
// hash fragment) are not worth the false-positive risk, so we ignore anything
// shorter. 24 chars excludes things like a 16-char hex colour or a git short
// SHA while still catching API tokens and base64 key blobs.
const minEntropyTokenLen = 24

// keyMarkers are high-signal PEM/OpenSSH private-key and PGP-key header markers.
// Unlike the text scanners above, HasKeyMarker matches these against RAW BYTES
// regardless of embedded NUL bytes, so a BINARY payload (which the line-based text
// scanners skip) that carries embedded key material is still caught. The set is
// intentionally small and near-zero false positive: an ordinary binary asset never
// contains these ASCII header strings.
var keyMarkers = [][]byte{
	[]byte("BEGIN OPENSSH PRIVATE KEY"),
	[]byte("BEGIN RSA PRIVATE KEY"),
	[]byte("BEGIN EC PRIVATE KEY"),
	[]byte("BEGIN DSA PRIVATE KEY"),
	[]byte("BEGIN PRIVATE KEY"),
	[]byte("BEGIN PGP PRIVATE KEY"),
}

// HasKeyMarker reports whether raw bytes contain a high-signal private-key header
// marker (PEM/OpenSSH/PGP). It is the BINARY-SAFE counterpart to IsBlockedFromRepo:
// it scans the raw bytes for the fixed header strings regardless of NUL bytes, so it
// works on binary/opaque content the line-based text scanners cannot handle. Both
// the bundle EXPORT and the bundle IMPORT/validate paths use this ONE function to
// classify a binary payload, so their secret checks stay symmetric — a binary
// carrying key bytes is withheld on export and refused on import identically. The
// match is case-insensitive on the marker to catch mixed-case headers.
func HasKeyMarker(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	lower := bytes.ToLower(data)
	// General PEM private-key header: `begin <anything> private key` covers every
	// variant — RSA/EC/DSA/OPENSSH/ENCRYPTED/SSH2/(bare) PRIVATE KEY — without having
	// to enumerate each, so a new label can't slip a key past us. Plus the PGP header.
	if keyMarkerRE.Match(lower) {
		return true
	}
	for _, m := range keyMarkers {
		if bytes.Contains(lower, bytes.ToLower(m)) {
			return true
		}
	}
	return false
}

// keyMarkerRE matches any PEM private-key BEGIN header regardless of the key-type
// label between "begin" and "private key" (matched on lowercased bytes).
var keyMarkerRE = regexp.MustCompile(`begin[ \-]*[a-z0-9 ]*private key`)

// HasBinarySecret reports whether raw bytes carry either a private-key header
// (PEM/OpenSSH/PGP, via HasKeyMarker) OR a constant-prefix provider token
// (AWS/GitHub/GCP/Slack/OpenAI/Stripe, via the namedToken regex applied to bytes).
// It is the binary-safe secret check for opaque payloads the line-based text
// scanners cannot handle. Bundle EXPORT and IMPORT/validate both use it, so a
// binary carrying key material OR a recognizable token is refused symmetrically on
// both sides — closing the gap where a `ghp_…`/`AKIA…` token embedded in a tracked
// binary (a compiled config, a binary plist) was bundled because only PEM headers
// were checked.
func HasBinarySecret(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if HasKeyMarker(data) {
		return true
	}
	return namedToken.Match(data)
}

// ScanText scans content line-by-line and returns every secret finding, with
// 1-based line numbers. Use this for text domains (dotfiles, wg .conf, shell
// rc). For opaque/binary domains (a plist value), use ScanValue.
func ScanText(content string) Findings {
	var out Findings
	// Split on \n; keep it simple and dependency-free. \r is trimmed per line.
	lines := strings.Split(content, "\n")
	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		lineNo := i + 1
		out = append(out, scanLine(line, lineNo)...)

		// F4: a credential-keyed line can carry its value on CONTINUATION lines a
		// per-line scan misses — a heredoc body (up to the delimiter) or a YAML
		// block scalar (the following, more-indented lines). Associate those with
		// the key here, gated on the same non-placeholder rule as the assignment
		// path so an interpolated/templated body is not flagged.
		if credentialKey.MatchString(line) {
			if m := heredocMarker.FindStringSubmatch(line); m != nil {
				out = append(out, scanHeredocBody(lines, i, m[1])...)
				continue
			}
		}
		if blockScalarOpener.MatchString(line) {
			out = append(out, scanBlockScalarBody(lines, i)...)
		}
	}
	return out
}

// scanHeredocBody flags the non-placeholder lines of a heredoc body that opens
// on lines[openIdx] with delimiter delim, up to (not including) the delimiter
// line. lines are the RAW split lines; \r is trimmed per line.
func scanHeredocBody(lines []string, openIdx int, delim string) Findings {
	var out Findings
	for j := openIdx + 1; j < len(lines); j++ {
		body := strings.TrimRight(lines[j], "\r")
		if strings.TrimSpace(body) == delim {
			break
		}
		if v := strings.TrimSpace(body); v != "" && isNonPlaceholderSecret(v) {
			out = append(out, Finding{
				Rule:       "secret-heredoc",
				Confidence: High,
				Line:       j + 1,
				Detail:     "credential heredoc value",
			})
		}
	}
	return out
}

// scanBlockScalarBody flags the non-placeholder lines of a YAML block-scalar
// body that opens on lines[openIdx], i.e. the following non-blank lines indented
// deeper than the opener, stopping at the first line indented at or below it.
func scanBlockScalarBody(lines []string, openIdx int) Findings {
	var out Findings
	base := leadingIndent(lines[openIdx])
	for j := openIdx + 1; j < len(lines); j++ {
		body := strings.TrimRight(lines[j], "\r")
		if strings.TrimSpace(body) == "" {
			continue
		}
		if leadingIndent(lines[j]) <= base {
			break
		}
		if v := strings.TrimSpace(body); isNonPlaceholderSecret(v) {
			out = append(out, Finding{
				Rule:       "secret-block-scalar",
				Confidence: High,
				Line:       j + 1,
				Detail:     "credential block-scalar value",
			})
		}
	}
	return out
}

// leadingIndent counts the leading space/tab characters of a line (tab counts as
// one column — comparison is relative, so a consistent width is all that matters).
func leadingIndent(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' || r == '\t' {
			n++
			continue
		}
		break
	}
	return n
}

// ScanValue scans a single opaque value and returns findings with Line == 0.
// Use this for binary/opaque domains where line-by-line review is meaningless
// to the caller (e.g. a plist string value gated whole at capture time).
//
// It splits the value on newlines and runs the per-line detectors over EACH
// line, still stamping Line 0. The per-line pass is required for correctness:
// secretAssignment, credentialKey and npmAuthLine are `^`-anchored WITHOUT the
// `(?m)` flag, so scanning the whole multi-line value at once could only ever
// match its FIRST line — a credential assignment on any later line of a
// multi-line plist/preference blob slipped the gate entirely. Scanning each
// line closes that gap while keeping the Line-0 (no line context) contract the
// opaque-domain callers expect.
func ScanValue(value string) Findings {
	if !strings.ContainsAny(value, "\n\r") {
		return scanLine(value, 0)
	}
	var out Findings
	for _, raw := range strings.Split(value, "\n") {
		out = append(out, scanLine(strings.TrimRight(raw, "\r"), 0)...)
	}
	return out
}

// scanLine runs every detector against a single piece of text and returns the
// findings, stamped with lineNo.
func scanLine(text string, lineNo int) Findings {
	var out Findings

	if pemPrivateKey.MatchString(text) {
		out = append(out, Finding{
			Rule:       "pem-private-key",
			Confidence: High,
			Line:       lineNo,
			Detail:     "PEM private key header",
		})
		// A private-key header is conclusive on its own; no further detectors
		// add value for this line.
		return out
	}

	if m := wireGuardKey.FindStringSubmatch(text); m != nil {
		out = append(out, Finding{
			Rule:       "wireguard-key",
			Confidence: High,
			Line:       lineNo,
			Detail:     "WireGuard " + m[1] + " assignment",
		})
		return out
	}

	// Named provider tokens (AWS/GitHub/GCP/Slack/OpenAI/Stripe): a fixed-prefix
	// match is near-certain regardless of the key name, so it is checked before
	// the assignment/entropy heuristics. The detail never echoes the token.
	if namedToken.MatchString(text) {
		out = append(out, Finding{
			Rule:       "named-token",
			Confidence: High,
			Line:       lineNo,
			Detail:     "named provider token",
		})
		return out
	}

	if m := secretAssignment.FindStringSubmatch(text); m != nil {
		key := m[1]
		// The value is whichever of the double-quoted (m[2]), single-quoted (m[3]),
		// or unquoted (m[4]) alternatives matched; only one is ever non-empty for a
		// real value. An empty quoted value ("") leaves all three empty and falls
		// through isLikelySecretValue's floor as before.
		val := m[2]
		if val == "" {
			val = m[3]
		}
		if val == "" {
			val = m[4]
		}
		if isLikelySecretValue(val) {
			out = append(out, Finding{
				Rule:       "secret-assignment",
				Confidence: High,
				Line:       lineNo,
				Detail:     "credential assignment (" + strings.ToLower(key) + ")",
			})
			return out
		}
	}

	// URL userinfo password (scheme://[user][:pass]@host). Only the captured
	// password gates the finding, via the shared placeholder/interpolation rules
	// (isNonPlaceholderSecret) but WITHOUT the assignment path's length floor: the
	// scheme://user:X@host structure is itself the signal that X is a credential,
	// so a short DB/cache password still blocks. A bare URL (empty capture) is
	// never flagged. The detail never echoes the password.
	if m := urlCredential.FindStringSubmatch(text); m != nil {
		if isNonPlaceholderSecret(m[1]) {
			out = append(out, Finding{
				Rule:       "url-credential",
				Confidence: High,
				Line:       lineNo,
				Detail:     "credential in URL userinfo",
			})
			return out
		}
	}

	// npm registry auth line (//<host>/:_authToken= / :_auth= / :_password=). The
	// credential keyword is not at line-start, so secretAssignment misses it; the
	// value is often a short base64/UUID token below the entropy floor, so the
	// entropy path misses it too. Only the captured value gates the finding, via
	// the shared placeholder/interpolation rule, so a `${NPM_TOKEN}` env-ref is
	// left un-flagged and carried verbatim. The detail never echoes the token.
	if m := npmAuthLine.FindStringSubmatch(text); m != nil {
		if isNonPlaceholderSecret(strings.TrimSpace(m[1])) {
			out = append(out, Finding{
				Rule:       "npm-auth-token",
				Confidence: High,
				Line:       lineNo,
				Detail:     "npm registry auth token",
			})
			return out
		}
	}

	// Authorization header token in value position (Bearer/Basic <tok>). Gated so a
	// prose "Bearer" mention or a placeholder token does not fire: the token must be
	// non-placeholder, secret-shaped (letters+digits, base64/hex alphabet), and at
	// least 8 chars.
	if m := authHeaderToken.FindStringSubmatch(text); m != nil {
		if tok := m[1]; len(tok) >= 8 && isSecretShaped(tok) && isNonPlaceholderSecret(tok) {
			out = append(out, Finding{
				Rule:       "auth-header-token",
				Confidence: High,
				Line:       lineNo,
				Detail:     "Authorization header token",
			})
			return out
		}
	}

	// Keyword-gated hex (F3): a long pure-hex value is a secret ONLY when the
	// line's KEY names a credential. Bare 32+ hex is deliberately NOT flagged
	// (git SHA / MD5 / stripped UUID); the entropy path also skips pure hex, so
	// this credentialKey gate is the ONLY route by which hex blocks.
	if credentialKey.MatchString(text) {
		for _, tok := range candidateTokens(text) {
			if hexSecret.MatchString(tok) {
				out = append(out, Finding{
					Rule:       "hex-secret",
					Confidence: High,
					Line:       lineNo,
					Detail:     "credential-keyword hex value",
				})
				return out
			}
		}
	}

	// Entropy heuristic: scan opaque tokens on the line. This is the only
	// detector that can misfire on real config, so it is the most conservative.
	for _, tok := range candidateTokens(text) {
		if looksLikeHighEntropySecret(tok) {
			out = append(out, Finding{
				Rule:       "high-entropy-token",
				Confidence: High,
				Line:       lineNo,
				Detail:     "high-entropy token",
			})
			// One entropy finding per line is enough to block; stop scanning
			// further tokens on the same line.
			break
		}
	}

	return out
}

// isLikelySecretValue decides whether the RHS of a credential ASSIGNMENT
// (KEY=val) is a real value worth flagging, as opposed to an
// empty/placeholder/templated one. It layers a minimum-length floor on top of
// the shared placeholder/interpolation check: for a bare `key = value`
// assignment the key name is only weak evidence, so a too-short value is treated
// as noise (a config flag, a version) rather than a secret. Lines like
// `password = ""` or `api_key = "${API_KEY}"` or a ferry placeholder do not trip
// the gate. Anything that survives is treated as a real secret regardless of
// entropy — a short literal password is still a secret.
func isLikelySecretValue(val string) bool {
	if len(val) < 6 {
		return false
	}
	// A filesystem PATH is not a secret, even under a credential-shaped key: keys
	// like `pwd`, `credentials_file`, `signing_key`, or `ssl_key` routinely point at
	// a file rather than carrying key material. Excluding path-shaped values here is
	// what lets credKeyword cover those abbreviated/adjacent names (pwd, pass,
	// credentials, *_key) without turning every `*_file = /some/path` into a false
	// positive. Real opaque secrets are not path-shaped (base64/hex segments carry
	// uppercase+digits, not lowercase path words), so this does not mask them.
	if looksLikePath(val) {
		return false
	}
	return isNonPlaceholderSecret(val)
}

// isNonPlaceholderSecret is the shared credential-value gate: it reports whether
// val is a real, literal secret as opposed to an empty/interpolated/placeholder
// one. It rejects empty values, shell/env interpolation (`${...}`, `$...`),
// ferry's own `{{...}}` placeholder, and obvious placeholder words. It carries NO
// length floor — that is the assignment path's concern (isLikelySecretValue). The
// URL-userinfo path uses this directly, because the `scheme://user:X@host`
// structure is itself the signal that X is a credential, so even a 2-char
// userinfo password must block.
func isNonPlaceholderSecret(val string) bool {
	if val == "" {
		return false
	}
	// Reject shell/env interpolation and ferry's own placeholder: these carry no
	// literal secret. The `$` test is NARROW — only `${VAR}` / `$(cmd)` expansions
	// or a value that is ENTIRELY a bare `$VAR` reference. A blanket "starts with $"
	// exemption also swallowed real leetspeak passwords (`$tr0ngPassw0rd!`,
	// `$uper$ecretPass99`), which are literal secrets, not interpolation.
	if strings.Contains(val, "${") || strings.Contains(val, "$(") {
		return false
	}
	if shellVarRef.MatchString(val) {
		return false
	}
	if strings.Contains(val, "{{") {
		return false
	}
	// A `<...>` angle-bracket span is a template placeholder (`<your-password>`),
	// but a bare `<` anywhere used to exempt the whole value — so `abc<def123` (a
	// literal secret with an interior `<`) slipped through. Require the structural
	// pair. `...` is likewise only a placeholder when it stands alone, not as a
	// substring of a longer literal value.
	if anglePlaceholder.MatchString(val) {
		return false
	}
	if strings.TrimSpace(val) == "..." {
		return false
	}
	lower := strings.ToLower(val)
	for _, ph := range []string{"changeme", "xxxx", "placeholder", "your_", "example", "redacted"} {
		if strings.Contains(lower, ph) {
			return false
		}
	}
	return true
}

// shellVarRef matches a value that is ENTIRELY one shell/env variable reference
// (`$FOO`, `${FOO}`, `$foo_bar`) — genuine interpolation carrying no literal
// secret. A value that merely STARTS with `$` but continues with other characters
// (`$tr0ngPassw0rd!`) is a literal, not a reference, and is NOT exempted.
var shellVarRef = regexp.MustCompile(`^\$\{?[A-Za-z_][A-Za-z0-9_]*\}?$`)

// anglePlaceholder matches an angle-bracket template span like `<your-password>`
// or `<PASSWORD>`. Used to exempt structural placeholders without exempting every
// value that merely contains a stray `<`.
var anglePlaceholder = regexp.MustCompile(`<[^<>]*>`)

// candidateTokens splits a line into opaque tokens suitable for the entropy
// heuristic. It splits on whitespace and a few separators that commonly bound a
// token (= : , ; ( ) " ' ), so an embedded "KEY=<token>" yields the token alone.
func candidateTokens(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '=', ':', ',', ';', '(', ')', '"', '\'':
			return true
		default:
			return false
		}
	})
	return fields
}

// looksLikeHighEntropySecret reports whether a single token is a long,
// high-entropy, secret-SHAPED string. All three gates must pass: length, a
// secret-like character set (no spaces, mostly the base64/hex/url-safe alphabet),
// and Shannon entropy above the threshold. Filesystem paths and URLs are
// excluded explicitly because a deep path can be long and look busy without
// being a secret.
func looksLikeHighEntropySecret(tok string) bool {
	if len(tok) < minEntropyTokenLen {
		return false
	}
	// Paths and URLs are long but not secrets. A URL scheme, or a path-SHAPED
	// token, rules it out. Crucially this is a path-SHAPE test — NOT a slash
	// COUNT and NOT a split of the candidate on '/': an AWS secret key
	// (base64 with 1-2 interior slashes) must keep whole-token entropy scoring
	// and still block (F1, adversarial A-2).
	if strings.Contains(tok, "://") {
		return false
	}
	if looksLikePath(tok) {
		return false
	}
	// Pure hex is handled ONLY by the keyword-gated hex rule; the entropy path
	// never flags it, so a bare git SHA / MD5 / SHA256 / stripped UUID is not a
	// false positive (F3, adversarial A-1).
	if isHex(tok) {
		return false
	}
	if !isSecretShaped(tok) {
		return false
	}
	return shannonEntropy(tok) >= entropyThreshold
}

// looksLikePath reports whether a token has FILESYSTEM-PATH shape rather than
// opaque key material: a leading /, ~/, ./ or ../, or an interior '/'-separated
// component that reads like a path segment (a lowercase word or a dotted
// filename). Base64 secrets with interior slashes are NOT path-shaped: their
// segments carry uppercase+digits, not lowercase words, so they fall through to
// entropy scoring.
func looksLikePath(tok string) bool {
	if strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "~/") ||
		strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "../") {
		return true
	}
	if !strings.Contains(tok, "/") {
		return false
	}
	for _, seg := range strings.Split(tok, "/") {
		if isPathishSegment(seg) {
			return true
		}
	}
	return false
}

// pathishSegment matches a lowercase-word / dotted-filename path component
// (usr, local, site-packages, init.el). A mixed-case+digit base64 chunk does NOT
// match, so an AWS-secret segment is never mistaken for a path segment.
var pathishSegment = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

func isPathishSegment(seg string) bool {
	if seg == "" {
		return false
	}
	// Must contain a letter (a pure-number segment is not evidence of a path).
	hasLetter := false
	for _, r := range seg {
		if r >= 'a' && r <= 'z' {
			hasLetter = true
			break
		}
	}
	return hasLetter && pathishSegment.MatchString(seg)
}

// isHex reports whether the token is entirely hexadecimal digits.
func isHex(tok string) bool {
	if tok == "" {
		return false
	}
	for _, r := range tok {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// isSecretShaped reports whether the token's character set looks like opaque key
// material: it must be composed almost entirely of the base64url / hex alphabet
// (alphanumerics plus + / - _ . =) and must contain BOTH letters and digits.
// Pure-word tokens (a long lowercase identifier) lack digits and are rejected;
// mixed-case-plus-digit blobs are what real tokens look like.
func isSecretShaped(tok string) bool {
	var hasLetter, hasDigit, allowed int
	for _, r := range tok {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter++
			allowed++
		case r >= '0' && r <= '9':
			hasDigit++
			allowed++
		case r == '+' || r == '/' || r == '-' || r == '_' || r == '.' || r == '=':
			allowed++
		}
	}
	// At least 90% of characters must be in the allowed alphabet (rejects prose
	// with punctuation), and the token needs both letters and digits.
	if allowed*10 < len([]rune(tok))*9 {
		return false
	}
	return hasLetter > 0 && hasDigit > 0
}

// shannonEntropy returns the Shannon entropy of s in bits per character.
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	counts := make(map[rune]int)
	var n float64
	for _, r := range s {
		counts[r]++
		n++
	}
	var h float64
	for _, c := range counts {
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}
