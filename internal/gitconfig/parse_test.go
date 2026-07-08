package gitconfig

import (
	"strings"
	"testing"
)

// realisticConfigs are byte-for-byte round-trip seeds: comments, blank lines,
// tabs vs spaces, `[section "subsection"]` headers, multivalue keys, dotted
// subsections, [includeIf "gitdir:…"], a trailing-newline-less file, and a
// CRLF line. Each must satisfy Reassemble(Parse(x)) == x exactly.
var realisticConfigs = []string{
	"",
	"\n",
	"\n\n\n",
	"# a lone comment\n",
	"[core]\n\teditor = vim\n",
	"[user]\n\temail = me@example.com\n\tname = Me\n",
	"[core]\n\teditor = vim\n\tautocrlf = input\n\n[alias]\n\tst = status\n\tlg = log --oneline\n",
	"[user]\n    email = spaces@example.com\n    name = Spaces Person\n",
	"[includeIf \"gitdir:~/work/\"]\n\tpath = ~/work/.gitconfig\n",
	"[url \"https://github.com/\"]\n\tinsteadOf = git://github.com/\n\tinsteadOf = ssh://git@github.com/\n",
	"[remote \"origin\"]\n\turl = git@github.com:me/repo.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n",
	"[core.subsection]\n\tfoo = bar\n",
	"; semicolon comment\n[core] ; inline after header\n\tbare\n",
	"[http]\n\textraHeader = Authorization: Bearer sometoken\n",
	"no leading section is junk\n[core]\n\teditor = nano\n",
	"[core]\n\teditor = vim", // no trailing newline
	"[core]\r\n\teditor = vim\r\n",
	"[core]\n\teditor = vim\n\n\n", // trailing blank run
	"[include]\n\tpath = ~/.gitconfig.local\n",
	// Inline `[section] key = value` and no-space forms.
	"[user] email = inline@example.com\n",
	"[user]email=nospace@example.com\n",
	"[core] editor = vim\n\tpager = less\n",
	"[core] ; trailing comment\n",
	"[url \"https://x]y/\"] insteadOf = git://x/\n", // ']' inside a quoted subsection
	// Backslash value-continuation (odd run continues, even run does not).
	"[user]\n\temail = foo\\\nbar@example.com\n",
	"[user]\n\temail = \\\nalice@example.com\n",
	"[core]\n\teditor = vi\\\\\n",
}

func TestReassembleParseRoundTrip(t *testing.T) {
	for _, in := range realisticConfigs {
		got := string(Reassemble(Parse([]byte(in))))
		if got != in {
			t.Errorf("round-trip mismatch\n input: %q\noutput: %q", in, got)
		}
	}
}

func TestParseSectionContext(t *testing.T) {
	lines := Parse([]byte("[user]\n\temail = me@x.com\n[core]\n\teditor = vim\n"))
	var got []string
	for _, l := range lines {
		if l.Kind == KeyValue {
			got = append(got, l.FullKey())
		}
	}
	want := []string{"user.email", "core.editor"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("FullKey context = %v, want %v", got, want)
	}
}

func TestParseSubsectionHeader(t *testing.T) {
	lines := Parse([]byte("[url \"https://github.com/\"]\n\tinsteadOf = git://github.com/\n"))
	if lines[0].Kind != Section || lines[0].Section != "url" || lines[0].Subsection != "https://github.com/" {
		t.Fatalf("subsection header parsed as %+v", lines[0])
	}
	if lines[1].FullKey() != "url.insteadof" {
		t.Errorf("insteadOf key = %q, want url.insteadof", lines[1].FullKey())
	}
}

func TestParseIncludeIfContext(t *testing.T) {
	lines := Parse([]byte("[includeIf \"gitdir:~/work/\"]\n\tpath = ~/work/.gitconfig\n"))
	if lines[0].Section != "includeif" {
		t.Errorf("includeIf section = %q, want includeif", lines[0].Section)
	}
}

// FuzzReassembleParseRoundTrip is the STOP-condition proof: for ANY input,
// Reassemble(Parse(x)) == x byte-for-byte. A failure here is the plan's
// "any parser fails Reassemble(Parse(x))==x -> stop" condition.
func FuzzReassembleParseRoundTrip(f *testing.F) {
	for _, seed := range realisticConfigs {
		f.Add([]byte(seed))
	}
	// Extra structural seeds the corpus should explore mutations around.
	f.Add([]byte("[a]\nb=c\n[d \"e\"]\nf = g\n; h\n\n[i.j]\nk\n"))
	f.Add([]byte("[\tweird ]\n = novalue\n[]\n[unclosed\n"))
	f.Fuzz(func(t *testing.T, in []byte) {
		got := Reassemble(Parse(in))
		if string(got) != string(in) {
			t.Fatalf("round-trip mismatch\n input: %q\noutput: %q", in, got)
		}
	})
}
