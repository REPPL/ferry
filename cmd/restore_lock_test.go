package cmd

// fn-6.2: `ferry restore` must serialise against a concurrent `ferry apply` by
// taking the SAME exclusive apply lock. Before this, runRestore took NO lock and
// could interleave its writes (file/domain revert, --packages uninstall, --purge)
// with an in-flight apply mutating the same managed paths.
//
// These tests drive the real runRestore against an isolated $HOME so they exercise
// the production Lock()/Unlock() path (not the injectable lockWithAlive seam the
// internal/backup unit tests use):
//
//   - a lock held by a LIVE owner makes restore fail closed (proving it takes the
//     lock: without the lock, restore would ignore the held lock and proceed);
//   - a STALE lock (dead owner) is reclaimed and then RELEASED, so no lock file is
//     left behind (proving restore both acquires and releases via defer Unlock).

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// newRestoreTestCmd builds a cobra command wired with restore's flags so a test
// can invoke runRestore directly with a controlled flag set and an isolated stdio.
func newRestoreTestCmd() (*cobra.Command, *bytes.Buffer) {
	c := &cobra.Command{Use: "restore", RunE: runRestore}
	c.Flags().Bool("packages", false, "")
	c.Flags().Bool("yes", false, "")
	c.Flags().Bool("purge-without-recovery", false, "")
	out := &bytes.Buffer{}
	c.SetOut(out)
	c.SetErr(out)
	c.SetIn(strings.NewReader(""))
	return c, out
}

// writeLockFile plants a lockfile owned by the given pid at ferry's lock path,
// matching the JSON the backup engine reads back (pid + acquired_at).
func writeLockFile(t *testing.T, stateDir string, pid int) string {
	t.Helper()
	lockPath := filepath.Join(stateDir, "lock")
	data, err := json.Marshal(struct {
		PID        int       `json:"pid"`
		AcquiredAt time.Time `json:"acquired_at"`
	}{PID: pid, AcquiredAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return lockPath
}

// liveForeignPID starts a real child process and returns its (live, not-ours) pid,
// registering cleanup that kills it. processAlive(pid) is true for the child.
func liveForeignPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a live child process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// deadForeignPID starts then reaps a child, returning a pid that is provably dead
// (processAlive(pid) is false), so a lock recording it is stale and reclaimable.
func deadForeignPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a child process: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return pid
}

// TestRestoreFailsClosedWhenApplyLockHeld: a live-owned apply lock makes restore
// refuse rather than race the apply that holds it. --packages (an empty installed
// set) is enough work to reach the lock; without the lock, restore would sail past
// the held lock into restorePackages and return nil.
func TestRestoreFailsClosedWhenApplyLockHeld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stateDir := filepath.Join(home, ".local", "state", "ferry")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pid := liveForeignPID(t)
	lockPath := writeLockFile(t, stateDir, pid)

	c, _ := newRestoreTestCmd()
	if err := c.Flags().Set("yes", "true"); err != nil {
		t.Fatal(err)
	}
	if err := c.Flags().Set("packages", "true"); err != nil {
		t.Fatal(err)
	}

	err := runRestore(c, nil)
	if err == nil {
		t.Fatalf("restore proceeded while the apply lock was held by a live owner (pid %d); want a fail-closed error", pid)
	}
	if !strings.Contains(err.Error(), "in progress") || !strings.Contains(err.Error(), strconv.Itoa(pid)) {
		t.Fatalf("err = %v, want an 'in progress' error naming pid %d", err, pid)
	}
	// The held lock must be left intact — restore must never steal a live owner's lock.
	data, rerr := os.ReadFile(lockPath)
	if rerr != nil {
		t.Fatalf("held lock file was removed by a refused restore: %v", rerr)
	}
	if !strings.Contains(string(data), strconv.Itoa(pid)) {
		t.Fatalf("held lock file was rewritten by a refused restore: %s", data)
	}
}

// TestRestoreReclaimsStaleLockAndReleases: a stale lock (dead owner) is reclaimed,
// restore completes, and the lock is released (no file left behind). If restore
// took no lock, the planted stale file would still be present after the run — so
// its ABSENCE proves restore acquired the lock and released it via defer Unlock.
func TestRestoreReclaimsStaleLockAndReleases(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stateDir := filepath.Join(home, ".local", "state", "ferry")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pid := deadForeignPID(t)
	lockPath := writeLockFile(t, stateDir, pid)

	c, _ := newRestoreTestCmd()
	if err := c.Flags().Set("yes", "true"); err != nil {
		t.Fatal(err)
	}
	// --packages with no recorded installed set is a clean, side-effect-free unit of
	// work: it reaches the lock, runs under it, and returns nil.
	if err := c.Flags().Set("packages", "true"); err != nil {
		t.Fatal(err)
	}

	if err := runRestore(c, nil); err != nil {
		t.Fatalf("restore over a stale lock failed: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock file still present after restore (stat err = %v); the lock was not reclaimed+released", err)
	}
}
