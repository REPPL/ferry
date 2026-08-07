package cmd

// Round-5: `ferry work receive` and `ferry work restore` hold the SAME exclusive
// apply lock as apply/restore/agents, and must speak the same language about it:
// a lock held by a live owner surfaces the friendly "another ferry apply is in
// progress (pid N)" message (not the raw backup.ErrLockHeld text), and a failed
// unlock is surfaced, never swallowed (that contract is exercised at the
// applyPlan/runRestore sites; these tests pin the message parity for work).

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// workLockHome builds an isolated $HOME carrying a machine config with a [work]
// store plus a real git project dir, so loadWorkContext succeeds and the run
// reaches the apply lock.
func workLockHome(t *testing.T) (home, project string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)

	cfgDir := filepath.Join(home, ".config", "ferry")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(home, "cargo")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "hostname = \"test-host\"\nrepo = \"" + filepath.Join(home, "repo") + "\"\n\n[work]\nstore = \"" + store + "\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	project = filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, project, "init", "-q", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("proj\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, project, "add", "-A")
	testGit(t, project, "commit", "-qm", "init")

	stateDir := filepath.Join(home, ".local", "state", "ferry")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return home, project
}

func newWorkTestCmd(run func(*cobra.Command, []string) error) (*cobra.Command, *bytes.Buffer) {
	c := &cobra.Command{Use: "work", RunE: run}
	c.Flags().Bool("force", false, "")
	c.Flags().Bool("allow-sync-root", false, "")
	c.Flags().String("bundle", "", "")
	out := &bytes.Buffer{}
	c.SetOut(out)
	c.SetErr(out)
	c.SetIn(strings.NewReader(""))
	return c, out
}

// TestWorkReceiveFailsClosedWhenApplyLockHeld: a live-owned apply lock makes
// `work receive` refuse with the same "in progress" message apply prints.
func TestWorkReceiveFailsClosedWhenApplyLockHeld(t *testing.T) {
	home, project := workLockHome(t)
	pid := liveForeignPID(t)
	writeLockFile(t, filepath.Join(home, ".local", "state", "ferry"), pid)

	c, _ := newWorkTestCmd(runWorkReceive)
	err := runWorkReceive(c, []string{project})
	if err == nil {
		t.Fatalf("work receive proceeded while the apply lock was held by a live owner (pid %d)", pid)
	}
	if !strings.Contains(err.Error(), "in progress") || !strings.Contains(err.Error(), strconv.Itoa(pid)) {
		t.Fatalf("err = %v, want an 'in progress' error naming pid %d", err, pid)
	}
}

// TestWorkRestoreFailsClosedWhenApplyLockHeld: same contract for `work restore`.
func TestWorkRestoreFailsClosedWhenApplyLockHeld(t *testing.T) {
	home, project := workLockHome(t)
	pid := liveForeignPID(t)
	writeLockFile(t, filepath.Join(home, ".local", "state", "ferry"), pid)

	c, _ := newWorkTestCmd(runWorkRestore)
	err := runWorkRestore(c, []string{project})
	if err == nil {
		t.Fatalf("work restore proceeded while the apply lock was held by a live owner (pid %d)", pid)
	}
	if !strings.Contains(err.Error(), "in progress") || !strings.Contains(err.Error(), strconv.Itoa(pid)) {
		t.Fatalf("err = %v, want an 'in progress' error naming pid %d", err, pid)
	}
}
