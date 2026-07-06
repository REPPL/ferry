package cmd

// Reproduction coverage for the `restore --packages` UNINSTALL rail: every
// package name ferry hands a package manager must carry a `--` end-of-options
// separator AND (on apt, which runs as root under `sudo ferry restore
// --packages`) be re-validated as a plain package name. A tampered
// deps-installed.txt entry such as `-oDPkg::Pre-Invoke::=touch /tmp/x` (a leading
// "-" apt reads as an option) or `ufw-` (a trailing "-" apt reads as its REMOVE
// modifier) must abort the whole uninstall before any argv reaches apt-get.
//
// Traceability: WS3-apt — every package list passed to a package manager carries
// `--` and is validated.

import (
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/platform"
)

// recordingRunner captures every command line uninstallPackages would execute,
// so a test can assert BOTH that a malicious entry never reaches argv and that a
// legitimate list is passed through with the `--` separator.
type recordingRunner struct {
	calls [][]string
}

func (r *recordingRunner) Run(args ...string) (string, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	return "", nil
}

// TestUninstallApt_RefusesInjectedOption asserts a deps-installed.txt entry that
// apt would read as an OPTION (leading "-") is refused before apt-get runs.
func TestUninstallApt_RefusesInjectedOption(t *testing.T) {
	r := &recordingRunner{}
	err := uninstallPackages(r, platform.ManagerApt, []string{"ripgrep", "-oDPkg::Pre-Invoke::=touch /tmp/x"})
	if err == nil {
		t.Fatalf("uninstallPackages accepted an injected apt option; want refusal")
	}
	if len(r.calls) != 0 {
		t.Fatalf("apt-get was invoked despite a bad entry: %v", r.calls)
	}
}

// TestUninstallApt_RefusesTrailingRemoveModifier asserts a trailing "-" (apt's
// REMOVE modifier, parsed after "--") is refused.
func TestUninstallApt_RefusesTrailingRemoveModifier(t *testing.T) {
	r := &recordingRunner{}
	err := uninstallPackages(r, platform.ManagerApt, []string{"ufw-"})
	if err == nil {
		t.Fatalf("uninstallPackages accepted a trailing-'-' REMOVE modifier; want refusal")
	}
	if len(r.calls) != 0 {
		t.Fatalf("apt-get was invoked despite a bad entry: %v", r.calls)
	}
}

// TestUninstallApt_RefusesTrailingInstallModifier asserts a trailing "+" (apt's
// INSTALL modifier, parsed during package resolution after "--") is refused on
// the uninstall rail. A tampered entry such as `openssh-server+` would otherwise
// run `apt-get remove -y -- openssh-server+` and INSTALL openssh-server as root
// instead of removing it.
func TestUninstallApt_RefusesTrailingInstallModifier(t *testing.T) {
	r := &recordingRunner{}
	err := uninstallPackages(r, platform.ManagerApt, []string{"openssh-server+"})
	if err == nil {
		t.Fatalf("uninstallPackages accepted a trailing-'+' INSTALL modifier; want refusal")
	}
	if len(r.calls) != 0 {
		t.Fatalf("apt-get was invoked despite a bad entry: %v", r.calls)
	}
}

// TestUninstallApt_LegitimatePassesWithSeparator asserts the legitimate path is
// preserved: valid names are passed to apt-get with a `--` separator ahead of
// the package list.
func TestUninstallApt_LegitimatePassesWithSeparator(t *testing.T) {
	r := &recordingRunner{}
	if err := uninstallPackages(r, platform.ManagerApt, []string{"ripgrep", "python3.11", "libfoo-dev"}); err != nil {
		t.Fatalf("uninstallPackages refused a legitimate list: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected one apt-get call, got %v", r.calls)
	}
	got := r.calls[0]
	want := []string{"apt-get", "remove", "-y", "--", "ripgrep", "python3.11", "libfoo-dev"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("apt-get args = %v, want %v", got, want)
	}
}

// TestUninstallBrew_PassesWithSeparator asserts the brew rail also inserts `--`
// ahead of the package names, so a recorded name beginning with "-" cannot be
// read as a brew flag.
func TestUninstallBrew_PassesWithSeparator(t *testing.T) {
	r := &recordingRunner{}
	if err := uninstallPackages(r, platform.ManagerBrew, []string{"ripgrep", `cask "iterm2"`}); err != nil {
		t.Fatalf("uninstallPackages(brew) failed: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected one brew call, got %v", r.calls)
	}
	got := r.calls[0]
	want := []string{"brew", "uninstall", "--", "ripgrep", "iterm2"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("brew args = %v, want %v", got, want)
	}
}
