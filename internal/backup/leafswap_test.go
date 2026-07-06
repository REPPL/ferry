package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The leaf-swap TOCTOU: guardResolvedContainment validates a managed target's
// PARENT chain, but not the final component (the leaf). After that check passes,
// a same-user process can swap the leaf itself to a symlink pointing into ~/.ssh
// or outside $HOME. These tests place that escaping leaf symlink STATICALLY
// (before the boundary runs) and assert the mutation is confined to the real
// parent directory — os.Root refuses to follow an escaping leaf, so the sentinel
// standing in for a private key is never read, written, or deleted through it.

// makeEscapingLeaf builds a managed target ~/dir/name whose real parent exists,
// then replaces the leaf with a symlink pointing at a sentinel file OUTSIDE the
// parent (standing in for a private key). It returns the target path and the
// sentinel path. The parent chain (~/dir) stays a genuine in-home directory, so
// guardResolvedContainment passes and only the leaf-level defence can stop a
// redirect.
func makeEscapingLeaf(t *testing.T, home, dir, name string) (target, sentinel string) {
	t.Helper()
	outside := t.TempDir()
	sentinel = filepath.Join(outside, "private-key")
	if err := os.WriteFile(sentinel, []byte("PRIVATE-KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(home, dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	target = filepath.Join(parent, name)
	if err := os.Symlink(sentinel, target); err != nil {
		t.Fatal(err)
	}
	return target, sentinel
}

func assertSentinelIntact(t *testing.T, sentinel string) {
	t.Helper()
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel unreadable (followed through leaf?): %v", err)
	}
	if string(got) != "PRIVATE-KEY" {
		t.Fatalf("sentinel mutated through leaf symlink: got %q, want PRIVATE-KEY", got)
	}
}

// TestBackupAndWriteConfinesLeafSwap: the write boundary must land the payload in
// the REAL parent directory (replacing the leaf symlink), never following it out
// to the sentinel.
func TestBackupAndWriteConfinesLeafSwap(t *testing.T) {
	e, home := homeEngine(t)
	target, sentinel := makeEscapingLeaf(t, home, ".config", "foo")

	r, err := e.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := e.BackupAndWrite(r, target, []byte("PAYLOAD"), 0o600); err != nil {
		t.Fatalf("BackupAndWrite: %v", err)
	}

	assertSentinelIntact(t, sentinel)
	// The payload must have landed at the real in-parent target, not through the
	// link: the leaf is now a regular file, and reading it via Lstat confirms it
	// is no longer a symlink pointing outside.
	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("Lstat target: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("target is still a symlink; write was not confined to the parent")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "PAYLOAD" {
		t.Fatalf("target content = %q, want PAYLOAD", got)
	}
}

// TestBackupAndRemoveConfinesLeafSwap: the delete boundary must unlink the leaf
// symlink itself, never delete the sentinel it points to.
func TestBackupAndRemoveConfinesLeafSwap(t *testing.T) {
	e, home := homeEngine(t)
	target, sentinel := makeEscapingLeaf(t, home, ".config", "foo")

	r, err := e.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := e.BackupAndRemove(r, target); err != nil {
		t.Fatalf("BackupAndRemove: %v", err)
	}

	assertSentinelIntact(t, sentinel)
	// The leaf symlink itself must be gone.
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leaf symlink survived remove: %v", err)
	}
}

// TestRestoreStateConfinesLeafSwap drives the restore write boundary directly: a
// KindFile baseline is written back to a target whose leaf is currently an
// escaping symlink. The restore must materialise the baseline bytes at the real
// in-parent path, never following the link to overwrite the sentinel.
func TestRestoreStateConfinesLeafSwap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target, sentinel := makeEscapingLeaf(t, home, ".config", "foo")

	st := PathState{Path: target, Kind: KindFile, Mode: 0o644, HasBlob: true}
	if err := restoreState(st, []byte("RESTORED")); err != nil {
		t.Fatalf("restoreState: %v", err)
	}

	assertSentinelIntact(t, sentinel)
	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("Lstat target: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("target still a symlink; restore followed the leaf")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "RESTORED" {
		t.Fatalf("target content = %q, want RESTORED", got)
	}
}

// TestLeafOpenForWriteRefusesEscape reproduces the exact syscall the restore
// write executes AFTER clearing the leaf — an open-for-write of the leaf name —
// in the committed state the leaf-swap RACE leaves behind: an escaping symlink
// present at the leaf at open time. The confined open (through os.Root, the way
// restoreState now writes) MUST refuse it; the pre-fix primitive (os.WriteFile,
// which follows the leaf symlink) would silently overwrite the sentinel. This is
// the deterministic, non-timing reproduction of the leaf-swap write redirect.
func TestLeafOpenForWriteRefusesEscape(t *testing.T) {
	home := t.TempDir()
	target, sentinel := makeEscapingLeaf(t, home, ".config", "foo")

	root, base, err := leafRoot(target)
	if err != nil {
		t.Fatalf("leafRoot: %v", err)
	}
	defer root.Close()

	// A confined write to the escaping leaf must be refused, not followed.
	if err := root.WriteFile(base, []byte("REDIRECTED"), 0o644); err == nil {
		t.Fatalf("confined write to an escaping leaf succeeded; want refusal")
	}
	assertSentinelIntact(t, sentinel)

	// Contrast: the pre-fix primitive follows the leaf and clobbers the sentinel,
	// demonstrating the window this closes. (Restores the sentinel afterwards so
	// the assertion above is the meaningful one.)
	if err := os.WriteFile(target, []byte("REDIRECTED"), 0o644); err != nil {
		t.Fatalf("os.WriteFile through leaf: %v", err)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("read sentinel after unconfined write: %v", err)
	}
	if string(got) != "REDIRECTED" {
		t.Fatalf("expected unconfined os.WriteFile to follow the leaf and clobber the sentinel, got %q", got)
	}
}
