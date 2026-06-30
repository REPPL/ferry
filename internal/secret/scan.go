package secret

import (
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
	// password / passwd / secret / api_key / apikey / token / access_key /
	// secret_key / client_secret. It accepts shell (KEY=val, export KEY=val),
	// TOML/ini (key = "val"), and YAML-ish (key: val) shapes. The value must be
	// non-trivially long and not an obvious placeholder (see isLikelySecretValue)
	// to avoid flagging empty or templated assignments.
	secretAssignment = regexp.MustCompile(`(?i)(?:^|export\s+)` +
		`(password|passwd|secret|secret_key|api[_-]?key|apikey|access[_-]?key|client[_-]?secret|token|auth[_-]?token)` +
		`\s*[:=]\s*` +
		`["']?([^"'\s]+)["']?`)
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

// isLikelySecretValue decides whether the RHS of a credential assignment is a
// real value worth flagging, as opposed to an empty/placeholder/templated one.
// We require a minimum length and reject obvious placeholders so that lines like
// `password = ""` or `api_key = "${API_KEY}"` or a ferry placeholder do not
// trip the gate. Anything that survives is treated as a real secret regardless
// of entropy — a short literal password is still a secret.
func isLikelySecretValue(val string) bool {
	if len(val) < 6 {
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
