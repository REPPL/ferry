package tmux

import "testing"

// TestSecretSpansOffsets pins that the recogniser isolates EXACTLY the quoted
// value's byte range, leaving the `set -g @token '…'` prefix and the quotes
// outside the span — the precondition for a byte-stable secret.SwapColumns swap.
func TestSecretSpansOffsets(t *testing.T) {
	const ghToken = "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8" // GitHub token: a named-token High secret
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
