package cmd

import (
	"github.com/spf13/cobra"
)

// Each command's RunE is implemented in its own cmd/<name>.go file
// (runInit, runApply, runCapture, runStatus, runDoctor, runDiff, runRestore)
// so Wave-2 command workers edit separate files without colliding on this one.

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "First-run setup: locate/clone the config repo and write ferry's config",
	Long: `First-run, once-per-machine setup.

init locates or clones your config repo (over HTTPS — no SSH key needed),
writes ferry's config file (~/.config/ferry/config.toml), creates or confirms
this machine's ferry.local.toml manifest before any mutation, and optionally
scaffolds your ~/ABCDevelopment dev tree (with explicit confirmation). It then
SHOWS the apply plan and stops; pass --apply (with --yes to skip the prompt)
to actually reconcile this machine.`,
	RunE: runInit,
}

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Reconcile this machine to the repo (deploy dotfiles, terminal settings)",
	Long: `Reconcile this machine to the repo.

apply deploys in-scope dotfiles and terminal settings, layering the per-machine
.local overlay last. It is idempotent and safe to re-run after every git pull.
By default it reconciles files only and runs unattended; dependency installs are
a separate, gated step run with --deps.`,
	RunE: runApply,
}

var captureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Pull local changes back into the repo (interactive, selective)",
	Long: `Pull local changes back into the repo.

capture is interactive and selective: it shows you each change and lets you
approve it, then route it shared (synced everywhere) or local (this machine
only). Only declared targets are visible, and a secret scan blocks sensitive
values from ever reaching the repo.`,
	RunE: runCapture,
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report config drift (what changed on this machine)",
	Long: `Report config drift.

status compares the live machine against the repo and reports what has changed
(git-status-like), classifying each managed target as clean, locally drifted,
repo-ahead, or in conflict.`,
	RunE: runStatus,
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Report machine/tool health",
	Long: `Report machine and tool health.

doctor checks that required tools (git, zsh, a package manager) are present and
reports anything that needs attention, with the recommended next step.`,
	RunE: runDoctor,
}

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Preview what apply would change",
	Long: `Preview what apply would change.

diff is read-only: it shows what apply would change on this machine without
writing anything.`,
	RunE: runDiff,
}

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Reverse ferry's changes, returning to the pre-ferry state from backup",
	Long: `Reverse ferry's changes.

restore reverts managed files AND registered terminal preference domains to the
pre-ferry baseline from ferry's automatic backup, snapshotting current state
first so the revert is itself reversible. restore <domain> scopes it to one
target. --packages additionally uninstalls only the packages ferry installed.`,
	RunE: runRestore,
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
