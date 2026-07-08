package evals

// tmux config plugin (v0.7.0 Phase B) — the include-sidecar carriage and the
// column-grained secret recogniser, exercised through the REAL binary.
//
// tmux rides the existing dotfiles FileDomain as an include-sidecar dotfile: a
// shared ~/.tmux.conf with ferry's injected `source-file -q ~/.tmux.conf.local`
// directive sourced LAST (so the per-machine sidecar wins), plus a
// ~/.tmux.conf.local materialised from local/tmux/tmux.conf.local. These evals
// mirror the zsh byte-stable migration eval and the secret-blocked eval, proving
// the tmux directive round-trips byte-for-byte and a literal `set -g @token`
// value NEVER reaches the shared repo.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tmuxManifest manages .tmux.conf — an include-sidecar dotfile whose shared
// source gains ferry's `source-file -q ~/.tmux.conf.local` directive when an
// overlay exists.
const tmuxManifest = `[manage]
dotfiles = [".tmux.conf"]
brew = false
iterm2 = false
fonts = false
`

// tmuxSecret is a GitHub-token-shaped High-confidence secret (no placeholder
// words like "example", so IsNonPlaceholderSecret accepts it as a real secret).
const tmuxSecret = "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"

// TestTmux_RoundTripByteStable mirrors the zsh byte-stable migration eval for
// tmux: apply materialises the include split (a shared ~/.tmux.conf ending in
// `source-file -q ~/.tmux.conf.local`, plus the ~/.tmux.conf.local sidecar), and
// a no-op capture + status leaves BOTH home files and BOTH repo sources
// byte-, mode-, size- AND mtime-identical, reporting clean.
func TestTmux_RoundTripByteStable(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, tmuxManifest)

	const sharedTmux = "# managed tmux.conf\nset -g mouse on\nset -g history-limit 10000\n"
	s.WriteRepoFile(t, filepath.Join("dotfiles", "tmux.conf"), sharedTmux)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".tmux.conf"), sharedTmux)

	// The overlay's presence is what makes apply materialise the include SPLIT.
	const overlayTmux = "# per-machine tmux\nset -g status-bg blue\n"
	s.WriteRepoFile(t, filepath.Join("local", "tmux", "tmux.conf.local"), overlayTmux)

	gitCommitAll(t, s.Repo, "baseline tmux + overlay")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	sharedTarget := s.HomePath(".tmux.conf")
	sidecarTarget := s.HomePath(".tmux.conf.local")

	// The include split must have materialised (both files exist) and the shared
	// file must carry ferry's tmux directive — else the byte-stability claim is
	// vacuous.
	got, err := os.ReadFile(sharedTarget)
	if err != nil {
		t.Fatalf("apply did not deploy ~/.tmux.conf: %v", err)
	}
	if !strings.Contains(string(got), "source-file -q ~/.tmux.conf.local") {
		t.Fatalf("~/.tmux.conf is missing ferry's injected tmux directive:\n%s", got)
	}
	sidecar, err := os.ReadFile(sidecarTarget)
	if err != nil {
		t.Fatalf("apply did not materialise the ~/.tmux.conf.local sidecar: %v", err)
	}
	if !strings.Contains(string(sidecar), "status-bg blue") {
		t.Fatalf("~/.tmux.conf.local sidecar missing the overlay content:\n%s", sidecar)
	}

	sharedTW := s.SnapshotFile(t, sharedTarget)
	sidecarTW := s.SnapshotFile(t, sidecarTarget)
	repoSharedTW := s.SnapshotFile(t, s.RepoPath("dotfiles", ".tmux.conf"))
	repoOverlayTW := s.SnapshotFile(t, s.RepoPath("local", "tmux", "tmux.conf.local"))

	// No-op capture (nothing drifted): must not rewrite any home file.
	if _, errOut, code := s.Ferry("capture"); code != 0 {
		t.Fatalf("no-op capture exited %d; stderr:\n%s", code, errOut)
	}

	statusOut, statusErr, statusCode := s.Ferry("status")
	statusCombined := statusOut + statusErr
	if statusCode != 0 {
		t.Errorf("clean `status` exited %d (want 0)\n%s", statusCode, statusCombined)
	}
	if !containsAnyFold(statusCombined,
		"no drift", "clean", "up to date", "up-to-date", "no changes", "nothing to", "in sync") {
		t.Errorf("clean `status` gave no positive clean/no-drift signal after a no-op capture\n%s", statusCombined)
	}

	// Load-bearing: both halves of the split and both repo sources are unchanged.
	sharedTW.AssertUnchanged(t)
	sidecarTW.AssertUnchanged(t)
	repoSharedTW.AssertUnchanged(t)
	repoOverlayTW.AssertUnchanged(t)
}

// TestTmux_SecretNeverReachesRepo is the load-bearing secret gate for tmux: a
// literal `set -g @token '<secret>'` value edited into the live ~/.tmux.conf is
// routed to the out-of-repo secret store as a column-grained span (the
// `set -g @token '…'` syntax preserved), a placeholder is committed instead, and
// a subsequent deploy renders the literal token back verbatim.
func TestTmux_SecretNeverReachesRepo(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, tmuxManifest)

	const sharedTmux = "# managed tmux.conf\nset -g mouse on\n"
	s.WriteRepoFile(t, filepath.Join("dotfiles", "tmux.conf"), sharedTmux)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".tmux.conf"), sharedTmux)
	gitCommitAll(t, s.Repo, "baseline tmux")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}

	// Edit the live file: add a literal secret inside a quoted tmux option value.
	secretLine := "set -g @token '" + tmuxSecret + "'\n"
	if err := os.WriteFile(s.HomePath(".tmux.conf"), []byte(sharedTmux+secretLine), 0o644); err != nil {
		t.Fatalf("stage tmux secret: %v", err)
	}

	// Accept the drifted hunk (y), route the blocked secret span to the store (x),
	// then route the (now placeholder-bearing) change to shared (s).
	out, errOut, _ := s.FerryWithInput("y\nx\ns\n", "capture")
	combined := out + errOut

	// HARD: the literal secret bytes never land in any repo file or git history.
	s.AssertNoSecretInRepo(t, tmuxSecret)

	// The shared repo source must carry a ferry placeholder in the option line —
	// the `set -g @token` prefix and the quotes preserved, only the value swapped.
	repoShared, err := os.ReadFile(s.RepoPath("dotfiles", "tmux.conf"))
	if err != nil {
		t.Fatalf("read repo shared tmux.conf: %v", err)
	}
	if !strings.Contains(string(repoShared), "set -g @token '{{ferry.secret") {
		t.Errorf("repo shared tmux.conf did not gain a column-grained placeholder in the option line:\n%s\ncapture output:\n%s", repoShared, combined)
	}
	if strings.Contains(string(repoShared), tmuxSecret) {
		t.Fatalf("the literal secret reached the shared repo source:\n%s", repoShared)
	}

	// After a full span-store capture, status must read CLEAN: last-applied
	// records the RENDERED-effective hash, so the still-live token is not
	// reported as spurious drift against the placeholder-bearing repo source.
	statusOut, statusErr, _ := s.Ferry("status")
	statusCombined := statusOut + statusErr
	if !containsAnyFold(statusCombined,
		"no drift", "clean", "up to date", "up-to-date", "no changes", "nothing to", "in sync") {
		t.Errorf("status did not read clean after a span-store capture (last-applied render regression?)\n%s", statusCombined)
	}

	// Deploy renders the placeholder back to the literal token: remove the live
	// file so apply re-materialises it fresh from the (placeholder-bearing) repo.
	if err := os.Remove(s.HomePath(".tmux.conf")); err != nil {
		t.Fatalf("remove live tmux.conf: %v", err)
	}
	// A secret-routed target is always risky (its deployed bytes hold a plaintext
	// secret), so confirm the guided-apply risk gate on the redeploy.
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("redeploy apply exited %d; stderr:\n%s", code, errOut)
	}
	deployed, err := os.ReadFile(s.HomePath(".tmux.conf"))
	if err != nil {
		t.Fatalf("read redeployed ~/.tmux.conf: %v", err)
	}
	if !strings.Contains(string(deployed), secretLine) {
		t.Errorf("deploy did not render the secret back literally into ~/.tmux.conf:\n%s", deployed)
	}
}

// TestTmux_EnvRefLeftVerbatim is the negative to the secret gate: a
// `set -g @token '${TMUX_TOKEN}'` env-ref is NOT a literal secret (tmux expands
// it at read time), so it is never gate-blocked and reaches the shared repo
// verbatim — the ${...} exemption the recogniser inherits from
// secret.IsNonPlaceholderSecret.
func TestTmux_EnvRefLeftVerbatim(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, tmuxManifest)

	const sharedTmux = "# managed tmux.conf\nset -g mouse on\n"
	s.WriteRepoFile(t, filepath.Join("dotfiles", "tmux.conf"), sharedTmux)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".tmux.conf"), sharedTmux)
	gitCommitAll(t, s.Repo, "baseline tmux")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}

	const envRef = "${TMUX_TOKEN}"
	envLine := "set -g @token '" + envRef + "'\n"
	if err := os.WriteFile(s.HomePath(".tmux.conf"), []byte(sharedTmux+envLine), 0o644); err != nil {
		t.Fatalf("stage tmux env-ref: %v", err)
	}

	// The env-ref is not a secret: accept the hunk (y) and route to shared (s).
	s.FerryWithInput("y\ns\n", "capture")

	repoShared, err := os.ReadFile(s.RepoPath("dotfiles", "tmux.conf"))
	if err != nil {
		t.Fatalf("read repo shared tmux.conf: %v", err)
	}
	if !strings.Contains(string(repoShared), envLine) {
		t.Errorf("the ${ENV} env-ref was not carried to the shared repo verbatim:\n%s", repoShared)
	}
}
