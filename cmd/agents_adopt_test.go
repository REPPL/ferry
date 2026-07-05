package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/agents"
	"github.com/REPPL/ferry/internal/config"
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

	// The bridge path is in the persisted target record, so the SCOPED
	// `ferry restore agents` covers it too (not only a full restore).
	recorded, err := agentsRestorePaths()
	if err != nil {
		t.Fatalf("agentsRestorePaths: %v", err)
	}
	inRecord := false
	for _, p := range recorded {
		if p == bridgePath {
			inRecord = true
		}
	}
	if !inRecord {
		t.Errorf("bridge path missing from the agents target record: %v", recorded)
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

// TestRefuseDirectoryBridges pins the loud refusal: a directory-level bridge
// (a symlinked ~/.claude, a whole-dir hooks link) must abort adopt with the
// exact manual fix — it is never migrated (irreversible: the engine cannot
// snapshot a directory) and never written through.
func TestRefuseDirectoryBridges(t *testing.T) {
	fileBridge := agents.Bridge{Path: "/home/u/.codex/AGENTS.md", Dest: "/sst/combined.md"}
	dirBridge := agents.Bridge{Path: "/home/u/.claude", Dest: "/sst", Dir: true}

	if err := refuseDirectoryBridges([]agents.Bridge{fileBridge}); err != nil {
		t.Errorf("file-level bridges alone must not refuse: %v", err)
	}
	err := refuseDirectoryBridges([]agents.Bridge{fileBridge, dirBridge})
	if err == nil {
		t.Fatal("directory-level bridge was not refused")
	}
	for _, want := range []string{"rm /home/u/.claude", "re-run adopt"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q missing %q", err.Error(), want)
		}
	}
}

// TestAdoptLeavesUnselectedBridgesInPlace pins the load-bearing half of the
// widened bridge scan: FindBridges reports bridges at built-in DEFAULT
// locations even when the current `assets` selection excludes that built-in,
// and the ONLY thing preventing the swap from journal-removing such a bridge
// with nothing to replace it is the partition in the adopt path. It drives
// the real production sequence (FindBridges -> Plan -> partitionAdoptBridges
// -> adoptTransaction) and asserts the three observables: the symlink stays
// on disk untouched, a warning names it, and it is absent from the migrated
// set and the persisted agents target record.
func TestAdoptLeavesUnselectedBridgesInPlace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// The adopted instruction dir: a hooks tree (bridged at the EXCLUDED
	// built-in ~/.claude/hooks) and a githooks tree (the selected mapping).
	adopted := filepath.Join(home, "Workspace", ".agents")
	for _, rel := range []string{"hooks", "githooks"} {
		if err := os.MkdirAll(filepath.Join(adopted, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(adopted, "hooks", "guard.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adopted, "githooks", "pre-commit"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Old-setup bridges: one at the excluded built-in location, one at the
	// selected custom mapping's target.
	if err := os.MkdirAll(filepath.Join(home, ".claude", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	strandedPath := filepath.Join(home, ".claude", "hooks", "guard.sh")
	strandedDest := filepath.Join(adopted, "hooks", "guard.sh")
	if err := os.Symlink(strandedDest, strandedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".githooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	migratedPath := filepath.Join(home, ".githooks", "pre-commit")
	if err := os.Symlink(filepath.Join(adopted, "githooks", "pre-commit"), migratedPath); err != nil {
		t.Fatal(err)
	}

	// The config repo carries only the selected mapping's tree; the selection
	// EXCLUDES every built-in mapping and harness.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "agents", "githooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "agents", "githooks", "pre-commit"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.AgentsConfig{
		Harnesses:    []string{},
		HarnessesSet: true,
		Assets:       []string{"githooks"},
		AssetsSet:    true,
		Asset: map[string]config.AgentsAsset{
			"githooks": {Source: "githooks", Target: ".githooks"},
		},
	}

	bridges, err := agents.FindBridges(home, adopted, cfg)
	if err != nil {
		t.Fatalf("FindBridges: %v", err)
	}
	foundStranded := false
	for _, b := range bridges {
		if b.Path == strandedPath {
			foundStranded = true
		}
	}
	if !foundStranded {
		t.Fatalf("precondition: the excluded built-in location was not scanned; bridges = %+v", bridges)
	}

	items, _, err := agents.Plan(agents.PlanInput{RepoRoot: repo, Home: home, Config: cfg})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var warnBuf strings.Builder
	toMigrate := partitionAdoptBridges(items, bridges, &warnBuf)

	// (b) A warning names the stranded bridge.
	if !strings.Contains(warnBuf.String(), strandedPath) {
		t.Errorf("no warning names the stranded bridge %s; output: %q", strandedPath, warnBuf.String())
	}
	// (c) It is not in the migrated set.
	for _, b := range toMigrate {
		if b.Path == strandedPath {
			t.Fatalf("stranded bridge %s is in the migrated set — the swap would remove it with nothing to replace it", strandedPath)
		}
	}

	if err := adoptTransaction(&cmdContext{}, items, toMigrate, io.Discard); err != nil {
		t.Fatalf("adoptTransaction: %v", err)
	}

	// (a) The stranded symlink is still on disk, untouched.
	got, lerr := os.Readlink(strandedPath)
	if lerr != nil {
		t.Fatalf("stranded bridge no longer a symlink (removed or replaced): %v", lerr)
	}
	if got != strandedDest {
		t.Errorf("stranded bridge repointed: %q, want %q", got, strandedDest)
	}
	// The selected mapping's bridge WAS migrated to a managed copy.
	if fi, serr := os.Lstat(migratedPath); serr != nil || !fi.Mode().IsRegular() {
		t.Errorf("selected mapping's bridge not replaced by a managed copy: %v %v", fi, serr)
	}
	// (c) And the stranded path is absent from the persisted target record.
	recorded, rerr := agentsRestorePaths()
	if rerr != nil {
		t.Fatalf("agentsRestorePaths: %v", rerr)
	}
	for _, p := range recorded {
		if p == strandedPath {
			t.Errorf("stranded bridge %s is in the agents target record — restore would treat it as ferry-managed", p)
		}
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
