package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// notImplemented is the stub RunE for every A0 command. Real logic lands in
// later waves; until then each verb resolves, parses its flags, and reports
// that it is not yet implemented.
func notImplemented(c *cobra.Command, _ []string) error {
	return fmt.Errorf("%s: not yet implemented", c.Name())
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "First-run setup: locate/clone the config repo and write ferry's config",
	Long: `First-run, once-per-machine setup.

init locates or clones your config repo, writes ferry's config file
(~/.config/ferry/config.toml), confirms this machine's manifest before any
mutation, and optionally scaffolds your dev directory tree (with explicit
confirmation). It ends by running apply, which shows the plan and asks for
confirmation.`,
	RunE: notImplemented,
}

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Reconcile this machine to the repo (deploy dotfiles, terminal settings)",
	Long: `Reconcile this machine to the repo.

apply deploys in-scope dotfiles and terminal settings, layering the per-machine
.local overlay last. It is idempotent and safe to re-run after every git pull.
By default it reconciles files only and runs unattended; dependency installs are
a separate, gated step run with --deps.`,
	RunE: notImplemented,
}

var captureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Pull local changes back into the repo (interactive, selective)",
	Long: `Pull local changes back into the repo.

capture is interactive and selective: it shows you each change and lets you
approve it, then route it shared (synced everywhere) or local (this machine
only). Only declared targets are visible, and a secret scan blocks sensitive
values from ever reaching the repo.`,
	RunE: notImplemented,
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report config drift (what changed on this machine)",
	Long: `Report config drift.

status compares the live machine against the repo and reports what has changed
(git-status-like), classifying each managed target as clean, locally drifted,
repo-ahead, or in conflict.`,
	RunE: notImplemented,
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Report machine/tool health",
	Long: `Report machine and tool health.

doctor checks that required tools (git, zsh, a package manager) are present and
reports anything that needs attention, with the recommended next step.`,
	RunE: notImplemented,
}

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Preview what apply would change",
	Long: `Preview what apply would change.

diff is read-only: it shows what apply would change on this machine without
writing anything.`,
	RunE: notImplemented,
}

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Reverse ferry's changes, returning to the pre-ferry state from backup",
	Long: `Reverse ferry's changes.

restore returns managed files to their pre-ferry state from ferry's automatic
backup, snapshotting current state first. It reverses files only.`,
	RunE: notImplemented,
}

func init() {
	// Documented apply flags (lowercase kebab-case). Behaviour lands later.
	applyCmd.Flags().Bool("deps", false, "install declared dependencies (separate, explicit step)")
	applyCmd.Flags().Bool("dry-run", false, "preview changes without writing (see also: ferry diff)")

	rootCmd.AddCommand(
		initCmd,
		applyCmd,
		captureCmd,
		statusCmd,
		doctorCmd,
		diffCmd,
		restoreCmd,
	)
}
