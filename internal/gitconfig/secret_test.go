package gitconfig

import "testing"

// ghToken is a GitHub-token-shaped High-confidence secret (named-token rule),
// with no placeholder words so IsNonPlaceholderSecret accepts it.
const ghToken = "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"

func TestSecretSpans_URLTokenBare(t *testing.T) {
	line := "\tinsteadOf = https://" + ghToken + "@github.com/"
	text := "[url \"x\"]\n" + line + "\n"
	spans := SecretSpans(text)
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d: %+v", len(spans), spans)
	}
	sp := spans[0]
	if sp.Value != ghToken {
		t.Errorf("span value = %q, want the bare token %q", sp.Value, ghToken)
	}
	// The column range must isolate ONLY the token: line[StartCol:EndCol] == token,
	// and the scheme+host survive.
	if sp.StartLine != 2 || !sp.HasColumns() {
		t.Fatalf("span not column-grained on line 2: %+v", sp)
	}
	if line[sp.StartCol:sp.EndCol] != ghToken {
		t.Errorf("column range does not isolate the token: got %q", line[sp.StartCol:sp.EndCol])
	}
}

func TestSecretSpans_URLTokenAfterUser(t *testing.T) {
	line := "\tinsteadOf = https://oauth2:" + ghToken + "@github.com/"
	spans := SecretSpans("[url \"x\"]\n" + line + "\n")
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d: %+v", len(spans), spans)
	}
	if spans[0].Value != ghToken {
		t.Errorf("span value = %q, want %q (only the password, not oauth2:)", spans[0].Value, ghToken)
	}
	if line[spans[0].StartCol:spans[0].EndCol] != ghToken {
		t.Errorf("column range wrong: got %q", line[spans[0].StartCol:spans[0].EndCol])
	}
}

func TestSecretSpans_ExtraHeaderBearer(t *testing.T) {
	line := "\textraHeader = Authorization: Bearer " + ghToken
	spans := SecretSpans("[http]\n" + line + "\n")
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d: %+v", len(spans), spans)
	}
	if line[spans[0].StartCol:spans[0].EndCol] != ghToken {
		t.Errorf("Bearer token not isolated: got %q", line[spans[0].StartCol:spans[0].EndCol])
	}
}

func TestSecretSpans_ExtraHeaderBasic(t *testing.T) {
	// A base64-shaped Basic credential of sufficient length trips the entropy /
	// hex heuristics; use a long token to guarantee High confidence.
	basic := "dXNlcjpnaHBfQTFiMkMzZDRFNWY2RzdoOEk5ajBLMWwyTTNuNE81cDZRN3I4"
	line := "\textraHeader = Authorization: Basic " + basic
	spans := SecretSpans("[http]\n" + line + "\n")
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d: %+v", len(spans), spans)
	}
	if line[spans[0].StartCol:spans[0].EndCol] != basic {
		t.Errorf("Basic token not isolated: got %q", line[spans[0].StartCol:spans[0].EndCol])
	}
}

func TestSecretSpans_ExtraHeaderQuotedNoQuoteCapture(t *testing.T) {
	// A QUOTED extraHeader value: the token span must stop BEFORE the closing
	// quote (security-review F3), so the quote is preserved outside the span.
	line := "\textraHeader = \"Authorization: Bearer " + ghToken + "\""
	spans := SecretSpans("[http]\n" + line + "\n")
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d: %+v", len(spans), spans)
	}
	if got := line[spans[0].StartCol:spans[0].EndCol]; got != ghToken {
		t.Errorf("token span captured a trailing quote: got %q, want %q", got, ghToken)
	}
}

func TestSecretSpans_EnvRefLeftVerbatim(t *testing.T) {
	// ${GIT_TOKEN} is an env-ref, not a literal secret — never flagged.
	for _, line := range []string{
		"\tinsteadOf = https://${GIT_TOKEN}@github.com/",
		"\textraHeader = Authorization: Bearer ${GIT_TOKEN}",
	} {
		if spans := SecretSpans("[x]\n" + line + "\n"); len(spans) != 0 {
			t.Errorf("env-ref wrongly flagged as a secret in %q: %+v", line, spans)
		}
	}
}

func TestSecretSpans_PlaceholderLeftVerbatim(t *testing.T) {
	line := `	insteadOf = https://{{ferry.secret "git.tok"}}@github.com/`
	if spans := SecretSpans("[x]\n" + line + "\n"); len(spans) != 0 {
		t.Errorf("ferry placeholder wrongly flagged: %+v", spans)
	}
}

func TestSecretSpans_NoCredentialNoSpan(t *testing.T) {
	// An ordinary URL with no embedded credential and no Bearer token: nothing.
	text := "[remote \"origin\"]\n\turl = https://github.com/me/repo.git\n[core]\n\teditor = vim\n"
	if spans := SecretSpans(text); len(spans) != 0 {
		t.Errorf("plain config flagged a secret: %+v", spans)
	}
}
