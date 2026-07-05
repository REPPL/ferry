package evals

// Install ACs that are testable offline. The `curl | bash` network path is
// deferred; the load-bearing offline assertions are: the binary is a single
// self-contained file, an install places exactly ferry into ~/.local/bin, and the
// PATH-advice line is printed (not applied to any rc file).
//
// install.sh does not exist yet. Where a test needs it, it locates it via the
// FERRY_INSTALL_SH env var (fallback: repo-root install.sh) and skips with a
// clear "(Wave-3/manual)" reason when absent — so the file always compiles.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// conformanceMode reports whether this is a final-conformance run (Wave-3+), in
// which a MISSING install.sh must FAIL the offline-gating installer ACs rather
// than skip — so an impl can't ship with no installer and still go green. Dev runs
// leave these unset and keep the absence-skip.
func conformanceMode() bool {
	return os.Getenv("FERRY_CONFORMANCE") == "1" || os.Getenv("FERRY_REQUIRE_INSTALL_SH") == "1"
}

// locateInstaller returns the path to install.sh. Absence of install.sh SKIPS on a
// normal dev run ("(Wave-3: install.sh not present)") but FAILS under conformance
// mode (FERRY_CONFORMANCE=1 / FERRY_REQUIRE_INSTALL_SH=1), so the final gate cannot
// silently pass without an installer. Once install.sh exists, the offline gates run
// (only the network/curl leg stays deferred elsewhere).
func locateInstaller(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("FERRY_INSTALL_SH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		missingInstaller(t, "FERRY_INSTALL_SH="+p+" does not exist")
	}
	// Fallback: repo-root install.sh relative to this test file's module root.
	// We probe a few likely locations without hard-coding an absolute path.
	for _, cand := range []string{"../install.sh", "install.sh"} {
		if abs, err := filepath.Abs(cand); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}
	missingInstaller(t, "set FERRY_INSTALL_SH to run installer evals once install.sh lands")
	return ""
}

// missingInstaller fails under conformance mode, otherwise skips with the Wave-3 reason.
func missingInstaller(t *testing.T, detail string) {
	t.Helper()
	if conformanceMode() {
		t.Fatalf("CONFORMANCE: install.sh is required but ABSENT — an offline-gating installer AC "+
			"(AC-install-path / AC-install-prints-path-line / installer-half of AC-install-no-homebrew) cannot pass without it. %s", detail)
	}
	t.Skipf("(Wave-3: install.sh not present): %s", detail)
}

// TestInstallSingleBinary covers AC-install-single-binary: ferry runs as a single
// self-contained binary. We copy ONLY the binary into a fresh empty dir (no other
// ferry-shipped files) and assert --help works.
func TestInstallSingleBinary_AC_install_single_binary(t *testing.T) {
	t.Parallel()
	bin := requireBin(t) // skips when FERRY_BIN unset

	isolated := t.TempDir()
	dst := filepath.Join(isolated, "ferry")
	if err := copyExecutable(bin, dst); err != nil {
		t.Fatalf("copy binary: %v", err)
	}

	// Run the isolated copy with a HOME pointed at another temp dir.
	home := t.TempDir()
	cmd := exec.Command(dst, "--help")
	cmd.Env = []string{"HOME=" + home, "PATH=" + isolated, "NO_COLOR=1"}
	cmd.Dir = isolated
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("AC-install-single-binary: isolated ferry --help failed (needs sidecar files?): %v\n%s", err, out)
	}
}

// TestInstallPrintsPathLine covers AC-install-prints-path-line AND the INSTALLER
// half of AC-no-shell-edit: when ~/.local/bin is NOT on PATH, the installer prints
// exactly one `export PATH=…$HOME/.local/bin…` line and edits NO shell rc file
// (it tells the user the line to add; it never adds it). When ~/.local/bin IS on
// PATH, the line is omitted. The rc-tripwire uses the same common rc-file set as
// the command-side AC-no-shell-edit eval (shellRCFiles in safety_test.go), so both
// the installer and ferry commands leave all of them byte-unchanged.
//
// This drives install.sh with a controlled PATH so it runs offline. Skips cleanly
// until install.sh exists.
func TestInstallPrintsPathLine_AC_install_prints_path_line(t *testing.T) {
	t.Parallel()
	installer := locateInstaller(t)

	s := NewSandbox(t)

	// Pre-seed the common shell rc files as tripwires; the installer (AC-no-shell-edit)
	// must not edit ANY of them.
	var snaps []fileTripwire
	for _, rc := range shellRCFiles {
		p := s.WriteHomeFile(t, rc, "# user rc "+rc+"\n", 0o644)
		snaps = append(snaps, s.SnapshotFile(t, p))
	}

	// Case 1: ~/.local/bin NOT on PATH -> installer SUCCEEDS (exit 0) and prints
	// EXACTLY ONE export PATH line. A failing installer that only prints the line
	// must NOT pass.
	pathWithout := "/usr/bin:/bin" // deliberately excludes ~/.local/bin
	out, code := runInstaller(t, installer, s.Home, pathWithout)
	if code != 0 {
		t.Errorf("AC-install-prints-path-line: installer exited %d (successful install must exit 0)\n%s", code, out)
	}
	if n := countExportPathLines(out); n != 1 {
		t.Errorf("AC-install-prints-path-line: installer printed %d `export PATH=….local/bin…` lines (want exactly 1)\n%s", n, out)
	}
	for _, snap := range snaps {
		snap.AssertUnchanged(t) // never edits shell rc
	}

	// Case 2: ~/.local/bin ON PATH -> the line need not be printed.
	// (We assert only the rc-untouched invariant strongly; omission of the line is
	// "need not", so we don't fail if it still prints informationally.)
	pathWith := s.BinDir + string(os.PathListSeparator) + "/usr/bin:/bin"
	_, _ = runInstaller(t, installer, s.Home, pathWith)
	for _, snap := range snaps {
		snap.AssertUnchanged(t)
	}
}

// offlineInstall is the result of driving install.sh offline: where the binary
// landed and the per-tool invocation logs for the "or run anything else" tripwire.
type offlineInstall struct {
	installed string // expected $HOME/.local/bin/ferry
	brewLog   string
	gitLog    string
	ferryLog  string
	out       []byte
}

// runOfflineInstall locates install.sh (skipping "(Wave-3: install.sh not present)"
// when absent), provides the offline binary source through several conventional
// channels (env hook, a `ferry` file in the installer CWD, a versioned bin/
// artifact) so it needs NO network, runs it with brew/git/ferry stubbed as
// zero-invocation tripwires, and returns the outcome. The actual ferry install
// path is exercised — not a test-helper copy.
func runOfflineInstall(t *testing.T, s *Sandbox) offlineInstall {
	t.Helper()
	installer := locateInstaller(t) // skips "(Wave-3: install.sh not present)" when absent

	cwd := t.TempDir()
	srcContent := "#!/bin/sh\necho ferry fake\n"
	envSrc := filepath.Join(cwd, "ferry-src")                      // FERRY_FAKE_BINARY hook
	cwdSrc := filepath.Join(cwd, "ferry")                          // a `ferry` file in the installer CWD
	binArtifact := filepath.Join(cwd, "bin", "ferry-darwin-arm64") // versioned build artifact
	if err := os.MkdirAll(filepath.Dir(binArtifact), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	for _, p := range []string{envSrc, cwdSrc, binArtifact} {
		writeStub(t, p, srcContent)
	}

	stubDir := t.TempDir()
	res := offlineInstall{
		installed: filepath.Join(s.BinDir, "ferry"),
		brewLog:   filepath.Join(stubDir, "brew.log"),
		gitLog:    filepath.Join(stubDir, "git.log"),
		ferryLog:  filepath.Join(stubDir, "ferry.log"),
	}
	writeStub(t, filepath.Join(stubDir, "brew"), "#!/bin/sh\necho \"$*\" >> "+shellQuote(res.brewLog)+"\nexit 0\n")
	writeStub(t, filepath.Join(stubDir, "git"), "#!/bin/sh\necho \"$*\" >> "+shellQuote(res.gitLog)+"\nexit 0\n")
	writeStub(t, filepath.Join(stubDir, "ferry"), "#!/bin/sh\necho \"$*\" >> "+shellQuote(res.ferryLog)+"\nexit 0\n")
	pathOverride := stubDir + string(os.PathListSeparator) + "/usr/bin:/bin"

	cmd := exec.Command("bash", installer)
	cmd.Dir = cwd
	cmd.Env = []string{
		"HOME=" + s.Home,
		"PATH=" + pathOverride,
		"NO_COLOR=1",
		"FERRY_FAKE_BINARY=" + envSrc,
		"FERRY_NO_NETWORK=1",
	}
	res.out, _ = cmd.CombinedOutput()
	return res
}

// TestInstallPath covers AC-install-path: the ACTUAL install path (install.sh)
// places an EXECUTABLE `ferry` at $HOME/.local/bin/ferry, offline, with no network.
// The placement proof comes from running install.sh — NOT a test-helper copy.
// GATING once install.sh exists; skips "(Wave-3: install.sh not present)" until then.
func TestInstallPath_AC_install_path(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	res := runOfflineInstall(t, s) // skips when install.sh absent

	info, statErr := os.Stat(res.installed)
	if statErr != nil {
		t.Fatalf("AC-install-path: install.sh ran but did NOT place ~/.local/bin/ferry offline "+
			"(offline placement is gating once install.sh exists; harness provided the binary via env/CWD/bin artifacts)\n%s", res.out)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("AC-install-path: ~/.local/bin/ferry is not a regular file")
	}
	if info.Mode()&0o100 == 0 {
		t.Errorf("AC-install-path: ~/.local/bin/ferry is not user-executable (mode %o)", info.Mode().Perm())
	}
	// It runs from that location (no network involved).
	cmd := exec.Command(res.installed, "--help")
	cmd.Env = []string{"HOME=" + s.Home, "PATH=" + s.BinDir, "NO_COLOR=1"}
	cmd.Dir = s.Home
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("AC-install-path: installed ~/.local/bin/ferry does not run: %v\n%s", err, out)
	}
}

// TestInstallPlacesBinaryNoHomebrew covers AC-install-single-binary (placement
// half) and AC-install-no-homebrew incl. the extended "or run anything else": after
// the OFFLINE install path, ~/.local/bin/ferry exists and is executable, and the
// installer ran ONLY binary placement (+ optional PATH print) — it invoked NONE of
// brew, git, or ferry (each a zero-invocation tripwire). GATING once install.sh
// exists (offline placement is not skippable for lack of an env hook).
func TestInstallPlacesBinaryNoHomebrew_AC_install_no_homebrew(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	res := runOfflineInstall(t, s)

	info, statErr := os.Stat(res.installed)
	if statErr != nil {
		t.Fatalf("AC-install-no-homebrew: install.sh ran but did NOT place ~/.local/bin/ferry offline\n%s", res.out)
	}
	if info.Mode()&0o100 == 0 {
		t.Errorf("AC-install-single-binary: installed ferry is not user-executable (mode %o)", info.Mode().Perm())
	}
	for name, log := range map[string]string{"brew": res.brewLog, "git": res.gitLog, "ferry": res.ferryLog} {
		if countInvocations(log) != 0 {
			t.Errorf("AC-install-no-homebrew: installer invoked %q (must do ONLY binary placement + optional PATH print)", name)
		}
	}
}

// noPMBootstrapTripwire is a curated PATH dir (no package manager discoverable)
// with curl/bash bootstrap recorders, plus the assertion that nothing bootstrapped
// a package manager. Shared by AC-install-no-homebrew and AC-deps-uses-present-pm.
type noPMBootstrapTripwire struct {
	dir     string // the curated PATH dir (use as the ONLY PATH entry)
	curlLog string
	bashLog string
}

// newNoPMBootstrapTripwire builds the curated PATH dir + bootstrap recorders.
func newNoPMBootstrapTripwire(t *testing.T) noPMBootstrapTripwire {
	t.Helper()
	dir := t.TempDir()
	tw := noPMBootstrapTripwire{
		dir:     dir,
		curlLog: filepath.Join(dir, "curl.log"),
		bashLog: filepath.Join(dir, "bash.log"),
	}
	// A Homebrew bootstrap is `/bin/bash -c "$(curl -fsSL …/install.sh)"`, so
	// curl+bash are the load-bearing tripwires. (We don't stub plain `sh` to avoid
	// false positives from benign internal shell-outs.)
	writeStub(t, filepath.Join(dir, "curl"), "#!/bin/sh\necho \"$*\" >> "+shellQuote(tw.curlLog)+"\nexit 0\n")
	writeStub(t, filepath.Join(dir, "bash"), "#!/bin/sh\necho \"$*\" >> "+shellQuote(tw.bashLog)+"\nexit 0\n")
	return tw
}

// pathEnv returns the PATH= override pinning PATH to ONLY the curated dir (no PM
// discoverable anywhere), making the no-bootstrap assertion host-independent.
func (tw noPMBootstrapTripwire) pathEnv() string { return "PATH=" + tw.dir }

// assertNoBootstrap fails if any package-manager binary appeared on the curated
// PATH or any fetch/run bootstrap (curl/bash) fired — i.e. ferry tried to INSTALL
// a package manager. cited names the AC for the failure message.
func (tw noPMBootstrapTripwire) assertNoBootstrap(t *testing.T, cited string) {
	t.Helper()
	for _, pm := range []string{"brew", "port", "apt", "apt-get", "dnf", "yum"} {
		if _, statErr := os.Stat(filepath.Join(tw.dir, pm)); statErr == nil {
			t.Errorf("%s: a %q binary appeared on PATH after `apply --deps` (bootstrapped a package manager)", cited, pm)
		}
	}
	if countInvocations(tw.curlLog) != 0 || countInvocations(tw.bashLog) != 0 {
		t.Errorf("%s: ferry invoked a fetch/run bootstrap (curl/bash) — must NEVER install Homebrew / a package manager", cited)
	}
}

// TestInstallNoHomebrewApplyDeps covers AC-install-no-homebrew (apply --deps half):
// with NO package manager present, `ferry apply --deps` REPORTS its absence AND —
// the load-bearing "does not install Homebrew or run anything else" promise — does
// NOT bootstrap one (no brew/curl|bash/PM-installer; no new PM binary on PATH).
func TestInstallNoHomebrewApplyDeps_AC_install_no_homebrew(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, `[manage]
dotfiles = [".zshrc"]
brew = true
`)
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")

	// Curated PATH with NO package manager discoverable + bootstrap recorders.
	tw := newNoPMBootstrapTripwire(t)

	out, errOut, _ := s.FerryEnv([]string{tw.pathEnv()}, "apply", "--deps")
	combined := out + errOut

	// (1) It must REPORT no package manager rather than installing one.
	if !containsAnyFold(combined, "no package manager", "package manager", "homebrew", "brew not", "not present", "not found") {
		t.Errorf("AC-install-no-homebrew: `apply --deps` with no package manager did not report its absence\n%s", combined)
	}
	// (2) It must NOT install/bootstrap one — the actual "never installs Homebrew /
	// or run anything else" gate, not just the message.
	tw.assertNoBootstrap(t, "AC-install-no-homebrew")
}

// TestHarnessBinaryRunsFromBinDir is a HARNESS SELF-CHECK — NOT an AC gate. It
// verifies the sandbox can place the built binary under ~/.local/bin and run it
// (so failures here point at the harness, not ferry's install). The AC-install-path
// gate (ferry's actual install behaviour) lives in TestInstallPath, which drives
// the real install.sh. This self-check makes no AC-install-path claim.
func TestHarnessBinaryRunsFromBinDir(t *testing.T) {
	t.Parallel()
	bin := requireBin(t) // built binary; skips when unset

	s := NewSandbox(t)
	dst := filepath.Join(s.BinDir, "ferry")
	if err := copyExecutable(bin, dst); err != nil {
		t.Fatalf("harness self-check: placement copy failed: %v", err)
	}
	cmd := exec.Command(dst, "--help")
	cmd.Env = []string{"HOME=" + s.Home, "PATH=" + s.BinDir, "NO_COLOR=1"}
	cmd.Dir = s.Home
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("harness self-check: binary under ~/.local/bin does not run: %v\n%s", err, out)
	}
}

// TestDepsUsesPresentPM covers AC-deps-uses-present-pm (the observable doc-level
// gate): with NO package manager present, `apply --deps` REPORTS "no package
// manager present" and does NOT bootstrap one. We stub a Homebrew-installer
// bootstrap path as a tripwire and assert it is never invoked, and that brew stays
// absent. (The positive "drives the present PM" half is AC-deps-install-attempted;
// the live install of declared deps is deferred to a real PM.)
func TestDepsUsesPresentPM_AC_deps_uses_present_pm(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, `[manage]
dotfiles = [".zshrc"]
brew = true
`)
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")

	// HOST-INDEPENDENT: a CURATED PATH that DEFINITELY lacks any package manager
	// (only the stub dir, no brew/port/apt/dnf/yum reachable) with curl/bash
	// bootstrap recorders. Shared tripwire with AC-install-no-homebrew.
	tw := newNoPMBootstrapTripwire(t)

	out, errOut, _ := s.FerryEnv([]string{tw.pathEnv()}, "apply", "--deps")
	combined := out + errOut

	// (1) Reports no package manager present.
	if !containsAnyFold(combined, "no package manager", "package manager", "homebrew", "brew not", "not present", "not found", "none") {
		t.Errorf("AC-deps-uses-present-pm: `apply --deps` with no PM did not report its absence\n%s", combined)
	}
	// (2) Did NOT bootstrap one — checked UNCONDITIONALLY (host-independent).
	tw.assertNoBootstrap(t, "AC-deps-uses-present-pm")
}

// countExportPathLines counts lines that both export PATH and reference .local/bin.
func countExportPathLines(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "export path") && strings.Contains(low, ".local/bin") {
			n++
		}
	}
	return n
}

// copyExecutable copies src to dst preserving the executable bit.
func copyExecutable(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		return err
	}
	return os.Chmod(dst, 0o755)
}

// runInstaller runs install.sh with a controlled HOME and PATH and returns its
// combined output and exit code. It never lets the installer touch the real environment.
func runInstaller(t *testing.T, installer, home, pathEnv string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", installer)
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + pathEnv,
		"NO_COLOR=1",
		"FERRY_NO_NETWORK=1", // installer MAY honour to stay offline
	}
	cmd.Dir = home
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if asExitError(err, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("runInstaller: failed to run install.sh: %v", err)
		}
	}
	return string(out), code
}

// --- checksums.txt fetch/verify (release-asset install path) --------------------
//
// These evals exercise install.sh's REAL fetch+verify logic offline: a fake
// release layout (a ferry-<goos>-<arch> binary plus an optional checksums.txt) is
// served over file:// through the TEST-ONLY FERRY_RELEASE_BASE_URL override, so
// the installer actually curls checksums.txt, extracts this target's hash, curls
// the binary, and compares — no GitHub, no network. They assert the fail-closed
// matrix the checksums-as-release-asset design promises: a valid checksum installs
// the exact served binary; a tampered binary, an absent checksums.txt, and a
// checksums.txt with no entry for this target each refuse and install NOTHING.

// fakeRelease is a directory mimicking a GitHub release's asset layout for THIS
// host's target: a `ferry-<goos>-<arch>` file, plus (optionally) a checksums.txt.
type fakeRelease struct {
	dir   string
	asset string // ferry-<goos>-<arch> for this host
}

// newFakeRelease writes the served binary (binContent) as this host's asset.
func newFakeRelease(t *testing.T, binContent []byte) fakeRelease {
	t.Helper()
	dir := t.TempDir()
	asset := "ferry-" + runtime.GOOS + "-" + runtime.GOARCH
	if err := os.WriteFile(filepath.Join(dir, asset), binContent, 0o755); err != nil {
		t.Fatalf("fakeRelease: write binary: %v", err)
	}
	return fakeRelease{dir: dir, asset: asset}
}

// writeChecksums writes checksums.txt from name->hash entries (sha256sum format).
// A correct entry yields a valid manifest; a wrong hash simulates a tampered
// binary; naming a different asset simulates a missing entry; not calling it at
// all omits checksums.txt entirely.
func (fr fakeRelease) writeChecksums(t *testing.T, entries map[string]string) {
	t.Helper()
	var b strings.Builder
	for name, hash := range entries {
		fmt.Fprintf(&b, "%s  %s\n", hash, name)
	}
	if err := os.WriteFile(filepath.Join(fr.dir, "checksums.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("fakeRelease: write checksums.txt: %v", err)
	}
}

// requireCurlFileScheme skips when curl is missing or lacks file:// support, so
// these evals never fail on a host that cannot serve the fake release locally.
func requireCurlFileScheme(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available; skipping checksums.txt fetch/verify evals")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		t.Fatalf("probe write: %v", err)
	}
	if err := exec.Command("curl", "-fsSL", "file://"+probe, "-o", filepath.Join(dir, "out")).Run(); err != nil {
		t.Skip("curl lacks file:// support; skipping checksums.txt fetch/verify evals")
	}
}

// runReleaseInstall drives install.sh against a fake release served over file://,
// with a controlled HOME and NO offline shortcut (FERRY_NO_NETWORK/FERRY_FAKE_BINARY
// unset) so the real fetch+verify path executes. Returns combined output, exit
// code, and the expected install path.
func runReleaseInstall(t *testing.T, installer string, s *Sandbox, fr fakeRelease) (out string, code int, installed string) {
	t.Helper()
	cmd := exec.Command("bash", installer)
	cmd.Dir = s.Home
	cmd.Env = []string{
		"HOME=" + s.Home,
		"PATH=" + os.Getenv("PATH"), // real curl / shasum / awk / cp
		"NO_COLOR=1",
		"FERRY_RELEASE_BASE_URL=file://" + fr.dir,
	}
	b, err := cmd.CombinedOutput()
	code = 0
	if err != nil {
		var ee *exec.ExitError
		if asExitError(err, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("runReleaseInstall: failed to run install.sh: %v", err)
		}
	}
	return string(b), code, filepath.Join(s.BinDir, "ferry")
}

// TestInstallVerifiesChecksumsSuccess: a valid checksums.txt entry → the installer
// downloads, verifies, and installs the EXACT served binary.
func TestInstallVerifiesChecksums_success(t *testing.T) {
	t.Parallel()
	installer := locateInstaller(t)
	requireCurlFileScheme(t)
	s := NewSandbox(t)

	content := []byte("#!/bin/sh\necho fake-ferry\n")
	fr := newFakeRelease(t, content)
	fr.writeChecksums(t, map[string]string{fr.asset: sha256Hex(content)})

	out, code, installed := runReleaseInstall(t, installer, s, fr)
	if code != 0 {
		t.Fatalf("valid checksum: installer exited %d (want 0)\n%s", code, out)
	}
	got, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("valid checksum: binary not installed at %s: %v\n%s", installed, err, out)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("valid checksum: installed bytes differ from the served release binary")
	}
}

// TestInstallVerifiesChecksumsTamperedRefused: the served binary does not match its
// checksums.txt hash → refuse (non-zero), install NOTHING.
func TestInstallVerifiesChecksums_tamperedRefused(t *testing.T) {
	t.Parallel()
	installer := locateInstaller(t)
	requireCurlFileScheme(t)
	s := NewSandbox(t)

	fr := newFakeRelease(t, []byte("tampered-binary-bytes\n"))
	// The manifest lists the hash of DIFFERENT bytes → mismatch on download.
	fr.writeChecksums(t, map[string]string{fr.asset: sha256Hex([]byte("the-legitimate-bytes\n"))})

	out, code, installed := runReleaseInstall(t, installer, s, fr)
	if code == 0 {
		t.Errorf("tampered binary: installer exited 0 (must refuse on hash mismatch)\n%s", out)
	}
	if _, err := os.Stat(installed); err == nil {
		t.Errorf("tampered binary: %s was installed despite a checksum mismatch (must install NOTHING)", installed)
	}
	if !strings.Contains(out, "SHA256 mismatch") {
		t.Errorf("tampered binary: expected a SHA256 mismatch message\n%s", out)
	}
}

// TestInstallVerifiesChecksumsAbsentRefused: no checksums.txt in the release →
// fail closed (non-zero), install NOTHING.
func TestInstallVerifiesChecksums_absentRefused(t *testing.T) {
	t.Parallel()
	installer := locateInstaller(t)
	requireCurlFileScheme(t)
	s := NewSandbox(t)

	fr := newFakeRelease(t, []byte("#!/bin/sh\necho fake-ferry\n")) // no writeChecksums

	out, code, installed := runReleaseInstall(t, installer, s, fr)
	if code == 0 {
		t.Errorf("absent checksums.txt: installer exited 0 (must fail closed)\n%s", out)
	}
	if _, err := os.Stat(installed); err == nil {
		t.Errorf("absent checksums.txt: %s installed with no checksums.txt (must install NOTHING)", installed)
	}
	if !strings.Contains(out, "checksums.txt") {
		t.Errorf("absent checksums.txt: expected a message naming the missing checksums.txt\n%s", out)
	}
}

// TestInstallVerifiesChecksumsMissingEntryRefused: checksums.txt exists but has no
// line for THIS target → fail closed (non-zero), install NOTHING.
func TestInstallVerifiesChecksums_missingEntryRefused(t *testing.T) {
	t.Parallel()
	installer := locateInstaller(t)
	requireCurlFileScheme(t)
	s := NewSandbox(t)

	content := []byte("#!/bin/sh\necho fake-ferry\n")
	fr := newFakeRelease(t, content)
	fr.writeChecksums(t, map[string]string{"ferry-nonexistent-target": sha256Hex(content)})

	out, code, installed := runReleaseInstall(t, installer, s, fr)
	if code == 0 {
		t.Errorf("no entry: installer exited 0 (must fail closed when its target is absent)\n%s", out)
	}
	if _, err := os.Stat(installed); err == nil {
		t.Errorf("no entry: %s installed though checksums.txt lacked its entry", installed)
	}
}
