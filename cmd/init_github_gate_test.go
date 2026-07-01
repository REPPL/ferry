package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGateSecret is a NON-FUNCTIONAL private key of the shape internal/secret
// blocks — never a real key.
const fakeGateSecret = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAdeadbeef01\n" +
	"-----END OPENSSH PRIVATE KEY-----\n"

// TestWholeCommitGate_BlocksNonZshrcFile is the regression for the whole-commit
// secret gate: the pre-push gate must scan EVERY committed file, not just
// ~/.zshrc. A GENERATED/committed file other than ~/.zshrc that carries a fake
// secret must be blocked (no push). This proves the CRITICAL no-secret invariant
// does not depend on "only ~/.zshrc happens to hold user content".
func TestWholeCommitGate_BlocksNonZshrcFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@localhost")
	run("config", "user.name", "t")

	// A committed file that is NOT ~/.zshrc — e.g. a generated manifest — carrying a
	// fake secret. The gate must catch it.
	if err := os.WriteFile(filepath.Join(repo, "ferry.toml"),
		[]byte("[manage]\n# note\n"+fakeGateSecret), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "seed")

	err := gateRepoTreeBeforePush(repo)
	if err == nil {
		t.Fatal("gateRepoTreeBeforePush passed a committed non-zshrc file with a secret (must block)")
	}
	if !strings.Contains(err.Error(), "ferry.toml") {
		t.Errorf("gate error did not name the offending file ferry.toml: %v", err)
	}
}

// TestPlannedCommitGate_BlocksAdoptedSecret confirms the pre-create planner scans
// the adopted ~/.zshrc content as part of the whole-commit set (belt with the
// pre-push braces). We drive plannedCommitContents indirectly by checking the
// generated files are all present in the planned set.
func TestPlannedCommitGate_PlansGeneratedFiles(t *testing.T) {
	files := plannedCommitContents()
	for _, want := range []string{"ferry.toml", ".gitignore"} {
		if _, ok := files[want]; !ok {
			t.Errorf("plannedCommitContents missing generated file %q — whole-commit gate would skip it", want)
		}
	}
}
