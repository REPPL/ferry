package cmd

import (
	"path/filepath"
	"testing"
)

// TestPathUnderSSHCaseInsensitive pins the ~/.ssh containment guard on repo and
// clone paths as case-INSENSITIVE, matching the dotfile deploy-target guard: on
// the default case-insensitive macOS filesystem a configured repo or clone
// source like ~/.SSH/repo is mapped by the kernel into the real ~/.ssh, so it
// must be refused exactly as ~/.ssh/repo is. Folding also refuses ~/.SSH/... on
// a case-sensitive filesystem, which is acceptable fail-closed behaviour. A
// FAKE $HOME is used; the real ~/.ssh is never touched.
func TestPathUnderSSHCaseInsensitive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	refused := []string{
		filepath.Join(home, ".SSH", "repo"),
		filepath.Join(home, ".Ssh", "x"),
		filepath.Join(home, ".SSH"),
		filepath.Join(home, ".ssh", "config"), // existing lowercase case, unchanged
		"~/.SSH/repo",
	}
	for _, p := range refused {
		under, err := pathUnderSSH(p)
		if err != nil {
			t.Errorf("pathUnderSSH(%q): unexpected error %v", p, err)
			continue
		}
		if !under {
			t.Errorf("pathUnderSSH(%q) = false, want true (refused)", p)
		}
		if err := rejectIfUnderSSH("repo", p); err == nil {
			t.Errorf("rejectIfUnderSSH(%q) = nil, want refusal", p)
		}
	}

	allowed := []string{
		filepath.Join(home, "projects", "repo"),
		filepath.Join(home, ".sshx"), // a distinct dir whose name merely starts with .ssh
		filepath.Join(home, ".config", "x"),
	}
	for _, p := range allowed {
		under, err := pathUnderSSH(p)
		if err != nil {
			t.Errorf("pathUnderSSH(%q): unexpected error %v", p, err)
			continue
		}
		if under {
			t.Errorf("pathUnderSSH(%q) = true, want false (allowed)", p)
		}
	}
}
