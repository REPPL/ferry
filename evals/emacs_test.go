package evals

// B3 — the Emacs configuration tree carried like a config-file terminal domain.
// These evals drive the real binary and assert observable outcomes: an emacs/
// tree deploys from the config repo to ~/.emacs.d/ as regular-file COPIES (never
// symlinks) when the domain is in scope; volatile, machine-generated paths (a
// decoy elpa/ package, a *.elc, the tangled inits/repp.el) are pruned and never
// deployed; the domain is gated by [manage] emacs; the per-machine
// local/emacs/<rel> overlay wins per file; and status reports clean after apply
// and drift after a live edit (repo-authoritative, no capture pass).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// emacsManifest enables the Emacs domain plus the dotfiles domain (so scope has
// a second domain to prove independence).
const emacsManifest = `[manage]
dotfiles = [".zshrc"]
emacs = true
brew = false
iterm2 = false
fonts = false
`

// TestEmacsAppliesTreeAsCopies proves an in-scope emacs/ tree deploys from the
// repo to ~/.emacs.d/ as regular-file copies (not symlinks), preserving the
// nested relpath, while excluded volatile paths are never deployed.
func TestEmacsAppliesTreeAsCopies(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, emacsManifest)

	// Carry set: init.el + a nested literate source.
	s.WriteRepoFile(t, filepath.Join("emacs", "init.el"), ";; shared init\n")
	s.WriteRepoFile(t, filepath.Join("emacs", "inits", "repp.org"), "* literate config\n")
	// Volatile decoys that must be pruned: a package file, compiled bytecode, and
	// the tangled Emacs-Lisp output.
	s.WriteRepoFile(t, filepath.Join("emacs", "elpa", "some-pkg", "foo.el"), ";; decoy pkg\n")
	s.WriteRepoFile(t, filepath.Join("emacs", "init.elc"), "decoy bytecode\n")
	s.WriteRepoFile(t, filepath.Join("emacs", "inits", "repp.el"), ";; tangled decoy\n")
	gitCommitAll(t, s.Repo, "emacs tree")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	// The carry set deployed, preserving the nested relpath.
	initTarget := s.HomePath(".emacs.d", "init.el")
	assertFileContains(t, initTarget, "shared init")
	assertFileContains(t, s.HomePath(".emacs.d", "inits", "repp.org"), "literate config")

	// Deployed as a REGULAR FILE copy — ferry never symlinks under $HOME.
	fi, err := os.Lstat(initTarget)
	if err != nil {
		t.Fatalf("lstat target: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("%s is a symlink; ferry must deploy a regular-file copy", initTarget)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("%s is not a regular file (mode %v)", initTarget, fi.Mode())
	}

	// The volatile decoys were pruned and never deployed.
	for _, rel := range [][]string{
		{".emacs.d", "elpa", "some-pkg", "foo.el"},
		{".emacs.d", "init.elc"},
		{".emacs.d", "inits", "repp.el"},
	} {
		p := s.HomePath(rel...)
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("excluded volatile path was deployed: %s", p)
		}
	}
}

// TestEmacsScopeGated proves the domain is OFF by default: with no `emacs` key
// in scope, a repo emacs/ tree is never deployed.
func TestEmacsScopeGated(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest) // no `emacs` key
	s.WriteRepoFile(t, filepath.Join("emacs", "init.el"), ";; should not deploy\n")
	gitCommitAll(t, s.Repo, "emacs not in scope")

	target := s.HomePath(".emacs.d", "init.el")
	tw := s.SnapshotFile(t, target) // absent
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	tw.AssertUnchanged(t) // still absent — domain out of scope
}

// TestEmacsLocalOverlayWins proves the per-machine overlay
// (local/emacs/<relpath>) overrides the shared repo copy for just that file,
// while a non-overridden file still deploys the shared content.
func TestEmacsLocalOverlayWins(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, emacsManifest)

	s.WriteRepoFile(t, filepath.Join("emacs", "init.el"), "SHARED_INIT\n")
	s.WriteRepoFile(t, filepath.Join("emacs", "inits", "custom.el"), "SHARED_CUSTOM\n")
	// Per-machine override of ONLY custom.el.
	s.WriteRepoFile(t, filepath.Join("local", "emacs", "inits", "custom.el"), "MACHINE_CUSTOM\n")
	gitCommitAll(t, s.Repo, "emacs with local overlay")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	// The overridden file carries the machine-local content; the base the shared.
	assertFileContains(t, s.HomePath(".emacs.d", "inits", "custom.el"), "MACHINE_CUSTOM")
	assertFileExcludes(t, s.HomePath(".emacs.d", "inits", "custom.el"), "SHARED_CUSTOM")
	assertFileContains(t, s.HomePath(".emacs.d", "init.el"), "SHARED_INIT")
}

// TestEmacsStatusCleanThenDrift proves status reports clean right after apply
// (with no spurious de-scope warning) and drift after a live edit — the
// repo-authoritative reconcile (apply would skip a live edit).
func TestEmacsStatusCleanThenDrift(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, emacsManifest)
	s.WriteRepoFile(t, filepath.Join("emacs", "init.el"), ";; shared init\n")
	gitCommitAll(t, s.Repo, "emacs")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	// Clean immediately after apply: in sync, no drift, and no spurious "no longer
	// managed" de-scope warning for the still-managed target.
	stdout, errOut, code := s.Ferry("status")
	if code != 0 {
		t.Fatalf("status exited %d; stderr:\n%s", code, errOut)
	}
	if !strings.Contains(stdout, "no drift detected") {
		t.Errorf("status did not report clean right after apply; stdout:\n%s", stdout)
	}
	if strings.Contains(stdout, "no longer managed") || strings.Contains(stdout, "no longer part of the emacs plan") {
		t.Errorf("clean status falsely warned the managed emacs target is de-scoped; stdout:\n%s", stdout)
	}

	// A live edit must surface as drift (repo-authoritative: apply would skip it).
	target := s.HomePath(".emacs.d", "init.el")
	if err := os.WriteFile(target, []byte(";; LOCAL EDIT\n"), 0o644); err != nil {
		t.Fatalf("live edit: %v", err)
	}
	stdout, errOut, code = s.Ferry("status")
	if code != 0 {
		t.Fatalf("status after edit exited %d; stderr:\n%s", code, errOut)
	}
	if strings.Contains(stdout, "no drift detected") || !strings.Contains(stdout, "drift") {
		t.Errorf("status did not report drift after a live edit; stdout:\n%s", stdout)
	}
}
