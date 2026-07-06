package secret

import (
	"bytes"
	"math"
	"regexp"
	"strings"
)

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
	// keyword must still start at line-start or after `export ` (no whitespace in
	// the prefix), so prose like `# password rotation notes` never matches.
	secretAssignment = regexp.MustCompile(`(?i)(?:^|export\s+)` +
		`(?:[A-Za-z0-9]+[._-])*` +
		`(password|passwd|secret|secret[_-]?key|api[_-]?key|apikey|access[_-]?key|client[_-]?secret|token|auth[_-]?token)` +
		`(?:[._-][A-Za-z0-9]+)*` +
		`\s*[:=]\s*` +
		`["']?([^"'\s]+)["']?`)

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
	}
	return out
}

// ScanValue scans a single opaque value as a whole (no line splitting) and
// returns findings with Line == 0. Use this for binary/opaque domains where
// line-by-line review is meaningless (e.g. a plist string value).
func ScanValue(value string) Findings {
	// Reuse the per-line detectors over the whole value, but report Line 0.
	return scanLine(value, 0)
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

	if m := secretAssignment.FindStringSubmatch(text); m != nil {
		key, val := m[1], m[2]
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
	// literal secret.
	if strings.Contains(val, "${") || strings.HasPrefix(val, "$") {
		return false
	}
	if strings.Contains(val, "{{") {
		return false
	}
	lower := strings.ToLower(val)
	for _, ph := range []string{"changeme", "xxxx", "placeholder", "your_", "example", "redacted", "<", "..."} {
		if strings.Contains(lower, ph) {
			return false
		}
	}
	return true
}

// candidateTokens splits a line into opaque tokens suitable for the entropy
// heuristic. It splits on whitespace and a few separators that commonly bound a
// token (= : , ; ( ) " ' ), so an embedded "KEY=<token>" yields the token alone.
func candidateTokens(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case ' ', '\t', '=', ':', ',', ';', '(', ')', '"', '\'':
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
	// Paths and URLs are long but not secrets. A '/' early on, or a URL scheme,
	// rules the token out. (Slashes appear in base64, but a real base64 secret
	// is dominated by alnum, not separated like a path.)
	if strings.Contains(tok, "://") {
		return false
	}
	if strings.Count(tok, "/") >= 2 || strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "~/") {
		return false
	}
	if !isSecretShaped(tok) {
		return false
	}
	return shannonEntropy(tok) >= entropyThreshold
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
