package secret

import (
	"regexp"
	"strings"
)

// SecretSpan is one line-grained flagged secret region of a text, VALUE-BEARING
// (unlike Finding, which never carries the value). StartLine/EndLine are
// 1-based and inclusive; Value is exactly the span's lines joined by \n.
//
// FlaggedSpans is the generic, domain-free extraction shape capture's
// placeholder-aware blocked path uses (Codex r7-M2): each span's Value is
// stored under a positional ref and ONLY that span is patched to a placeholder.
type SecretSpan struct {
	StartLine int
	EndLine   int
	Value     string
}

// pemEndLine matches a PEM END line, closing a BEGIN..END private-key run.
var pemEndLine = regexp.MustCompile(`(?i)END(?:\s+[A-Z0-9]+)*\s+PRIVATE\s+KEY`)

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
			end = f.Line
			for j := f.Line; j <= len(lines); j++ {
				if j > f.Line && placeholderRe.MatchString(lines[j-1]) {
					break // never widen a span across a placeholder line
				}
				end = j
				if pemEndLine.MatchString(lines[j-1]) {
					break
				}
			}
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
