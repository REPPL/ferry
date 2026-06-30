package evals

// AC-scope-bidirectional: the SAME effective manifest governs BOTH apply
// (repo -> machine) and capture (machine -> repo). A domain disabled in scope is
// neither applied nor captured; an enabled domain is both.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScopeBidirectional covers AC-scope-bidirectional: against ONE manifest, a
// disabled domain shows no apply write AND no capture ingest, while an enabled
// domain shows both. Here dotfiles(.zshrc) is enabled; fonts is disabled — the
// same allowlist must make fonts invisible in BOTH directions.
func TestScopeBidirectional_AC_scope_bidirectional(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest) // dotfiles=[".zshrc"], fonts=false
	gitCommitAll(t, s.Repo, "manifest")

	// Repo content for BOTH an enabled dotfile and a DISABLED font domain.
	s.WriteRepoFile(t, ".zshrc", "# enabled domain\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# enabled domain\n")
	s.WriteRepoFile(t, filepath.Join("fonts", "Disabled.ttf"), "DISABLED_FONT_BYTES")

	// --- apply direction ---
	fontTarget := s.HomePath("Library", "Fonts", "Disabled.ttf")
	fontTW := s.SnapshotFile(t, fontTarget) // absent
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-scope-bidirectional: apply exited %d; stderr:\n%s", code, errOut)
	}
	// Enabled domain applied.
	if _, err := os.Stat(s.HomePath(".zshrc")); err != nil {
		t.Errorf("AC-scope-bidirectional[apply]: enabled domain .zshrc not applied: %v", err)
	}
	// Disabled domain not applied.
	fontTW.AssertUnchanged(t)

	// --- capture direction (same manifest) ---
	inMarker := "BIDIR_INSCOPE_MARKER"
	outMarker := "BIDIR_OUTSCOPE_FONT_MARKER"
	if err := os.WriteFile(s.HomePath(".zshrc"), []byte("# zshrc\n"+inMarker+"\n"), 0o644); err != nil {
		t.Fatalf("in-scope edit: %v", err)
	}
	if err := os.MkdirAll(s.HomePath("Library", "Fonts"), 0o755); err != nil {
		t.Fatalf("mkdir fonts: %v", err)
	}
	if err := os.WriteFile(s.HomePath("Library", "Fonts", "New.ttf"), []byte(outMarker), 0o644); err != nil {
		t.Fatalf("out-of-scope font: %v", err)
	}

	// Accept + route shared for everything offered.
	s.FerryWithInput("y\ns\ny\ns\ny\ns\n", "capture")

	// Enabled domain captured; disabled domain never ingested — SAME allowlist.
	if !repoContains(t, s, inMarker) {
		t.Errorf("AC-scope-bidirectional[capture]: enabled domain change was not captured")
	}
	if repoContains(t, s, outMarker) {
		t.Errorf("AC-scope-bidirectional[capture]: disabled domain (fonts) was captured — allowlist not shared across directions")
	}
}
