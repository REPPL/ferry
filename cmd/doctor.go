package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/platform"
)

// runDoctor reports machine/tool health, READ-ONLY: it inspects host tools, the
// platform, and (stat-only) ~/.ssh permissions. It never installs, writes, or
// reads file CONTENTS — and is the sole command permitted to stat ~/.ssh/ for
// permission reporting. Exit is non-zero when a REQUIRED prerequisite (git) is
// missing.
func runDoctor(c *cobra.Command, _ []string) error {
	out := c.OutOrStdout()
	colour := stateColourer(out)

	healthy := true

	// Platform note.
	fmt.Fprintf(out, "platform: %s/%s\n", platform.OS(), platform.Arch())
	if platform.IsDarwin() {
		fmt.Fprintln(out, "  macOS detected (terminal preference domains available)")
	}

	// Host tools. git is REQUIRED; a missing git is a hard failure.
	fmt.Fprintln(out, "\nhost tools:")
	if reportTool(out, colour, "git", "required", "install git, then re-run `ferry doctor`") {
		// present
	} else {
		healthy = false
	}

	// Package manager: report whichever is present, or that none is.
	reportPackageManager(out, colour)

	// zsh: not required, but ferry's dotfiles target zsh; recommend apply --deps.
	reportTool(out, colour, "zsh", "recommended", "ferry's target shell; install it via `ferry apply --deps`")

	// ~/.ssh permission check (read-only: stat only, never read contents, never
	// modify). ferry is hands-off with ~/.ssh contents.
	reportSSHPerms(out)

	fmt.Fprintln(out) // blank line before the verdict footer
	if !healthy {
		// A required prerequisite is missing: signal an unhealthy machine with a
		// clear message and a non-zero exit.
		fmt.Fprintf(out, "%s: a required prerequisite is missing (see [fail] above)\n", colour(colRed, "doctor: unhealthy"))
		return fmt.Errorf("doctor: required prerequisite missing")
	}
	fmt.Fprintf(out, "%s: all prerequisites present\n", colour(colGreen, "doctor: ok"))
	return nil
}

// reportTool looks up a tool on PATH and prints a pass/fail/warn line with a fix
// hint when absent. A "required" tool that is absent is [fail]; a "recommended"
// tool that is absent is [warn]. Returns whether the tool is present. The tool
// name and a missing-word stay on the SAME line so a machine grep (and the doctor
// eval) can pair them.
func reportTool(out io.Writer, colour func(*color.Color, string) string, name, importance, fixHint string) bool {
	if _, err := exec.LookPath(name); err == nil {
		fmt.Fprintf(out, "  %s %-6s found (%s)\n", colour(colGreen, "[pass]"), name, importance)
		return true
	}
	if importance == "required" {
		fmt.Fprintf(out, "  %s %-6s MISSING (%s) — not found on PATH; %s\n", colour(colRed, "[fail]"), name, importance, fixHint)
	} else {
		fmt.Fprintf(out, "  %s %-6s missing (%s) — not found on PATH; %s\n", colour(colYellow, "[warn]"), name, importance, fixHint)
	}
	return false
}

// reportPackageManager names the detected package manager (or reports that none
// is present). Detection is PATH-aware (platform.DetectPackageManager). A missing
// package manager is reported, never treated as a hard failure (apply --deps
// reports it too).
func reportPackageManager(out io.Writer, colour func(*color.Color, string) string) {
	switch platform.DetectPackageManager() {
	case platform.ManagerBrew:
		fmt.Fprintf(out, "  %s package manager: Homebrew (brew) found\n", colour(colGreen, "[pass]"))
	case platform.ManagerApt:
		fmt.Fprintf(out, "  %s package manager: apt found\n", colour(colGreen, "[pass]"))
	default:
		fmt.Fprintf(out, "  %s package manager: none present — ferry uses whatever is present and never installs one\n", colour(colYellow, "[warn]"))
	}
}

// reportSSHPerms STATS ~/.ssh and a fixed set of well-known files (NEVER reading
// their contents, NEVER enumerating the directory) and FLAGS permissions that
// look too open. It never modifies anything. Correct perms (dir 0700, config/keys
// 0600) are not flagged.
//
// Deliberately stat-only: ferry lstats only the directory and a hard-coded list
// of well-known paths. It does NOT os.ReadDir ~/.ssh — enumerating the directory
// (listing its filenames) is itself a form of reading it, which the hands-off
// contract (AC-ssh-not-read / AC-ssh-untouched, docs/ssh.md) forbids. The price
// of never reading the dir: unusually-named keys are not perm-checked. That is
// the correct trade-off — doctor's sole permitted exception is stat/lstat of
// known paths, not directory enumeration.
func reportSSHPerms(out io.Writer) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	sshDir := filepath.Join(home, ".ssh")

	info, err := os.Lstat(sshDir)
	if err != nil {
		// No ~/.ssh at all: nothing to report (ferry never creates it).
		return
	}
	if !info.IsDir() {
		return
	}

	fmt.Fprintln(out, "~/.ssh permissions (read-only check):")

	// The directory itself must be 0700.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		fmt.Fprintf(out, "  ~/.ssh directory permissions are too open (%04o); should be 0700 — ferry never changes them\n", perm)
	}

	// Lstat a FIXED set of secret-sensitive well-known files by name (no
	// ReadDir, no content read). These should not be group/other readable or
	// writable (0600): config, authorized_keys, and the common default private
	// key names. known_hosts and *.pub are NOT secret (0644 is legitimate) and
	// are deliberately not checked. Arbitrary key filenames cannot be checked
	// without enumerating the directory, which is forbidden.
	wellKnown := []string{
		"config",
		"authorized_keys",
		"id_rsa",
		"id_ed25519",
		"id_ecdsa",
		"id_dsa",
	}
	for _, name := range wellKnown {
		fi, err := os.Lstat(filepath.Join(sshDir, name))
		if err != nil {
			// File doesn't exist (or can't be stat'd): nothing to flag.
			continue
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			fmt.Fprintf(out, "  ~/.ssh/%s permissions are too open (%04o); should be 0600 — ferry never changes them\n", name, perm)
		}
	}
}
