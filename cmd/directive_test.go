package cmd

import (
	"strings"
	"testing"
)

// TestDirectiveSpecSelection pins the per-bare-name directive syntax selection:
// tmux gets the tmux `source-file` directive; the zsh family (and any other
// include-style name) gets the shell `source` directive.
func TestDirectiveSpecSelection(t *testing.T) {
	if got := directiveSpecFor("tmux.conf").render(".tmux.conf.local"); got != "source-file -q ~/.tmux.conf.local" {
		t.Errorf("tmux render = %q", got)
	}
	for _, bare := range []string{"zshrc", "zshenv", "zprofile"} {
		want := "[ -f ~/." + bare + ".local ] && source ~/." + bare + ".local"
		if got := directiveSpecFor(bare).render("." + bare + ".local"); got != want {
			t.Errorf("%s render = %q, want %q", bare, got, want)
		}
	}
	if got := directiveSpecFor("gitconfig").render(".gitconfig.local"); got != "[include]\n\tpath = ~/.gitconfig.local" {
		t.Errorf("git render = %q", got)
	}
}

// TestGitDirectiveInject pins git's injected TWO-line block: the shared marker
// comment plus the `[include]` / `path = ~/.gitconfig.local` block, appended last
// so git last-wins-merges the machine-local file. strip must remove the whole
// block (header included) with no orphan `[include]`.
func TestGitDirectiveInject(t *testing.T) {
	raw := []byte("[core]\n\teditor = vim\n")
	got := appendSourceDirective(raw, ".gitconfig.local", gitDirective)
	want := "[core]\n\teditor = vim\n\n# ferry: per-machine overlay, sourced last so it wins\n[include]\n\tpath = ~/.gitconfig.local\n"
	if string(got) != want {
		t.Errorf("git injected block:\n got %q\nwant %q", got, want)
	}
	// Idempotent: a second append is a no-op.
	if again := appendSourceDirective(got, ".gitconfig.local", gitDirective); string(again) != string(got) {
		t.Errorf("append not idempotent for git:\n%q", again)
	}
	// strip removes marker + [include] header + path line, leaving no orphan.
	stripped := stripSourceDirective(got, ".gitconfig.local", gitDirective)
	if string(stripped) != string(raw) {
		t.Errorf("git strip did not restore the shared body:\n got %q\nwant %q", stripped, raw)
	}
	if strings.Contains(string(stripped), "[include]") {
		t.Errorf("orphan [include] header survived the strip:\n%q", stripped)
	}
}

// TestZshDirectiveByteIdentical is the load-bearing guard that the directiveSpec
// generalisation did NOT change a single byte of the zsh include block ferry has
// always injected — the marker comment and the guarded `[ -f … ] && source …`
// line, in the exact shape the overlay-bypass eval and the byte-stable migration
// eval depend on.
func TestZshDirectiveByteIdentical(t *testing.T) {
	raw := []byte("# managed zshrc\nexport EDITOR=vim\n")
	got := appendSourceDirective(raw, ".zshrc.local", directiveSpecFor("zshrc"))
	want := "# managed zshrc\nexport EDITOR=vim\n\n# ferry: per-machine overlay, sourced last so it wins\n[ -f ~/.zshrc.local ] && source ~/.zshrc.local\n"
	if string(got) != want {
		t.Errorf("zsh injected block changed:\n got %q\nwant %q", got, want)
	}
}

// TestTmuxDirectiveInject pins tmux's injected block: the shared marker comment
// plus the `source-file -q ~/.tmux.conf.local` include line, sourced last.
func TestTmuxDirectiveInject(t *testing.T) {
	raw := []byte("set -g mouse on\n")
	got := appendSourceDirective(raw, ".tmux.conf.local", tmuxDirective)
	want := "set -g mouse on\n\n# ferry: per-machine overlay, sourced last so it wins\nsource-file -q ~/.tmux.conf.local\n"
	if string(got) != want {
		t.Errorf("tmux injected block:\n got %q\nwant %q", got, want)
	}
	// Idempotent: a second append is a no-op (the directive is already present).
	if again := appendSourceDirective(got, ".tmux.conf.local", tmuxDirective); string(again) != string(got) {
		t.Errorf("append not idempotent for tmux:\n%q", again)
	}
}

// TestDirectiveRoundTrip is the Reassemble(Parse(x)) == x property for BOTH
// include formats: strip(append(x)) reproduces the shared body byte-for-byte for
// any newline-terminated source that does not itself carry ferry's directive.
func TestDirectiveRoundTrip(t *testing.T) {
	cases := []struct {
		spec directiveSpec
		file string
		body string
	}{
		{tmuxDirective, ".tmux.conf.local", "set -g mouse on\nset -g @plugin 'x'\n"},
		{tmuxDirective, ".tmux.conf.local", "\n"},
		{tmuxDirective, ".tmux.conf.local", "# only a comment\n"},
		{shellDirective, ".zshrc.local", "export EDITOR=vim\n"},
		{shellDirective, ".zshrc.local", "# managed\nalias gs='git status'\n"},
		{gitDirective, ".gitconfig.local", "[core]\n\teditor = vim\n"},
		{gitDirective, ".gitconfig.local", "[alias]\n\tst = status\n\tlg = log\n"},
		{gitDirective, ".gitconfig.local", "# only a comment\n"},
	}
	for _, tc := range cases {
		app := appendSourceDirective([]byte(tc.body), tc.file, tc.spec)
		got := stripSourceDirective(app, tc.file, tc.spec)
		if string(got) != tc.body {
			t.Errorf("round-trip: strip(append(%q)) = %q, want %q", tc.body, got, tc.body)
		}
	}
}

// FuzzDirectiveRoundTrip proves strip(append(x)) == x cannot corrupt or lose a
// newline-terminated shared body that carries neither ferry's marker nor an
// include directive — the same property the parser STOP condition guards.
func FuzzDirectiveRoundTrip(f *testing.F) {
	f.Add("set -g mouse on\n")
	f.Add("# c\nset -g @x 'v'\n")
	f.Add("export EDITOR=vim\n")
	f.Fuzz(func(t *testing.T, body string) {
		// Constrain to the shape apply passes: a newline-terminated body with no
		// ferry marker and no existing include directive (else append is a no-op).
		if strings.Contains(body, "ferry:") || strings.Contains(body, "source-file") ||
			strings.Contains(body, "source ") || strings.Contains(body, ". ") {
			t.Skip()
		}
		if body == "" || !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		// appendSourceDirective prefixes its block with one blank line and
		// stripSourceDirective collapses the trailing blank RUN back to a single
		// newline — so the identity holds only for a body that does not itself end
		// in a blank line (a trailing "\n\n" would be collapsed on the way back).
		if strings.HasSuffix(body, "\n\n") {
			t.Skip()
		}
		for _, spec := range []struct {
			d    directiveSpec
			file string
		}{{tmuxDirective, ".tmux.conf.local"}, {shellDirective, ".zshrc.local"}} {
			app := appendSourceDirective([]byte(body), spec.file, spec.d)
			got := stripSourceDirective(app, spec.file, spec.d)
			if string(got) != body {
				t.Fatalf("round-trip: got %q want %q", got, body)
			}
		}
	})
}
