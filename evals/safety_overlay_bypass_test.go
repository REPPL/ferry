package evals

// v0.2.1 data-loss SAFETY eval — the empty-over-substantial OVERLAY BYPASS.
//
// The empty-over-substantial guard reads the EFFECTIVE staged content. For the
// zsh include path, apply stages the shared source WITH ferry's injected
// `[ -f ~/.zshrc.local ] && source ~/.zshrc.local` directive appended (because a
// per-machine overlay exists). When the shared source is empty/comment-only, that
// injected `source` line is the only non-comment content, so a naive near-empty
// check reads FALSE and the guard stands down — letting a default `ferry apply`
// (no --force) REPLACE a substantial live ~/.zshrc with ferry's ~2-line stub. This
// is the same silent-config-erasure class as the original incident, reached via
// the overlay route.
//
// These drive the REAL binary through the harness and are RED-until-impl: with
// FERRY_BIN unset every s.Ferry* call skips. Each eval asserts a user-observable
// outcome (file bytes/mode/mtime, exit code, message wording), never a source proxy.
//
// Coverage:
//   - AC-apply-refuses-empty-over-substantial (overlay route): empty/comment-only
//     shared zsh source + a per-machine overlay + a substantial live ~/.zshrc ->
//     default apply REFUSES, naming the file and both sides of the hazard; the live
//     file is byte-/mode-/mtime-unchanged; restore stays clean.
//   - AC-apply-force-allows-with-warning (overlay route): --force proceeds but WARNS
//     naming the file and both sides of the hazard; restore reverts.
//   - Guard does NOT false-fire: a REAL-content shared source + an overlay deploys
//     normally (the injected directive alone must not be mistaken for user content).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// overlayManifest manages .zshrc — the include-style domain whose shared source
// gets ferry's `source ~/.zshrc.local` directive injected when an overlay exists.
const overlayManifest = `[manage]
dotfiles = [".zshrc"]
brew = false
iterm2 = false
fonts = false
`

// seedZshOverlay writes the shared zsh source (canonical + dotted layouts) plus a
// per-machine overlay at local/zsh/zshrc.local, so apply takes the include path
// and injects the overlay `source` directive into the staged shared content.
func seedZshOverlay(t *testing.T, s *Sandbox, shared, overlay string) {
	t.Helper()
	s.WriteRepoFile(t, filepath.Join("dotfiles", "zshrc"), shared)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), shared)
	s.WriteRepoFile(t, filepath.Join("local", "zsh", "zshrc.local"), overlay)
}

// TestApplyRefusesEmptyOverSubstantialViaOverlay covers the overlay bypass on the
// no-force path: an empty/comment-only shared zsh source + an overlay must NOT let
// apply zero a substantial live ~/.zshrc. The refusal must be observable and name
// the file plus both sides of the empty-over-substantial hazard.
func TestApplyRefusesEmptyOverSubstantialViaOverlay_AC_apply_refuses_empty_over_substantial(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		shared string // near-empty shared source (empty / comment-only)
	}{
		{"empty-shared", ""},
		{"comment-only-shared", "# managed by ferry\n# per-machine overlay lives in local/zsh\n"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			s.SeedSharedManifest(t, overlayManifest)
			// Near-empty shared source + a real per-machine overlay -> apply injects
			// `source ~/.zshrc.local` into the staged shared content.
			seedZshOverlay(t, s, tc.shared, "export FERRY_LOCAL=1\n")

			// The substantial live ~/.zshrc that must NOT be zeroed.
			target := s.WriteHomeFile(t, ".zshrc", substantialZshrc, 0o644)
			snap := s.SnapshotFile(t, target)

			out, errOut, code := s.Ferry("apply")
			combined := out + errOut

			namesFile := containsAnyFold(combined, ".zshrc", "zshrc")
			namesEmptySide := containsAnyFold(combined, "empty", "blank")
			namesDestructiveSide := containsAnyFold(combined, "erase", "wipe", "zero", "delete", "destroy", "substantial", "overwrit", "clobber", "existing content")
			if !(namesFile && namesEmptySide && namesDestructiveSide) {
				t.Errorf("overlay bypass[%s]: refusal did not name the file AND both sides of the empty-over-substantial hazard\nexit=%d\n%s", tc.name, code, combined)
			}

			// Load-bearing: the live file is byte-, mode-, mtime-identical.
			snap.AssertUnchanged(t)

			// A refused apply left the file at its pre-ferry baseline; restore stays clean.
			if _, rErr, rCode := s.Ferry("restore"); rCode != 0 {
				t.Errorf("overlay bypass[%s]: restore exited %d after a refused apply\n%s", tc.name, rCode, rErr)
			}
			snap.AssertUnchanged(t)
		})
	}
}

// TestApplyForceOverlayAllowsWithWarning covers the overlay bypass on the --force
// path: --force may proceed with the empty-over-substantial overwrite, but MUST
// warn (naming the file and both sides of the hazard). Reversibility is checked.
func TestApplyForceOverlayAllowsWithWarning_AC_apply_force_allows_with_warning(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, overlayManifest)
	seedZshOverlay(t, s, "", "export FERRY_LOCAL=1\n")

	target := s.WriteHomeFile(t, ".zshrc", substantialZshrc, 0o644)
	preSnap := s.SnapshotFile(t, target)

	out, errOut, code := s.Ferry("apply", "--force")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("apply --force exited %d (force should proceed)\n%s", code, errOut)
	}

	// The overwrite proceeded: the live file no longer carries the substantial config.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target after --force: %v", err)
	}
	if strings.Contains(string(got), "alias gs='git status'") {
		t.Errorf("--force did not replace the substantial live ~/.zshrc (still %d bytes)", len(got))
	}

	warnNamesFile := containsAnyFold(combined, ".zshrc", "zshrc")
	warnEmptySide := containsAnyFold(combined, "empty", "blank")
	warnDestructiveSide := containsAnyFold(combined, "erase", "wipe", "zero", "delete", "destroy", "substantial", "overwrit", "clobber", "existing content")
	if !(warnNamesFile && warnEmptySide && warnDestructiveSide) {
		t.Errorf("--force overlay warning did not name the file AND flag both sides of the hazard\n%s", combined)
	}

	// Reversibility: the pre-force content was backed up, so restore returns it.
	if _, errOut, code := s.Ferry("restore"); code != 0 {
		t.Fatalf("restore exited %d\n%s", code, errOut)
	}
	preSnap.AssertUnchanged(t)
}

// TestApplyOverlayRealSourceDeploysNormally proves the fix does not false-fire:
// a REAL-content shared source + an overlay is a legitimate deploy. Stripping
// ferry's injected directive must leave the user's real lines, so the guard stands
// down and apply proceeds (exit 0). This pins that the fix only excludes ferry's
// own boilerplate, never real user content.
func TestApplyOverlayRealSourceDeploysNormally(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, overlayManifest)
	// A REAL shared source (substantial user content) + an overlay.
	seedZshOverlay(t, s, substantialZshrc, "export FERRY_LOCAL=1\n")

	// A pre-existing live file (also substantial) that apply legitimately updates.
	s.WriteHomeFile(t, ".zshrc", "export OLD=1\nalias x='echo old'\nsetopt SHARE_HISTORY\n", 0o644)

	out, errOut, code := s.Ferry("apply")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("apply of a real-content shared source + overlay was refused (guard false-fired); exit=%d\n%s", code, combined)
	}

	// The deploy happened: the live ~/.zshrc now carries the shared source content
	// AND ferry's injected overlay include.
	got, err := os.ReadFile(s.HomePath(".zshrc"))
	if err != nil {
		t.Fatalf("read ~/.zshrc after apply: %v", err)
	}
	if !strings.Contains(string(got), "alias gs='git status'") {
		t.Errorf("real-content shared source was not deployed to ~/.zshrc")
	}
	if !strings.Contains(string(got), "source ~/.zshrc.local") {
		t.Errorf("ferry's overlay include was not injected into the deployed ~/.zshrc")
	}

	// The materialised sidecar carries the overlay content.
	sidecar, err := os.ReadFile(s.HomePath(".zshrc.local"))
	if err != nil {
		t.Fatalf("read ~/.zshrc.local after apply: %v", err)
	}
	if !strings.Contains(string(sidecar), "FERRY_LOCAL=1") {
		t.Errorf("overlay sidecar ~/.zshrc.local was not materialised with the overlay content")
	}
}
