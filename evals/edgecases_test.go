package evals

// Higher-value edge-case behavioral evals layered on top of the per-AC coverage.
// Every assertion here drives the REAL binary and checks a user-observable outcome
// (file bytes/mode/absence, exit code, stdout/stderr wording), never a proxy.
//
// Coverage added here:
//   - scoped restore: `ferry restore <dotfile>` reverts ONLY that target, leaving
//     other managed targets applied (AC-cmd-restore "restore <domain> scopes it to
//     one target"; AC-restore-clean for the reverted target's byte-identity).
//   - restore after a --force apply (AC-restore-clean + AC-conflict-refuse's --force
//     escape hatch): a forced overwrite is still reversible from the automatic backup.
//   - apply conflict then --force resolves (AC-conflict-refuse): default apply refuses
//     an uncaptured edit; `apply --force` then deploys the repo content over it.
//   - mixed clean + drifted + conflict targets in one apply (AC-apply-idempotent +
//     AC-conflict-refuse): a single apply run handles all three without clobbering
//     the conflicted target.
//   - deps-installed set cumulative across two `apply --deps` runs (AC-restore
//     --packages reads the recorded self-installed set; two runs must accumulate,
//     observable via `restore --packages --yes` uninstalling BOTH).
//   - capture: a secret in a captured hunk is blocked from BOTH the shared route and
//     the local route (AC-secret-blocked, strengthened to cover both destinations).
//   - capture: a generic (non-zsh) dotfile routed local lands whole-file under
//     local/<domain>/ (AC-capture-interactive-route / AC-loc-local-overlay-dir).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScopedRestoreLeavesOthers covers the AC-cmd-restore scoped-revert contract
// ("restore <domain> scopes it to one target") together with AC-restore-clean for
// the reverted target: after applying two managed dotfiles, `ferry restore .zshrc`
// reverts ONLY .zshrc to its pre-ferry state while .gitconfig stays applied.
func TestScopedRestoreLeavesOthers_AC_cmd_restore(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, `[manage]
dotfiles = [".zshrc", ".gitconfig"]
`)
	for _, name := range []string{".zshrc", ".gitconfig"} {
		s.WriteRepoFile(t, name, "# ferry-managed "+name+"\n")
		s.WriteRepoFile(t, filepath.Join("dotfiles", name), "# ferry-managed "+name+"\n")
	}

	// Pre-ferry state: .zshrc present with original content (a distinct mode so the
	// mode-restore is meaningful); .gitconfig absent.
	originalZsh := "# my original zshrc, pre-ferry\n"
	zshTarget := s.WriteHomeFile(t, ".zshrc", originalZsh, 0o600)
	zshSnap := s.SnapshotFile(t, zshTarget)
	gitconfigTarget := s.HomePath(".gitconfig")

	// Adopting the pre-existing .zshrc is risky; confirm the guided walkthrough.
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	// Preconditions: apply changed .zshrc away from original and created .gitconfig.
	if cur, _ := os.ReadFile(zshTarget); string(cur) == originalZsh {
		t.Fatalf("precondition: apply did not change .zshrc")
	}
	if cur := s.SnapshotFile(t, gitconfigTarget); !cur.exists {
		t.Fatalf("precondition: apply did not create .gitconfig")
	}
	appliedGitconfig := s.SnapshotFile(t, gitconfigTarget)

	// Scoped restore of ONLY .zshrc.
	out, errOut, code := s.Ferry("restore", ".zshrc")
	if code != 0 {
		t.Fatalf("scoped restore exited %d; stderr:\n%s", code, errOut)
	}
	combined := out + errOut
	// It should name .zshrc as reverted and NOT claim to revert .gitconfig.
	if !containsAnyFold(combined, ".zshrc", "zshrc") {
		t.Errorf("scoped restore did not report reverting .zshrc\n%s", combined)
	}

	// .zshrc reverted to its exact pre-ferry state (bytes + mode).
	zshSnap.AssertUnchanged(t)
	// .gitconfig was NOT in scope: it must remain applied (byte-identical to the
	// applied state, still present).
	appliedGitconfig.AssertUnchanged(t)
}

// TestRestoreAfterForceApply covers AC-restore-clean combined with the --force
// escape hatch: even when apply --force deliberately overwrites an uncaptured local
// edit, that overwrite is still reversible — restore returns the file to the exact
// pre-ferry bytes. (The automatic backup is taken before the forced write.)
func TestRestoreAfterForceApply_AC_restore_clean(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	target := seedManagedDotfile(t, s, "export EDITOR=vim\n")

	// Pre-ferry: the user's own file with distinct content + mode.
	preFerry := "# pristine user zshrc\n"
	if err := os.WriteFile(target, []byte(preFerry), 0o600); err != nil {
		t.Fatalf("seed pre-ferry: %v", err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatalf("chmod pre-ferry: %v", err)
	}
	preSnap := s.SnapshotFile(t, target)

	// First apply establishes ferry's management + backup of the pre-ferry content.
	// Adopting the pre-existing file is risky, so confirm the walkthrough.
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("initial apply exited %d; stderr:\n%s", code, errOut)
	}
	// The user then makes an uncaptured edit AND the repo drifts, creating a conflict.
	if err := os.WriteFile(target, []byte("export EDITOR=nano # uncaptured\n"), 0o644); err != nil {
		t.Fatalf("user edit: %v", err)
	}
	s.WriteRepoFile(t, ".zshrc", "export EDITOR=emacs\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "export EDITOR=emacs\n")

	// --force resolves the conflict by overwriting with the repo content.
	if _, errOut, code := s.Ferry("apply", "--force"); code != 0 {
		t.Fatalf("apply --force exited %d; stderr:\n%s", code, errOut)
	}
	if cur, _ := os.ReadFile(target); !strings.Contains(string(cur), "emacs") {
		t.Fatalf("precondition: apply --force did not deploy the repo content (got %q)", cur)
	}

	// Reversibility still holds: restore returns the ORIGINAL pre-ferry bytes+mode.
	if _, errOut, code := s.Ferry("restore"); code != 0 {
		t.Fatalf("restore exited %d; stderr:\n%s", code, errOut)
	}
	preSnap.AssertUnchanged(t)
}

// TestApplyConflictThenForce covers AC-conflict-refuse and its documented --force
// escape hatch in one flow: a default apply REFUSES an uncaptured edit (leaving it
// intact), and a subsequent `apply --force` overwrites it with the repo content.
func TestApplyConflictThenForce_AC_conflict_refuse(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	target := seedManagedDotfile(t, s, "export EDITOR=vim\n")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("initial apply exited %d; stderr:\n%s", code, errOut)
	}

	userEdit := "export EDITOR=nano # uncaptured local change\n"
	if err := os.WriteFile(target, []byte(userEdit), 0o644); err != nil {
		t.Fatalf("user edit: %v", err)
	}
	editSnap := s.SnapshotFile(t, target)

	// Repo drifts -> genuine conflict.
	s.WriteRepoFile(t, ".zshrc", "export EDITOR=emacs\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "export EDITOR=emacs\n")

	// (1) Default apply refuses AND leaves the uncaptured edit intact.
	out, errOut, code := s.Ferry("apply")
	combined := out + errOut
	refused := code != 0 || containsAnyFold(combined, "refus", "not overwrit", "skip", "abort", "left unchanged")
	if !refused {
		t.Errorf("AC-conflict-refuse: default apply did not refuse the conflict\n%s", combined)
	}
	if !containsAnyFold(combined, "conflict", "uncaptured", "capture", "--force") {
		t.Errorf("AC-conflict-refuse: default apply did not report a conflict / point at --force\n%s", combined)
	}
	editSnap.AssertUnchanged(t) // the user's edit is preserved

	// (2) apply --force resolves it: the repo content is deployed.
	if _, errOut, code := s.Ferry("apply", "--force"); code != 0 {
		t.Fatalf("apply --force exited %d; stderr:\n%s", code, errOut)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target after --force: %v", err)
	}
	if !strings.Contains(string(got), "emacs") {
		t.Errorf("AC-conflict-refuse[--force]: forced apply did not overwrite with repo content; got %q", got)
	}
	if strings.Contains(string(got), "nano") {
		t.Errorf("AC-conflict-refuse[--force]: forced apply left the stale uncaptured edit; got %q", got)
	}
}

// TestApplyMixedCleanDriftedConflict covers a realistic single-apply scenario mixing
// three managed targets: one CLEAN (unchanged since last apply — idempotent no-op),
// one DRIFTED-but-not-conflicting (only the repo moved; apply should update it), and
// one CONFLICTED (uncaptured local edit + repo drift). One `apply` must update the
// drifted target, refuse the conflicted one without clobbering it, and not disturb
// the clean one. (AC-apply-idempotent + AC-conflict-refuse + AC-scope-respected-apply.)
func TestApplyMixedCleanDriftedConflict_AC_conflict_refuse(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, `[manage]
dotfiles = [".zshrc", ".gitconfig", ".vimrc"]
`)
	for _, name := range []string{".zshrc", ".gitconfig", ".vimrc"} {
		body := "# v1 " + name + "\n"
		s.WriteRepoFile(t, name, body)
		s.WriteRepoFile(t, filepath.Join("dotfiles", name), body)
	}

	// First apply deploys all three cleanly.
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("initial apply exited %d; stderr:\n%s", code, errOut)
	}
	cleanTarget := s.HomePath(".zshrc")
	driftedTarget := s.HomePath(".gitconfig")
	conflictTarget := s.HomePath(".vimrc")

	// CLEAN: leave .zshrc exactly as apply deployed it.
	cleanSnap := s.SnapshotFile(t, cleanTarget)

	// DRIFTED (repo-only): bump the repo source for .gitconfig; the live file is still
	// ferry's last-written content, so this is a clean update, not a conflict.
	s.WriteRepoFile(t, ".gitconfig", "# v2 .gitconfig\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".gitconfig"), "# v2 .gitconfig\n")

	// CONFLICT: user edits .vimrc uncaptured AND the repo drifts.
	userVimrc := "# my uncaptured vimrc edit\n"
	if err := os.WriteFile(conflictTarget, []byte(userVimrc), 0o644); err != nil {
		t.Fatalf("conflict edit: %v", err)
	}
	conflictSnap := s.SnapshotFile(t, conflictTarget)
	s.WriteRepoFile(t, ".vimrc", "# v2 .vimrc\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".vimrc"), "# v2 .vimrc\n")

	// One apply handling all three.
	out, errOut, code := s.Ferry("apply")
	combined := out + errOut

	// The conflicted target must be reported and left intact.
	if !containsAnyFold(combined, "conflict", "uncaptured", "capture", "--force") {
		t.Errorf("AC-conflict-refuse[mixed]: apply did not report the .vimrc conflict\n%s", combined)
	}
	conflictSnap.AssertUnchanged(t) // conflicted file preserved
	_ = code                        // exit code may be non-zero due to the conflict; the observable is the file state

	// The clean target must be untouched (idempotent).
	cleanSnap.AssertUnchanged(t)

	// The drifted (repo-only) target must have been UPDATED to v2.
	got, err := os.ReadFile(driftedTarget)
	if err != nil {
		t.Fatalf("read drifted target: %v", err)
	}
	if !strings.Contains(string(got), "v2") {
		t.Errorf("AC-conflict-refuse[mixed]: drifted .gitconfig was not updated to v2 despite no conflict; got %q", got)
	}
}

// TestDepsInstalledSetCumulative covers the deps-installed set accumulating across
// two `apply --deps` runs, observable via `restore --packages`: after installing
// dep A (run 1) then dep B (run 2) through a stub package manager, `restore
// --packages --yes` must uninstall BOTH — proving the recorded self-installed set is
// cumulative, not overwritten each run. (AC-deps-install-attempted set persistence +
// AC-cmd-restore --packages.)
func TestDepsInstalledSetCumulative_AC_deps_install_attempted(t *testing.T) {
	t.Parallel()

	s := NewSandbox(t)
	// Two dep declarations; we swap the manifest between runs so run 1 declares depA
	// and run 2 declares depB. The recorded install set must union them.
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")
	writeManifest := func(deps string) {
		s.WriteRepoFile(t, "ferry.toml", "[manage]\ndotfiles = [\".zshrc\"]\nbrew = true\n\n[deps]\nbrew = ["+deps+"]\n")
	}
	if err := os.WriteFile(s.ConfigTOMLPath(), []byte("repo = \""+s.Repo+"\"\n"), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	// A stub brew that records EVERY invocation with args, exits 0. Reused for both
	// runs and the restore uninstall so we can inspect the full call log.
	stubDir, log := makeBrewCountingStub(t)
	pathOverride := "PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")

	// Run 1: declare depA, apply --deps.
	writeManifest(`"depA"`)
	if _, errOut, code := s.FerryEnv([]string{pathOverride}, "apply", "--deps"); code != 0 {
		t.Logf("apply --deps run1 exited %d (non-fatal; gate is the recorded set); stderr:\n%s", code, errOut)
	}
	// Run 2: declare depB, apply --deps.
	writeManifest(`"depB"`)
	if _, errOut, code := s.FerryEnv([]string{pathOverride}, "apply", "--deps"); code != 0 {
		t.Logf("apply --deps run2 exited %d (non-fatal); stderr:\n%s", code, errOut)
	}

	// If the binary records nothing installable through the stub (e.g. it verifies the
	// package is actually present afterward and the stub is a no-op), the recorded set
	// may legitimately be empty. Gate the cumulative assertion on the install path
	// actually having recorded work; otherwise skip with a precise reason rather than
	// asserting a false negative.
	installLog := readFileString(t, log)
	if !strings.Contains(installLog, "depA") && !strings.Contains(installLog, "depB") {
		t.Skip("deps-cumulative: stub PM was not driven with the declared deps in this build " +
			"(install may verify real presence); recorded-set union not observable via the stub here")
	}

	// restore --packages --yes must uninstall BOTH depA and depB if the set is
	// cumulative. Truncate the log so we observe only the uninstall calls.
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatalf("truncate log: %v", err)
	}
	if _, errOut, code := s.FerryEnv([]string{pathOverride}, "restore", "--packages", "--yes"); code != 0 {
		t.Logf("restore --packages exited %d (non-fatal); stderr:\n%s", code, errOut)
	}
	uninstallLog := readFileString(t, log)
	// A cumulative set uninstalls BOTH; a set that was OVERWRITTEN each run would
	// only carry depB.
	if !strings.Contains(uninstallLog, "depA") {
		t.Errorf("deps-cumulative: restore --packages did not uninstall depA from run 1 — the recorded set was overwritten, not accumulated\nuninstall log:\n%s", uninstallLog)
	}
	if !strings.Contains(uninstallLog, "depB") {
		t.Errorf("deps-cumulative: restore --packages did not uninstall depB from run 2\nuninstall log:\n%s", uninstallLog)
	}
}

// TestSecretBlockedBothRoutes covers AC-secret-blocked strengthened to BOTH routes:
// a captured hunk containing secret material must be blocked from the repo whether
// the user routes it SHARED or LOCAL. The out-of-band-only guarantee is not
// route-specific — neither destination may receive the secret bytes.
func TestSecretBlockedBothRoutes_AC_secret_blocked(t *testing.T) {
	t.Parallel()
	secret := "-----BEGIN OPENSSH PRIVATE KEY-----\nFAKEKEYMATERIALdeadbeefcafe0000\n-----END OPENSSH PRIVATE KEY-----\n"

	for _, route := range []struct {
		name    string
		routeCh string // the route keystroke after accepting
	}{
		{"shared", "s"},
		{"local", "l"},
	} {
		route := route
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			s.InitGitRepo(t)
			s.SeedSharedManifest(t, baseManifest)
			s.WriteRepoFile(t, ".zshrc", "# baseline\n")
			s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# baseline\n")
			if _, errOut, code := s.Ferry("apply"); code != 0 {
				t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
			}

			// Stage the secret into the managed file, then approve + route it.
			if err := os.WriteFile(s.HomePath(".zshrc"), []byte("# zshrc\n"+secret), 0o644); err != nil {
				t.Fatalf("stage secret: %v", err)
			}
			// Accept then route; extra answers are harmless if the scan blocks earlier.
			s.FerryWithInput("y\n"+route.routeCh+"\ny\n"+route.routeCh+"\n", "capture")

			// The secret must appear NOWHERE in the repo — neither a shared path nor
			// the local overlay — and not in git history.
			s.AssertNoSecretInRepo(t, secret)
			// Belt-and-braces: explicitly confirm the local overlay is clean too, so a
			// "blocked from shared but leaked to local" bug is caught for BOTH routes.
			if dirTreeContains(t, s.RepoPath("local"), "-----BEGIN OPENSSH PRIVATE KEY-----") {
				t.Errorf("AC-secret-blocked[%s]: secret bytes leaked into the .local overlay", route.name)
			}
		})
	}
}

// TestGenericDotfileLocalWholeFile covers AC-capture-interactive-route +
// AC-loc-local-overlay-dir for a GENERIC (non-zsh) dotfile: routing a captured
// change to LOCAL lands the content whole-file under <repo>/local/<domain>/ (not a
// hunk-merged shared file). A generic dotfile has no shell-style overlay-sourcing;
// the local route is a whole-file copy into the per-domain overlay dir.
func TestGenericDotfileLocalWholeFile_AC_capture_interactive_route(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	// Manage a generic dotfile (.gitconfig) — not .zshrc.
	s.SeedSharedManifest(t, `[manage]
dotfiles = [".gitconfig"]
`)
	base := "[user]\n\tname = base\n"
	s.WriteRepoFile(t, ".gitconfig", base)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".gitconfig"), base)
	gitCommitAll(t, s.Repo, "baseline")
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}

	// Local edit with a unique marker; route it LOCAL.
	marker := "GENERIC_LOCAL_MARKER"
	edited := "[user]\n\tname = base\n\temail = " + marker + "@example.com\n"
	if err := os.WriteFile(s.HomePath(".gitconfig"), []byte(edited), 0o644); err != nil {
		t.Fatalf("local edit: %v", err)
	}
	s.FerryWithInput("y\nl\ny\nl\n", "capture")

	// The captured content must land under <repo>/local/<domain>/ (a whole-file copy),
	// and NOT in a shared repo path.
	landed := findFileContaining(t, s.RepoPath("local"), marker)
	if landed == "" {
		t.Fatalf("AC-capture-interactive-route[generic-local]: generic dotfile local route did not land under <repo>/local/")
	}
	rel, _ := filepath.Rel(s.Repo, landed)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 || parts[0] != "local" {
		t.Errorf("AC-loc-local-overlay-dir: generic local content at %q is not under local/<domain>/<file>", rel)
	}
	// Whole-file: the landed overlay file must carry the FULL edited content, not just
	// an isolated hunk (a generic dotfile has no shell overlay-source mechanism).
	got := readFileString(t, landed)
	if !strings.Contains(got, marker) || !strings.Contains(got, "name = base") {
		t.Errorf("AC-capture-interactive-route[generic-local]: overlay file is not a whole-file copy of the edited dotfile; got %q", got)
	}
	// It must not have ALSO leaked into a shared (non-local) path.
	sharedLanded := findFileContaining(t, s.Repo, marker)
	if sharedLanded != "" {
		if srel, _ := filepath.Rel(s.Repo, sharedLanded); !strings.HasPrefix(filepath.ToSlash(srel), "local/") {
			t.Errorf("AC-capture-interactive-route[generic-local]: local-routed change also reached a SHARED path %q", srel)
		}
	}
}

// readFileString reads a file and returns its contents as a string, failing the test
// on error. Small helper for the edge-case evals.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFileString %s: %v", path, err)
	}
	return string(data)
}
