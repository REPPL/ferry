package agents

import (
	"testing"
)

func TestRecordTargetsIsCumulative(t *testing.T) {
	stateDir := t.TempDir()

	// First apply records the initial plan.
	if err := RecordTargets(stateDir, map[string]string{
		"agents/claude": "/home/u/.claude/CLAUDE.md",
		"agents/codex":  "/home/u/.codex/AGENTS.md",
	}); err != nil {
		t.Fatal(err)
	}
	// A later apply (harness de-scoped, devtree added) must UNION, never drop:
	// the de-scoped codex entry is exactly what restore needs to keep seeing.
	if err := RecordTargets(stateDir, map[string]string{
		"agents/claude":  "/home/u/.claude/CLAUDE.md",
		"agents/devtree": "/home/u/Workspace/CLAUDE.md",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := RecordedTargetPaths(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/home/u/.claude/CLAUDE.md",
		"/home/u/.codex/AGENTS.md",
		"/home/u/Workspace/CLAUDE.md",
	}
	if len(got) != len(want) {
		t.Fatalf("RecordedTargetPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("path[%d] = %s, want %s (sorted)", i, got[i], want[i])
		}
	}
}

func TestRecordedTargetPathsAbsentRecord(t *testing.T) {
	got, err := RecordedTargetPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("absent record yielded %v, want empty", got)
	}
}

func TestRecordTargetsKeepsMovedDestinations(t *testing.T) {
	stateDir := t.TempDir()
	if err := RecordTargets(stateDir, map[string]string{"agents/devtree": "/home/u/Old/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	if err := RecordTargets(stateDir, map[string]string{"agents/devtree": "/home/u/New/CLAUDE.md"}); err != nil {
		t.Fatal(err)
	}
	got, err := RecordedTargetPaths(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	// The record is keyed by PATH: a moved destination (renamed devtree)
	// keeps BOTH the old and new paths restorable.
	want := []string{"/home/u/New/CLAUDE.md", "/home/u/Old/CLAUDE.md"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("RecordedTargetPaths = %v, want %v", got, want)
	}
}
