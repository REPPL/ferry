package cmd

// Round-5: a locally-drifted agents target is a capture candidate (the agents
// domain has a capture pass), so the skip guidance must name `ferry capture`.
// A true conflict stays resolved at the repo copy — capture refuses a
// divergence — so the conflict wording keeps that remedy.

import (
	"strings"
	"testing"
)

func TestAgentsDriftGuidanceNamesCapture(t *testing.T) {
	t.Parallel()
	if msg := fileSkippedMessage("agents"); !strings.Contains(msg, "ferry capture") {
		t.Fatalf("agents locally-drifted skip message must name `ferry capture`: %q", msg)
	}
	if msg := fileConflictMessage("agents"); !strings.Contains(msg, "repo copy") {
		t.Fatalf("agents conflict message must keep the repo-copy remedy: %q", msg)
	}
}

// Round-7: the remedy wording is keyed on the registry's Captures() flag, not a
// hand-maintained name list — the hand-coded list silently dropped
// iterm2-profiles into the dotfiles wording, steering users at a capture pass
// that does not exist for the domain.
func TestRepoAuthoritativeDomainsNeverSteerAtCapture(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"terminals", "keybindings", "emacs", "iterm2-profiles"} {
		if msg := fileConflictMessage(name); strings.Contains(msg, "ferry capture") || !strings.Contains(msg, "repo source") {
			t.Errorf("%s conflict message must point at the repo source, never capture: %q", name, msg)
		}
		if msg := fileSkippedMessage(name); msg == "" || strings.Contains(msg, "ferry capture") {
			t.Errorf("%s skip message must carry the repo-source remedy, never capture: %q", name, msg)
		}
	}
	// Dotfiles keep the generic capture-candidate wording.
	if msg := fileConflictMessage("dotfiles"); !strings.Contains(msg, "ferry capture") {
		t.Errorf("dotfiles conflict message must keep the capture remedy: %q", msg)
	}
}
