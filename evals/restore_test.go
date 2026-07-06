package evals

// Restore-behaviour ACs. apply -> restore must leave no trace: previously-present
// targets return byte-identical (with mode), previously-absent targets are absent
// again, and ferry-created files are removed.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRestoreClean covers AC-restore-clean: after apply then restore, every
// managed path returns to its exact pre-ferry state.
func TestRestoreClean_AC_restore_clean(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, `[manage]
dotfiles = [".zshrc", ".gitconfig"]
`)
	// Repo sources for two managed dotfiles.
	for _, name := range []string{".zshrc", ".gitconfig"} {
		s.WriteRepoFile(t, name, "# ferry-managed "+name+"\n")
		s.WriteRepoFile(t, filepath.Join("dotfiles", name), "# ferry-managed "+name+"\n")
	}

	// Pre-ferry state: .zshrc exists with original content; .gitconfig is ABSENT.
	originalZsh := "# my original zshrc, pre-ferry\n"
	zshTarget := s.WriteHomeFile(t, ".zshrc", originalZsh, 0o600) // note non-default mode
	zshSnap := s.SnapshotFile(t, zshTarget)

	gitconfigTarget := s.HomePath(".gitconfig")
	gitconfigSnap := s.SnapshotFile(t, gitconfigTarget) // absent

	// Apply (adopts .zshrc, creates .gitconfig), then restore. Adopting the
	// pre-existing .zshrc is risky, so confirm the guided walkthrough.
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("AC-restore-clean: apply exited %d; stderr:\n%s", code, errOut)
	}
	// Sanity: apply actually did something (otherwise the test is vacuous).
	if cur := s.SnapshotFile(t, gitconfigTarget); !cur.exists {
		t.Fatalf("AC-restore-clean: precondition failed — apply did not create .gitconfig, nothing to restore")
	}

	if _, errOut, code := s.Ferry("restore"); code != 0 {
		t.Fatalf("AC-restore-clean: restore exited %d; stderr:\n%s", code, errOut)
	}

	// Previously-present target: byte-identical with original mode.
	zshSnap.AssertUnchanged(t)
	// Previously-absent target: absent again (ferry-created file removed).
	gitconfigSnap.AssertUnchanged(t)
}

// TestRestoreRoundTrip covers AC-backup-before-change (round-trip half) at the
// restore boundary: a managed file present with content X before apply is X again
// after restore, proving restore consumed the automatic backup.
func TestRestoreRoundTrip_AC_backup_before_change(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "export VERSION=ferry\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "export VERSION=ferry\n")

	originalX := "export VERSION=original\n"
	// A deliberately non-default mode so the mode-preservation check is meaningful.
	const originalMode os.FileMode = 0o600
	target := s.WriteHomeFile(t, ".zshrc", originalX, originalMode)

	// Overwriting the pre-existing ~/.zshrc is risky; confirm the walkthrough.
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("AC-backup-before-change: apply exited %d; stderr:\n%s", code, errOut)
	}
	// apply should have changed the content away from X.
	if cur, _ := os.ReadFile(target); string(cur) == originalX {
		t.Fatalf("AC-backup-before-change: precondition failed — apply did not change the managed file")
	}

	if _, errOut, code := s.Ferry("restore"); code != 0 {
		t.Fatalf("AC-backup-before-change: restore exited %d; stderr:\n%s", code, errOut)
	}
	// CONTENT preserved.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("AC-backup-before-change: target missing after restore: %v", err)
	}
	if string(got) != originalX {
		t.Errorf("AC-backup-before-change: after restore content = %q, want original X %q", got, originalX)
	}
	// MODE preserved (the backup must restore the original mode, not a default).
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("AC-backup-before-change: stat after restore: %v", err)
	}
	if info.Mode().Perm() != originalMode.Perm() {
		t.Errorf("AC-backup-before-change: after restore mode = %o, want original %o", info.Mode().Perm(), originalMode.Perm())
	}
}
