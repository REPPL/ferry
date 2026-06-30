package evals

// status & diff behavioral ACs.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStatusReportsDrift covers AC-status-reports-drift: after `apply`, `status`
// reports clean for managed files; editing a managed file makes `status` name that
// file/domain as drifted/changed.
func TestStatusReportsDrift_AC_status_reports_drift(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# managed baseline\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed baseline\n")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-status-reports-drift: apply exited %d; stderr:\n%s", code, errOut)
	}

	// Clean state: status must give a POSITIVE clean/no-drift signal AND exit 0
	// (absence of a drift token is not enough — a silent status would falsely pass).
	cleanOut, cleanErr, cleanCode := s.Ferry("status")
	cleanCombined := cleanOut + cleanErr
	if cleanCode != 0 {
		t.Errorf("AC-status-reports-drift: clean `status` exited %d (want 0)\n%s", cleanCode, cleanCombined)
	}
	if !containsAnyFold(cleanCombined, "no drift", "clean", "up to date", "up-to-date", "no changes", "nothing to", "in sync", "no drift detected") {
		t.Errorf("AC-status-reports-drift: clean `status` gave no positive clean/no-drift signal\n%s", cleanCombined)
	}

	// Edit the managed file -> status must name it as drifted/changed.
	if err := os.WriteFile(s.HomePath(".zshrc"), []byte("# locally drifted\n"), 0o644); err != nil {
		t.Fatalf("edit managed file: %v", err)
	}
	driftOut, driftErr, _ := s.Ferry("status")
	driftCombined := driftOut + driftErr
	namesFile := containsAnyFold(driftCombined, ".zshrc", "zsh")
	saysDrift := containsAnyFold(driftCombined, "drift", "changed", "modified", "dirty")
	if !(namesFile && saysDrift) {
		t.Errorf("AC-status-reports-drift: after editing .zshrc, status did not name it as drifted/changed\n%s", driftCombined)
	}
}

// TestDiffPreviewOnly covers AC-diff-preview-only: `ferry diff` reports the change
// `apply` would make WITHOUT making it (tripwire the target), then `apply` makes
// the predicted change (diff predicted reality).
//
// Cross-cites AC-cmd-diff: the "diff never writes managed files" tripwire below
// also satisfies AC-cmd-diff's gating behavioral assertion (also gated standalone
// in TestDiffNoWrite_AC_cmd_diff in commands_test.go).
func TestDiffPreviewOnly_AC_diff_preview_only(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# to be deployed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# to be deployed\n")

	target := s.HomePath(".zshrc")
	beforeTW := s.SnapshotFile(t, target) // absent

	// diff: describes the pending change, exits 0, target unchanged.
	out, errOut, code := s.Ferry("diff")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("AC-diff-preview-only: diff exited %d; stderr:\n%s", code, errOut)
	}
	// It must describe the SPECIFIC pending change — name the changed file/domain
	// (.zshrc / zsh), not just a generic diff token a blanket message would satisfy.
	if !containsAnyFold(combined, ".zshrc", "zsh") {
		t.Errorf("AC-diff-preview-only: diff did not name the specific pending file/domain (.zshrc)\n%s", combined)
	}
	// And it should also carry a change/action signal for that file (add/create/deploy/would).
	if !containsAnyFold(combined, "would", "add", "creat", "deploy", "new", "+") {
		t.Errorf("AC-diff-preview-only: diff named the file but described no change/action for it\n%s", combined)
	}
	// TRIPWIRE: diff made no change — the target is still absent.
	beforeTW.AssertUnchanged(t)

	// apply makes the predicted change: the target now exists with repo content.
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-diff-preview-only: apply exited %d; stderr:\n%s", code, errOut)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("AC-diff-preview-only: apply did not create the target diff predicted: %v", err)
	}
	if string(got) != "# to be deployed\n" {
		t.Errorf("AC-diff-preview-only: applied content = %q, want the repo source diff previewed", got)
	}
}
