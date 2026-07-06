package evals

// Apply-behaviour ACs. These drive the real binary against a seeded repo + HOME
// and assert observable file/exit outcomes. Each test seeds a minimal manifest
// and a managed dotfile, runs `ferry apply`, and inspects the materialised HOME.
//
// CONTRACT NOTE: the exact on-disk layout of the repo (where the source of a
// managed dotfile lives) is not spelled out verbatim in the four docs. We use the
// most natural mapping: a managed dotfile `.zshrc` is sourced from `<repo>/.zshrc`
// (or `<repo>/dotfiles/.zshrc`). Tests accept either by seeding both candidates
// where it is cheap; where it is not, a TODO(contract) marks the assumption.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baseManifest = `[manage]
dotfiles = [".zshrc"]
brew = false
iterm2 = false
fonts = false
`

// seedManagedDotfile seeds a repo with ferry.toml managing .zshrc and a source
// file for it in the two most likely repo locations, then points config.toml at
// the repo. Returns the expected materialised target in HOME.
func seedManagedDotfile(t *testing.T, s *Sandbox, content string) string {
	t.Helper()
	s.SeedSharedManifest(t, baseManifest)
	// Seed both candidate source layouts so the test isn't brittle to the exact
	// repo convention the implementation chooses.
	s.WriteRepoFile(t, ".zshrc", content)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), content)
	return s.HomePath(".zshrc")
}

// TestApplyIdempotent covers AC-apply-idempotent: two consecutive applies with no
// intervening change produce no second-run modification and exit 0 both times.
func TestApplyIdempotent_AC_apply_idempotent(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	target := seedManagedDotfile(t, s, "export EDITOR=vim\n")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-apply-idempotent: first apply exited %d; stderr:\n%s", code, errOut)
	}
	// After the first apply the target should exist; snapshot whatever state we're in.
	snap := s.SnapshotFile(t, target)
	if !snap.exists {
		t.Fatalf("AC-apply-idempotent: expected %s to be deployed by first apply", target)
	}

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-apply-idempotent: second apply exited %d; stderr:\n%s", code, errOut)
	}
	// Second run must not have touched content/mode.
	snap.AssertUnchanged(t)
}

// TestApplyRecordsDeployedBaseline proves the v0.5.0 Foundation primitive is
// wired to the live CLI: a real `ferry apply` records, in its version-2
// last-applied state file, a per-target last-deployed content snapshot that is
// content-addressed by the recorded hash. This is what the guided-apply risk
// gate and capture-back divergence detection later read.
func TestApplyRecordsDeployedBaseline(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	seedManagedDotfile(t, s, "export EDITOR=vim\n")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	statePath := s.HomePath(".local", "state", "ferry", "dotfile-last-applied.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read last-applied state %s: %v", statePath, err)
	}

	var env struct {
		Version  int               `json:"version"`
		Applied  map[string]string `json:"applied"`
		Deployed map[string][]byte `json:"deployed"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("parse state file: %v\n%s", err, raw)
	}
	if env.Version != 2 {
		t.Fatalf("state version = %d, want 2", env.Version)
	}
	if len(env.Applied) == 0 {
		t.Fatalf("no applied entries recorded; apply did not deploy anything")
	}

	// Every recorded hash must have a content-addressed deployed snapshot: the
	// baseline exists AND its stored bytes hash back to their key.
	for name, hash := range env.Applied {
		snap, ok := env.Deployed[hash]
		if !ok {
			t.Fatalf("target %q (hash %s) has no last-deployed snapshot; the baseline was not recorded", name, hash)
		}
		sum := sha256.Sum256(snap)
		if got := hex.EncodeToString(sum[:]); got != hash {
			t.Fatalf("target %q snapshot is not content-addressed: sha256(bytes)=%s, key=%s", name, got, hash)
		}
	}
}

// TestApplySecretRoutedBaselineHashOnly proves the secret-at-rest fix end-to-end
// via the live CLI: applying a managed dotfile that references the secret store
// renders the real secret into the LIVE file, but records ONLY the content hash in
// the last-applied state file — the rendered plaintext secret appears NOWHERE in
// that on-disk bookkeeping file, while drift detection survives via the recorded
// hash.
func TestApplySecretRoutedBaselineHashOnly(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)

	const secretValue = "sk-live-SECRET-VALUE-9876543210"
	src := "export API_KEY={{ferry.secret \"api.key\"}}\n"
	s.WriteRepoFile(t, ".zshrc", src)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), src)
	seedSecret(t, s, "api", "key", secretValue)

	// A secret-routed target is risky (it deploys a real secret value): confirm
	// the guided walkthrough so the deploy proceeds.
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	// The live file received the RENDERED secret (the deploy really is secret-routed).
	live, err := os.ReadFile(s.HomePath(".zshrc"))
	if err != nil {
		t.Fatalf("read live .zshrc: %v", err)
	}
	if !strings.Contains(string(live), secretValue) {
		t.Fatalf("secret was not rendered into the live file:\n%s", live)
	}

	statePath := s.HomePath(".local", "state", "ferry", "dotfile-last-applied.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read last-applied state %s: %v", statePath, err)
	}
	// The rendered plaintext secret must appear NOWHERE in the state file bytes.
	if bytes.Contains(raw, []byte(secretValue)) {
		t.Fatalf("SECRET LEAK: rendered plaintext %q found in %s:\n%s", secretValue, statePath, raw)
	}

	var env struct {
		Version  int               `json:"version"`
		Applied  map[string]string `json:"applied"`
		Deployed map[string][]byte `json:"deployed"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("parse state file: %v\n%s", err, raw)
	}
	// Drift/baseline detection survives: the secret target's hash IS recorded.
	hash, ok := env.Applied["zshrc"]
	if !ok {
		t.Fatalf("no applied hash recorded for the secret-routed target; drift detection lost\n%s", raw)
	}
	// But its bytes are NOT: the deployed map has no entry for that hash (hash-only).
	if _, present := env.Deployed[hash]; present {
		t.Fatalf("secret-routed target recorded a deployed byte snapshot (hash %s); it must be hash-only\n%s", hash, raw)
	}
}

// TestConflictRefuse covers AC-conflict-refuse: a locally-edited (uncaptured)
// managed file is NOT clobbered; apply reports a conflict and leaves the edit.
func TestConflictRefuse_AC_conflict_refuse(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	target := seedManagedDotfile(t, s, "export EDITOR=vim\n")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-conflict-refuse: initial apply exited %d; stderr:\n%s", code, errOut)
	}

	// User edits the live file without capturing.
	userEdit := "export EDITOR=nano # my uncaptured local change\n"
	if err := os.WriteFile(target, []byte(userEdit), 0o644); err != nil {
		t.Fatalf("write user edit: %v", err)
	}
	tw := s.SnapshotFile(t, target)

	// Repo source also moves on, creating a genuine conflict.
	s.WriteRepoFile(t, ".zshrc", "export EDITOR=emacs\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "export EDITOR=emacs\n")

	out, errOut, code := s.Ferry("apply")
	combined := out + errOut

	// Require ALL THREE (an opaque failure that happens to leave the file alone
	// must NOT pass — the conflict must be reported in words):
	// (a) apply REFUSES: non-zero exit OR a clear refusal in the output.
	refused := code != 0 || containsAnyFold(combined, "refus", "not overwrit", "skip", "abort", "left unchanged")
	if !refused {
		t.Errorf("AC-conflict-refuse: apply did not refuse (exit 0 with no refusal wording)\n%s", combined)
	}
	// (b) OBSERVABLE conflict report: specific conflict/uncaptured/capture guidance,
	//     not merely any non-zero exit.
	if !containsAnyFold(combined, "conflict", "uncaptured", "capture") {
		t.Errorf("AC-conflict-refuse: apply did not report a conflict / uncaptured-changes / capture guidance\n%s", combined)
	}
	// (c) the managed file is NOT overwritten: content+mode+mtime+size unchanged.
	tw.AssertUnchanged(t)
}

// TestLocalWins covers AC-local-wins: the .local overlay must actually WIN /
// layer LAST in the effective config — not merely materialise (that is
// AC-loc-local-materialised). The docs say the shared ~/.zshrc sources ~/.zshrc.local
// "last", so the effective value is the overlay's. We assert the OVERRIDE outcome:
// shared sets VAL=shared, the overlay sets VAL=local, and the effective config
// (the materialised shared ~/.zshrc, sourcing ~/.zshrc.local after its own VAL)
// yields the .local value last. Re-apply must not disturb that outcome.
func TestLocalWins_AC_local_wins(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	// Shared sets VAL=shared.
	sharedZsh := "export VAL=shared\n"
	s.WriteRepoFile(t, ".zshrc", sharedZsh)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), sharedZsh)
	// Overlay overrides VAL=local.
	overlayContent := "export VAL=local # machine-specific, must win\n"
	s.WriteRepoFile(t, filepath.Join("local", "zsh", "zshrc.local"), overlayContent)

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-local-wins: apply exited %d; stderr:\n%s", code, errOut)
	}

	// The overlay materialised with its value.
	localTarget := s.HomePath(".zshrc.local")
	gotLocal, err := os.ReadFile(localTarget)
	if err != nil {
		t.Fatalf("AC-local-wins: expected materialised overlay at %s: %v", localTarget, err)
	}
	if !contains(string(gotLocal), "VAL=local") {
		t.Errorf("AC-local-wins: materialised .local does not carry the overriding value: %q", gotLocal)
	}

	// OVERRIDE OUTCOME: the shared ~/.zshrc must EFFECTIVELY SOURCE ~/.zshrc.local
	// via a real `source ~/.zshrc.local` / `. ~/.zshrc.local` directive (NOT a
	// comment or a non-source mention), positioned AFTER the shared VAL assignment so
	// the overlay's value wins. A commented/non-source mention must NOT pass.
	sharedTarget := s.HomePath(".zshrc")
	gotShared, err := os.ReadFile(sharedTarget)
	if err != nil {
		t.Fatalf("AC-local-wins: shared ~/.zshrc not deployed: %v", err)
	}
	lines := strings.Split(string(gotShared), "\n")
	srcLine := effectiveSourceLineIndex(lines, "zshrc.local")
	valLine := lineIndexContaining(lines, "VAL=shared")
	if srcLine < 0 {
		t.Errorf("AC-local-wins: shared ~/.zshrc has no EFFECTIVE `source ~/.zshrc.local` / `. ~/.zshrc.local` directive (a comment/mention does not count); overlay would not win\n%s", gotShared)
	} else if valLine >= 0 && srcLine < valLine {
		t.Errorf("AC-local-wins: shared ~/.zshrc sources .local (line %d) BEFORE setting VAL=shared (line %d) — overlay would not win\n%s", srcLine, valLine, gotShared)
	}

	// Re-apply does not disturb the override outcome.
	tw := s.SnapshotFile(t, localTarget)
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-local-wins: second apply exited %d; stderr:\n%s", code, errOut)
	}
	tw.AssertUnchanged(t)
}

// effectiveSourceLineIndex returns the index of the first line that is an EFFECTIVE
// shell source directive for a file matching `target` — `source <…target…>` or
// `. <…target…>` — that is NOT commented out. Returns -1 if none.
func effectiveSourceLineIndex(lines []string, target string) int {
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue // comment / blank — not effective
		}
		// Strip any trailing inline comment so a "# …zshrc.local" note in a code
		// line isn't mistaken for the directive's own target.
		code := line
		if h := strings.IndexByte(code, '#'); h >= 0 {
			code = strings.TrimSpace(code[:h])
		}
		if !strings.Contains(strings.ToLower(code), strings.ToLower(target)) {
			continue
		}
		// Must be a source directive: `source <path>` or `. <path>`.
		lc := strings.ToLower(code)
		if strings.HasPrefix(lc, "source ") || strings.HasPrefix(code, ". ") ||
			strings.Contains(lc, "&& source ") || strings.Contains(lc, "; source ") ||
			strings.Contains(code, "&& . ") || strings.Contains(code, "; . ") {
			return i
		}
	}
	return -1
}

// lineIndexContaining returns the index of the first line containing needle (fold),
// or -1.
func lineIndexContaining(lines []string, needle string) int {
	for i, l := range lines {
		if strings.Contains(strings.ToLower(l), strings.ToLower(needle)) {
			return i
		}
	}
	return -1
}

// TestLocalSurvivesApply covers AC-local-survives-apply: the REPO .local overlay
// (local/<domain>/) is layered LAST and is NOT overwritten or deleted by a shared
// apply. The AC's scope is the overlay layer specifically — it does NOT require an
// arbitrary uncaptured LIVE-file edit to survive (that protection is the conflict
// path, AC-conflict-refuse). So we seed a REPO overlay with a distinguishing value,
// apply (it materialises to ~/.zshrc.local), then run a second shared-only apply and
// assert the overlay's content STILL survives (shared didn't stomp it).
func TestLocalSurvivesApply_AC_local_survives_apply(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# shared\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# shared\n")
	// The repo .local overlay with a distinguishing value (the per-machine layer).
	const overlayContent = "export FROM_OVERLAY=1 # deliberate per-machine difference\n"
	s.WriteRepoFile(t, filepath.Join("local", "zsh", "zshrc.local"), overlayContent)

	// First apply materialises the overlay to its real path.
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-local-survives-apply: first apply exited %d; stderr:\n%s", code, errOut)
	}
	target := s.HomePath(".zshrc.local")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("AC-local-survives-apply: overlay did not materialise to %s: %v", target, err)
	}
	if string(got) != overlayContent {
		t.Errorf("AC-local-survives-apply: materialised overlay content = %q, want %q", got, overlayContent)
	}

	// A SECOND (shared-only) apply must NOT overwrite or delete the overlay's effect
	// — the .local layer is layered last and always wins.
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-local-survives-apply: second apply exited %d; stderr:\n%s", code, errOut)
	}
	got2, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("AC-local-survives-apply: overlay deleted by second apply: %v", err)
	}
	if string(got2) != overlayContent {
		t.Errorf("AC-local-survives-apply: second (shared) apply clobbered the overlay: content = %q, want %q", got2, overlayContent)
	}
}

// TestScopeRespectedApply covers AC-scope-respected-apply: apply only writes
// in-scope domains; an out-of-scope domain's target is never created.
func TestScopeRespectedApply_AC_scope_respected_apply(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	// dotfiles in scope; fonts explicitly disabled.
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")
	// Seed an out-of-scope font payload in the repo; if ferry honoured it, a font
	// would appear under ~/Library/Fonts. That path is our tripwire.
	s.WriteRepoFile(t, filepath.Join("fonts", "Bogus.ttf"), "FONTDATA")
	fontTarget := s.HomePath("Library", "Fonts", "Bogus.ttf")
	fontTW := s.SnapshotFile(t, fontTarget) // absent now

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-scope-respected-apply: apply exited %d; stderr:\n%s", code, errOut)
	}

	// In-scope deployed.
	if _, err := os.Stat(s.HomePath(".zshrc")); err != nil {
		t.Errorf("AC-scope-respected-apply: in-scope .zshrc not deployed: %v", err)
	}
	// Out-of-scope never created.
	fontTW.AssertUnchanged(t)
}

// TestDepsNotDuringDefaultApply covers AC-deps-not-during-default-apply: a plain
// `apply` (no --deps) installs nothing / invokes no package manager. We stub a
// `brew` tripwire on PATH and assert it is never called.
func TestDepsNotDuringDefaultApply_AC_deps_not_during_default_apply(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, `[manage]
dotfiles = [".zshrc"]
brew = true
`)
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")

	stubDir, marker := makeBrewStub(t)
	// Prepend stub dir so our fake brew shadows any real one.
	pathOverride := "PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")

	if _, errOut, code := s.FerryEnv([]string{pathOverride}, "apply"); code != 0 {
		t.Fatalf("AC-deps-not-during-default-apply: apply exited %d; stderr:\n%s", code, errOut)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Errorf("AC-deps-not-during-default-apply: plain `apply` invoked the package manager (brew stub marker present)")
	}
}

// TestBackupBeforeChange covers AC-backup-before-change (round-4 fix): the
// automatic-backup promise is asserted as OBSERVABLE BEHAVIOUR, not as a
// discoverable backup artifact/format/location. After `apply` changes a managed
// file that had prior content X, a subsequent `restore` returns it byte-identical
// to X (original mode). The backup's existence is proven by restore working — we
// do NOT inspect ferry's internal backup store. (Cross-ref AC-restore-clean.)
func TestBackupBeforeChange_AC_backup_before_change(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "export VERSION=new\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "export VERSION=new\n")

	// Pre-existing managed file with known content X and a distinct original mode.
	originalX := "export VERSION=old # pre-ferry\n"
	target := s.WriteHomeFile(t, ".zshrc", originalX, 0o600)
	origTW := s.SnapshotFile(t, target)

	// Adopting/overwriting a pre-existing ~/.zshrc is risky; confirm it.
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("AC-backup-before-change: apply exited %d; stderr:\n%s", code, errOut)
	}
	// apply must have changed the content away from X (otherwise vacuous).
	if cur, _ := os.ReadFile(target); string(cur) == originalX {
		t.Fatalf("AC-backup-before-change: precondition failed — apply did not change the managed file")
	}

	// Observable: restore returns the file byte-identical to X with original mode.
	if _, errOut, code := s.Ferry("restore"); code != 0 {
		t.Fatalf("AC-backup-before-change: restore exited %d; stderr:\n%s", code, errOut)
	}
	origTW.AssertUnchanged(t)
}

// TestDepsInstallAttempted covers AC-deps-install-attempted — the GATING
// schema-free DIFFERENTIAL: with a stub package manager earlier on PATH than the
// real one, `apply --deps` invokes it >=1x (proving --deps is NOT a no-op and
// drives the present PM), while a default `apply` invokes it ZERO times; and ferry
// never installs the PM itself. The exact declared-deps fixture bytes are finalized
// at the Layer-2 eval-vs-AC pass — only the differential is gated here.
func TestDepsInstallAttempted_AC_deps_install_attempted(t *testing.T) {
	t.Parallel()

	// --- default apply: stub PM invoked ZERO times ---
	sDefault := NewSandbox(t)
	sDefault.SeedSharedManifest(t, `[manage]
dotfiles = [".zshrc"]
brew = true
`)
	sDefault.WriteRepoFile(t, ".zshrc", "# managed\n")
	sDefault.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")
	defStubDir, defCount := makeBrewCountingStub(t)
	defPath := "PATH=" + defStubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	if _, errOut, code := sDefault.FerryEnv([]string{defPath}, "apply"); code != 0 {
		t.Fatalf("AC-deps-install-attempted: default apply exited %d; stderr:\n%s", code, errOut)
	}
	if n := countInvocations(defCount); n != 0 {
		t.Errorf("AC-deps-install-attempted: default `apply` invoked the package manager %d times (want 0)", n)
	}

	// --- apply --deps: stub PM invoked >=1 time, AND ferry NEVER installs a PM ---
	sDeps := NewSandbox(t)
	sDeps.SeedSharedManifest(t, `[manage]
dotfiles = [".zshrc"]
brew = true
`)
	sDeps.WriteRepoFile(t, ".zshrc", "# managed\n")
	sDeps.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")
	depStubDir, depCount := makeBrewCountingStub(t)
	// In the SAME stub dir (which is the present PM, found first on PATH), add
	// curl/bash bootstrap recorders. With a PM already present, ferry must USE it
	// and must NEVER fetch/run a PM install script — the "never installs the PM
	// itself" half of the gate.
	curlLog := filepath.Join(depStubDir, "curl.log")
	bashLog := filepath.Join(depStubDir, "bash.log")
	writeStub(t, filepath.Join(depStubDir, "curl"), "#!/bin/sh\necho \"$*\" >> "+shellQuote(curlLog)+"\nexit 0\n")
	writeStub(t, filepath.Join(depStubDir, "bash"), "#!/bin/sh\necho \"$*\" >> "+shellQuote(bashLog)+"\nexit 0\n")
	depPath := "PATH=" + depStubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	if _, errOut, code := sDeps.FerryEnv([]string{depPath}, "apply", "--deps"); code != 0 {
		// A non-zero exit may be legitimate if no deps are declared yet; the gate is
		// the invocation differential, so we don't fail on exit code alone here.
		t.Logf("AC-deps-install-attempted: `apply --deps` exited %d (deps fixture finalized at Layer-2); stderr:\n%s", code, errOut)
	}
	if n := countInvocations(depCount); n < 1 {
		t.Errorf("AC-deps-install-attempted: `apply --deps` invoked the package manager %d times (want >=1; --deps must not be a no-op)", n)
	}
	// "never installs the package manager itself": with a PM present, no bootstrap
	// fetch/run may fire, and no NEW PM binary may be created on PATH.
	if countInvocations(curlLog) != 0 || countInvocations(bashLog) != 0 {
		t.Errorf("AC-deps-install-attempted: `apply --deps` ran a fetch/run bootstrap (curl/bash) with a PM ALREADY present — ferry must use the present PM, never install one")
	}
	for _, pm := range []string{"port", "apt", "apt-get", "dnf", "yum"} {
		if _, statErr := os.Stat(filepath.Join(depStubDir, pm)); statErr == nil {
			t.Errorf("AC-deps-install-attempted: ferry created a %q binary (installed a package manager)", pm)
		}
	}
}

// makeBrewStub writes a fake `brew` executable into a temp dir that records each
// invocation by touching a marker file. Returns (dir, markerPath).
func makeBrewStub(t *testing.T) (dir, marker string) {
	t.Helper()
	dir = t.TempDir()
	marker = filepath.Join(dir, "brew_was_called")
	script := "#!/bin/sh\ntouch " + shellQuote(marker) + "\nexit 0\n"
	stub := filepath.Join(dir, "brew")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("makeBrewStub: %v", err)
	}
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatalf("makeBrewStub chmod: %v", err)
	}
	return dir, marker
}

// shellQuote single-quotes a path for safe embedding in the stub script.
func shellQuote(p string) string {
	return "'" + filepath.Clean(p) + "'"
}

// makeBrewCountingStub writes a fake `brew` that APPENDS one line per invocation
// (with its args) to a log file, so callers can count invocations. Returns
// (dir, logPath).
func makeBrewCountingStub(t *testing.T) (dir, logPath string) {
	t.Helper()
	dir = t.TempDir()
	logPath = filepath.Join(dir, "brew_invocations.log")
	// Each call appends a line: "invoked <args>".
	script := "#!/bin/sh\necho \"invoked $*\" >> " + shellQuote(logPath) + "\nexit 0\n"
	stub := filepath.Join(dir, "brew")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("makeBrewCountingStub: %v", err)
	}
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatalf("makeBrewCountingStub chmod: %v", err)
	}
	return dir, logPath
}

// countInvocations returns the number of recorded stub invocations (log lines).
func countInvocations(logPath string) int {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0 // no log => never invoked
	}
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	return n
}
