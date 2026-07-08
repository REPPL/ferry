package evals

// npm `~/.npmrc` config plugin (v0.7.0 Phase B) — exercised through the REAL
// binary. `~/.npmrc` rides the EXISTING flat dotfiles FileDomain as a plain
// whole-file-replace dotfile (no include sidecar, no nesting): a shared
// ~/.npmrc reconciled by hash like any other dotfile. The registry auth token is
// the only hazard, routed through ferry's existing secret pipeline:
//   - a LITERAL `//registry.npmjs.org/:_authToken=npm_…` line is gate-blocked,
//     stored out-of-repo, and only a placeholder reaches the shared repo; a
//     redeploy renders the literal token back so npm sees the real value;
//   - a `${NPM_TOKEN}` env-ref (npm expands the environment at read time) is NOT
//     a literal secret, so it is carried to the shared repo verbatim;
//   - the deployed ~/.npmrc is a regular-file copy (never a symlink), and status
//     reads clean after apply.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// npmrcManifest manages .npmrc as a plain whole-file dotfile (NOT an
// include-sidecar domain — it must never gain a sourced sidecar).
const npmrcManifest = `[manage]
dotfiles = [".npmrc"]
brew = false
iterm2 = false
fonts = false
`

// npmToken is a realistic npm automation token (the `npm_` prefix). It carries no
// placeholder word, so IsNonPlaceholderSecret accepts it as a real secret.
const npmToken = "npm_FAKE0000aaaa1111bbbb2222cccc3333dddd"

// seedNpmrc writes the manifest and a shared, secret-FREE ~/.npmrc source in both
// repo layout paths (dot and dot-stripped), matching the git/tmux eval seeds.
func seedNpmrc(t *testing.T, s *Sandbox, shared string) {
	t.Helper()
	s.SeedSharedManifest(t, npmrcManifest)
	s.WriteRepoFile(t, filepath.Join("dotfiles", "npmrc"), shared)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".npmrc"), shared)
	s.WriteRepoFile(t, ".gitignore", "local/\n")
}

// TestNpmrc_DeploysAsRegularFileCopy proves the baseline carriage: apply deploys
// ~/.npmrc as a regular-file copy (never a symlink), the shared non-secret
// content lands verbatim, no include sidecar is materialised, and a no-op capture
// + status reports clean and leaves the deployed file byte-, mode-, size- and
// mtime-identical.
func TestNpmrc_DeploysAsRegularFileCopy(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	const sharedNpmrc = "save-exact=true\nregistry=https://registry.npmjs.org/\n"
	seedNpmrc(t, s, sharedNpmrc)
	gitCommitAll(t, s.Repo, "baseline npmrc (no secret)")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	target := s.HomePath(".npmrc")
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("apply did not deploy ~/.npmrc: %v", err)
	}
	// The core invariant: nothing under $HOME is symlinked — it is a regular file.
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("~/.npmrc was deployed as a SYMLINK — ferry must deploy regular-file copies")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read ~/.npmrc: %v", err)
	}
	if !strings.Contains(string(got), "save-exact=true") {
		t.Errorf("~/.npmrc lost the shared content:\n%s", got)
	}
	// A whole-file dotfile must NOT gain any sourced sidecar directive.
	if strings.Contains(string(got), "source") || strings.Contains(string(got), "[include]") {
		t.Errorf("~/.npmrc gained an include/sidecar directive — it must be a plain whole-file dotfile:\n%s", got)
	}
	if _, err := os.Lstat(s.HomePath(".npmrc.local")); err == nil {
		t.Errorf("apply materialised a ~/.npmrc.local sidecar — .npmrc is not an include-sidecar domain")
	}

	tw := s.SnapshotFile(t, target)
	if _, errOut, code := s.Ferry("capture"); code != 0 {
		t.Fatalf("no-op capture exited %d; stderr:\n%s", code, errOut)
	}
	statusOut, statusErr, statusCode := s.Ferry("status")
	statusCombined := statusOut + statusErr
	if statusCode != 0 {
		t.Errorf("clean status exited %d\n%s", statusCode, statusCombined)
	}
	if !containsAnyFold(statusCombined,
		"no drift", "clean", "up to date", "up-to-date", "no changes", "nothing to", "in sync") {
		t.Errorf("clean status gave no positive no-drift signal after a no-op capture\n%s", statusCombined)
	}
	tw.AssertUnchanged(t)
}

// TestNpmrc_AuthTokenNeverReachesRepo is the load-bearing secret gate for
// `~/.npmrc` (plan STOP condition): a LITERAL registry auth token edited into the
// live ~/.npmrc is gate-blocked, routed to the out-of-repo secret store, and only
// a placeholder reaches the shared repo; a redeploy renders the literal token
// back so npm reads the real value.
func TestNpmrc_AuthTokenNeverReachesRepo(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	const sharedNpmrc = "save-exact=true\n"
	seedNpmrc(t, s, sharedNpmrc)
	gitCommitAll(t, s.Repo, "baseline npmrc")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}

	// Edit the live file: add a literal npm registry auth token line.
	tokenLine := "//registry.npmjs.org/:_authToken=" + npmToken + "\n"
	if err := os.WriteFile(s.HomePath(".npmrc"), []byte(sharedNpmrc+tokenLine), 0o600); err != nil {
		t.Fatalf("stage npmrc secret: %v", err)
	}

	// The live ~/.npmrc now holds a high-confidence secret and the repo source
	// carries no placeholder yet, so the whole-file block escape is the only repo
	// route offered: [r]eject / secret-store [x]. Route it to the out-of-repo
	// store (x) — the value is stored out of band and a placeholder is written to
	// the committed source in its place.
	out, errOut, _ := s.FerryWithInput("x\n", "capture")
	combined := out + errOut

	// HARD: the literal token never lands in any repo file or git history — it
	// lives only in the out-of-repo secret store (~/.config/ferry/secrets-local).
	s.AssertNoSecretInRepo(t, npmToken)

	repoShared, err := os.ReadFile(s.RepoPath("dotfiles", "npmrc"))
	if err != nil {
		t.Fatalf("read repo shared npmrc: %v", err)
	}
	if strings.Contains(string(repoShared), npmToken) {
		t.Fatalf("the literal npm token reached the shared repo:\n%s", repoShared)
	}
	if !strings.Contains(string(repoShared), "{{ferry.secret") {
		t.Errorf("repo shared npmrc did not gain a ferry placeholder in place of the secret:\n%s\ncapture output:\n%s", repoShared, combined)
	}

	// Redeploy renders the placeholder back to the literal token so npm sees the
	// real value: remove the live file so apply re-materialises it fresh from the
	// placeholder-bearing repo. A secret-routed target is risky, so confirm the gate.
	if err := os.Remove(s.HomePath(".npmrc")); err != nil {
		t.Fatalf("remove live npmrc: %v", err)
	}
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("redeploy apply exited %d; stderr:\n%s", code, errOut)
	}
	deployed, err := os.ReadFile(s.HomePath(".npmrc"))
	if err != nil {
		t.Fatalf("read redeployed ~/.npmrc: %v", err)
	}
	if !strings.Contains(string(deployed), tokenLine) {
		t.Errorf("deploy did not render the npm token back literally into ~/.npmrc:\n%s", deployed)
	}
}

// TestNpmrc_EnvRefLeftVerbatim is the negative to the secret gate: a
// `//registry.npmjs.org/:_authToken=${NPM_TOKEN}` env-ref is NOT a literal secret
// (npm expands the environment at read time), so it is never gate-blocked and
// reaches the shared repo verbatim — the ${…} exemption from
// secret.IsNonPlaceholderSecret. This is the recommended pattern.
func TestNpmrc_EnvRefLeftVerbatim(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	const sharedNpmrc = "save-exact=true\n"
	seedNpmrc(t, s, sharedNpmrc)
	gitCommitAll(t, s.Repo, "baseline npmrc")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}

	const envLine = "//registry.npmjs.org/:_authToken=${NPM_TOKEN}\n"
	if err := os.WriteFile(s.HomePath(".npmrc"), []byte(sharedNpmrc+envLine), 0o600); err != nil {
		t.Fatalf("stage npmrc env-ref: %v", err)
	}

	// The env-ref is not a secret: accept the hunk (y) and route to shared (s).
	s.FerryWithInput("y\ns\n", "capture")

	repoShared, err := os.ReadFile(s.RepoPath("dotfiles", "npmrc"))
	if err != nil {
		t.Fatalf("read repo shared npmrc: %v", err)
	}
	if !strings.Contains(string(repoShared), envLine) {
		t.Errorf("the ${NPM_TOKEN} env-ref was not carried to the shared repo verbatim:\n%s", repoShared)
	}
}
