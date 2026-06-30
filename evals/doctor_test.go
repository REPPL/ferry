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
