package secret

import "fmt"

// SwapColumns replaces the byte range [startCol, endCol) of line with
// replacement, preserving every byte OUTSIDE that range — the prefix
// line[:startCol] and the suffix line[endCol:] — exactly. It is the
// arbitrary-interior-offset counterpart of the zsh plugin's assignment-only swap
// (which rewrites only the post-'=' RHS, keeping body[:eq+1] + replacement +
// term): a git recognizer passes the byte range of a token inside a URL or after
// "Bearer "/"Basic "; a tmux recognizer passes the range between the quotes. Every
// surrounding byte — the URL scheme and host, the quotes, the "Bearer " prefix — is
// copied through untouched.
//
// Offsets are BYTE offsets into line, not rune indices, so multibyte/UTF-8 bytes on
// either side are preserved verbatim. Slicing line at byte boundaries is what makes
// the surrounding syntax byte-stable; the caller chooses a range on UTF-8 boundaries
// if it needs the replaced substring itself to be valid UTF-8, but the prefix and
// suffix are preserved regardless. line is expected to be a single line: a newline is
// treated as an ordinary byte and never given special handling.
//
// SwapColumns is its own inverse. For any valid range,
//
//	SwapColumns(SwapColumns(line, s, e, r), s, s+len(r), line[s:e]) == line
//
// byte-for-byte. That is the reverse-render path a plugin uses to restore the
// original bytes from a committed placeholder: locate the placeholder's byte range
// in the committed line and SwapColumns it back to the value read from the store.
//
// Out-of-range or inverted offsets return an error and NEVER panic or slice out of
// bounds: the contract is 0 <= startCol <= endCol <= len(line).
func SwapColumns(line string, startCol, endCol int, replacement string) (string, error) {
	if startCol < 0 || endCol < startCol || endCol > len(line) {
		return "", fmt.Errorf(
			"secret: column range [%d,%d) out of bounds for line of %d bytes",
			startCol, endCol, len(line))
	}
	return line[:startCol] + replacement + line[endCol:], nil
}

// IsNonPlaceholderSecret reports whether val is a real, literal secret rather than
// an empty / interpolated / placeholder one. It is the EXPORTED form of the shared
// credential-value gate the scanner uses internally (isNonPlaceholderSecret): a
// sub-value recognizer (a git insteadOf/http.extraHeader token, a tmux quoted option
// value) calls it on the EXACT substring it extracted so the SAME
// ${...} / $... / {{...}} / placeholder-word exemption applies — an env-ref value
// like ${NPM_TOKEN} or a ferry placeholder is NOT a secret and must be left in the
// repo verbatim, never stored. Reusing this one predicate here (instead of
// re-deriving the rule inside each plugin) keeps the exemption from forking.
func IsNonPlaceholderSecret(val string) bool {
	return isNonPlaceholderSecret(val)
}
