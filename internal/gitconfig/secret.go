package gitconfig

import (
	"regexp"
	"strings"

	"github.com/REPPL/ferry/internal/secret"
)

// urlCredentialRe matches a URL with an embedded userinfo credential inside a
// git-config value or subsection header: `scheme://[user:]<TOKEN>@host`. Group 1
// is the TOKEN — the password when the userinfo has a `user:pass` shape
// (`https://oauth2:<TOKEN>@…`), or the whole userinfo when it does not
// (`https://<TOKEN>@…`). Its FindStringSubmatchIndex byte offsets become the
// SecretSpan column range, so ONLY the token is replaced and the surrounding
// scheme, optional `user:`, `@host`, and quotes survive byte-for-byte.
//
// The optional `(?:[^:/@\s]*:)?` consumes a leading `user:` so a `user:pass`
// URL isolates the password; a plain `token@` URL isolates the token.
var urlCredentialRe = regexp.MustCompile(`://(?:[^:/@\s]*:)?([^:/@\s]+)@`)

// extraHeaderRe matches the credential after an HTTP auth scheme in an
// http.extraHeader value: `Authorization: Bearer <TOKEN>` / `Basic <TOKEN>`.
// Group 1 is the TOKEN; its byte offsets narrow the SecretSpan so the
// `Authorization:` name and the `Bearer `/`Basic ` scheme prefix are preserved.
// The token group stops before whitespace AND before a trailing quote, so a
// quoted `extraHeader = "Authorization: Bearer <TOKEN>"` isolates <TOKEN> without
// swallowing the closing quote into the placeholder or the stored value
// (security-review F3).
var extraHeaderRe = regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+([^\s"']+)`)

// SecretSpans returns the column-grained secret spans for git-config lines whose
// value embeds a real, high-confidence, literal credential:
//
//   - a token inside a URL userinfo (url.<base>.insteadOf / insteadOf /
//     pushInsteadOf values, and a `[url "…"]` subsection header) — the token
//     between `://[user:]` and `@`;
//   - the credential after `Bearer `/`Basic ` in an http.extraHeader value.
//
// A span carries the byte range of ONLY the token (StartCol/EndCol on a single
// line), so the capture site replaces just that range via secret.SwapColumns and
// leaves every surrounding byte — the URL scheme and host, the `user:` prefix,
// the `Authorization: Bearer ` prefix, and any quotes — byte-identical.
//
// A token is a secret only when BOTH gates hold, exactly as the tmux/zsh
// sub-value recognisers require:
//   - secret.IsNonPlaceholderSecret(token) — a `${GIT_TOKEN}` env-ref, a ferry
//     `{{…}}` placeholder, or an empty value is NOT a secret and is left in the
//     repo verbatim; and
//   - secret.ScanText(token).HasHigh() — the token is a real high-confidence
//     secret, not an ordinary username or host fragment.
//
// If a git secret shape cannot be isolated to a clean span it contributes
// nothing here; the capture caller then degrades to a REFUSAL (never a whole-line
// clobber, never a repo write), mirroring tmux.
func SecretSpans(text string) []secret.SecretSpan {
	var spans []secret.SecretSpan
	for i, line := range strings.Split(text, "\n") {
		lineNo := i + 1
		spans = appendMatchSpans(spans, line, lineNo, urlCredentialRe)
		if isExtraHeaderLine(line) {
			spans = appendMatchSpans(spans, line, lineNo, extraHeaderRe)
		}
	}
	return spans
}

// appendMatchSpans finds every group-1 match of re in line and appends a
// column-grained span for each token that passes both secret gates. It uses byte
// offsets (FindAllStringSubmatchIndex) so the span range slices line on byte
// boundaries — the surrounding syntax is preserved verbatim by SwapColumns.
func appendMatchSpans(spans []secret.SecretSpan, line string, lineNo int, re *regexp.Regexp) []secret.SecretSpan {
	for _, m := range re.FindAllStringSubmatchIndex(line, -1) {
		if len(m) < 4 || m[2] < 0 || m[3] < 0 {
			continue
		}
		start, end := m[2], m[3]
		token := line[start:end]
		if !secret.IsNonPlaceholderSecret(token) {
			continue // ${ENV} / {{placeholder}} / empty: left verbatim, never stored
		}
		if !secret.ScanText(token).HasHigh() {
			continue // not a real high-confidence secret
		}
		spans = append(spans, secret.SecretSpan{
			StartLine: lineNo,
			EndLine:   lineNo,
			Value:     token,
			StartCol:  start,
			EndCol:    end,
		})
	}
	return spans
}

// isExtraHeaderLine reports whether a line is (or plausibly is) an
// http.extraHeader assignment — the only place a `Bearer `/`Basic ` credential
// scan is applied, so an unrelated line mentioning "Bearer" is not scanned. It
// matches on the key text before '=' so leading `[http]`-section indentation and
// spacing do not matter.
func isExtraHeaderLine(line string) bool {
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return false
	}
	return strings.Contains(strings.ToLower(line[:eq]), "extraheader")
}
