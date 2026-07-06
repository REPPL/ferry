package backup

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/REPPL/ferry/internal/sshguard"
)

// homeEngine builds an Engine whose state root is a temp dir AND points
// os.UserHomeDir() at a separate temp "home" via $HOME, so the write-boundary
// containment guard (which derives $HOME from the environment) is active for
// targets placed under that home. The state root is deliberately NOT under home,
// so it stays a no-op for HardenStoreDir.
func homeEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := t.TempDir()
	e, err := NewAt(state)
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	return e, home
}

// TestBackupAndWriteRefusesParentSwappedToSymlink is the finding (c) reproduction:
// after the plan-time check, a same-user process swaps an intermediate parent to
// a symlink escaping $HOME. BackupAndWrite must re-validate the resolved parent
// chain at the write boundary and refuse, rather than write through the link.
func TestBackupAndWriteRefusesParentSwappedToSymlink(t *testing.T) {
	e, home := homeEngine(t)
	outside := t.TempDir()

	// A legitimate nested target: ~/.config/foo, with ~/.config a real dir.
	cfg := filepath.Join(home, ".config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(cfg, "foo")

	// The attacker swaps ~/.config to a symlink pointing outside $HOME in the
	// window before the write lands.
	if err := os.RemoveAll(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, cfg); err != nil {
		t.Fatal(err)
	}

	r, err := e.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	err = e.BackupAndWrite(r, target, []byte("payload"), 0o600)
	if !errors.Is(err, sshguard.ErrPathEscapesHome) {
		t.Fatalf("BackupAndWrite through swapped parent = %v, want ErrPathEscapesHome", err)
	}
	// The write must NOT have landed outside $HOME through the link.
	if _, statErr := os.Lstat(filepath.Join(outside, "foo")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("write escaped through symlinked parent: %v", statErr)
	}
}

// TestBackupAndRemoveRefusesParentSwappedToSymlink reproduces the delete-boundary
// TOCTOU: after plan time a same-user process swaps an intermediate parent to a
// symlink escaping $HOME. BackupAndRemove must re-validate the resolved parent
// chain and refuse, rather than READ (into baseline) and os.RemoveAll through the
// link — which would unlink a file outside $HOME.
func TestBackupAndRemoveRefusesParentSwappedToSymlink(t *testing.T) {
	e, home := homeEngine(t)
	outside := t.TempDir()

	// A real file living outside $HOME that must survive the refused delete.
	bystander := filepath.Join(outside, "foo")
	if err := os.WriteFile(bystander, []byte("OUTSIDE-DATA"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A legitimate nested target ~/.config/foo, then ~/.config swapped to a
	// symlink pointing outside $HOME so the leaf resolves to outside/foo.
	cfg := filepath.Join(home, ".config")
	target := filepath.Join(cfg, "foo")
	if err := os.Symlink(outside, cfg); err != nil {
		t.Fatal(err)
	}

	r, err := e.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	err = e.BackupAndRemove(r, target)
	if !errors.Is(err, sshguard.ErrPathEscapesHome) {
		t.Fatalf("BackupAndRemove through swapped parent = %v, want ErrPathEscapesHome", err)
	}
	// The out-of-home file must NOT have been deleted through the link.
	got, readErr := os.ReadFile(bystander)
	if readErr != nil {
		t.Fatalf("out-of-home file deleted through symlinked parent: %v", readErr)
	}
	if string(got) != "OUTSIDE-DATA" {
		t.Fatalf("out-of-home file mutated: got %q, want OUTSIDE-DATA", got)
	}
}

// TestBackupAndRemoveRefusesParentSwappedIntoSSH reproduces the ~/.ssh invariant
// violation on the delete boundary: a parent swapped to a symlink into ~/.ssh
// would make BackupAndRemove READ an ssh key into the immutable baseline and then
// DELETE it through the link. The guard must refuse before any read or delete.
func TestBackupAndRemoveRefusesParentSwappedIntoSSH(t *testing.T) {
	e, home := homeEngine(t)

	// A real ssh key ferry must never read or delete.
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(ssh, "id_rsa")
	if err := os.WriteFile(key, []byte("PRIVATE-KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A legitimate nested target ~/.config/id_rsa, then ~/.config swapped to a
	// symlink into ~/.ssh so the leaf resolves to ~/.ssh/id_rsa.
	cfg := filepath.Join(home, ".config")
	target := filepath.Join(cfg, "id_rsa")
	if err := os.Symlink(ssh, cfg); err != nil {
		t.Fatal(err)
	}

	r, err := e.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	err = e.BackupAndRemove(r, target)
	if !errors.Is(err, sshguard.ErrForbiddenSSHPath) {
		t.Fatalf("BackupAndRemove into ~/.ssh = %v, want ErrForbiddenSSHPath", err)
	}
	// The ssh key must survive untouched.
	got, readErr := os.ReadFile(key)
	if readErr != nil {
		t.Fatalf("ssh key deleted through symlinked parent: %v", readErr)
	}
	if string(got) != "PRIVATE-KEY" {
		t.Fatalf("ssh key mutated: got %q, want PRIVATE-KEY", got)
	}
}

// TestRestoreRefusesParentSwappedToSymlink is the finding (b) reproduction: a
// baseline path whose parent is swapped to a symlink escaping $HOME before
// `ferry restore`. Restore must refuse that entry (surfacing an error) and NOT
// write through the redirected path, while still restoring the other entries.
func TestRestoreRefusesParentSwappedToSymlink(t *testing.T) {
	e, home := homeEngine(t)
	outside := t.TempDir()

	// Two managed nested files with real parents, each captured into baseline via
	// a normal transactional write.
	victimDir := filepath.Join(home, ".config")
	safeDir := filepath.Join(home, ".other")
	for _, d := range []string{victimDir, safeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	victim := filepath.Join(victimDir, "foo") // will have its parent swapped
	safe := filepath.Join(safeDir, "bar")     // must still restore
	if err := os.WriteFile(victim, []byte("ORIG-victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(safe, []byte("ORIG-safe"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := e.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := e.BackupAndWrite(r, victim, []byte("CHANGED-victim"), 0o644); err != nil {
		t.Fatalf("BackupAndWrite victim: %v", err)
	}
	if err := e.BackupAndWrite(r, safe, []byte("CHANGED-safe"), 0o644); err != nil {
		t.Fatalf("BackupAndWrite safe: %v", err)
	}
	if err := r.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// The attacker swaps ~/.config to a symlink escaping $HOME before restore.
	if err := os.RemoveAll(victim); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(victimDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, victimDir); err != nil {
		t.Fatal(err)
	}

	_, err = e.Restore()
	if !errors.Is(err, sshguard.ErrPathEscapesHome) {
		t.Fatalf("Restore with swapped parent = %v, want a surfaced ErrPathEscapesHome refusal", err)
	}
	// The victim's baseline content must NOT have been written through the link.
	if _, statErr := os.Lstat(filepath.Join(outside, "foo")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("restore wrote through symlinked parent: %v", statErr)
	}
	// The safe entry, whose parent was untouched, must still be restored.
	got, readErr := os.ReadFile(safe)
	if readErr != nil {
		t.Fatalf("read safe after restore: %v", readErr)
	}
	if string(got) != "ORIG-safe" {
		t.Fatalf("safe entry not restored: got %q, want ORIG-safe", got)
	}
}

// TestRestoreDoesNotSnapshotThroughParentSwappedIntoSSH reproduces the
// pre-restore-snapshot READ leak: restore snapshots the CURRENT state of every
// baseline path (so an unwanted restore is reversible) BEFORE the per-path write
// guard fires. That snapshot read used os.Lstat+os.ReadFile, which follow
// intermediate PARENT symlinks, so a parent swapped to a symlink into ~/.ssh
// would make restore READ the private key and persist it into the snapshot store
// — violating "ferry never reads ~/.ssh". Restore must guard each real path
// BEFORE the snapshot reads it, refusing the entry and capturing nothing.
func TestRestoreDoesNotSnapshotThroughParentSwappedIntoSSH(t *testing.T) {
	e, home := homeEngine(t)

	// A real ssh key ferry must never read or capture.
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "PRIVATE-KEY-DO-NOT-CAPTURE"
	key := filepath.Join(ssh, "id_rsa")
	if err := os.WriteFile(key, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	// A legitimate managed nested file ~/.config/id_rsa captured into baseline.
	cfg := filepath.Join(home, ".config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(cfg, "id_rsa")
	if err := os.WriteFile(target, []byte("ORIG"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := e.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := e.BackupAndWrite(r, target, []byte("CHANGED"), 0o644); err != nil {
		t.Fatalf("BackupAndWrite: %v", err)
	}
	if err := r.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// The attacker swaps ~/.config to a symlink into ~/.ssh before restore, so the
	// leaf ~/.config/id_rsa now resolves to ~/.ssh/id_rsa.
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ssh, cfg); err != nil {
		t.Fatal(err)
	}

	_, err = e.Restore()
	if !errors.Is(err, sshguard.ErrForbiddenSSHPath) {
		t.Fatalf("Restore into ~/.ssh = %v, want a surfaced ErrForbiddenSSHPath refusal", err)
	}

	// The ssh key's bytes must NOT appear anywhere under the snapshot store: the
	// guard must fire BEFORE the snapshot reads through the swapped parent.
	err = filepath.WalkDir(e.snapshotDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("ssh key bytes leaked into snapshot store at %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk snapshot store: %v", err)
	}

	// The ssh key must survive untouched.
	got, readErr := os.ReadFile(key)
	if readErr != nil {
		t.Fatalf("ssh key mutated after restore: %v", readErr)
	}
	if string(got) != secret {
		t.Fatalf("ssh key mutated: got %q, want %q", got, secret)
	}
}
