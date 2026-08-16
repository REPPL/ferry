package cmd

// The `work pack --acknowledge` retry must not undo the secret gate's own
// withholding: a PathSecret finding's name IS the secret, so the "not covered by
// --acknowledge" list has to name it REDACTED, exactly as the gate error does.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/work"
)

// newWorkPackTestCmd builds a cobra command carrying `work pack`'s flag set.
func newWorkPackTestCmd() (*cobra.Command, *bytes.Buffer) {
	c := &cobra.Command{Use: "pack", RunE: runWorkPack}
	c.Flags().StringArray("exclude", nil, "")
	c.Flags().Bool("allow-empty", false, "")
	c.Flags().StringArray("acknowledge", nil, "")
	c.Flags().Bool("allow-sync-root", false, "")
	out := &bytes.Buffer{}
	c.SetOut(out)
	c.SetErr(out)
	c.SetIn(strings.NewReader(""))
	return c, out
}

// TestWorkPackAckUnmatchedRedactsSecretPath: with a non-matching --acknowledge,
// the retry aborts and lists the findings it could not pin. A PathSecret
// finding must appear REDACTED there — printing it raw would re-leak, in the
// very same message, the token the gate deliberately withheld.
func TestWorkPackAckUnmatchedRedactsSecretPath(t *testing.T) {
	home, project := workLockHome(t)

	// The required handover note, so the pack reaches the secret gate.
	workLocal := filepath.Join(project, ".abcd", ".work.local")
	if err := os.MkdirAll(workLocal, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workLocal, "NEXT.md"), []byte("handover\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A cargo file whose NAME is secret-shaped: the finding the gate withholds.
	const secretComp = "AKIAIOSFODNN7EXAMPLEXYZ.txt"
	memory := filepath.Join(home, ".claude", "projects", work.ClaudeProjectsKey(project), "memory")
	if err := os.MkdirAll(memory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memory, secretComp), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c, _ := newWorkPackTestCmd()
	if err := c.Flags().Set("acknowledge", "agent-memory/some-other-file.md"); err != nil {
		t.Fatal(err)
	}
	err := runWorkPack(c, []string{project})
	if err == nil {
		t.Fatal("pack with a secret-shaped cargo path and a non-matching --acknowledge: want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not:") {
		t.Fatalf("want the unmatched-findings list in the error, got: %s", msg)
	}
	if strings.Contains(msg, secretComp) || strings.Contains(msg, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("the unmatched list echoed the withheld secret-shaped path: %s", msg)
	}
	if !strings.Contains(msg, "<redacted>") {
		t.Errorf("want the unmatched entry redacted, got: %s", msg)
	}
	// Fail-closed: nothing acknowledged is persisted by a partially-matched run.
	if _, err := os.Stat(filepath.Join(workLocal, work.HandoverMarkerName)); err == nil {
		t.Errorf("a refused pack must not write the handover marker")
	}
}
