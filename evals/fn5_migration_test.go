package evals

// fn-5 domain convergence — the byte-stable migration safety net.
//
// This eval is the guard the fn-5 `isZsh()` -> registry cutover must not
// regress. It proves that the CURRENT (pre-cutover) zsh two-strip + include-
// sidecar round-trip is byte-STABLE (content, mode, size AND mtime identical)
// across a deploy -> capture -> status cycle with no intervening user change:
//
//   - apply materialises the include-style split — a shared ~/.zshrc ending in a
//     `source ~/.zshrc.local` directive, and the ~/.zshrc.local sidecar itself.
//   - a no-op capture (nothing drifted) followed by status must leave BOTH home
//     files byte-identical (the mtime dimension makes it byte-STABLE, not merely
//     byte-equal — a same-bytes rewrite would fail) and report a clean/no-drift
//     status.
//
// Recorded now against current behaviour so the fn-5 port can re-run it and
// prove the sidecar / two-strip contract survived the convergence unchanged.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFn5_ZshRoundTripByteStable_Baseline records the current zsh include-sidecar
// round-trip as byte-stable, before the isZsh -> registry cutover.
func TestFn5_ZshRoundTripByteStable_Baseline(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)

	// Scope: .zshrc managed (baseManifest declares dotfiles = [".zshrc"]).
	s.SeedSharedManifest(t, baseManifest)

	// Shared zsh source (seed both candidate layouts so the eval is not brittle
	// to the exact repo convention the implementation resolves).
	const sharedZsh = "# managed zshrc\nexport EDITOR=vim\nexport PAGER=less\n"
	s.WriteRepoFile(t, ".zshrc", sharedZsh)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), sharedZsh)

	// Per-machine overlay: its presence is what makes apply materialise the
	// include-style SPLIT — the shared ~/.zshrc gains a trailing
	// `source ~/.zshrc.local` and the ~/.zshrc.local sidecar is written.
	const overlayZsh = "# per-machine overlay\nexport FROM_OVERLAY=1\n"
	s.WriteRepoFile(t, filepath.Join("local", "zsh", "zshrc.local"), overlayZsh)

	gitCommitAll(t, s.Repo, "baseline zsh + overlay")

	// Deploy. Both home targets are absent, so this is a fresh create (no risk
	// gate) — plain apply.
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	sharedTarget := s.HomePath(".zshrc")
	sidecarTarget := s.HomePath(".zshrc.local")

	// Sanity: the include-style split actually materialised (both files exist).
	// Without both, the byte-stability claim below would be vacuous.
	if _, err := os.Stat(sharedTarget); err != nil {
		t.Fatalf("apply did not deploy ~/.zshrc: %v", err)
	}
	if _, err := os.Stat(sidecarTarget); err != nil {
		t.Fatalf("apply did not materialise the ~/.zshrc.local sidecar: %v (the include split did not form)", err)
	}

	// Snapshot BOTH halves of the split immediately after deploy — content, mode,
	// size AND mtime. The mtime dimension is what makes this byte-STABLE.
	sharedTW := s.SnapshotFile(t, sharedTarget)
	sidecarTW := s.SnapshotFile(t, sidecarTarget)
	// Also snapshot the repo sources: a byte-stable round-trip must not rewrite
	// the committed shared source or the overlay either.
	repoSharedTW := s.SnapshotFile(t, s.RepoPath("dotfiles", ".zshrc"))
	repoOverlayTW := s.SnapshotFile(t, s.RepoPath("local", "zsh", "zshrc.local"))

	// No-op capture: nothing drifted, so capture offers nothing and must not
	// rewrite either home file. Empty stdin => any prompt sees EOF (the harness
	// 30s timeout Fatals on a hang).
	if _, errOut, code := s.Ferry("capture"); code != 0 {
		t.Fatalf("no-op capture exited %d; stderr:\n%s", code, errOut)
	}

	// status must report a POSITIVE clean/no-drift signal and exit 0 (absence of
	// a drift token alone is not enough — a silent status would falsely pass).
	statusOut, statusErr, statusCode := s.Ferry("status")
	statusCombined := statusOut + statusErr
	if statusCode != 0 {
		t.Errorf("clean `status` exited %d (want 0)\n%s", statusCode, statusCombined)
	}
	if !containsAnyFold(statusCombined,
		"no drift", "clean", "up to date", "up-to-date", "no changes", "nothing to", "in sync") {
		t.Errorf("clean `status` gave no positive clean/no-drift signal after a no-op capture\n%s", statusCombined)
	}

	// The load-bearing assertion: both halves of the include split are byte-,
	// mode-, size- and mtime-identical to the post-apply snapshot — the two-strip
	// + sidecar round-trip did not touch them.
	sharedTW.AssertUnchanged(t)
	sidecarTW.AssertUnchanged(t)
	// And the repo sources were not rewritten by the no-op capture.
	repoSharedTW.AssertUnchanged(t)
	repoOverlayTW.AssertUnchanged(t)
}
