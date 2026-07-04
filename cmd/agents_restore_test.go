package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/agents"
)

// TestAgentsRestorePathsIsRepoIndependentAndCoversDescoped pins the two
// scoped-restore guarantees for the agents domain:
//
//  1. the revert set comes from the PERSISTED target record — so a target the
//     manifest has since dropped (a de-scoped harness: exactly the case the
//     apply-time warning points `ferry restore agents` at) is still resolved;
//  2. NO config repo is consulted — restore's repo-independence guarantee —
//     verified by resolving with no repo configured at all.
func TestAgentsRestorePathsIsRepoIndependentAndCoversDescoped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Simulate two applies: the second de-scoped the codex harness. The
	// record is cumulative, so codex must survive.
	stateDir := filepath.Join(home, ".local", "state", "ferry")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := agents.RecordTargets(stateDir, map[string]string{
		"agents/claude": filepath.Join(home, ".claude", "CLAUDE.md"),
		"agents/codex":  filepath.Join(home, ".codex", "AGENTS.md"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := agents.RecordTargets(stateDir, map[string]string{
		"agents/claude": filepath.Join(home, ".claude", "CLAUDE.md"),
	}); err != nil {
		t.Fatal(err)
	}

	// NO repo exists and none is configured — resolution must not need one.
	paths, err := agentsRestorePaths()
	if err != nil {
		t.Fatalf("agentsRestorePaths: %v", err)
	}
	got := map[string]bool{}
	for _, p := range paths {
		got[p] = true
	}
	if !got[filepath.Join(home, ".codex", "AGENTS.md")] {
		t.Errorf("de-scoped target missing from the restore set: %v", paths)
	}
	if !got[filepath.Join(home, ".claude", "CLAUDE.md")] {
		t.Errorf("in-scope target missing from the restore set: %v", paths)
	}
}

// TestAgentsRestorePathsEmptyRecordIsClearError: a machine where the domain
// was never applied reports that clearly instead of claiming success.
func TestAgentsRestorePathsEmptyRecordIsClearError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := agentsRestorePaths()
	if err == nil || !strings.Contains(err.Error(), "no agents targets recorded") {
		t.Errorf("err = %v, want the no-record message", err)
	}
}
