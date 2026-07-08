package evals

// doctor host-tool health AC. (AC-doctor-ssh-readonly lives in safety_test.go.)

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDoctorReportsHostTools covers AC-doctor-reports-host-tools: `ferry doctor`
// names the documented host-tool prerequisites — at least `git` and the package
// manager (Homebrew on macOS) — reporting presence/absence; and when a required
// prerequisite is missing from PATH, doctor warns and/or exits non-zero rather
// than silently passing.
func TestDoctorReportsHostTools_AC_doctor_reports_host_tools(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)

	// Case 1: git + a package manager present. Provide stub git/brew on a clean
	// PATH so the report is deterministic regardless of the host.
	stubDir := t.TempDir()
	writeStub(t, filepath.Join(stubDir, "git"), "#!/bin/sh\necho git version 2.40.0\nexit 0\n")
	writeStub(t, filepath.Join(stubDir, "brew"), "#!/bin/sh\necho stub-brew\nexit 0\n")
	withTools := "PATH=" + stubDir

	out, errOut, code := s.FerryEnv([]string{withTools}, "doctor")
	combined := out + errOut
	if !containsAnyFold(combined, "git") {
		t.Errorf("AC-doctor-reports-host-tools: doctor output does not name git\n%s", combined)
	}
	if !containsAnyFold(combined, "brew", "homebrew", "package manager") {
		t.Errorf("AC-doctor-reports-host-tools: doctor output does not name the package manager\n%s", combined)
	}
	// Healthy status: with both present, doctor must convey a HEALTHY/OK signal and
	// must NOT flag git/PM as missing, and should exit 0.
	if !containsAnyFold(combined, "ok", "healthy", "found", "present", "✓", "pass", "installed", "available") {
		t.Errorf("AC-doctor-reports-host-tools: with git+PM present, doctor gave no healthy/OK status\n%s", combined)
	}
	if containsAnyFold(combined, "git not found", "git missing", "git: missing", "no git") {
		t.Errorf("AC-doctor-reports-host-tools: doctor flagged git as missing when it was present\n%s", combined)
	}
	if code != 0 {
		t.Errorf("AC-doctor-reports-host-tools: doctor exited %d with all prerequisites present (want 0)", code)
	}

	// Case 2: git MISSING from PATH -> doctor must NAME git as missing/unhealthy.
	// An opaque non-zero exit is NOT enough — the output must report git itself as
	// missing/required/not-found (so silently passing or an unrelated failure fails).
	emptyDir := t.TempDir() // no git, no brew discoverable here
	missingPath := "PATH=" + emptyDir
	out2, errOut2, _ := s.FerryEnv([]string{missingPath}, "doctor")
	combined2 := out2 + errOut2
	if !namesToolMissing(combined2, "git") {
		t.Errorf("AC-doctor-reports-host-tools: with git absent, doctor did not NAME git as missing/required/not-found\n%s", combined2)
	}
}

// namesToolMissing reports whether out names the given tool together with a
// missing/unhealthy signal — on the SAME line (so "git" + "missing" must co-occur,
// not just appear independently anywhere in the output).
func namesToolMissing(out, tool string) bool {
	missingWords := []string{"missing", "not found", "not installed", "required", "unavailable", "no " + tool, "install " + tool, "✗"}
	for _, line := range splitLinesLower(out) {
		if !strings.Contains(line, tool) {
			continue
		}
		for _, w := range missingWords {
			if strings.Contains(line, strings.ToLower(w)) {
				return true
			}
		}
	}
	return false
}

// splitLinesLower lowercases and splits text into lines.
func splitLinesLower(s string) []string {
	return strings.Split(strings.ToLower(s), "\n")
}

// TestDoctorReportsInvariants covers the read-only managed-target invariant
// checks: on a clean managed setup `ferry doctor` OBSERVES that the deployed
// target is a regular-file copy (not a symlink), does not resolve under ~/.ssh,
// and lives inside $HOME — reporting each as a pass and exiting 0. It is the
// black-box proof that the invariant lines are present and pass on a healthy
// machine.
func TestDoctorReportsInvariants_AC_doctor_invariants(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	target := seedManagedDotfile(t, s, "export EDITOR=vim\n")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}
	if fi, err := os.Lstat(target); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("setup: expected %s to be a regular file after apply (err=%v)", target, err)
	}

	out, errOut, code := s.Ferry("doctor")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("AC-doctor-invariants: doctor exited %d on a clean managed setup (want 0)\n%s", code, combined)
	}
	if !containsAnyFold(combined, "invariant") {
		t.Errorf("AC-doctor-invariants: doctor did not report the managed-target invariant section\n%s", combined)
	}
	// The symlink invariant line must be present AND report a pass (no breach).
	symLine := lineContaining(combined, "symlink")
	if symLine == "" {
		t.Errorf("AC-doctor-invariants: doctor did not report the no-symlink invariant\n%s", combined)
	} else if !strings.Contains(symLine, "pass") {
		t.Errorf("AC-doctor-invariants: clean setup did not report the no-symlink invariant as pass\n%s", combined)
	}
	// The ~/.ssh and containment invariants must be observed too.
	if !containsAllFoldOK(combined, ".ssh") {
		t.Errorf("AC-doctor-invariants: doctor did not report the ~/.ssh invariant\n%s", combined)
	}
	if !containsAnyFold(combined, "inside $home", "inside $HOME") {
		t.Errorf("AC-doctor-invariants: doctor did not report the $HOME-containment invariant\n%s", combined)
	}
}

// TestDoctorFlagsSymlinkTarget plants a symlink where a ferry-deployed regular
// file should be and asserts doctor OBSERVES the breach: it names the offending
// target as a symlink and exits non-zero. The symlink points at another in-$HOME
// file (so it does not itself escape containment) — the breach is purely "a
// symlink where a regular-file copy belongs", exactly what ferry's copy-not-link
// invariant forbids. doctor detects it by lstat alone (never following the link,
// never reading contents).
func TestDoctorFlagsSymlinkTarget_AC_doctor_invariants(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	target := seedManagedDotfile(t, s, "export EDITOR=vim\n")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}

	// Replace the deployed regular file with a symlink to another in-$HOME file.
	decoy := s.HomePath("decoy.txt")
	if err := os.WriteFile(decoy, []byte("export EDITOR=vim\n"), 0o644); err != nil {
		t.Fatalf("write decoy: %v", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove target: %v", err)
	}
	if err := os.Symlink(decoy, target); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	out, errOut, code := s.Ferry("doctor")
	combined := out + errOut
	if code == 0 {
		t.Errorf("AC-doctor-invariants: doctor exited 0 despite a symlinked managed target (want non-zero)\n%s", combined)
	}
	symLine := lineContaining(combined, "symlink")
	if symLine == "" || !strings.Contains(symLine, "fail") {
		t.Errorf("AC-doctor-invariants: doctor did not report the symlinked target as a [fail]\n%s", combined)
	}
	if !containsAnyFold(combined, ".zshrc") {
		t.Errorf("AC-doctor-invariants: doctor did not name the offending target\n%s", combined)
	}
}

// lineContaining returns the first lowercased line of out that contains substr
// (case-insensitive), or "" if none does. Lets an assertion pair a signal
// (pass/fail) with the specific invariant line it belongs to.
func lineContaining(out, substr string) string {
	sub := strings.ToLower(substr)
	for _, line := range splitLinesLower(out) {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return ""
}

// containsAllFoldOK is a small case-insensitive substring check (the eval package
// already has containsAnyFold; this is the single-needle "contains" variant).
func containsAllFoldOK(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// writeStub writes an executable shell stub at path.
func writeStub(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writeStub %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("writeStub chmod %s: %v", path, err)
	}
}
