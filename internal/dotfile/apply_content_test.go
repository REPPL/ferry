package dotfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// contentBackuper is a minimal Backuper for exercising the in-memory apply
// core: it writes the file (creating parents) and forces the exact mode so
// perm assertions observe what the core requested.
type contentBackuper struct{ writes int }

func (f *contentBackuper) BackupAndWrite(target string, content []byte, perm os.FileMode) error {
	f.writes++
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(target, content, perm); err != nil {
		return err
	}
	return os.Chmod(target, perm)
}

// TestApplyContentDeferredSharesTheApplyCore pins the consolidation contract:
// the in-memory apply (ApplyContentDeferred) runs the SAME decision table as
// the file-based apply — same actions, same conflict/skip refusals, same
// data-loss guard — with the fresh-write mode honoured and last-applied only
// ever deferred (PendingHash, never a direct store write).
func TestApplyContentDeferredSharesTheApplyCore(t *testing.T) {
	type step struct {
		name        string
		content     string
		force       bool
		pre         func(t *testing.T, home string, store *Store) // mutate live/store first
		wantAction  Action
		wantErrAs   string // "" | "conflict" | "guard"
		wantPending bool
		wantForced  bool // res.ForcedEmptyOverSubstantial set (--force over the guard)
	}
	steps := []step{
		{
			name: "fresh home creates", content: "v1\n",
			wantAction: ActionCreated, wantPending: true,
		},
		{
			name: "identical re-apply noops", content: "v1\n",
			wantAction: ActionNoop, wantPending: false,
		},
		{
			name: "content advance updates", content: "v2\n",
			wantAction: ActionUpdated, wantPending: true,
		},
		{
			name: "live edit skips without force", content: "v2\n",
			pre: func(t *testing.T, home string, _ *Store) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(home, ".tool", "RULES.md"), []byte("edited\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantAction: ActionSkipped, wantPending: false,
		},
		{
			name: "live edit + content advance conflicts without force", content: "v3\n",
			wantAction: ActionConflict, wantErrAs: "conflict",
		},
		{
			name: "force resolves the conflict", content: "v3\n", force: true,
			wantAction: ActionUpdated, wantPending: true,
		},
		{
			name: "near-empty over substantial refuses without force", content: "# only a comment\n",
			pre: func(t *testing.T, home string, store *Store) {
				t.Helper()
				big := make([]byte, 0, 400)
				for i := 0; i < 10; i++ {
					big = append(big, []byte("a real config line with content\n")...)
				}
				if err := os.WriteFile(filepath.Join(home, ".tool", "RULES.md"), big, 0o644); err != nil {
					t.Fatal(err)
				}
				// Record the live hash so the state is repo-ahead (no local edit),
				// which is the deploy path that hits the data-loss guard.
				if err := store.set("tool", hashBytes(big)); err != nil {
					t.Fatal(err)
				}
			},
			wantAction: ActionConflict, wantErrAs: "guard",
		},
		{
			name: "force overrides the guard but flags the hazard", content: "# only a comment\n",
			force:      true,
			wantAction: ActionUpdated, wantPending: true, wantForced: true,
		},
	}

	home := t.TempDir()
	store, err := OpenStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &contentBackuper{}
	target, err := NestedTarget(home, ".tool/RULES.md", "tool")
	if err != nil {
		t.Fatal(err)
	}

	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if st.pre != nil {
				st.pre(t, home, store)
			}
			res, err := ApplyContentDeferred(target, []byte(st.content), 0o644, store, b, st.force)
			switch st.wantErrAs {
			case "conflict":
				var conflict *ConflictError
				if !errors.As(err, &conflict) {
					t.Fatalf("err = %v, want *ConflictError", err)
				}
			case "guard":
				var guard *EmptyOverSubstantialError
				if !errors.As(err, &guard) {
					t.Fatalf("err = %v, want *EmptyOverSubstantialError", err)
				}
			case "":
				if err != nil {
					t.Fatalf("ApplyContentDeferred: %v", err)
				}
			}
			if res.Action != st.wantAction {
				t.Errorf("action = %s, want %s", res.Action, st.wantAction)
			}
			if (res.PendingHash != "") != st.wantPending {
				t.Errorf("PendingHash = %q, wantPending=%v", res.PendingHash, st.wantPending)
			}
			if res.ForcedEmptyOverSubstantial != st.wantForced {
				t.Errorf("ForcedEmptyOverSubstantial = %v, want %v", res.ForcedEmptyOverSubstantial, st.wantForced)
			}
			// The deferred contract: the core never writes the store directly —
			// last-applied only advances via CommitLastApplied.
			if err == nil {
				if err := CommitLastApplied([]Result{res}, store); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// TestApplyContentDeferredFreshPerm pins the fresh-write mode: a first-ever
// write takes the caller's freshPerm (0755 keeps hook scripts runnable), and
// an existing destination's mode is preserved on a later update — exactly the
// dotfile convention.
func TestApplyContentDeferredFreshPerm(t *testing.T) {
	home := t.TempDir()
	store, err := OpenStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &contentBackuper{}

	hook, err := NestedTarget(home, ".hooks/guard.sh", "hook")
	if err != nil {
		t.Fatal(err)
	}
	res, err := ApplyContentDeferred(hook, []byte("#!/bin/sh\n"), 0o755, store, b, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := CommitLastApplied([]Result{res}, store); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(hook.Home)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("fresh write mode = %v, want 0755", fi.Mode().Perm())
	}

	// The user tightens the mode; an update preserves it (existing-dest rule).
	if err := os.Chmod(hook.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	if res, err = ApplyContentDeferred(hook, []byte("#!/bin/sh\nset -e\n"), 0o755, store, b, false); err != nil {
		t.Fatal(err)
	}
	_ = res
	fi, err = os.Stat(hook.Home)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("update mode = %v, want the preserved 0700", fi.Mode().Perm())
	}
}
