package sshguard

import (
	"path/filepath"
	"testing"
)

// TestUnderHomeSSH pins the folding containment primitive: a path is under
// ~/.ssh when its home parents match (case-folded) AND its next component folds
// to ".ssh". The home leaf and the ".ssh" segment are BOTH compared
// case-insensitively so a case-variant that the kernel maps into the real
// ~/.ssh on a case-insensitive filesystem is still caught.
func TestUnderHomeSSH(t *testing.T) {
	home := filepath.FromSlash("/Users/alice")
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"ssh dir itself", "/Users/alice/.ssh", true},
		{"under ssh", "/Users/alice/.ssh/config", true},
		{"folded ssh leaf", "/Users/alice/.SSH/config", true},
		{"folded home parent", "/Users/ALICE/.ssh/config", true},
		{"folded home parent and leaf", "/Users/Alice/.Ssh/id_ed25519", true},
		{"folded upper home root", "/USERS/alice/.ssh/config", true},
		{"home itself", "/Users/alice", false},
		{"sibling of ssh", "/Users/alice/.config/x", false},
		{"prefix-only name", "/Users/alice/.sshx", false},
		{"outside home", "/Users/bob/.ssh/config", false},
		{"distinct home prefix", "/Users/alicez/.ssh/config", false},
	}
	for _, c := range cases {
		got := UnderHomeSSH(home, filepath.FromSlash(c.path))
		if got != c.want {
			t.Errorf("%s: UnderHomeSSH(%q, %q) = %v, want %v", c.name, home, c.path, got, c.want)
		}
	}
}
