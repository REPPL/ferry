package gitconfig

import (
	"strings"
	"testing"
)

const identityConfig = `[core]
	editor = vim
[init]
	defaultBranch = main
[alias]
	st = status
[user]
	email = alice@example.com
	name = Alice Example
	signingkey = ABCD1234
[gpg]
	program = gpg2
[credential]
	helper = osxkeychain
[includeIf "gitdir:~/work/"]
	path = ~/work/.gitconfig
`

func TestSharedContentDropsIdentity(t *testing.T) {
	shared := string(SharedContent([]byte(identityConfig)))

	for _, leak := range []string{
		"alice@example.com", "Alice Example", "ABCD1234", "gpg2", "osxkeychain",
		"[user]", "[gpg]", "[credential]", "[includeIf", "gitdir:", "signingkey",
	} {
		if strings.Contains(shared, leak) {
			t.Errorf("IDENTITY LEAK: shared content still contains %q:\n%s", leak, shared)
		}
	}
	// Non-identity content is preserved verbatim.
	for _, keep := range []string{"[core]", "editor = vim", "[init]", "defaultBranch = main", "[alias]", "st = status"} {
		if !strings.Contains(shared, keep) {
			t.Errorf("shared content dropped non-identity %q:\n%s", keep, shared)
		}
	}
}

func TestSharedContentNoOpOnCleanConfig(t *testing.T) {
	clean := "[core]\n\teditor = vim\n[alias]\n\tst = status\n\n[include]\n\tpath = ~/.gitconfig.local\n"
	got := string(SharedContent([]byte(clean)))
	if got != clean {
		t.Errorf("SharedContent must be a byte-for-byte no-op on an identity-free config\n in: %q\nout: %q", clean, got)
	}
}

func TestSharedContentKeepsPlainInclude(t *testing.T) {
	// A plain [include] is NOT identity — only [includeIf …] routes to local.
	in := "[include]\n\tpath = ~/.shared-extra\n[includeIf \"gitdir:~/w/\"]\n\tpath = ~/w/.gitconfig\n"
	shared := string(SharedContent([]byte(in)))
	if !strings.Contains(shared, "[include]") || !strings.Contains(shared, "~/.shared-extra") {
		t.Errorf("plain [include] was wrongly dropped:\n%s", shared)
	}
	if strings.Contains(shared, "includeIf") || strings.Contains(shared, "~/w/.gitconfig") {
		t.Errorf("[includeIf …] leaked into shared:\n%s", shared)
	}
}

func TestSharedContentKeepsCredentialSubsection(t *testing.T) {
	// A [credential "https://host"] subsection is per-host config (shared), NOT
	// the identity-only bare [credential] helper.
	in := "[credential \"https://ghe.example.com\"]\n\tprovider = generic\n[credential]\n\thelper = osxkeychain\n"
	shared := string(SharedContent([]byte(in)))
	if !strings.Contains(shared, "[credential \"https://ghe.example.com\"]") || !strings.Contains(shared, "provider = generic") {
		t.Errorf("credential subsection wrongly dropped:\n%s", shared)
	}
	if strings.Contains(shared, "helper = osxkeychain") {
		t.Errorf("bare credential.helper leaked into shared:\n%s", shared)
	}
}

// TestSharedContentInlineHeaderNoLeak is the ruthless-review FINDING-1
// regression: git parses `[section] key = value` (and `[section]key=value` with
// no spaces) as a header PLUS an assignment, so an identity key written inline
// with its header must STILL be firewalled out of the shared output. The parser
// splits the physical line so the trailing identity key is dropped.
func TestSharedContentInlineHeaderNoLeak(t *testing.T) {
	cases := []string{
		"[user] email = inline@leak.example\n",
		"[user]email=nospace@leak.example\n",
		"[user] name = Inline Name\n",
		"[gpg] program = /leak/gpg\n",
		"[user] email = a@leak.example\n\tname = B Leak\n\tsigningkey = LEAKKEY\n",
	}
	for _, in := range cases {
		shared := string(SharedContent([]byte(in)))
		for _, leak := range []string{"leak.example", "Inline Name", "/leak/gpg", "B Leak", "LEAKKEY"} {
			if strings.Contains(shared, leak) {
				t.Errorf("IDENTITY LEAK via inline header:\n in: %q\nshared: %q (found %q)", in, shared, leak)
			}
		}
	}
	// A NON-identity inline header is preserved byte-for-byte.
	nonID := "[core] editor = vim\n"
	if got := string(SharedContent([]byte(nonID))); got != nonID {
		t.Errorf("non-identity inline header must be a no-op\n in: %q\nout: %q", nonID, got)
	}
}

// TestCredentialHelperStoreInline is the FINDING-1 companion: an inline
// `[credential] helper = store` must be detected (so it is warned and stripped).
func TestCredentialHelperStoreInline(t *testing.T) {
	for _, in := range []string{
		"[credential] helper = store\n",
		"[credential]helper=store\n",
	} {
		if !CredentialHelperStore([]byte(in)) {
			t.Errorf("inline credential.helper=store not detected: %q", in)
		}
		if strings.Contains(string(SharedContent([]byte(in))), "store") {
			t.Errorf("inline credential.helper=store leaked into shared: %q", in)
		}
	}
}

// TestSharedContentBackslashContinuationNoLeak is the FINDING-2 regression: git
// joins a value across physical lines when a line ends in an ODD run of trailing
// backslashes. The continuation fragment must be dropped WITH its owning identity
// key, never survive as a fresh non-identity key.
func TestSharedContentBackslashContinuationNoLeak(t *testing.T) {
	cases := []struct{ name, in string }{
		{"fragment", "[user]\n\temail = foo\\\nbar@leak.example\n"},
		{"whole-on-continuation", "[user]\n\temail = \\\nalice@leak.example\n"},
		{"multi-continuation", "[user]\n\temail = a\\\nb\\\nc@leak.example\n"},
		{"inline-then-continuation", "[user] email = foo\\\nbar@leak.example\n"},
	}
	for _, tc := range cases {
		shared := string(SharedContent([]byte(tc.in)))
		for _, leak := range []string{"leak.example", "bar@", "alice@", "c@leak", "foo", "bar"} {
			if strings.Contains(shared, leak) {
				t.Errorf("%s: IDENTITY LEAK via backslash continuation:\n in: %q\nshared: %q (found %q)", tc.name, tc.in, shared, leak)
			}
		}
	}
	// An EVEN backslash run is NOT a continuation: the next line is a fresh key and
	// stands on its own (here a non-identity key, kept).
	even := "[user]\n\temail = a@x.com\n[core]\n\teditor = vi\\\\\n"
	if got := string(SharedContent([]byte(even))); !strings.Contains(got, "editor = vi\\\\") {
		t.Errorf("even backslash run wrongly treated as continuation:\n%s", got)
	}
}

// TestSharedContentMalformedHeaderNoLeak is the security-review F1 regression:
// an identity key under an UNCLOSED section header (`[user` with no `]`, which
// git itself rejects) must STILL be firewalled out of the shared output — it is
// classified leniently as the `user` section and dropped, never mis-attributed
// to a previous section and carried through.
func TestSharedContentMalformedHeaderNoLeak(t *testing.T) {
	cases := []string{
		"[user\n\temail = secret@corp.com\n\tname = Real Name\n",
		"[alias]\n\tco = checkout\n[user\n\temail = secret@corp.com\n",
		"[USER\n\tEMAIL = secret@corp.com\n",
	}
	for _, in := range cases {
		shared := string(SharedContent([]byte(in)))
		if strings.Contains(shared, "secret@corp.com") || strings.Contains(shared, "Real Name") {
			t.Errorf("IDENTITY LEAK via malformed header:\n in: %q\nshared: %q", in, shared)
		}
	}
}

// TestSharedContentMalformedHeaderRoundTripUnaffected confirms the lenient header
// parse never breaks the byte-faithful reassembly.
func TestSharedContentMalformedHeaderRoundTripUnaffected(t *testing.T) {
	for _, in := range []string{"[user\n\temail = x\n", "[]\nfoo = bar\n", "[unclosed"} {
		if got := string(Reassemble(Parse([]byte(in)))); got != in {
			t.Errorf("round-trip broke on malformed header\n in: %q\nout: %q", in, got)
		}
	}
}

// TestSharedContentDropsCredentialUsername proves credential.username (account
// identity) is forced local — dropped from shared for a bare [credential], a
// per-host [credential "https://host"] subsection (FullKey collapses it), and the
// inline `[credential] username = x` form, on canonical and inline spellings.
func TestSharedContentDropsCredentialUsername(t *testing.T) {
	cases := []string{
		"[credential]\n\tusername = alex\n",
		"[credential \"https://github.example.com\"]\n\tusername = alex\n",
		"[credential] username = alex\n",
		"[credential \"https://ghe.example.com\"] username = alex\n",
	}
	for _, in := range cases {
		shared := string(SharedContent([]byte(in)))
		if strings.Contains(shared, "alex") || strings.Contains(shared, "username") {
			t.Errorf("IDENTITY LEAK: credential.username reached shared\n in: %q\nshared: %q", in, shared)
		}
	}
	// A non-username per-host credential key is still shared (only username/helper
	// are identity under credential).
	keep := "[credential \"https://ghe.example.com\"]\n\tprovider = generic\n"
	if got := string(SharedContent([]byte(keep))); !strings.Contains(got, "provider = generic") {
		t.Errorf("non-identity per-host credential key wrongly dropped:\n%s", got)
	}
}

func TestCredentialHelperStore(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"store", "[credential]\n\thelper = store\n", true},
		{"quoted-store", "[credential]\n\thelper = \"store\"\n", true},
		{"osxkeychain", "[credential]\n\thelper = osxkeychain\n", false},
		{"store-with-args", "[credential]\n\thelper = store --file ~/x\n", true},
		{"other-helper", "[credential]\n\thelper = manager-core\n", false},
		{"no-helper", "[core]\n\teditor = vim\n", false},
	}
	for _, tc := range cases {
		if got := CredentialHelperStore([]byte(tc.in)); got != tc.want {
			t.Errorf("%s: CredentialHelperStore = %v, want %v", tc.name, got, tc.want)
		}
	}
}
