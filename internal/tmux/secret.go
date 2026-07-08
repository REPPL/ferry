// Package tmux holds ferry's tmux-format knowledge that must NOT live in the
// generic secret scanner: the recogniser that finds a secret sitting inside a
// `set -g @token '<VALUE>'` option value. It is the tmux analogue of the
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

// setOptionPrefixRe matches the tmux command PREFIX that assigns a value to a
// user option (`@name`), up to and including the whitespace before the value:
//
//	set / set-option / setw / set-window-option  [-flags]…  @name<space>
//
// Group 1 is the whole prefix; its end offset (FindStringSubmatchIndex) is where
// the value token begins. The value itself — quoted or bare, with an optional
// trailing comment — is isolated in code (extractValueSpan) rather than in the
// regex, so the `set -g @name` prefix and any quotes/comment stay outside the
// replaced span and survive byte-for-byte.
//
// Command spellings are ordered longest-first so the intended alternative wins,
// and each is anchored by the mandatory `\s+@name` that follows: `setxyz @o` and
// `set-bogus @o` never match. Flags are the usual `-g`, `-a`, `-u`, `-ga`, … in
// any number; a flag that itself takes a separate argument (`set -t main @o v`)
// is not modelled and simply degrades to a refusal (no span).
var setOptionPrefixRe = regexp.MustCompile(
	`^(\s*(?:set-window-option|set-option|setw|set)(?:\s+-[A-Za-z]+)*\s+@[A-Za-z0-9_-]+\s+)`)

// trailerRe matches what may legally follow an isolated value: nothing but
// optional whitespace and an optional `# …` comment to end of line. A value
// whose tail is anything else cannot be cleanly isolated and is refused.
var trailerRe = regexp.MustCompile(`^\s*(?:#.*)?$`)

// SecretSpans returns the column-grained secret spans for tmux option lines
// whose assigned VALUE is a real, high-confidence, literal secret. The value may
// be single- or double-quoted, or a bare unquoted token, on any of the four
// option-setting commands (set / set-option / setw / set-window-option), and may
// carry a trailing `# comment`. A span carries the byte range of the VALUE ONLY
// (StartCol/EndCol, excluding any quotes), so the capture site replaces just that
// range via secret.SwapColumns and leaves the surrounding `set -g @token '…' #…`
// syntax byte-identical.
//
// A value is a secret only when BOTH hold, exactly as the git/zsh sub-value
// recognisers gate:
//   - secret.IsNonPlaceholderSecret(value) — an env-ref like ${TMUX_TOKEN} or
//     $TMUX_TOKEN, a ferry {{…}} placeholder, or an empty value is NOT a secret
//     and is left in the repo verbatim; and
//   - secret.ScanText(value).HasHigh() — the value is a real high-confidence
//     secret, not an ordinary short option string.
//
// SAFETY: any shape the recogniser cannot cleanly isolate — an unbalanced quote,
// a value whose tail is neither whitespace nor a comment, a flag that consumes an
// argument — yields NO span, so capture degrades to a whole-file refusal rather
// than a mis-sliced or whole-line clobber. The capture site's post-patch re-gate
// (secret.IsBlockedFromRepo) is the backstop.
func SecretSpans(text string) []secret.SecretSpan {
	var spans []secret.SecretSpan
	for i, line := range strings.Split(text, "\n") {
		m := setOptionPrefixRe.FindStringSubmatchIndex(line)
		if m == nil {
			continue // not a `set…@name <value>` line at all
		}
		valStart, valEnd, ok := extractValueSpan(line, m[3])
		if !ok {
			continue // value could not be cleanly isolated: refuse, do not clobber
		}
		value := line[valStart:valEnd]
		if !secret.IsNonPlaceholderSecret(value) {
			continue // ${ENV} / $ENV / {{placeholder}} / empty: left verbatim, never stored
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

// extractValueSpan isolates the option value that begins at byte offset start on
// line, returning the value's [vStart, vEnd) byte range EXCLUDING any quotes.
// ok is false — a clean refusal — whenever the value cannot be unambiguously
// bounded, so the caller contributes no span rather than risk a mis-slice.
//
// Quoted value: the token opens with `'` or `"`; the value ends at the LAST
// matching quote of the SAME kind whose tail is only whitespace and an optional
// comment. Scanning from the end keeps the original greedy behaviour (a value may
// contain the OTHER quote kind) while still rejecting a trailing comment's quotes
// and an unbalanced pair.
//
// Unquoted value: a bare run of non-whitespace bytes, ending at the first space
// (or end of line); the remainder must be only whitespace and an optional
// comment. A `#` is a comment only when whitespace-separated from the value.
func extractValueSpan(line string, start int) (vStart, vEnd int, ok bool) {
	rem := line[start:]
	if rem == "" {
		return 0, 0, false
	}
	if q := rem[0]; q == '\'' || q == '"' {
		for j := len(rem) - 1; j >= 1; j-- {
			if rem[j] != q {
				continue
			}
			if trailerRe.MatchString(rem[j+1:]) {
				return start + 1, start + j, true // inside the quotes
			}
		}
		return 0, 0, false // no matching close quote with a clean tail
	}
	end := strings.IndexAny(rem, " \t")
	if end < 0 {
		end = len(rem)
	}
	if !trailerRe.MatchString(rem[end:]) {
		return 0, 0, false // trailing junk after the bare token: refuse
	}
	return start, start + end, true
}
