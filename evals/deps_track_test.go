package evals

// Black-box evals for the v0.7.0 deps track: Homebrew Brewfile drift in the
// managed status surface, and npm globals as a coexisting deps manager. Both
// fake the package manager with a PATH shim (ferry resolves brew/npm through
// PATH), so no real brew/npm is required and the assertions are host-independent.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// brewfileName is the deps Brewfile filename for the ferry binary's platform
// (the manifest is selected by the binary's runtime.GOOS, which equals the test
// process's GOOS since FERRY_BIN is this host's build).
func brewfileName() string { return "Brewfile." + runtime.GOOS }

// stubPathEnv puts dir first on PATH so a stub brew/npm shadows any real one,
// while the real PATH still resolves git/coreutils behind it.
func stubPathEnv(dir string) string {
	return "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

// TestStatusReportsBrewfileDrift: with `brew = true` managed, `ferry status`
// reports Brewfile drift by comparing the live `brew bundle dump` against the
// repo Brewfile — READ-ONLY (it never installs and never rewrites the Brewfile).
func TestStatusReportsBrewfileDrift(t *testing.T) {
	t.Parallel()
	requireBin(t)
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, "[manage]\nbrew = true\n")
	brewfile := s.WriteRepoFile(t, filepath.Join("deps", brewfileName()), "brew \"a\"\nbrew \"b\"\n")
	snap := s.SnapshotFile(t, brewfile) // status must not rewrite the Brewfile

	stub := t.TempDir()
	// Live dump: a + c. `b` is repo-only (to install), `c` is live-only (to capture).
	writeStub(t, filepath.Join(stub, "brew"),
		"#!/bin/sh\ncase \"$*\" in\n*'bundle dump'*) printf 'brew \"a\"\\nbrew \"c\"\\n' ;;\nesac\nexit 0\n")

	out, errOut, code := s.FerryEnv([]string{stubPathEnv(stub)}, "status")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("status exited %d\n%s", code, combined)
	}
	if _, ok := containsAllFold(combined, "brew", "drifted"); !ok {
		t.Errorf("status did not report Brewfile drift:\n%s", combined)
	}
	snap.AssertUnchanged(t) // read-only: the repo Brewfile is untouched
}

// TestStatusBrewfileClean: when the live dump matches the repo Brewfile, status
// reports brew as clean (no drift).
func TestStatusBrewfileClean(t *testing.T) {
	t.Parallel()
	requireBin(t)
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, "[manage]\nbrew = true\n")
	s.WriteRepoFile(t, filepath.Join("deps", brewfileName()), "brew \"a\"\nbrew \"b\"\n")

	stub := t.TempDir()
	writeStub(t, filepath.Join(stub, "brew"),
		"#!/bin/sh\ncase \"$*\" in\n*'bundle dump'*) printf 'brew \"a\"\\nbrew \"b\"\\n' ;;\nesac\nexit 0\n")

	out, errOut, code := s.FerryEnv([]string{stubPathEnv(stub)}, "status")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("status exited %d\n%s", code, combined)
	}
	if !containsAnyFold(combined, "brew") {
		t.Fatalf("status did not mention brew at all:\n%s", combined)
	}
	if strings.Contains(strings.ToLower(combined), "drifted") {
		t.Errorf("status reported drift for a matching Brewfile:\n%s", combined)
	}
}

// TestNpmGlobalsCaptureWritesSortedNames: with `npm-globals = true` managed,
// `ferry capture` re-dumps the live global set to deps/npm-globals.txt as a
// deterministic, sorted, NAMES-ONLY list (versions dropped, npm itself excluded).
func TestNpmGlobalsCaptureWritesSortedNames(t *testing.T) {
	t.Parallel()
	requireBin(t)
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, "[manage]\nnpm-globals = true\n")

	stub := t.TempDir()
	writeStub(t, filepath.Join(stub, "npm"),
		"#!/bin/sh\ncase \"$*\" in\n*'ls -g'*) printf '{\"dependencies\":{\"zed\":{\"version\":\"1\"},\"apple\":{\"version\":\"2\"},\"npm\":{\"version\":\"3\"}}}\\n' ;;\nesac\nexit 0\n")

	out, errOut, code := s.FerryEnv([]string{stubPathEnv(stub)}, "capture")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("capture exited %d\n%s", code, combined)
	}
	got, err := os.ReadFile(s.RepoPath("deps", "npm-globals.txt"))
	if err != nil {
		t.Fatalf("capture did not write deps/npm-globals.txt: %v\n%s", err, combined)
	}
	if want := "apple\nzed\n"; string(got) != want {
		t.Errorf("npm-globals.txt = %q, want %q (sorted, names-only, npm excluded)", got, want)
	}
}

// TestNpmGlobalsApplyInstallsAlongsideBrew: `apply --deps` reconciles npm globals
// via `npm i -g` from the committed list AND, because npm globals COEXIST with the
// OS package manager, brew is driven in the same run (both managers invoked).
func TestNpmGlobalsApplyInstallsAlongsideBrew(t *testing.T) {
	t.Parallel()
	requireBin(t)
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, "[manage]\nbrew = true\nnpm-globals = true\n")
	s.WriteRepoFile(t, filepath.Join("deps", brewfileName()), "brew \"a\"\n")
	s.WriteRepoFile(t, filepath.Join("deps", "npm-globals.txt"), "typescript\npyright\n")

	stub := t.TempDir()
	npmLog := filepath.Join(stub, "npm.log")
	brewLog := filepath.Join(stub, "brew.log")
	// brew: log every call, return empty (list → empty installed set), exit 0.
	writeStub(t, filepath.Join(stub, "brew"),
		"#!/bin/sh\necho \"$*\" >> "+shellQuote(brewLog)+"\nexit 0\n")
	// npm: log every call; `i -g` is the reconcile we assert on.
	writeStub(t, filepath.Join(stub, "npm"),
		"#!/bin/sh\necho \"$*\" >> "+shellQuote(npmLog)+"\nexit 0\n")

	out, errOut, code := s.FerryEnv([]string{stubPathEnv(stub)}, "apply", "--deps")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("apply --deps exited %d\n%s", code, combined)
	}

	npmCalls, _ := os.ReadFile(npmLog)
	if !strings.Contains(string(npmCalls), "i -g") ||
		!strings.Contains(string(npmCalls), "typescript") ||
		!strings.Contains(string(npmCalls), "pyright") {
		t.Errorf("npm was not driven with `i -g <names>`:\nnpm log:\n%s\nferry:\n%s", npmCalls, combined)
	}
	// Coexistence: the OS package manager ran in the SAME apply (brew not suppressed
	// by npm detection, nor npm by brew detection).
	if countInvocations(brewLog) == 0 {
		t.Errorf("brew was never invoked — npm globals must COEXIST with brew, not replace it\n%s", combined)
	}
}

// TestNpmGlobalsAbsentSkipsCleanly: with npm-globals managed but no npm on PATH,
// `apply --deps` skips cleanly (reports npm absent, non-fatal) and installs nothing.
func TestNpmGlobalsAbsentSkipsCleanly(t *testing.T) {
	t.Parallel()
	requireBin(t)
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, "[manage]\nnpm-globals = true\n")
	s.WriteRepoFile(t, filepath.Join("deps", "npm-globals.txt"), "typescript\n")

	// Curated PATH with NEITHER npm nor any package manager discoverable.
	stub := t.TempDir()
	out, errOut, code := s.FerryEnv([]string{"PATH=" + stub}, "apply", "--deps")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("apply --deps exited %d with npm absent (must skip cleanly)\n%s", code, combined)
	}
	if !containsAnyFold(combined, "npm") {
		t.Errorf("apply --deps did not note the npm-globals skip when npm is absent:\n%s", combined)
	}
}
