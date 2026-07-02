package zsh

import (
	"bytes"
	"testing"

	"github.com/REPPL/ferry/internal/plugin"
)

// AC-zsh-parse-lossless: parse -> Reassemble is BYTE-IDENTICAL over
// representative fixtures, including no-trailing-newline, CRLF, and non-UTF-8
// byte shapes.
func TestParseLossless(t *testing.T) {
	p := New()
	fixtures := map[string]string{
		"multi-section":       "# path\nexport PATH=\"$HOME/bin:$PATH\"\n\n# aliases\nalias gs='git status'\nalias ll='ls -la'\n\neval \"$(/opt/homebrew/bin/brew shellenv)\"\n",
		"no-trailing-newline": "# header\nexport EDITOR=vim\n\nalias gs='git status'",
		"crlf":                "# header\r\nexport EDITOR=vim\r\n\r\nalias gs='git status'\r\n",
		"leading-blanks":      "\n\n# first\nalias a='b'\n\n\nalias c='d'\n",
		"blank-only":          "\n\n\n",
		"empty":               "",
		"non-utf8":            "# bin\xff\xfe\nexport A=b\n\nalias x='\x80'\n",
		"single-line":         "export PATH=$PATH",
		"comments-only":       "# one\n# two\n",
		"trailing-blanks":     "alias a='b'\n\n\n",
	}
	for name, fx := range fixtures {
		blocks, err := p.Parse([]byte(fx))
		if err != nil {
			t.Errorf("%s: Parse error: %v", name, err)
			continue
		}
		if got := plugin.Reassemble(blocks); !bytes.Equal(got, []byte(fx)) {
			t.Errorf("%s: Reassemble(Parse(x)) != x\ngot:  %q\nwant: %q", name, got, fx)
		}
	}
}

// Blocks are blank-line-separated paragraphs, 0-based in file order, and each
// Raw owns its trailing separator (the pinned block-indexing contract).
func TestParseBlockBoundaries(t *testing.T) {
	p := New()
	const fx = "# path\nexport PATH=\"$HOME/bin:$PATH\"\n\n# alias\nalias gs='git status'\n\neval \"$(/opt/homebrew/bin/brew shellenv)\"\n"
	blocks, err := p.Parse([]byte(fx))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(blocks))
	}
	wants := []string{
		"# path\nexport PATH=\"$HOME/bin:$PATH\"\n\n",
		"# alias\nalias gs='git status'\n\n",
		"eval \"$(/opt/homebrew/bin/brew shellenv)\"\n",
	}
	for i, w := range wants {
		if string(blocks[i].Raw) != w {
			t.Errorf("block %d Raw = %q, want %q", i, blocks[i].Raw, w)
		}
	}
	if blocks[0].Start != 1 || blocks[1].Start != 4 || blocks[2].Start != 7 {
		t.Errorf("block Start lines = %d,%d,%d; want 1,4,7", blocks[0].Start, blocks[1].Start, blocks[2].Start)
	}
}

// AC-zsh-classify: representative lines classify into the right BlockKind.
func TestClassify(t *testing.T) {
	cases := []struct {
		raw  string
		want plugin.BlockKind
	}{
		{"export PATH=\"$HOME/bin:$PATH\"\n", plugin.PathExport},
		{"# path setup\nexport PATH=/usr/local/bin:$PATH\n", plugin.PathExport},
		{"alias gs='git status'\nalias ll='ls -la'\n", plugin.Alias},
		{"greet() {\n  echo hi\n}\n", plugin.Function},
		{"function greet {\n  echo hi\n}\n", plugin.Function},
		{"source $ZSH/oh-my-zsh.sh\n", plugin.PluginInit},
		{"zinit light zsh-users/zsh-autosuggestions\n", plugin.PluginInit},
		{"PROMPT='%1~ %# '\n", plugin.Prompt},
		{"eval \"$(starship init zsh)\"\n", plugin.Prompt},
		{"source ~/.aliases\n", plugin.Source},
		{". ~/.profile\n", plugin.Source},
		{"# just a comment\n# another\n", plugin.Comment},
		{"ulimit -n 4096\n", plugin.Other},
		{"export EDITOR=vim\n", plugin.Other},
	}
	for _, c := range cases {
		if got := classify([]byte(c.raw)); got != c.want {
			t.Errorf("classify(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// FuzzParseReassemble is the native fuzz target pinning the loss-less
// invariant: for ANY byte input, Reassemble(Parse(x)) == x.
func FuzzParseReassemble(f *testing.F) {
	seeds := []string{
		"",
		"\n",
		"a",
		"# c\nexport A=b\n\nalias x='y'\n",
		"no trailing newline",
		"\r\n\r\nalias a='b'\r\n",
		"\xff\xfe\x80 binary-ish\n\n\x00\n",
		"\n\nleading blanks\n\n\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	p := New()
	f.Fuzz(func(t *testing.T, data []byte) {
		blocks, err := p.Parse(data)
		if err != nil {
			t.Fatalf("Parse errored on %q: %v", data, err)
		}
		if got := plugin.Reassemble(blocks); !bytes.Equal(got, data) {
			t.Fatalf("Reassemble(Parse(x)) != x\ngot:  %q\nwant: %q", got, data)
		}
	})
}
