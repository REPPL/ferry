package agents

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/dotfile"
)

// fakeBackuper mimics the engine's BackupAndWrite for unit tests: it writes
// the file (creating parents) and records the call. Tests assert both the
// on-disk outcome and that EVERY mutation went through the Backuper.
type fakeBackuper struct {
	writes int
}

func (f *fakeBackuper) BackupAndWrite(target string, content []byte, perm os.FileMode) error {
	f.writes++
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(target, content, perm); err != nil {
		return err
	}
	// os.WriteFile only applies perm on creation (and via umask); force the
	// exact mode so the perm assertions observe what apply requested.
	return os.Chmod(target, perm)
}

// testItem builds an Item targeting rel under home with the given content.
func testItem(t *testing.T, home, rel, key string, content []byte, exec bool) Item {
	t.Helper()
	target, err := dotfile.NestedTarget(home, rel, key)
	if err != nil {
		t.Fatal(err)
	}
	return Item{Key: key, Label: "agents:" + key, Target: target, Content: content, Exec: exec}
}

func openStore(t *testing.T) *dotfile.Store {
	t.Helper()
	store, err := dotfile.OpenStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestApplyItemLifecycle(t *testing.T) {
	home := t.TempDir()
	store := openStore(t)
	b := &fakeBackuper{}
	it := testItem(t, home, ".codex/AGENTS.md", "agents/codex", []byte("v1\n"), false)

	// 1. Fresh home: created.
	res, err := ApplyItem(it, store, b, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.Action != dotfile.ActionCreated || res.PendingHash == "" {
		t.Fatalf("create: action=%s pending=%q, want created with a pending hash", res.Action, res.PendingHash)
	}
	if err := dotfile.CommitLastApplied([]dotfile.Result{res}, store); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(it.Target.Home)
	if err != nil || string(got) != "v1\n" {
		t.Fatalf("live file = %q, %v", got, err)
	}

	// 2. Idempotent re-apply: noop, no write.
	writesBefore := b.writes
	res, err = ApplyItem(it, store, b, false)
	if err != nil || res.Action != dotfile.ActionNoop {
		t.Fatalf("re-apply: action=%s err=%v, want noop", res.Action, err)
	}
	if b.writes != writesBefore {
		t.Fatalf("re-apply wrote %d time(s); a clean target must be hash-gated to zero writes", b.writes-writesBefore)
	}

	// 3. Repo advance: updated.
	it2 := it
	it2.Content = []byte("v2\n")
	res, err = ApplyItem(it2, store, b, false)
	if err != nil || res.Action != dotfile.ActionUpdated {
		t.Fatalf("update: action=%s err=%v, want updated", res.Action, err)
	}
	if err := dotfile.CommitLastApplied([]dotfile.Result{res}, store); err != nil {
		t.Fatal(err)
	}

	// 4. Live edit, repo unchanged: skipped without force (repo-authoritative
	// or not, ferry never silently discards a live edit).
	if err := os.WriteFile(it.Target.Home, []byte("edited live\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = ApplyItem(it2, store, b, false)
	if err != nil || res.Action != dotfile.ActionSkipped {
		t.Fatalf("drift: action=%s err=%v, want skipped", res.Action, err)
	}
	if live, _ := os.ReadFile(it.Target.Home); string(live) != "edited live\n" {
		t.Fatalf("drifted live file was overwritten without force: %q", live)
	}

	// 5. Force overwrites the drift (backed up via the Backuper).
	res, err = ApplyItem(it2, store, b, true)
	if err != nil || res.Action != dotfile.ActionUpdated {
		t.Fatalf("force: action=%s err=%v, want updated", res.Action, err)
	}
	if live, _ := os.ReadFile(it.Target.Home); string(live) != "v2\n" {
		t.Fatalf("force did not deploy: %q", live)
	}
}

func TestApplyItemConflictRefusedWithoutForce(t *testing.T) {
	home := t.TempDir()
	store := openStore(t)
	b := &fakeBackuper{}
	it := testItem(t, home, ".gemini/GEMINI.md", "agents/gemini", []byte("v1\n"), false)

	res, err := ApplyItem(it, store, b, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dotfile.CommitLastApplied([]dotfile.Result{res}, store); err != nil {
		t.Fatal(err)
	}
	// Both sides move: live edited AND repo advanced.
	if err := os.WriteFile(it.Target.Home, []byte("live edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	it.Content = []byte("v2\n")

	_, err = ApplyItem(it, store, b, false)
	var conflict *dotfile.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *dotfile.ConflictError", err)
	}
	if live, _ := os.ReadFile(it.Target.Home); string(live) != "live edit\n" {
		t.Fatalf("conflicted live file was touched: %q", live)
	}

	// Force resolves it in the repo's favour.
	if res, err = ApplyItem(it, store, b, true); err != nil || res.Action != dotfile.ActionUpdated {
		t.Fatalf("force after conflict: action=%s err=%v", res.Action, err)
	}
}

func TestApplyItemPreservesExecutableBit(t *testing.T) {
	home := t.TempDir()
	store := openStore(t)
	b := &fakeBackuper{}

	hook := testItem(t, home, ".claude/hooks/guard.sh", "agents/hooks/guard.sh", []byte("#!/bin/sh\n"), true)
	if _, err := ApplyItem(hook, store, b, false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(hook.Target.Home)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("hook deployed without an executable bit: %v", fi.Mode())
	}

	instr := testItem(t, home, ".claude/CLAUDE.md", "agents/claude", []byte("text\n"), false)
	if _, err := ApplyItem(instr, store, b, false); err != nil {
		t.Fatal(err)
	}
	fi, err = os.Stat(instr.Target.Home)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("instruction file mode = %v, want 0644", fi.Mode().Perm())
	}
}

func TestApplyItemAdoptsIdenticalPreexistingFile(t *testing.T) {
	home := t.TempDir()
	store := openStore(t)
	b := &fakeBackuper{}
	it := testItem(t, home, ".companion/COMPANION.md", "agents/companion", []byte("same\n"), false)

	if err := os.MkdirAll(filepath.Dir(it.Target.Home), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(it.Target.Home, []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ApplyItem(it, store, b, false)
	if err != nil || res.Action != dotfile.ActionNoop {
		t.Fatalf("adopt: action=%s err=%v, want noop", res.Action, err)
	}
	if res.PendingHash == "" {
		t.Error("identical pre-existing file was not adopted into last-applied (empty PendingHash)")
	}
	if b.writes != 0 {
		t.Errorf("adoption wrote %d time(s), want 0", b.writes)
	}
}

func TestApplyItemRefusesEmptyOverSubstantial(t *testing.T) {
	home := t.TempDir()
	store := openStore(t)
	b := &fakeBackuper{}
	it := testItem(t, home, ".codex/AGENTS.md", "agents/codex", []byte("# only a comment\n"), false)

	substantial := strings.Repeat("real config content line\n", 10)
	if err := os.MkdirAll(filepath.Dir(it.Target.Home), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(it.Target.Home, []byte(substantial), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ApplyItem(it, store, b, false)
	var guard *dotfile.EmptyOverSubstantialError
	if !errors.As(err, &guard) {
		t.Fatalf("err = %v, want *dotfile.EmptyOverSubstantialError", err)
	}
	if live, _ := os.ReadFile(it.Target.Home); string(live) != substantial {
		t.Fatalf("guarded live file was touched: %q", live)
	}

	// Force proceeds but flags the hazard for the caller's loud warning.
	res, err := ApplyItem(it, store, b, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ForcedEmptyOverSubstantial || res.ForcedPath != it.Target.Home {
		t.Errorf("force did not flag the empty-over-substantial hazard: %+v", res)
	}
}

func TestApplyItemRefusesSymlinkTarget(t *testing.T) {
	home := t.TempDir()
	store := openStore(t)
	b := &fakeBackuper{}
	it := testItem(t, home, ".codex/AGENTS.md", "agents/codex", []byte("v1\n"), false)

	other := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(it.Target.Home), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, it.Target.Home); err != nil {
		t.Fatal(err)
	}

	_, err := ApplyItem(it, store, b, false)
	var kind *dotfile.UnexpectedKindError
	if !errors.As(err, &kind) {
		t.Fatalf("err = %v, want *dotfile.UnexpectedKindError (symlink target refused)", err)
	}
	if b.writes != 0 {
		t.Errorf("symlink target was written through (%d writes)", b.writes)
	}
}
