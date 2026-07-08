package secret

import (
	"strings"
	"testing"
)

// tokenSwap finds tok inside line, swaps ONLY that byte range for the placeholder,
// and returns the result plus the byte offsets it used. It fails the test if tok is
// absent or ambiguous — the realistic fixtures each contain the token exactly once.
func tokenSwap(t *testing.T, line, tok, ref string) (out string, start, end int) {
	t.Helper()
	start = strings.Index(line, tok)
	if start < 0 {
		t.Fatalf("token %q not found in line %q", tok, line)
	}
	if strings.Index(line[start+1:], tok) >= 0 {
		t.Fatalf("token %q appears more than once in line %q", tok, line)
	}
	end = start + len(tok)
	got, err := SwapColumns(line, start, end, Placeholder(ref))
	if err != nil {
		t.Fatalf("SwapColumns(%q, %d, %d): %v", line, start, end, err)
	}
	return got, start, end
}

// assertSurroundingPreserved checks that ONLY the [start,end) range of line changed:
// the prefix and suffix bytes are byte-identical, and out is exactly
// prefix + placeholder + suffix. It also proves the swap is invertible: swapping the
// placeholder's range back to the original substring reproduces line byte-for-byte.
func assertSurroundingPreserved(t *testing.T, line, out string, start, end int, ref string) {
	t.Helper()
	ph := Placeholder(ref)
	want := line[:start] + ph + line[end:]
	if out != want {
		t.Fatalf("swap result mismatch\n got: %q\nwant: %q", out, want)
	}
	if out[:start] != line[:start] {
		t.Errorf("prefix changed: got %q want %q", out[:start], line[:start])
	}
	if out[start+len(ph):] != line[end:] {
		t.Errorf("suffix changed: got %q want %q", out[start+len(ph):], line[end:])
	}
	// Inverse: swap the placeholder's byte range back to the original substring.
	back, err := SwapColumns(out, start, start+len(ph), line[start:end])
	if err != nil {
		t.Fatalf("inverse SwapColumns: %v", err)
	}
	if back != line {
		t.Fatalf("round-trip did not restore original\n got: %q\nwant: %q", back, line)
	}
}

// TestSwapColumns_GitInsteadOfURL: a git url.<base>.insteadOf section header carries
// the token INSIDE the URL (between "https://" and "@github.com/"). Only the token
// must change; the scheme, host, path and the surrounding [url "..."] syntax stay.
func TestSwapColumns_GitInsteadOfURL(t *testing.T) {
	tok := "ghp_16C7e42F292c6912E7710c838347Ae178B4a01"
	line := `[url "https://` + tok + `@github.com/"]`
	out, start, end := tokenSwap(t, line, tok, "git.url_token")
	assertSurroundingPreserved(t, line, out, start, end, "git.url_token")
	if !strings.HasPrefix(out, `[url "https://`) || !strings.HasSuffix(out, `@github.com/"]`) {
		t.Errorf("URL scaffolding not preserved: %q", out)
	}
}

// TestSwapColumns_HTTPExtraHeaderBearer: an http.extraHeader value carries the token
// after "Authorization: Bearer ". Only the token after "Bearer " must change.
func TestSwapColumns_HTTPExtraHeaderBearer(t *testing.T) {
	tok := "ghp_16C7e42F292c6912E7710c838347Ae178B4a01"
	line := "\textraHeader = Authorization: Bearer " + tok
	out, start, end := tokenSwap(t, line, tok, "git.extra_header")
	assertSurroundingPreserved(t, line, out, start, end, "git.extra_header")
	if !strings.Contains(out, "Authorization: Bearer ") {
		t.Errorf(`"Bearer " prefix not preserved: %q`, out)
	}
	if !strings.HasPrefix(out, "\textraHeader = ") {
		t.Errorf("leading tab / key not preserved: %q", out)
	}
}

// TestSwapColumns_TmuxQuotedOption: a tmux `set -g @token 'SECRET'` carries the value
// BETWEEN single quotes. Only the value inside the quotes must change; the quotes and
// the `set -g @token ` prefix stay.
func TestSwapColumns_TmuxQuotedOption(t *testing.T) {
	tok := "sk-AbC123secretVALUE456789xyz"
	line := "set -g @token '" + tok + "'"
	out, start, end := tokenSwap(t, line, tok, "tmux.token")
	assertSurroundingPreserved(t, line, out, start, end, "tmux.token")
	if !strings.HasPrefix(out, "set -g @token '") || !strings.HasSuffix(out, "'") {
		t.Errorf("quotes/prefix not preserved: %q", out)
	}
}

// TestSwapColumns_UTF8Surroundings: multibyte runes on BOTH sides of the swapped
// range must be preserved byte-for-byte. Offsets are byte offsets, so slicing lands
// between whole runes here and the é / → / ✓ bytes survive intact.
func TestSwapColumns_UTF8Surroundings(t *testing.T) {
	tok := "sk-XYZ123opaqueTOKENvalue999"
	line := "# café → set @tok '" + tok + "' ✓ done"
	out, start, end := tokenSwap(t, line, tok, "tmux.token")
	assertSurroundingPreserved(t, line, out, start, end, "tmux.token")
	for _, want := range []string{"café", "→", "✓ done"} {
		if !strings.Contains(out, want) {
			t.Errorf("multibyte context %q lost: %q", want, out)
		}
	}
}

// TestSwapColumns_OutOfRange: bad offsets return an error and never panic.
func TestSwapColumns_OutOfRange(t *testing.T) {
	const line = "set -g @token 'x'"
	cases := []struct {
		name       string
		start, end int
	}{
		{"negative start", -1, 3},
		{"end before start", 5, 2},
		{"end past len", 0, len(line) + 1},
		{"start past len", len(line) + 1, len(line) + 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := SwapColumns(line, c.start, c.end, "REPL")
			if err == nil {
				t.Fatalf("want error for [%d,%d), got out=%q", c.start, c.end, out)
			}
			if out != "" {
				t.Errorf("error path should return empty string, got %q", out)
			}
		})
	}
}

// TestSwapColumns_EmptyAndFullRange: the boundary ranges are valid, not errors.
func TestSwapColumns_EmptyAndFullRange(t *testing.T) {
	const line = "abcdef"
	// Empty range at start: pure insertion.
	if out, err := SwapColumns(line, 0, 0, "XY"); err != nil || out != "XYabcdef" {
		t.Fatalf("insert-at-start: out=%q err=%v", out, err)
	}
	// Empty range at end: pure append.
	if out, err := SwapColumns(line, len(line), len(line), "XY"); err != nil || out != "abcdefXY" {
		t.Fatalf("insert-at-end: out=%q err=%v", out, err)
	}
	// Whole-line range: replace everything.
	if out, err := SwapColumns(line, 0, len(line), "XY"); err != nil || out != "XY" {
		t.Fatalf("replace-all: out=%q err=%v", out, err)
	}
}

// TestSecretSpan_HasColumns: the zero-value column fields are the whole-line sentinel
// (HasColumns false); a real interior range reports true. This is the backward-compat
// contract in miniature.
func TestSecretSpan_HasColumns(t *testing.T) {
	whole := SecretSpan{StartLine: 2, EndLine: 4, Value: "x"}
	if whole.HasColumns() {
		t.Error("whole-line span (zero cols) must report HasColumns()==false")
	}
	interior := SecretSpan{StartLine: 1, EndLine: 1, Value: "tok", StartCol: 8, EndCol: 40}
	if !interior.HasColumns() {
		t.Error("interior span must report HasColumns()==true")
	}
	// An empty interior range (StartCol==EndCol) is NOT a column span.
	if (SecretSpan{StartCol: 5, EndCol: 5}).HasColumns() {
		t.Error("empty range must report HasColumns()==false")
	}
}

// TestFlaggedSpans_BackwardCompatNoColumns: every span the existing line-grained
// extractor produces leaves the column fields at zero (HasColumns false), so no
// existing consumer sees interior offsets. This locks the backward-compat guarantee.
func TestFlaggedSpans_BackwardCompatNoColumns(t *testing.T) {
	text := "export DB_PASSWORD=supersecretvalue123\n" +
		"-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"bodyLine\n" +
		"-----END OPENSSH PRIVATE KEY-----\n"
	spans := FlaggedSpans(text)
	if len(spans) == 0 {
		t.Fatal("expected flagged spans")
	}
	for i, s := range spans {
		if s.HasColumns() || s.StartCol != 0 || s.EndCol != 0 {
			t.Errorf("span %d carried unexpected columns: %+v", i, s)
		}
	}
}

// TestIsNonPlaceholderSecret_Exported proves the exported gate keeps the ${...}
// exemption: an env-ref sub-value is NOT a secret, a literal token IS. This is the
// predicate a sub-value recognizer applies to the substring it extracted.
func TestIsNonPlaceholderSecret_Exported(t *testing.T) {
	notSecret := []string{"", "${NPM_TOKEN}", "$TOKEN", `{{ferry.secret "x.y"}}`, "changeme", "your_token"}
	for _, v := range notSecret {
		if IsNonPlaceholderSecret(v) {
			t.Errorf("IsNonPlaceholderSecret(%q) = true, want false (exempt)", v)
		}
	}
	secrets := []string{"ghp_16C7e42F292c6912E7710c838347Ae178B4a01", "sk-liveRealTokenValue123"}
	for _, v := range secrets {
		if !IsNonPlaceholderSecret(v) {
			t.Errorf("IsNonPlaceholderSecret(%q) = false, want true (literal secret)", v)
		}
	}
}

// normOffset maps an arbitrary int into a valid byte offset [0,n] for a line of n
// bytes, so the fuzz body always exercises SwapColumns' success path (out-of-range is
// covered separately). It handles negatives and MinInt without overflow.
func normOffset(v, n int) int {
	m := n + 1
	v %= m
	if v < 0 {
		v += m
	}
	return v
}

// FuzzSwapColumns: for random lines, random VALID [start,end) ranges and random
// replacements, the swap (a) never panics, (b) preserves the prefix and suffix bytes
// exactly, and (c) is invertible back to the original line. Seeded with the three
// realistic fixtures (git URL, http.extraHeader Bearer, tmux quoted option).
func FuzzSwapColumns(f *testing.F) {
	f.Add(`[url "https://ghp_TOKEN@github.com/"]`, 14, 23, `{{ferry.secret "git.url_token"}}`)
	f.Add("\textraHeader = Authorization: Bearer ghp_TOKEN", 37, 46, `{{ferry.secret "git.extra_header"}}`)
	f.Add("set -g @token 'SECRETvalue'", 15, 26, `{{ferry.secret "tmux.token"}}`)
	f.Add("# café → 'sk-XYZ' ✓", 11, 17, "R")

	f.Fuzz(func(t *testing.T, line string, a, b int, repl string) {
		n := len(line)
		start := normOffset(a, n)
		end := normOffset(b, n)
		if start > end {
			start, end = end, start
		}

		out, err := SwapColumns(line, start, end, repl)
		if err != nil {
			t.Fatalf("valid range [%d,%d) on %d-byte line errored: %v", start, end, n, err)
		}

		// (b) prefix and suffix bytes byte-preserved.
		if out[:start] != line[:start] {
			t.Fatalf("prefix mutated: got %q want %q", out[:start], line[:start])
		}
		suffixStart := start + len(repl)
		if out[suffixStart:] != line[end:] {
			t.Fatalf("suffix mutated: got %q want %q", out[suffixStart:], line[end:])
		}
		if out != line[:start]+repl+line[end:] {
			t.Fatalf("unexpected splice: got %q", out)
		}

		// (c) invertible back to the original line.
		back, err := SwapColumns(out, start, suffixStart, line[start:end])
		if err != nil {
			t.Fatalf("inverse errored: %v", err)
		}
		if back != line {
			t.Fatalf("inverse did not restore original\n got: %q\nwant: %q", back, line)
		}
	})
}
