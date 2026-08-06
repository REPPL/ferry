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
