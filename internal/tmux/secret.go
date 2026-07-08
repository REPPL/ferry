// Package tmux holds ferry's tmux-format knowledge that must NOT live in the
// generic secret scanner: the recogniser that finds a secret sitting inside a
// quoted `set -g @token '<VALUE>'` option value. It is the tmux analogue of the
// zsh plugin's assignment recogniser (internal/plugin/zsh) — format-specific
// span extraction that CALLS the generic secret primitives (secret.ScanText,
// secret.IsNonPlaceholderSecret, and, at the capture site, secret.SwapColumns)
// but never teaches internal/secret any tmux syntax.
package tmux

import (
	"regexp"
	"strings"

	"github.com/REPPL/ferry/internal/secret"
)

// setOptionRe matches a tmux `set` / `set-option` line that assigns a QUOTED
// value to a user option (`@name`). The value carries the potential secret,
// where zsh's whole-line fallback would clobber the `set -g @name` prefix and
// the quotes. Groups: 1 = everything up to and including the opening quote,
// 2 = the opening quote, 3 = the value, 4 = the closing quote. Group 3's byte
// offsets (FindStringSubmatchIndex) become the SecretSpan column range so only
// the value — never the surrounding `set -g @name '…'` syntax — is replaced.
//
// The value group is greedy (`.*`) up to the LAST quote before optional trailing
// whitespace: a tmux option takes a single value, so a greedy match cleanly spans
// a value that itself contains a quote of the OTHER kind. A mismatched
// open/close quote pair is rejected by the caller.
var setOptionRe = regexp.MustCompile(
	`^(\s*set(?:-option)?(?:\s+-[A-Za-z]+)*\s+@[A-Za-z0-9_-]+\s+(["']))(.*)(["'])\s*$`)

// SecretSpans returns the column-grained secret spans for tmux
// `set -g @token '<VALUE>'` option lines whose quoted VALUE is a real,
// high-confidence, literal secret. A span carries the byte range of the VALUE
// INSIDE the quotes (StartCol/EndCol), so the capture site can replace ONLY that
// range via secret.SwapColumns and leave the `set -g @token '…'` syntax and the
// quotes byte-identical.
//
// A value is a secret only when BOTH hold, exactly as the git/zsh sub-value
// recognisers gate:
//   - secret.IsNonPlaceholderSecret(value) — an env-ref like ${TMUX_TOKEN}, a
//     ferry {{…}} placeholder, or an empty value is NOT a secret and is left in
//     the repo verbatim; and
//   - secret.ScanText(value).HasHigh() — the value is a real high-confidence
//     secret, not an ordinary short option string.
//
// A line without a quoted `@name` value, or one whose open/close quotes differ,
// contributes nothing.
func SecretSpans(text string) []secret.SecretSpan {
	var spans []secret.SecretSpan
	for i, line := range strings.Split(text, "\n") {
		m := setOptionRe.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		// m indexes: [0:2] whole, [2:4] group1, [4:6] group2 (open quote),
		// [6:8] group3 (value), [8:10] group4 (close quote).
		openQuote := line[m[4]:m[5]]
		closeQuote := line[m[8]:m[9]]
		if openQuote != closeQuote {
			continue // an unbalanced quote pair is not a clean value
		}
		valStart, valEnd := m[6], m[7]
		value := line[valStart:valEnd]
		if !secret.IsNonPlaceholderSecret(value) {
			continue // ${ENV} / {{placeholder}} / empty: left verbatim, never stored
		}
		if !secret.ScanText(value).HasHigh() {
			continue // not a real high-confidence secret
		}
		spans = append(spans, secret.SecretSpan{
			StartLine: i + 1,
			EndLine:   i + 1,
			Value:     value,
			StartCol:  valStart,
			EndCol:    valEnd,
		})
	}
	return spans
}
