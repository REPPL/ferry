package tmux

import (
	"testing"

	"github.com/REPPL/ferry/internal/secret"
)

// TestSecretSpansOffsets pins that the recogniser isolates EXACTLY the quoted
// value's byte range, leaving the `set -g @token '…'` prefix and the quotes
// outside the span — the precondition for a byte-stable secret.SwapColumns swap.
func TestSecretSpansOffsets(t *testing.T) {
	line := "set -g @token '" + ghToken + "'"
	spans := SecretSpans(line + "\n")
	if len(spans) != 1 {
		t.Fatalf("SecretSpans found %d spans, want 1: %+v", len(spans), spans)
	}
	sp := spans[0]
	if !sp.HasColumns() {
		t.Fatalf("span is not column-grained: %+v", sp)
	}
	if sp.Value != ghToken {
		t.Errorf("span value = %q, want %q", sp.Value, ghToken)
	}
	// The span must cover ONLY the value, so the byte before StartCol is the
	// opening quote and the byte at EndCol is the closing quote.
	if line[sp.StartCol-1] != '\'' || line[sp.EndCol] != '\'' {
		t.Errorf("span [%d,%d) does not sit inside the quotes: %q", sp.StartCol, sp.EndCol, line)
	}
	if line[sp.StartCol:sp.EndCol] != ghToken {
		t.Errorf("span byte range = %q, want %q", line[sp.StartCol:sp.EndCol], ghToken)
	}
}

// TestSecretSpansDoubleQuotesAndSetOption confirms the recogniser accepts the
// `set-option` spelling and double quotes, and preserves the value byte range.
func TestSecretSpansDoubleQuotesAndSetOption(t *testing.T) {
	line := `set-option -g @api "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"`
	spans := SecretSpans(line)
	if len(spans) != 1 {
		t.Fatalf("SecretSpans found %d spans, want 1", len(spans))
	}
	if line[spans[0].StartCol:spans[0].EndCol] != "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8" {
		t.Errorf("value range = %q", line[spans[0].StartCol:spans[0].EndCol])
	}
}

// TestSecretSpansSkipsNonSecrets is the load-bearing negative: an env-ref, a
// ferry placeholder, an empty value, and an ordinary short option string are
// NEVER flagged — they must reach the repo verbatim.
func TestSecretSpansSkipsNonSecrets(t *testing.T) {
	for _, line := range []string{
		`set -g @token '${TMUX_TOKEN}'`,                              // env-ref: expanded at read time
		`set -g @token "$TMUX_TOKEN"`,                                // bare env-ref
		`set -g @token '{{ferry.secret "t.k"}}'`,                     // already a ferry placeholder
		`set -g @token ''`,                                           // empty
		`set -g @plugin 'tmux-plugins/tmux-sensible'`,                // ordinary option string, not a secret
		`set -g status-bg black`,                                     // unquoted, not a @user option
		`# set -g @token 'ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8'`, // recognises the line shape but…
	} {
		if spans := SecretSpans(line); len(spans) != 0 {
			t.Errorf("SecretSpans(%q) flagged %d spans, want 0: %+v", line, len(spans), spans)
		}
	}
}

// TestSecretSpansMismatchedQuotes rejects an unbalanced quote pair (no clean
// value boundary): nothing is flagged rather than a mis-sliced span.
func TestSecretSpansMismatchedQuotes(t *testing.T) {
	if spans := SecretSpans(`set -g @token 'AKIAIOSFODNN7EXAMPLE"`); len(spans) != 0 {
		t.Errorf("mismatched quotes flagged %d spans, want 0", len(spans))
	}
}

const ghToken = "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8" // GitHub token: a named-token High secret

// wantValueSpan asserts SecretSpans found exactly one span whose byte range is
// value and whose surrounding bytes are untouched.
func wantValueSpan(t *testing.T, line, value string) secret.SecretSpan {
	t.Helper()
	spans := SecretSpans(line)
	if len(spans) != 1 {
		t.Fatalf("SecretSpans(%q) found %d spans, want 1: %+v", line, len(spans), spans)
	}
	sp := spans[0]
	if !sp.HasColumns() {
		t.Fatalf("span is not column-grained: %+v", sp)
	}
	if got := line[sp.StartCol:sp.EndCol]; got != value {
		t.Errorf("span byte range = %q, want %q (line %q)", got, value, line)
	}
	if sp.Value != value {
		t.Errorf("span value = %q, want %q", sp.Value, value)
	}
	return sp
}

// TestSecretSpansSetw proves the `setw` command spelling is recognised and the
// span isolates exactly the quoted value.
func TestSecretSpansSetw(t *testing.T) {
	line := "setw -g @token '" + ghToken + "'"
	sp := wantValueSpan(t, line, ghToken)
	if line[sp.StartCol-1] != '\'' || line[sp.EndCol] != '\'' {
		t.Errorf("span [%d,%d) not inside the quotes: %q", sp.StartCol, sp.EndCol, line)
	}
}

// TestSecretSpansSetWindowOption proves the long `set-window-option` spelling.
func TestSecretSpansSetWindowOption(t *testing.T) {
	line := `set-window-option -g @api "` + ghToken + `"`
	sp := wantValueSpan(t, line, ghToken)
	if line[sp.StartCol-1] != '"' || line[sp.EndCol] != '"' {
		t.Errorf("span [%d,%d) not inside the quotes: %q", sp.StartCol, sp.EndCol, line)
	}
}

// TestSecretSpansTrailingComment proves a `# comment` after the quoted value is
// left outside the span — including a comment that itself contains a quote, which
// must not extend the greedy value match past the true closing quote.
func TestSecretSpansTrailingComment(t *testing.T) {
	line := "set -g @token '" + ghToken + "' # it's the API note"
	sp := wantValueSpan(t, line, ghToken)
	// The byte at EndCol is the true closing quote; the comment (with its own
	// apostrophe) stays intact after it.
	if line[sp.EndCol] != '\'' {
		t.Errorf("EndCol %d does not sit on the closing quote: %q", sp.EndCol, line)
	}
	if got := line[sp.EndCol:]; got != "' # it's the API note" {
		t.Errorf("trailing bytes after span = %q, want the closing quote + comment", got)
	}
}

// TestSecretSpansUnquoted proves a bare unquoted value token is isolated, both
// alone and with a trailing comment, and that offsets cover exactly the token.
func TestSecretSpansUnquoted(t *testing.T) {
	line := "set -g @token " + ghToken
	sp := wantValueSpan(t, line, ghToken)
	if sp.StartCol != len("set -g @token ") || sp.EndCol != len(line) {
		t.Errorf("unquoted span [%d,%d), want [%d,%d)", sp.StartCol, sp.EndCol, len("set -g @token "), len(line))
	}

	withComment := "setw -g @token " + ghToken + "   # trailing note"
	sp = wantValueSpan(t, withComment, ghToken)
	if withComment[sp.EndCol] != ' ' {
		t.Errorf("EndCol %d should stop at the whitespace before the comment: %q", sp.EndCol, withComment)
	}
}

// TestSecretSpansEnvRefUnquoted keeps the env-ref exemption for BARE values: an
// unquoted ${ENV} / $ENV is expanded at read time, never a literal secret.
func TestSecretSpansEnvRefUnquoted(t *testing.T) {
	for _, line := range []string{
		`set -g @token ${TMUX_TOKEN}`,
		`setw -g @token $TMUX_TOKEN`,
		`set-window-option -g @token ${TMUX_TOKEN} # ref`,
	} {
		if spans := SecretSpans(line); len(spans) != 0 {
			t.Errorf("SecretSpans(%q) flagged %d spans, want 0: %+v", line, len(spans), spans)
		}
	}
}

// TestSecretSpansRefusesUnisolable is the SAFETY case: shapes the recogniser
// cannot cleanly bound must yield NO span (capture then blocks whole-file via the
// re-gate) rather than a mis-slice or whole-line clobber.
func TestSecretSpansRefusesUnisolable(t *testing.T) {
	for _, line := range []string{
		// A closing quote followed by a second bare token: the tail is neither
		// whitespace nor a comment, so there is no clean value boundary.
		"set -g @token '" + ghToken + "' extra",
		// A flag that consumes its own argument is not modelled: `main` is read as
		// the flag argument, the `@token` prefix never matches.
		"set -t main @token '" + ghToken + "'",
		// Unbalanced quote (open only): no matching close with a clean tail.
		"set -g @token '" + ghToken,
	} {
		if spans := SecretSpans(line); len(spans) != 0 {
			t.Errorf("SecretSpans(%q) flagged %d spans, want 0 (must degrade to refusal): %+v", line, len(spans), spans)
		}
	}
}
