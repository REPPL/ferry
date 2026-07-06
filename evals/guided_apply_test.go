package evals

// W1 (v0.5.0) guided-apply ACs, driven through the real binary. They prove the
// four contract paths end to end:
//
//   - SAFE AUTO-APPLY: a change that overwrites nothing (create-where-absent, or
//     an update whose live file still matches the last-deployed baseline) applies
//     unattended and exits 0.
//   - RISKY CONFIRM: an overwrite of a pre-existing file halts; a typed "yes"
//     confirms it and it applies.
//   - NON-INTERACTIVE FAIL CLOSED: the same risky change with empty stdin exits
//     non-zero, lists what needs a human, and leaves the file untouched.
//   - --skip-wizard: risky changes are refused (fail closed) while safe changes in
//     the same run still apply.
//
// It also proves the clean-in-sync one-liner and the skip-always exclusion in the
// .local layer.

import (
	"os"
	"path/filepath"
	"testing"
)

// guidedRiskyManifest manages one dotfile whose home path already holds
// user-authored content ferry never wrote — a risky first-touch adoption.
const guidedRiskyManifest = "[manage]\ndotfiles = [\".gitconfig\"]\n"

// seedRiskyAdoption seeds a repo .gitconfig plus a DIFFERENT pre-existing live
// ~/.gitconfig, so the first apply is a risky overwrite of a pre-existing file.
// Returns the live target path and the exact repo content apply would deploy.
func seedRiskyAdoption(t *testing.T, s *Sandbox) (target, repoContent, liveContent string) {
	t.Helper()
	s.SeedSharedManifest(t, guidedRiskyManifest)
	repoContent = "[user]\n\tname = Repo Managed\n\temail = managed@example.com\n"
	s.WriteRepoFile(t, ".gitconfig", repoContent)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".gitconfig"), repoContent)
	liveContent = "[user]\n\tname = My Pre-Ferry Name\n\temail = me@personal.example\n"
	target = s.WriteHomeFile(t, ".gitconfig", liveContent, 0o644)
	return target, repoContent, liveContent
}

// TestGuidedSafeAutoApply proves the SAFE AUTO-APPLY path: a create-where-absent
// change and a subsequent baseline-matching update both apply unattended (empty
// stdin), exit 0, and never prompt.
func TestGuidedSafeAutoApply(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "export EDITOR=vim\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "export EDITOR=vim\n")

	// (1) create-where-absent is SAFE: applies unattended.
	target := s.HomePath(".zshrc")
	if out, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("safe create: apply exited %d (should auto-apply)\nstdout:%s\nstderr:%s", code, out, errOut)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "export EDITOR=vim\n" {
		t.Fatalf("safe create: ~/.zshrc = %q (err %v), want the repo content", got, err)
	}

	// (2) a repo-ahead UPDATE whose live file still matches the last-deployed
	// baseline is SAFE too: applies unattended.
	s.WriteRepoFile(t, ".zshrc", "export EDITOR=nvim\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "export EDITOR=nvim\n")
	if out, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("safe update: apply exited %d (should auto-apply)\nstdout:%s\nstderr:%s", code, out, errOut)
	}
	if got, _ := os.ReadFile(target); string(got) != "export EDITOR=nvim\n" {
		t.Errorf("safe update: ~/.zshrc = %q, want the updated repo content", got)
	}
}

// TestGuidedCleanInSyncOneLine proves a clean, in-sync apply prints one line and
// does not walk.
func TestGuidedCleanInSyncOneLine(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "export EDITOR=vim\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "export EDITOR=vim\n")
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("first apply exited %d; %s", code, errOut)
	}
	// Second apply: nothing changed -> one-line in-sync report, exit 0.
	out, errOut, code := s.Ferry("apply")
	if code != 0 {
		t.Fatalf("clean re-apply exited %d; %s", code, errOut)
	}
	if !containsAnyFold(out+errOut, "in sync", "already match", "nothing to apply") {
		t.Errorf("clean re-apply did not print the in-sync one-liner\n%s", out+errOut)
	}
}

// TestGuidedRiskyConfirm proves the RISKY CONFIRM path: an overwrite of a
// pre-existing file halts, and a typed "yes" confirms it so it applies.
func TestGuidedRiskyConfirm(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	target, repoContent, _ := seedRiskyAdoption(t, s)

	out, errOut, code := s.FerryWithInput("yes\n", "apply")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("risky-confirm: apply exited %d after confirmation\n%s", code, combined)
	}
	// The walkthrough surfaced it as needing review.
	if !containsAnyFold(combined, "review", "risky", "overwrite", "adopt") {
		t.Errorf("risky-confirm: walkthrough did not describe the change as risky/review\n%s", combined)
	}
	// Confirmed -> the pre-existing file was overwritten with the repo content.
	if got, _ := os.ReadFile(target); string(got) != repoContent {
		t.Errorf("risky-confirm: ~/.gitconfig = %q, want the repo content %q after confirmation", got, repoContent)
	}
}

// TestGuidedNonInteractiveFailClosed proves the FAIL-CLOSED path: the same risky
// change with empty stdin exits non-zero, lists what needs a human, and leaves the
// file byte-for-byte untouched.
func TestGuidedNonInteractiveFailClosed(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	target, _, _ := seedRiskyAdoption(t, s)
	tw := s.SnapshotFile(t, target)

	out, errOut, code := s.Ferry("apply") // empty stdin == non-interactive
	combined := out + errOut
	if code == 0 {
		t.Errorf("fail-closed: apply exited 0 on a risky change with no confirmation (must fail closed)\n%s", combined)
	}
	// It listed what needs a human and named the target.
	if !containsAnyFold(combined, "refus", "risky", "review") {
		t.Errorf("fail-closed: apply did not report refusing the risky change\n%s", combined)
	}
	if !containsAnyFold(combined, ".gitconfig", "gitconfig") {
		t.Errorf("fail-closed: refusal did not name the target that needs review\n%s", combined)
	}
	// Nothing risky happened unattended: the live file is untouched.
	tw.AssertUnchanged(t)
}

// TestGuidedSkipWizardRefusesRiskyAppliesSafe proves --skip-wizard: risky changes
// are refused (fail closed, non-zero exit) while a SAFE change in the same run
// still applies.
func TestGuidedSkipWizardRefusesRiskyAppliesSafe(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	// Two managed dotfiles: a risky adoption (.gitconfig, pre-existing) and a safe
	// create-where-absent (.zshrc, no live file).
	s.SeedSharedManifest(t, "[manage]\ndotfiles = [\".gitconfig\", \".zshrc\"]\n")
	s.WriteRepoFile(t, ".gitconfig", "[user]\n\tname = Managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".gitconfig"), "[user]\n\tname = Managed\n")
	s.WriteRepoFile(t, ".zshrc", "export EDITOR=vim\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "export EDITOR=vim\n")
	riskyTarget := s.WriteHomeFile(t, ".gitconfig", "[user]\n\tname = Pre-Ferry\n", 0o644)
	riskyTW := s.SnapshotFile(t, riskyTarget)
	safeTarget := s.HomePath(".zshrc") // absent

	out, errOut, code := s.Ferry("apply", "--skip-wizard")
	combined := out + errOut
	// Risky refused -> non-zero exit, risky file untouched.
	if code == 0 {
		t.Errorf("--skip-wizard: exited 0 with a risky change pending (must refuse risky)\n%s", combined)
	}
	riskyTW.AssertUnchanged(t)
	// Safe change STILL applied (quiet when safe).
	if got, err := os.ReadFile(safeTarget); err != nil || string(got) != "export EDITOR=vim\n" {
		t.Errorf("--skip-wizard: the safe create-where-absent .zshrc did not apply (got %q, err %v)", got, err)
	}
}

// TestGuidedSkipAlwaysRemembered proves the skip-always detail action persists to
// the .local layer and suppresses the change on later runs.
func TestGuidedSkipAlwaysRemembered(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	target, _, liveContent := seedRiskyAdoption(t, s)

	// Drill into details and choose skip-always for the risky change.
	if _, errOut, code := s.FerryWithInput("details\nx\n", "apply"); code != 0 {
		// Not applied and not refused -> exit 0 (a deliberate user skip).
		t.Fatalf("skip-always: apply exited %d (expected 0; the change was skipped by choice)\n%s", code, errOut)
	}
	// The exclusion is recorded in the gitignored .local layer.
	skipFile := filepath.Join(s.Repo, "local", "skip-always.txt")
	data, err := os.ReadFile(skipFile)
	if err != nil {
		t.Fatalf("skip-always: %s was not written: %v", skipFile, err)
	}
	if !containsAnyFold(string(data), "gitconfig") {
		t.Errorf("skip-always: %s does not record the target\n%s", skipFile, data)
	}
	// The live file was NOT overwritten.
	if got, _ := os.ReadFile(target); string(got) != liveContent {
		t.Errorf("skip-always: the live file was changed despite skip-always (got %q)", got)
	}
	// A later apply (empty stdin) no longer fails closed on it — the change is
	// skipped, so the run exits 0.
	out, errOut, code := s.Ferry("apply")
	if code != 0 {
		t.Errorf("skip-always: a later apply still failed closed on the excluded change (exit %d)\n%s", code, out+errOut)
	}
	if got, _ := os.ReadFile(target); string(got) != liveContent {
		t.Errorf("skip-always: later apply changed the excluded file (got %q)", got)
	}
}
