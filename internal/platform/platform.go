// Package platform provides OS/arch detection and package-manager detection.
// It performs detection only — it never installs or mutates anything.
package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// OS returns the running operating system (e.g. "darwin", "linux").
func OS() string { return runtime.GOOS }

// Arch returns the running architecture (e.g. "arm64", "amd64").
func Arch() string { return runtime.GOARCH }

// IsDarwin reports whether the host is macOS. macOS-only domains (iTerm2,
// Apple Terminal, defaults, mdls) gate on this and cleanly skip on Linux.
func IsDarwin() bool { return runtime.GOOS == "darwin" }

// IsLinux reports whether the host is Linux.
func IsLinux() bool { return runtime.GOOS == "linux" }

// PackageManager identifies a detected package manager.
type PackageManager string

const (
	ManagerNone PackageManager = ""
	ManagerBrew PackageManager = "brew"
	ManagerApt  PackageManager = "apt"
)

// brewPrefixes are the standard Homebrew install prefixes, in detection order:
// Apple Silicon, Intel macOS, then Linuxbrew. Used ONLY as a secondary fallback
// for locating the prefix; presence/detection is PATH-gated, never prefix-probed.
var brewPrefixes = []string{
	"/opt/homebrew",
	"/usr/local",
	"/home/linuxbrew/.linuxbrew",
}

// HasBrew reports whether a brew executable is resolvable on the current PATH.
// Detection is PATH-aware so it stays consistent with how the manager is
// actually invoked (deps resolves the binary via PATH). A brew install on disk
// at /opt/homebrew that is NOT on PATH does NOT count as present — this lets a
// curated PATH with no package manager correctly yield "no manager".
func HasBrew() bool {
	_, err := exec.LookPath("brew")
	return err == nil
}

// HasApt reports whether apt-get (or apt) is resolvable on the current PATH
// (Debian/Ubuntu). PATH-gated for the same reason as HasBrew.
func HasApt() bool {
	for _, name := range []string{"apt-get", "apt"} {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}

// BrewPrefix returns the Homebrew installation prefix. It anchors to the
// PATH-resolved brew (the resolved path's parent's parent, i.e. <prefix>/bin/brew
// → <prefix>) so the prefix matches the brew that would actually run, falling
// back to probing the standard prefixes only as a secondary. Returns "" if brew
// is not on PATH and no known prefix carries a brew executable. The prefix is
// DETECTED, never hardcoded, so one binary works across the Apple Silicon,
// Intel, and Linuxbrew layouts.
func BrewPrefix() string {
	if p, err := exec.LookPath("brew"); err == nil {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
		// <prefix>/bin/brew → parent's parent is <prefix>.
		return filepath.Dir(filepath.Dir(p))
	}
	for _, prefix := range brewPrefixes {
		if isExecutable(filepath.Join(prefix, "bin", "brew")) {
			return prefix
		}
	}
	return ""
}

// DetectPackageManager returns the available package manager, preferring
// Homebrew (the primary manager on both macOS and Linux) over apt. Returns
// ManagerNone when neither is resolvable on PATH. Detection only — nothing is
// installed.
func DetectPackageManager() PackageManager {
	if HasBrew() {
		return ManagerBrew
	}
	if HasApt() {
		return ManagerApt
	}
	return ManagerNone
}

// isExecutable reports whether path exists, is a regular file, and is
// executable by someone. Used only by BrewPrefix's secondary prefix probe.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
