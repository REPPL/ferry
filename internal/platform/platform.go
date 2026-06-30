// Package platform provides OS/arch detection and package-manager detection.
// It performs detection only — it never installs or mutates anything.
package platform

import (
	"os"
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
// Apple Silicon, Intel macOS, then Linuxbrew.
var brewPrefixes = []string{
	"/opt/homebrew",
	"/usr/local",
	"/home/linuxbrew/.linuxbrew",
}

// BrewPrefix detects the Homebrew installation prefix by probing the standard
// locations for a brew executable. Returns "" if Homebrew is not found. The
// prefix is DETECTED, never hardcoded, so the same binary works across the
// Apple Silicon, Intel, and Linuxbrew layouts.
func BrewPrefix() string {
	for _, prefix := range brewPrefixes {
		if isExecutable(filepath.Join(prefix, "bin", "brew")) {
			return prefix
		}
	}
	return ""
}

// HasBrew reports whether Homebrew is installed on this machine.
func HasBrew() bool { return BrewPrefix() != "" }

// HasApt reports whether the apt package manager is present (Debian/Ubuntu).
func HasApt() bool {
	for _, p := range []string{"/usr/bin/apt", "/usr/bin/apt-get"} {
		if isExecutable(p) {
			return true
		}
	}
	return false
}

// DetectPackageManager returns the available package manager, preferring
// Homebrew (the primary manager on both macOS and Linux) over apt. Returns
// ManagerNone when neither is present. Detection only — nothing is installed.
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
// executable by someone.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
