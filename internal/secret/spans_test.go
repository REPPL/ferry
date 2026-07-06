package secret

import (
	"strings"
	"testing"
)

// TestWidenPEMSpan covers the ONE shared PEM widener: a complete BEGIN..END run
// widens to its END line, an unterminated run widens to end-of-text, and a
// placeholder line STOPS the widening before it.
func TestWidenPEMSpan(t *testing.T) {
	complete := strings.Split(
		"-----BEGIN OPENSSH PRIVATE KEY-----\n"+
			"bodyLine\n"+
			"-----END OPENSSH PRIVATE KEY-----\n"+
			"export AFTER=ok", "\n")
	if got := WidenPEMSpan(complete, 1); got != 3 {
		t.Errorf("complete run: end = %d, want 3 (the END line)", got)
	}

	unterminated := strings.Split(
		"-----BEGIN RSA PRIVATE KEY-----\n"+
			"bodyA\n"+
			"bodyB", "\n")
	if got := WidenPEMSpan(unterminated, 1); got != 3 {
		t.Errorf("unterminated run: end = %d, want 3 (end of text)", got)
	}

	withPlaceholder := strings.Split(
		"-----BEGIN OPENSSH PRIVATE KEY-----\n"+
			"bodyLine\n"+
			`{{ferry.secret "wg.k"}}`+"\n"+
			"-----END OPENSSH PRIVATE KEY-----", "\n")
	if got := WidenPEMSpan(withPlaceholder, 1); got != 2 {
		t.Errorf("placeholder run: end = %d, want 2 (stops before the placeholder)", got)
	}
}

// TestFlaggedSpans_PEMWidenAndPlaceholderStop checks FlaggedSpans consumes the
// shared widener: a PEM span widens to its END line, and a placeholder inside the
// run is never captured into the span Value.
func TestFlaggedSpans_PEMWidenAndPlaceholderStop(t *testing.T) {
	text := "# key\n" +
		"-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"bodyLine\n" +
		"-----END OPENSSH PRIVATE KEY-----\n"
	spans := FlaggedSpans(text)
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d: %+v", len(spans), spans)
	}
	if spans[0].StartLine != 2 || spans[0].EndLine != 4 {
		t.Errorf("span = [%d,%d], want [2,4]", spans[0].StartLine, spans[0].EndLine)
	}

	withPH := "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"bodyLine\n" +
		`{{ferry.secret "wg.k"}}` + "\n" +
		"-----END OPENSSH PRIVATE KEY-----\n"
	spans = FlaggedSpans(withPH)
	if len(spans) == 0 {
		t.Fatal("expected a span")
	}
	if strings.Contains(spans[0].Value, "{{ferry.secret") {
		t.Errorf("span Value swallowed a placeholder:\n%q", spans[0].Value)
	}
}
