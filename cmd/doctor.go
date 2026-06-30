package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

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

	healthy := true

	// Platform note.
	fmt.Fprintf(out, "platform: %s/%s\n", platform.OS(), platform.Arch())
	if platform.IsDarwin() {
		fmt.Fprintln(out, "  macOS detected (terminal preference domains available)")
	}

	// Host tools. git is REQUIRED; a missing git is a hard failure.
	if reportTool(out, "git", "required") {
		// present
	} else {
		healthy = false
	}

	// Package manager: report whichever is present, or that none is.
	reportPackageManager(out)

	// zsh: not required, but ferry's dotfiles target zsh; recommend apply --deps.
	if !reportTool(out, "zsh", "recommended") {
		fmt.Fprintln(out, "  zsh is ferry's target shell; install it via `ferry apply --deps`")
	}

	// ~/.ssh permission check (read-only: stat only, never read contents, never
	// modify). ferry is hands-off with ~/.ssh contents.
	reportSSHPerms(out)

	if !healthy {
		// A required prerequisite is missing: signal an unhealthy machine with a
		// clear message and a non-zero exit.
		fmt.Fprintln(out, "doctor: unhealthy — a required prerequisite is missing (see above)")
		return fmt.Errorf("doctor: required prerequisite missing")
	}
	fmt.Fprintln(out, "doctor: ok")
	return nil
}

// reportTool looks up a tool on PATH and prints a present/absent line. Returns
// whether the tool is present.
func reportTool(out io.Writer, name, importance string) bool {
	if _, err := exec.LookPath(name); err == nil {
		fmt.Fprintf(out, "  %-6s found (%s)\n", name, importance)
		return true
	}
	fmt.Fprintf(out, "  %-6s MISSING (%s) — not found on PATH\n", name, importance)
	return false
}

// reportPackageManager names the detected package manager (or reports that none
// is present). Detection is PATH-aware (platform.DetectPackageManager). A missing
// package manager is reported, never treated as a hard failure (apply --deps
// reports it too).
func reportPackageManager(out io.Writer) {
	switch platform.DetectPackageManager() {
	case platform.ManagerBrew:
		fmt.Fprintln(out, "  package manager: Homebrew (brew) found")
	case platform.ManagerApt:
		fmt.Fprintln(out, "  package manager: apt found")
	default:
		fmt.Fprintln(out, "  package manager: MISSING — no package manager present (ferry uses whatever is present and never installs one)")
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
