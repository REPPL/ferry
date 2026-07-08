package cmd

import (
	"testing"

	"github.com/REPPL/ferry/internal/domains"
	"github.com/REPPL/ferry/internal/dotfile"
)

// TestFileDomainOverlay pins each converged FileDomain's overlay strategy — the
// DATA-DRIVEN replacement for the deleted isZsh() oracle. Only the include-style
// dotfile names (the zsh family, tmux, and git — whose formats have a real
// include point) compose their per-machine overlay as a sourced sidecar; every
// other name (and every other domain) is a whole-file replace.
func TestFileDomainOverlay(t *testing.T) {
	dot := dotfilesFileDomain{}
	for _, name := range []string{"zshrc", "zshenv", "zprofile", "tmux.conf", "gitconfig"} {
		if got := dot.Overlay(name); got != dotfile.OverlayIncludeSidecar {
			t.Errorf("dotfiles Overlay(%q) = %q, want OverlayIncludeSidecar", name, got)
		}
	}
	for _, name := range []string{"npmrc", "vimrc"} {
		if got := dot.Overlay(name); got != dotfile.OverlayWholeFileReplace {
			t.Errorf("dotfiles Overlay(%q) = %q, want OverlayWholeFileReplace", name, got)
		}
	}
	// agents and config-file terminals never use the sidecar split.
	if got := (agentsFileDomain{}).Overlay("anything"); got != dotfile.OverlayWholeFileReplace {
		t.Errorf("agents Overlay = %q, want OverlayWholeFileReplace", got)
	}
	if got := (termcfgFileDomain{}).Overlay("anything"); got != dotfile.OverlayWholeFileReplace {
		t.Errorf("terminals Overlay = %q, want OverlayWholeFileReplace", got)
	}
	if got := (keybindingsFileDomain{}).Overlay("anything"); got != dotfile.OverlayWholeFileReplace {
		t.Errorf("keybindings Overlay = %q, want OverlayWholeFileReplace", got)
	}
	if got := (emacsFileDomain{}).Overlay("anything"); got != dotfile.OverlayWholeFileReplace {
		t.Errorf("emacs Overlay = %q, want OverlayWholeFileReplace", got)
	}
	if got := (iterm2ProfilesFileDomain{}).Overlay("anything"); got != dotfile.OverlayWholeFileReplace {
		t.Errorf("iterm2-profiles Overlay = %q, want OverlayWholeFileReplace", got)
	}
}

// TestUsesIncludeSidecar pins the isZsh() replacement helper: it delegates to the
// dotfiles FileDomain's Overlay(), so exactly the zsh names route to the sidecar.
func TestUsesIncludeSidecar(t *testing.T) {
	for _, bare := range []string{"zshrc", "zshenv", "zprofile", "tmux.conf", "gitconfig"} {
		if !usesIncludeSidecar(bare) {
			t.Errorf("usesIncludeSidecar(%q) = false, want true", bare)
		}
	}
	for _, bare := range []string{"bashrc", "zsh", "zshrc.local"} {
		if usesIncludeSidecar(bare) {
			t.Errorf("usesIncludeSidecar(%q) = true, want false", bare)
		}
	}
}

// TestFileDomainCaptures pins the capture asymmetry: dotfiles and agents are
// offered back to capture; config-file terminals are repo-authoritative (no
// capture pass), which is exactly why Captures() is false for them.
func TestFileDomainCaptures(t *testing.T) {
	cases := map[string]struct {
		fd   domains.FileDomain
		want bool
	}{
		"dotfiles":        {dotfilesFileDomain{}, true},
		"agents":          {agentsFileDomain{}, true},
		"terminals":       {termcfgFileDomain{}, false},
		"keybindings":     {keybindingsFileDomain{}, false},
		"emacs":           {emacsFileDomain{}, false},
		"iterm2-profiles": {iterm2ProfilesFileDomain{}, false},
	}
	for name, tc := range cases {
		if tc.fd.Name() != name {
			t.Errorf("Name() = %q, want %q", tc.fd.Name(), name)
		}
		if got := tc.fd.Captures(); got != tc.want {
			t.Errorf("%s Captures() = %v, want %v", name, got, tc.want)
		}
	}
}

// TestBuildRegistryOrder pins the LOAD-BEARING registry order: FileDomains are
// [dotfiles, agents, terminals, keybindings] and ResourceDomains are
// [iterm2, terminal], matching the pre-fn-5 dispatch sequence so
// plan/status/diff/capture output ordering is unchanged (keybindings appends
// last, after terminals, so existing ordering is unchanged). Each FileDomain must
// also satisfy the cmd filePlanner upcast the plan driver relies on.
func TestBuildRegistryOrder(t *testing.T) {
	reg := buildRegistry(&cmdContext{RepoPath: t.TempDir()})

	wantFile := []string{"dotfiles", "agents", "terminals", "keybindings", "emacs", "iterm2-profiles"}
	if len(reg.FileDomains) != len(wantFile) {
		t.Fatalf("FileDomains len = %d, want %d", len(reg.FileDomains), len(wantFile))
	}
	for i, name := range wantFile {
		if reg.FileDomains[i].Name() != name {
			t.Errorf("FileDomains[%d] = %q, want %q", i, reg.FileDomains[i].Name(), name)
		}
		if _, ok := reg.FileDomains[i].(filePlanner); !ok {
			t.Errorf("FileDomains[%d] (%s) does not implement filePlanner", i, name)
		}
	}

	wantResource := []string{"iterm2", "terminal"}
	if len(reg.ResourceDomains) != len(wantResource) {
		t.Fatalf("ResourceDomains len = %d, want %d", len(reg.ResourceDomains), len(wantResource))
	}
	for i, name := range wantResource {
		if reg.ResourceDomains[i].Name() != name {
			t.Errorf("ResourceDomains[%d] = %q, want %q", i, reg.ResourceDomains[i].Name(), name)
		}
	}
}
