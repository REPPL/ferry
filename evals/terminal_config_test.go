package evals

// W3 — config-file terminal emulators carried like dotfiles. These evals drive
// the real binary and assert observable outcomes: a directory terminal
// (alacritty) and a single-file terminal (wezterm) deploy from the repo's
// terminals/ area to their $HOME destinations when the domain is in scope; the
// domain is gated by [manage] terminals; and the per-machine .local overlay
// wins per file (the per-machine colour-scheme case).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// terminalsManifest enables the config-file terminal domain plus the dotfiles
// domain (so scope has a second domain to prove independence).
const terminalsManifest = `[manage]
dotfiles = [".zshrc"]
terminals = true
brew = false
iterm2 = false
fonts = false
`

// TestTerminalConfigApplies proves an in-scope directory terminal and
// single-file terminal both deploy from terminals/ to their home paths.
func TestTerminalConfigApplies(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, terminalsManifest)

	// A directory terminal (alacritty) with a nested theme file, and a
	// single-file terminal (wezterm) at ~/.wezterm.lua.
	s.WriteRepoFile(t, filepath.Join("terminals", "alacritty", "alacritty.toml"), "# alacritty shared\n")
	s.WriteRepoFile(t, filepath.Join("terminals", "alacritty", "themes", "dark.toml"), "# dark theme\n")
	s.WriteRepoFile(t, filepath.Join("terminals", "wezterm.lua"), "-- wezterm shared\n")
	gitCommitAll(t, s.Repo, "terminals")

	// Nothing risky here (all create-where-absent), so a plain apply suffices.
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	assertFileContains(t, s.HomePath(".config", "alacritty", "alacritty.toml"), "alacritty shared")
	assertFileContains(t, s.HomePath(".config", "alacritty", "themes", "dark.toml"), "dark theme")
	assertFileContains(t, s.HomePath(".wezterm.lua"), "wezterm shared")
}

// TestTerminalConfigScopeGated proves the domain is OFF by default: with no
// `terminals` key in scope, a repo terminal config is never deployed.
func TestTerminalConfigScopeGated(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest) // no `terminals` key
	s.WriteRepoFile(t, filepath.Join("terminals", "alacritty", "alacritty.toml"), "# should not deploy\n")
	gitCommitAll(t, s.Repo, "terminals not in scope")

	target := s.HomePath(".config", "alacritty", "alacritty.toml")
	tw := s.SnapshotFile(t, target) // absent
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	tw.AssertUnchanged(t) // still absent — domain out of scope
}

// TestTerminalConfigLocalOverlayWins proves the per-machine .local overlay
// (local/terminals/<source>/<relpath>) overrides the shared repo copy for just
// that file — the per-machine colour-scheme case — while a non-overridden file
// still deploys the shared content.
func TestTerminalConfigLocalOverlayWins(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, terminalsManifest)

	s.WriteRepoFile(t, filepath.Join("terminals", "alacritty", "alacritty.toml"), "# shared base\n")
	s.WriteRepoFile(t, filepath.Join("terminals", "alacritty", "themes", "colours.toml"), "SHARED_COLOURS\n")
	// Per-machine override of ONLY the colour scheme.
	s.WriteRepoFile(t, filepath.Join("local", "terminals", "alacritty", "themes", "colours.toml"), "MACHINE_COLOURS\n")
	gitCommitAll(t, s.Repo, "terminals with local overlay")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	// The overridden file carries the machine-local content; the base file the shared.
	assertFileContains(t, s.HomePath(".config", "alacritty", "themes", "colours.toml"), "MACHINE_COLOURS")
	assertFileExcludes(t, s.HomePath(".config", "alacritty", "themes", "colours.toml"), "SHARED_COLOURS")
	assertFileContains(t, s.HomePath(".config", "alacritty", "alacritty.toml"), "shared base")
}

// TestTerminalConfigRiskGateFailsClosed proves the guided risk gate covers the
// terminal domain: once ferry has deployed a terminal config, a NON-INTERACTIVE
// re-apply that would overwrite a locally-modified copy fails closed (non-zero
// exit) and leaves the live file untouched — the same safety rail dotfiles get.
func TestTerminalConfigRiskGateFailsClosed(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, terminalsManifest)
	s.WriteRepoFile(t, filepath.Join("terminals", "wezterm.lua"), "-- v1\n")
	gitCommitAll(t, s.Repo, "wezterm v1")

	// First apply establishes the deployed baseline (create where absent = safe).
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("first apply exited %d; stderr:\n%s", code, errOut)
	}
	target := s.HomePath(".wezterm.lua")
	assertFileContains(t, target, "v1")

	// The user edits the live file, and the repo also advances — a true divergence.
	if err := os.WriteFile(target, []byte("-- LOCAL EDIT\n"), 0o644); err != nil {
		t.Fatalf("local edit: %v", err)
	}
	s.WriteRepoFile(t, filepath.Join("terminals", "wezterm.lua"), "-- v2\n")
	gitCommitAll(t, s.Repo, "wezterm v2")

	tw := s.SnapshotFile(t, target)
	// Non-interactive apply (empty stdin) must NOT overwrite the local edit.
	_, _, code := s.Ferry("apply")
	if code == 0 {
		t.Errorf("non-interactive apply over a locally-modified terminal config should fail closed (non-zero), got exit 0")
	}
	tw.AssertUnchanged(t) // the local edit survives — nothing risky applied unattended
}

// TestTerminalConfigCleanReapplyNoDescopeWarning proves a clean idempotent
// re-apply of an in-scope terminal domain does NOT falsely warn that its
// targets are "no longer managed". Terminal targets are keyed under the
// terminals/ prefix in the shared last-applied store; the dotfile de-scope pass
// must skip them (the terminals domain reports its own de-scope), or every
// still-managed terminal target is reported as de-scoped on every apply.
func TestTerminalConfigCleanReapplyNoDescopeWarning(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, terminalsManifest)
	s.WriteRepoFile(t, filepath.Join("terminals", "wezterm.lua"), "-- wezterm shared\n")
	gitCommitAll(t, s.Repo, "wezterm")

	// First apply deploys the target (create where absent = safe).
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("first apply exited %d; stderr:\n%s", code, errOut)
	}
	assertFileContains(t, s.HomePath(".wezterm.lua"), "wezterm shared")

	// Second apply with the domain STILL in scope and the file STILL in the repo
	// is a clean no-op — no de-scope warning may appear.
	stdout, errOut, code := s.Ferry("apply")
	if code != 0 {
		t.Fatalf("second apply exited %d; stderr:\n%s", code, errOut)
	}
	if strings.Contains(stdout, "no longer managed") || strings.Contains(stdout, "no longer part of the terminals plan") {
		t.Errorf("clean re-apply falsely warned a managed terminal target is de-scoped; stdout:\n%s", stdout)
	}
}

// TestTerminalConfigDriftGuidanceNoCapture proves a locally-drifted terminal
// target's preview guidance points at the repo source / `ferry apply --force`,
// NOT at `ferry capture`: capture has no config-file terminal pass, so that
// guidance would send the user to a command that does nothing for this domain.
func TestTerminalConfigDriftGuidanceNoCapture(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, terminalsManifest)
	s.WriteRepoFile(t, filepath.Join("terminals", "wezterm.lua"), "-- shared\n")
	gitCommitAll(t, s.Repo, "wezterm")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	// Drift the live file only (no repo change) -> StateLocallyDrifted.
	if err := os.WriteFile(s.HomePath(".wezterm.lua"), []byte("-- LOCAL EDIT\n"), 0o644); err != nil {
		t.Fatalf("local edit: %v", err)
	}

	stdout, errOut, code := s.Ferry("diff")
	if code != 0 {
		t.Fatalf("diff exited %d; stderr:\n%s", code, errOut)
	}
	// The wezterm line must surface the drift but never send the user to capture.
	line := ""
	for _, l := range strings.Split(stdout, "\n") {
		if strings.Contains(l, "wezterm") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("no wezterm line in diff output:\n%s", stdout)
	}
	if strings.Contains(line, "ferry capture") {
		t.Errorf("drifted terminal target points at `ferry capture` (no config-file terminal capture pass exists); line:\n%s", line)
	}
	if !strings.Contains(line, "ferry apply --force") {
		t.Errorf("drifted terminal target should point at `ferry apply --force`; line:\n%s", line)
	}
}

func assertFileContains(t *testing.T, path, needle string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
	if !strings.Contains(string(data), needle) {
		t.Errorf("%s does not contain %q; got:\n%s", path, needle, data)
	}
}

func assertFileExcludes(t *testing.T, path, needle string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
	if strings.Contains(string(data), needle) {
		t.Errorf("%s unexpectedly contains %q; got:\n%s", path, needle, data)
	}
}
