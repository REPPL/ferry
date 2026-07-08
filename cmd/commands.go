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

init locates or clones your config repo (over HTTPS — no SSH key needed) into
ferry's own space (~/.config/ferry/repo by default), writes ferry's config file
(~/.config/ferry/config.toml), and creates or confirms this machine's
ferry.local.toml manifest before any mutation. A bare "ferry init" starts a
fresh repo at that default location; "ferry init --fresh <dir>" places it
elsewhere. It then SHOWS the apply plan and stops; pass --apply (with --yes to
skip the prompt) to actually reconcile this machine.`,
	RunE: runInit,
}

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Reconcile this machine to the repo (deploy dotfiles, terminal settings)",
	Long: `Reconcile this machine to the repo.

apply moves config in one direction — repo -> this machine — deploying the
repo's version onto the machine. It writes in-scope dotfiles and terminal
settings, layering the per-machine .local overlay last. It is idempotent and
safe to re-run after every git pull. A run with changes walks a guided,
domain-grouped review that applies safe changes automatically and prompts on
risky ones; non-interactively, risky changes fail closed. A file you have
edited locally is left untouched for "ferry capture" to bring back — apply
never overwrites uncaptured local work. Dependency installs are a separate,
gated step run with --deps.`,
	RunE: runApply,
}

var captureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Pull local changes back into the repo (interactive, selective)",
	Long: `Pull local changes back into the repo.

capture moves config in the other direction to apply — this machine -> the
repo — bringing local edits back into the source of truth. It is interactive
and selective: it shows you each change and lets you approve it, then route it
shared (synced everywhere) or local (this machine only). Only declared targets
are visible, and a secret scan blocks sensitive values from ever reaching the
repo.`,
	RunE: runCapture,
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Publish captured changes and pull remote ones for a managed repo",
	Long: `Publish local changes and pull remote ones in one command.

sync is the everyday update for a managed (route-2) repo: it pulls remote work,
commits your locally-captured changes, and pushes them — WITHOUT ever losing
local work or force-pushing. It integrates the remote first from a clean
baseline, gates the whole push range for secrets, and pushes a single explicit
ref. On a conflict it leaves your machine byte-for-byte unchanged and asks you
to resolve with git. It never runs apply — run "ferry apply" afterwards to
deploy the pulled changes.`,
	RunE: runSync,
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

	// sync flags: an optional commit message for the captured changes, and the
	// route-1 override (a route-2 managed repo needs neither).
	syncCmd.Flags().StringP("message", "m", "", "commit message for locally-captured changes (default: a generated one)")
	syncCmd.Flags().Bool("allow-unmanaged", false, "sync a repo not marked managed (still HTTPS-only, secret-gated, never force-pushed)")

	rootCmd.AddCommand(
		initCmd,
		applyCmd,
		captureCmd,
		syncCmd,
		statusCmd,
		doctorCmd,
		diffCmd,
		restoreCmd,
	)
}
