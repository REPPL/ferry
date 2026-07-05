package cmd

import (
	"os"
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

// TestPathUnderSSHFoldsWrongCaseHomeParent pins the containment guard as
// case-insensitive on the HOME PARENT components too, not just the ".ssh" leaf.
// On the default case-insensitive macOS filesystem a wrong-case HOME parent
// (e.g. .../ALICE vs .../alice) names the SAME directory, so a candidate such as
// .../ALICE/.ssh/repo still resolves inside the real ~/.ssh and must be refused
// exactly as .../alice/.ssh/repo is. A FAKE $HOME is used; the real ~/.ssh is
// never touched.
func TestPathUnderSSHFoldsWrongCaseHomeParent(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "alice")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	// The candidate differs from HOME only in the case of the leaf HOME component
	// ("ALICE" vs "alice"); its ".ssh" segment is exact-case.
	wrongCase := filepath.Join(base, "ALICE", ".ssh", "repo")
	under, err := pathUnderSSH(wrongCase)
	if err != nil {
		t.Fatalf("pathUnderSSH(%q): unexpected error %v", wrongCase, err)
	}
	if !under {
		t.Errorf("pathUnderSSH(%q) = false, want true (a wrong-case HOME parent still lands inside ~/.ssh)", wrongCase)
	}
	if err := rejectIfUnderSSH("repo", wrongCase); err == nil {
		t.Errorf("rejectIfUnderSSH(%q) = nil, want refusal", wrongCase)
	}
}
