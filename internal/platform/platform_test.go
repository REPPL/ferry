package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// writeStub creates an executable shell stub named name in dir and returns dir.
// It lets a test put a fake brew/apt-get earlier on PATH without ever invoking a
// real package manager.
func writeStub(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

func TestDetect_BrewOnPath(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "brew")
	t.Setenv("PATH", dir)

	if !HasBrew() {
		t.Error("HasBrew() = false; want true with brew stub on PATH")
	}
	if got := DetectPackageManager(); got != ManagerBrew {
		t.Errorf("DetectPackageManager() = %q; want %q", got, ManagerBrew)
	}
}

// TestDetect_NoManager pins the eval scenario: a curated PATH with NO package
// manager must report absence even on a host that has brew installed on disk.
func TestDetect_NoManager(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if HasBrew() {
		t.Error("HasBrew() = true; want false on a PATH with no manager")
	}
	if HasApt() {
		t.Error("HasApt() = true; want false on a PATH with no manager")
	}
	if got := DetectPackageManager(); got != ManagerNone {
		t.Errorf("DetectPackageManager() = %q; want %q", got, ManagerNone)
	}
}

func TestDetect_AptOnly(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "apt-get")
	t.Setenv("PATH", dir)

	if HasBrew() {
		t.Error("HasBrew() = true; want false with only apt-get on PATH")
	}
	if !HasApt() {
		t.Error("HasApt() = false; want true with apt-get stub on PATH")
	}
	if got := DetectPackageManager(); got != ManagerApt {
		t.Errorf("DetectPackageManager() = %q; want %q", got, ManagerApt)
	}
}

// TestDetect_BrewPreferredOverApt confirms brew wins when both are on PATH.
func TestDetect_BrewPreferredOverApt(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "brew")
	writeStub(t, dir, "apt-get")
	t.Setenv("PATH", dir)

	if got := DetectPackageManager(); got != ManagerBrew {
		t.Errorf("DetectPackageManager() = %q; want %q (brew preferred)", got, ManagerBrew)
	}
}

// TestBrewPrefix_AnchorsToPathBrew checks the prefix is derived from the
// PATH-resolved brew (<prefix>/bin/brew → <prefix>), not a hardcoded location.
func TestBrewPrefix_AnchorsToPathBrew(t *testing.T) {
	prefix := t.TempDir()
	// BrewPrefix resolves symlinks (e.g. macOS /var → /private/var), so anchor
	// the expectation to the resolved prefix too.
	if resolved, err := filepath.EvalSymlinks(prefix); err == nil {
		prefix = resolved
	}
	bin := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	writeStub(t, bin, "brew")
	t.Setenv("PATH", bin)

	if got := BrewPrefix(); got != prefix {
		t.Errorf("BrewPrefix() = %q; want %q", got, prefix)
	}
}
