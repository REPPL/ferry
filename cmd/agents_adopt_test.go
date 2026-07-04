package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/agents"
	"github.com/REPPL/ferry/internal/dotfile"
)

// adoptFixture builds a fake $HOME with an old-style instruction dir and one
// bridge symlink (~/.claude/CLAUDE.md -> <sst>/general.md), returning the
// pieces a transaction test needs.
func adoptFixture(t *testing.T) (home, sst, bridgePath string, item agents.Item) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)

	sst = filepath.Join(home, "Workspace", ".agents")
	if err := os.MkdirAll(sst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sst, "general.md"), []byte("GENERAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	bridgePath = filepath.Join(home, ".claude", "CLAUDE.md")
	if err := os.Symlink(filepath.Join(sst, "general.md"), bridgePath); err != nil {
		t.Fatal(err)
	}

	target, err := dotfile.NestedTarget(home, ".claude/CLAUDE.md", "agents/claude")
	if err != nil {
		t.Fatal(err)
	}
	item = agents.Item{
		Key:     "agents/claude",
		Label:   "agents:claude",
		Target:  target,
		Content: []byte("GENERAL\n"),
	}
	return home, sst, bridgePath, item
}

// TestAdoptTransactionJournalsBridgeRemoval pins the CRITICAL recoverability
// contract: a bridge symlink is removed THROUGH the backup engine, so its
// symlink state (link target) is in the immutable baseline — after a
// successful adopt, `ferry restore` recreates the symlink exactly. A raw
// os.Remove would leave no baseline and no way back.
func TestAdoptTransactionJournalsBridgeRemoval(t *testing.T) {
	_, sst, bridgePath, item := adoptFixture(t)

	ctx := &cmdContext{}
	err := adoptTransaction(ctx, []agents.Item{item},
		[]agents.Bridge{{Path: bridgePath, Dest: filepath.Join(sst, "general.md")}}, io.Discard)
	if err != nil {
		t.Fatalf("adoptTransaction: %v", err)
	}

	// The bridge is now a managed regular-file copy.
	fi, err := os.Lstat(bridgePath)
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("bridge not replaced by a regular file: %v %v", fi, err)
	}
	if got, _ := os.ReadFile(bridgePath); string(got) != "GENERAL\n" {
		t.Fatalf("managed copy content = %q", got)
	}

	// The symlink's prior state is in the baseline (the recoverability proof).
	eng, err := ctx.Engine()
	if err != nil {
		t.Fatal(err)
	}
	if !eng.HasBaseline(bridgePath) {
		t.Fatal("no baseline recorded for the removed bridge symlink — restore could not revert the adopt")
	}

	// And restore actually brings the symlink back, byte-identical wiring.
	if _, err := eng.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	target, err := os.Readlink(bridgePath)
	if err != nil {
		t.Fatalf("restored bridge is not a symlink: %v", err)
	}
	if want := filepath.Join(sst, "general.md"); target != want {
		t.Errorf("restored link target = %q, want %q", target, want)
	}
}

// TestAdoptTransactionRollsBackOnFailure pins the write-then-swap ordering as
// a TRANSACTION: when any copy fails to deploy (here: the data-loss guard
// refusing a near-empty source over a substantial live file), the whole run
// rolls back inline — the already-removed bridge symlink comes back and no
// half-migrated state remains.
func TestAdoptTransactionRollsBackOnFailure(t *testing.T) {
	home, sst, bridgePath, okItem := adoptFixture(t)

	// A second item that MUST fail: near-empty content over a substantial
	// pre-existing live file trips the empty-over-substantial guard.
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	substantial := strings.Repeat("a real config line with content\n", 10)
	if err := os.WriteFile(filepath.Join(home, ".codex", "AGENTS.md"), []byte(substantial), 0o644); err != nil {
		t.Fatal(err)
	}
	codexTarget, err := dotfile.NestedTarget(home, ".codex/AGENTS.md", "agents/codex")
	if err != nil {
		t.Fatal(err)
	}
	failingItem := agents.Item{
		Key:     "agents/codex",
		Label:   "agents:codex",
		Target:  codexTarget,
		Content: []byte("# just a comment\n"),
	}

	ctx := &cmdContext{}
	err = adoptTransaction(ctx, []agents.Item{okItem, failingItem},
		[]agents.Bridge{{Path: bridgePath, Dest: filepath.Join(sst, "general.md")}}, io.Discard)
	if err == nil {
		t.Fatal("adoptTransaction succeeded; the guard item should have failed it")
	}

	// The rollback must have recreated the bridge symlink…
	target, lerr := os.Readlink(bridgePath)
	if lerr != nil {
		t.Fatalf("bridge symlink not restored after rollback: %v", lerr)
	}
	if want := filepath.Join(sst, "general.md"); target != want {
		t.Errorf("rolled-back link target = %q, want %q", target, want)
	}
	// …and left the substantial live file untouched.
	if got, _ := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md")); string(got) != substantial {
		t.Errorf("guarded live file mutated by a rolled-back adopt: %q", got)
	}
}
