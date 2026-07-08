package secret

import (
	"regexp"
	"strings"
)

// SecretSpan is one flagged secret region of a text, VALUE-BEARING (unlike
// Finding, which never carries the value). StartLine/EndLine are 1-based and
// inclusive; Value is exactly the span's lines joined by \n.
//
// FlaggedSpans is the generic, domain-free extraction shape capture's
// placeholder-aware blocked path uses (Codex r7-M2): each span's Value is
// stored under a positional ref and ONLY that span is patched to a placeholder.
//
// StartCol/EndCol OPTIONALLY narrow the span to a byte range WITHIN a single
// line — a half-open [StartCol, EndCol) byte offset (0-based) into that line —
// for secrets that live inside a substring rather than as a whole line or a
// post-'=' RHS (a token inside a URL, after "Bearer "/"Basic ", or between
// quotes). They are only meaningful when StartLine == EndLine. The zero value
// (both 0, an empty range) is the WHOLE-LINE sentinel: HasColumns reports false
// and every existing line-grained producer/consumer — which leaves these fields
// at their zero value — is byte-for-byte unaffected. A real interior span always
// has EndCol > StartCol, so HasColumns cleanly distinguishes the two. Offsets are
// BYTE offsets, not rune indices (see SwapColumns).
type SecretSpan struct {
	StartLine int
	EndLine   int
	Value     string
	StartCol  int
	EndCol    int
}

// HasColumns reports whether the span carries a byte-offset column range within
// its line (EndCol > StartCol). When false the span is whole-line-grained and the
// column fields MUST be ignored; this is what keeps every existing SecretSpan
// (which leaves StartCol/EndCol at zero) behaving exactly as before.
func (s SecretSpan) HasColumns() bool { return s.EndCol > s.StartCol }

// pemEndLine matches a PEM END line, closing a BEGIN..END private-key run.
var pemEndLine = regexp.MustCompile(`(?i)END(?:\s+[A-Z0-9]+)*\s+PRIVATE\s+KEY`)

// WidenPEMSpan returns the 1-based END line of the PEM private-key span that
// BEGINs at startLine (also 1-based) within lines. It widens across the
// contiguous BEGIN..END run — a missing END widens toward the end of the text
// (conservative: better to over-extract than to leak body lines) — but STOPS
// before a line bearing a ferry placeholder: a stored span Value must NEVER
// contain a {{ferry.secret}} line (storing one would nest it inside a stored
// value, and apply's single non-recursive render pass would leave it literal in
// the deployed file). This is the ONE widener shared by both span extractors
// (FlaggedSpans here and the zsh plugin's secretSpansInBlock) so their PEM
// boundary — including the placeholder stop — can never diverge.
func WidenPEMSpan(lines []string, startLine int) int {
	end := startLine
	for j := startLine; j <= len(lines); j++ {
		if j > startLine && placeholderRe.MatchString(lines[j-1]) {
			break // never widen a span across a placeholder line
		}
		end = j
		if pemEndLine.MatchString(lines[j-1]) {
			break
		}
	}
	return end
}

// FlaggedSpans scans text with the SAME detectors as the gate (ScanText) and
// returns the line-grained secret spans: a single line for a bare token /
// credential assignment / WireGuard key, the contiguous BEGIN..END run for PEM
// material (widened; a missing END widens toward the end of the text —
// conservative: better to over-extract than to leak body lines). Widening
// STOPS before a placeholder-bearing line: a span Value must NEVER contain a
// ferry placeholder (storing one would nest it inside a stored value, and
// apply's single non-recursive render pass would leave it literal in the
// deployed file). Findings on lines already inside a widened span are
// suppressed, so spans never overlap.
func FlaggedSpans(text string) []SecretSpan {
	findings := ScanText(text)
	if !findings.HasHigh() {
		return nil
	}
	lines := strings.Split(text, "\n")
	covered := map[int]bool{}
	var out []SecretSpan
	for _, f := range findings {
		if f.Confidence != High || f.Line < 1 || f.Line > len(lines) || covered[f.Line] {
			continue
		}
		start, end := f.Line, f.Line
		if f.Rule == "pem-private-key" {
			end = WidenPEMSpan(lines, f.Line)
		}
		for j := start; j <= end; j++ {
			covered[j] = true
		}
		out = append(out, SecretSpan{
			StartLine: start,
			EndLine:   end,
			Value:     strings.Join(lines[start-1:end], "\n"),
		})
	}
	return out
}
