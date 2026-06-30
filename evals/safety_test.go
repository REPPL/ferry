package evals

// Safety ACs: the load-bearing invariants. ferry never touches ~/.ssh/ (write or
// read), doctor is read-only on ssh AND flags wrong perms, no command needs
// root/sudo or writes to system locations of its own accord, and ferry never edits
// the shell of its own accord.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// safetyRuns are command invocations across the documented surface used to
// exercise the broad safety tripwires (includes apply --deps).
var safetyRuns = [][]string{
	{"init"},
	{"apply"},
	{"apply", "--deps"},
	{"capture"},
	{"status"},
	{"diff"},
	{"doctor"},
	{"restore"},
}

// nonDoctorRuns are every documented command EXCEPT doctor — used for AC-ssh-not-read
// (doctor is the sole command permitted to stat ~/.ssh/ for perm reporting).
var nonDoctorRuns = [][]string{
	{"init"},
	{"apply"},
	{"apply", "--deps"},
	{"capture"},
	{"status"},
	{"diff"},
	{"restore"},
}

// adminSafeRuns are the documented commands run under the no-admin/system-location
// tripwire. It EXCLUDES `apply --deps` (the present PM's own writes are not ferry's)
// and in-scope terminal-app application (native preference store), per AC-no-admin-system-locations.
var adminSafeRuns = [][]string{
	{"init"},
	{"apply"},
	{"capture"},
	{"status"},
	{"diff"},
	{"doctor"},
	{"restore"},
}

// TestSSHUntouched covers AC-ssh-untouched: no ferry command writes/creates/
// deletes/modifies anything under ~/.ssh/. We seed the tripwire, run the whole
// documented surface, and assert ~/.ssh/ is byte-and-mode identical.
func TestSSHUntouched_AC_ssh_untouched(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")
	s.SSHTripwire(t)

	for _, args := range safetyRuns {
		// Feed empty stdin so interactive commands (capture/init) don't hang.
		s.FerryWithInput("", args...)
	}

	s.AssertSSHUntouched(t)
}

// TestDoctorSSHReadonly covers AC-doctor-ssh-readonly (STRENGTHENED): three halves.
//
//	(1) wrong ~/.ssh perms -> doctor FLAGS them with a SPECIFIC permission signal
//	    (an incidental ".ssh" mention does NOT count),
//	(2) doctor changes nothing under ~/.ssh/ (mode + content tripwire), and
//	(3) CORRECT perms -> doctor does NOT flag a permission problem.
func TestDoctorSSHReadonly_AC_doctor_ssh_readonly(t *testing.T) {
	t.Parallel()

	// flagsPerms detects a SPECIFIC "~/.ssh permissions are wrong" signal — a perms
	// word PLUS a wrong/open qualifier, or an explicit mode. A bare ".ssh" mention
	// is intentionally NOT sufficient.
	flagsPerms := func(out string) bool {
		hasPermWord := containsAnyFold(out, "permission", "perms", "mode")
		hasWrong := containsAnyFold(out, "too open", "too permissive", "wrong", "incorrect", "insecure", "should be", "world", "group-readable", "0777", "777", "0644")
		explicit := containsAnyFold(out, "0777", "too open", "too permissive", "insecure perm")
		return (hasPermWord && hasWrong) || explicit
	}

	// --- wrong perms branch ---
	t.Run("wrong_perms_flagged_and_untouched", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		s.SSHTripwire(t)
		sshDir := s.HomePath(".ssh")
		if err := os.Chmod(sshDir, 0o777); err != nil {
			t.Fatalf("chmod .ssh 0777: %v", err)
		}
		if err := os.Chmod(filepath.Join(sshDir, "id_ed25519"), 0o644); err != nil {
			t.Fatalf("chmod key 0644: %v", err)
		}
		s.ReSnapshotSSH(t) // record the wrong modes as the expected-preserved state

		out, errOut, _ := s.Ferry("doctor")
		combined := out + errOut
		if !flagsPerms(combined) {
			t.Errorf("AC-doctor-ssh-readonly: doctor did not flag the WRONG ~/.ssh permissions with a specific signal\n%s", combined)
		}
		// Tripwire: doctor changed nothing under ~/.ssh/ (modes or content).
		s.AssertSSHUntouched(t)
	})

	// --- correct perms branch ---
	t.Run("correct_perms_not_flagged", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		s.SSHTripwire(t) // SSHTripwire already sets canonical 700/600/644 modes
		sshDir := s.HomePath(".ssh")
		if err := os.Chmod(sshDir, 0o700); err != nil {
			t.Fatalf("chmod .ssh 0700: %v", err)
		}
		if err := os.Chmod(filepath.Join(sshDir, "id_ed25519"), 0o600); err != nil {
			t.Fatalf("chmod key 0600: %v", err)
		}
		s.ReSnapshotSSH(t)

		out, errOut, _ := s.Ferry("doctor")
		combined := out + errOut
		if flagsPerms(combined) {
			t.Errorf("AC-doctor-ssh-readonly: doctor flagged a permission problem when ~/.ssh perms were CORRECT (700/600)\n%s", combined)
		}
		s.AssertSSHUntouched(t)
	})
}

// TestSSHNotRead covers AC-ssh-not-read: non-`doctor` commands do not read files
// under ~/.ssh/. atime is unreliable, so the always-testable observable is
// no-write + no-capture (level 1 in the AC). Where a syscall tracer is available
// on macOS (fs_usage, needs privilege), level 2 (no open under ~/.ssh/) can be
// added; absent that, the contents-not-read depth is a documented limit.
func TestSSHNotRead_AC_ssh_not_read(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")
	s.SSHTripwire(t)

	secret := "-----BEGIN OPENSSH PRIVATE KEY-----"

	// Level 1 (always testable): after each NON-doctor command, ~/.ssh/ is
	// byte/mode unchanged AND no ~/.ssh/ content reached the repo / overlay.
	for _, args := range nonDoctorRuns {
		s.FerryWithInput("", args...)
	}
	s.AssertSSHUntouched(t)
	s.AssertNoSecretInRepo(t, secret)

	// Level 2 (no open()/openat() under ~/.ssh/): on macOS, where an fs_usage trace
	// is actually usable, ENFORCE it. fs_usage requires elevated privilege; when it
	// is not usable in this sandbox we capability-detect and t.Skip the strict check
	// with a precise reason — the Level-1 tripwire above is the floor.
	if runtime.GOOS != "darwin" {
		t.Log("AC-ssh-not-read: non-darwin host; contents-not-read verified to level 1 (no-write + no-capture).")
		return
	}
	if !fsUsageUsable(t, s) {
		t.Skip("AC-ssh-not-read: fs_usage syscall tracing not usable here (needs root/privileged fs_usage); " +
			"strict no-open(~/.ssh) check skipped. Level-1 tripwire (no-write + no-capture) asserted above.")
	}
	// fs_usage IS usable: assert ZERO opens under ~/.ssh/ for each non-doctor command.
	sshDir := s.HomePath(".ssh")
	for _, args := range nonDoctorRuns {
		opens := traceSSHOpens(t, s, sshDir, args...)
		if len(opens) > 0 {
			t.Errorf("AC-ssh-not-read: `ferry %v` opened path(s) under ~/.ssh/ (must not read):\n%s",
				args, strings.Join(opens, "\n"))
		}
	}
}

// fsUsageUsable reports whether an fs_usage trace can actually be captured here
// (binary present AND a trivial trace run succeeds — it fails without privilege).
func fsUsageUsable(t *testing.T, s *Sandbox) bool {
	t.Helper()
	if _, err := exec.LookPath("fs_usage"); err != nil {
		return false
	}
	// A 1-second probe trace of `true`; non-zero / permission error => not usable.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "fs_usage", "-w", "-f", "filesys")
	out, err := cmd.CombinedOutput()
	low := strings.ToLower(string(out))
	if err != nil && (strings.Contains(low, "permission") || strings.Contains(low, "must be run") || strings.Contains(low, "root")) {
		return false
	}
	return err == nil || ctx.Err() == context.DeadlineExceeded
}

// traceSSHOpens runs `ferry args...` under fs_usage and returns any open/openat
// lines that reference a path under sshDir. Empty slice => no ~/.ssh opens.
func traceSSHOpens(t *testing.T, s *Sandbox, sshDir string, args ...string) []string {
	t.Helper()
	bin := requireBin(t)

	// Start fs_usage tracing filesystem opens, then run ferry; correlate by path.
	ctx, cancel := context.WithTimeout(context.Background(), ferryTimeout)
	defer cancel()

	tracer := exec.CommandContext(ctx, "fs_usage", "-w", "-f", "filesys")
	var traceBuf strings.Builder
	tracer.Stdout = &traceBuf
	tracer.Stderr = &traceBuf
	if err := tracer.Start(); err != nil {
		t.Skipf("AC-ssh-not-read: could not start fs_usage: %v", err)
	}
	// Give the tracer a moment to attach, then run the command.
	time.Sleep(300 * time.Millisecond)

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = s.env()
	cmd.Dir = s.Home
	cmd.Stdin = strings.NewReader("")
	_ = cmd.Run()

	time.Sleep(300 * time.Millisecond)
	_ = tracer.Process.Kill()
	_ = tracer.Wait()

	var hits []string
	for _, line := range strings.Split(traceBuf.String(), "\n") {
		low := strings.ToLower(line)
		if !strings.Contains(low, "open") {
			continue
		}
		if strings.Contains(line, sshDir) {
			hits = append(hits, line)
		}
	}
	return hits
}

// TestNoAdmin covers AC-noadmin: no documented command invokes sudo / needs root,
// AND the documented happy-path commands SUCCEED without root (exit 0) — so a
// "refuses unless root" binary fails. We stub `sudo` as a tripwire recorder and
// assert it is never called, then assert exit 0 for the deterministic happy path.
func TestNoAdmin_AC_noadmin(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")

	stubDir, marker := makeSudoStub(t)
	// Prepend the stub dir so our fake sudo shadows any real one.
	pathOverride := "PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")

	// Exercise the whole surface for the sudo tripwire.
	for _, args := range safetyRuns {
		s.FerryEnv([]string{pathOverride}, args...)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("AC-noadmin: a documented command invoked `sudo` (privilege escalation)")
	}

	// Happy-path commands must SUCCEED as a non-root user (exit 0) — a binary that
	// refuses unless root would fail here. Deterministic, seeded-repo commands:
	for _, args := range [][]string{{"apply"}, {"diff"}, {"status"}, {"doctor"}, {"restore"}} {
		_, errOut, code := s.FerryEnv([]string{pathOverride}, args...)
		if code != 0 {
			t.Errorf("AC-noadmin: `ferry %v` exited %d as non-root (must work without admin)\n%s", args, code, errOut)
		}
	}

	// init and capture are offline-runnable HAPPY PATHS: each must COMPLETE without
	// admin/root — exit 0 (so a "refuses unless root" binary that exits non-zero
	// fails) AND never emit a root/admin refusal. init runs against a local file://
	// repo; capture with empty stdin is a no-op review (nothing approved).
	if _, err := exec.LookPath("git"); err == nil {
		sInit := NewSandbox(t)
		bare, _ := makeBareRepo(t)
		fileURL := "file://" + bare
		_, initErrOut, initCode := sInit.FerryEnv([]string{pathOverride}, "init", fileURL)
		if initCode != 0 {
			t.Errorf("AC-noadmin: `ferry init file://<local-repo>` exited %d as non-root (offline happy path must complete without admin)\n%s", initCode, initErrOut)
		}
		assertNoRootRefusal(t, "init", initErrOut)

		sCap := newCaptureSandboxNoApply(t)
		_, capErrOut, capCode := sCap.FerryEnv([]string{pathOverride}, "capture")
		if capCode != 0 {
			t.Errorf("AC-noadmin: `ferry capture` (empty stdin no-op) exited %d as non-root (offline happy path must complete without admin)\n%s", capCode, capErrOut)
		}
		assertNoRootRefusal(t, "capture", capErrOut)
	}
}

// assertNoRootRefusal fails if the command's output refused specifically for lack
// of root/admin (a "needs root / run as root / permission denied: must be root"
// signal). The exit-0 happy-path requirement is asserted separately by the caller.
func assertNoRootRefusal(t *testing.T, cmd, errOut string) {
	t.Helper()
	if containsAnyFold(errOut,
		"must be root", "requires root", "run as root", "need root", "needs root",
		"as administrator", "requires sudo", "must run with sudo", "elevated privileges",
		"permission denied: must", "are you root") {
		t.Errorf("AC-noadmin: `ferry %s` refused for lack of root/admin (must work without admin)\n%s", cmd, errOut)
	}
}

// shellRCFiles is the common set of shell rc files AC-no-shell-edit requires
// ferry (and the installer) never edit of their own accord. Shared by the command
// eval here and the installer eval (install_test.go) so both cover the same set.
var shellRCFiles = []string{
	".zshrc", ".zshenv", ".zprofile",
	".bashrc", ".bash_profile", ".profile",
	".config/fish/config.fish",
}

// TestNoShellEdit covers AC-no-shell-edit: ferry never modifies the user's shell
// rc files of its own accord. We pre-seed the common rc files (NOT in the managed
// scope) and assert init/apply leave them byte-unchanged. (The INSTALLER half of
// AC-no-shell-edit is covered by the installer rc-tripwire eval in install_test.go,
// which also cites AC-no-shell-edit and uses this same rc-file set.)
func TestNoShellEdit_AC_no_shell_edit(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	// Manifest manages .gitconfig only — NOT any shell rc — so any change to a
	// shell rc would be ferry acting "on its own accord".
	s.SeedSharedManifest(t, `[manage]
dotfiles = [".gitconfig"]
`)
	s.WriteRepoFile(t, ".gitconfig", "[user]\n  name = x\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".gitconfig"), "[user]\n  name = x\n")

	// Pre-seed the common shell rc files with snapshots.
	var snaps []fileTripwire
	for _, rc := range shellRCFiles {
		p := s.WriteHomeFile(t, rc, "# user's own "+rc+", do not touch\n", 0o644)
		snaps = append(snaps, s.SnapshotFile(t, p))
	}

	for _, args := range [][]string{{"init"}, {"apply"}} {
		s.FerryWithInput("", args...)
	}

	for _, snap := range snaps {
		snap.AssertUnchanged(t)
	}
}

// TestNoAdminSystemLocations covers AC-no-admin-system-locations (RENAMED from
// AC-no-writes-outside-home-and-installdir; round-2 reword). It asserts the ACTUAL
// doc promises, NOT a "$HOME-only" framing:
//
//	(a) no command requires sudo/root or writes to admin/system locations of its
//	    own accord (/usr/local/bin, /etc, /opt, /Library), and
//	(c) no shell-rc edits (covered separately by AC-no-shell-edit).
//
// EXCLUDES `apply --deps` (the present PM's own writes) and in-scope terminal-app
// application (native preference store) — so we run adminSafeRuns, not safetyRuns.
// (b) installer-targets-~/.local/bin is covered by AC-install-path.
//
// HONEST LIMIT: AssertNoWritesOutsideHomeAndInstallDir samples promised system
// roots via mtime rather than tracing every syscall; the sudo tripwire is the
// strong, portable signal. The user's repo clone and temp files are permitted.
func TestNoAdminSystemLocations_AC_no_admin_system_locations(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")

	stubDir, sudoMarker := makeSudoStub(t)
	pathOverride := "PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")

	s.AssertNoWritesOutsideHomeAndInstallDir(t, func() {
		for _, args := range adminSafeRuns {
			s.FerryEnv([]string{pathOverride}, args...)
		}
	})

	// (a) sudo never invoked.
	if _, err := os.Stat(sudoMarker); err == nil {
		t.Errorf("AC-no-admin-system-locations: a documented command invoked `sudo`")
	}
}

// makeSudoStub writes a fake `sudo` that records invocation and exits non-zero
// (so a caller that genuinely depends on it would also fail loudly).
func makeSudoStub(t *testing.T) (dir, marker string) {
	t.Helper()
	dir = t.TempDir()
	marker = filepath.Join(dir, "sudo_was_called")
	script := "#!/bin/sh\ntouch " + shellQuote(marker) + "\nexit 1\n"
	stub := filepath.Join(dir, "sudo")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("makeSudoStub: %v", err)
	}
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatalf("makeSudoStub chmod: %v", err)
	}
	return dir, marker
}
