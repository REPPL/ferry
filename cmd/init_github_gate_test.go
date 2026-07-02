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

// TestPlannedCommitGate_PlansGeneratedFiles confirms the pre-create gate scans
// the whole planned commit set, rendered from the SAME seedPlan the seeding
// writes ("lockstep with the SeedPlan", F2-4): generated files always, the
// shared seed exactly when the plan carries one.
func TestPlannedCommitGate_PlansGeneratedFiles(t *testing.T) {
	plan := &seedPlan{manifest: sharedManifestBody, shared: []byte("# adopted\nexport EDITOR=vim\n")}
	files := plannedCommitContents(plan)
	for _, want := range []string{"ferry.toml", ".gitignore", "dotfiles/zshrc"} {
		if _, ok := files[want]; !ok {
			t.Errorf("plannedCommitContents missing planned file %q — whole-commit gate would skip it", want)
		}
	}
	if files["dotfiles/zshrc"] != "# adopted\nexport EDITOR=vim\n" {
		t.Errorf("planned dotfiles/zshrc is not the seedPlan's shared bytes: %q", files["dotfiles/zshrc"])
	}
	// A plan with no shared seed plans no zshrc source (declared, no seed).
	if files := plannedCommitContents(declareOnlyPlan("")); len(files) != 2 {
		t.Errorf("declare-only plan planned %d files, want 2 (manifest + gitignore): %v", len(files), files)
	}
}

// UNIT-PHASE arm of AC-github-seedplan / AC-github-secret-extracted: a seedPlan
// BUG leaking a RAW secret into the planned commit still blocks (the retained
// defense-in-depth gate) — and the gate is PURE: abort creates NOTHING (the old
// MkdirAll-on-abort is gone, F3-6).
func TestPlannedCommitGate_BlocksLeakedSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plan := &seedPlan{manifest: sharedManifestBody, shared: []byte("# leaked\n" + fakeGateSecret)}
	err := gateManagedContentBeforeCommit(plan)
	if err == nil {
		t.Fatal("gateManagedContentBeforeCommit passed a seedPlan carrying a raw secret (defense-in-depth gate must block)")
	}
	if !strings.Contains(err.Error(), "dotfiles/zshrc") {
		t.Errorf("gate error does not name the offending planned file: %v", err)
	}
	// Purity: no repo dir (or anything else) materialised under ~/.config/ferry.
	if _, statErr := os.Stat(filepath.Join(home, ".config", "ferry", "repo")); statErr == nil {
		t.Error("gate abort created the repo dir (MkdirAll-on-abort must be removed, F3-6)")
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Errorf("gate abort wrote into HOME: %v", entries)
	}
}
