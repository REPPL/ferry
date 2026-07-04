package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/agents"
	"github.com/REPPL/ferry/internal/backup"
	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/paths"
)

var agentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "Onboard project repos and migrate agent-instruction setups",
	Long: `Companion commands for the agents domain.

The domain itself deploys through the normal lifecycle: enable it with
"agents = true" under [manage], then ferry apply / status / diff / restore
handle the per-harness instruction files, the optional devtree workspace file,
and the ~/.claude skills/agents/hooks assets. These subcommands cover the two
one-off operations around it: scaffolding a project repo, and adopting an
existing symlink-based instruction directory into ferry.`,
}

var agentsScaffoldCmd = &cobra.Command{
	Use:   "scaffold <repo-dir> [name]",
	Short: "Set a project repo up for the multi-tool agent pipeline",
	Long: `Set a project repo up for the multi-tool agent pipeline.

Default mode (your own repo — tracked files): an AGENTS.md router stamped from
the config repo's template ({{PROJECT}}/{{DATE}} substituted), CLAUDE.md and
GEMINI.md as relative symlinks to AGENTS.md inside the repo, a committed
.work/ skeleton (NEXT.md, DECISIONS.md), .gitignore entries for the scratch
parts, and a pre-commit config when the repo has none.

--private mode (a repo you don't own — zero tracked trace): .work.local/ only
(NEXT.md, DECISIONS.md, ISSUES.md), hidden via .git/info/exclude, which is
never committed or pushed. No AGENTS.md, no symlinks, no .gitignore edits.

Idempotent; never overwrites an existing file.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runAgentsScaffold,
}

var agentsAdoptCmd = &cobra.Command{
	Use:   "adopt <dir>",
	Short: "Migrate an existing symlink-based instruction directory into ferry",
	Long: `Migrate an existing symlink-based agent-instruction setup into ferry.

adopt imports <dir>'s source files (general.md, coding.md, templates/,
skills/, agents/, hooks/) into the config repo's agents/ area, then replaces
each $HOME bridge symlink that points into <dir> with a ferry-managed
regular-file copy (the removed symlinks are listed in a timestamped record
first). It is non-destructive: <dir> itself is only ever read — delete its old
sync script yourself once you are satisfied.

Requires the agents domain to be enabled ("agents = true" under [manage]).`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentsAdopt,
}

func init() {
	agentsScaffoldCmd.Flags().Bool("private", false, "leave no tracked trace: .work.local/ only, excluded via .git/info/exclude")
	agentsCmd.AddCommand(agentsScaffoldCmd, agentsAdoptCmd)
	rootCmd.AddCommand(agentsCmd)
}

// runAgentsScaffold stamps the standard per-repo layout into a project repo
// from the config repo's agents/templates/. The project repo is the USER's
// repo (scaffold writes tracked project content there, or a private overlay);
// the only ~/.ssh-relevant path is the repo dir itself, which is refused when
// it resolves under ~/.ssh.
func runAgentsScaffold(c *cobra.Command, args []string) error {
	private, _ := c.Flags().GetBool("private")

	ctx, err := loadContext()
	if err != nil {
		return err
	}
	repoDir, err := guardRepoPath("scaffold repo dir", args[0])
	if err != nil {
		return err
	}
	name := ""
	if len(args) > 1 {
		name = args[1]
	}

	return agents.Scaffold(agents.ScaffoldOptions{
		RepoDir:      repoDir,
		Name:         name,
		Private:      private,
		TemplatesDir: filepath.Join(ctx.RepoPath, agents.RepoSubdir, "templates"),
		Guard:        func(cand string) (string, error) { return safeRepoPath(ctx.RepoPath, cand) },
	}, c.OutOrStdout())
}

// runAgentsAdopt migrates an existing symlink-based instruction directory:
// import its sources into the config repo, then — in ONE journalled
// transaction — replace every $HOME bridge symlink pointing into it with the
// managed regular-file copy (adoptTransaction). The bridge removals go
// through the backup engine (BackupAndRemove), so each symlink's prior state
// is in the baseline + journal: a failure rolls the whole run back inline
// (the symlinks come back), and even after success `ferry restore` can
// recreate them. <dir> is never modified.
func runAgentsAdopt(c *cobra.Command, args []string) error {
	ctx, err := loadContext()
	if err != nil {
		return err
	}
	out := c.OutOrStdout()

	if !ctx.Scope.IsManaged("agents") {
		return errors.New("the agents domain is not enabled: add `agents = true` under [manage] in ferry.toml (or ferry.local.toml), then re-run")
	}

	dir, err := guardRepoPath("adopt source dir", args[0])
	if err != nil {
		return err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	if fi, serr := os.Stat(dir); serr != nil || !fi.IsDir() {
		return fmt.Errorf("agents adopt: %s is not an existing directory", args[0])
	}

	// 1. Import the source set into the config repo's agents/ area. <dir> is
	// only read; existing repo files that differ are skipped, never clobbered.
	destDir, err := safeRepoPath(ctx.RepoPath, filepath.Join(ctx.RepoPath, agents.RepoSubdir))
	if err != nil {
		return err
	}
	if err := agents.ImportSST(dir, destDir, func(cand string) (string, error) { return safeRepoPath(ctx.RepoPath, cand) }, out); err != nil {
		return err
	}

	// 2. Find the $HOME bridge symlinks pointing into <dir> (harness targets,
	// the optional devtree file, the ~/.claude asset locations, and any
	// symlinked ANCESTOR of those — a directory-level bridge like a symlinked
	// ~/.claude itself), and record them to a timestamped list first.
	cfg, err := config.LoadAgents(ctx.RepoPath)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	bridges, err := agents.FindBridges(home, dir, cfg)
	if err != nil {
		return err
	}
	if len(bridges) > 0 {
		recordPath, rerr := recordAdoptedBridges(bridges)
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(out, "adopt: recorded %d bridge symlink(s) in %s\n", len(bridges), recordPath)
	} else {
		fmt.Fprintln(out, "adopt: no bridge symlinks into that directory found at the managed locations")
	}
	if cfg.Devtree == "" {
		fmt.Fprintln(out, "note: no [agents] devtree is configured — if the old setup linked a workspace CLAUDE.md, set devtree in ferry.toml and re-run adopt (or remove that symlink yourself)")
	}

	// 3. The swap: expand the (just-imported) repo sources into their deploy
	// items and replace the bridges with managed copies in ONE journalled run.
	items, warnings, err := agents.Plan(agents.PlanInput{
		RepoRoot: ctx.RepoPath,
		Home:     home,
		Config:   cfg,
		Guard:    func(cand string) (string, error) { return safeRepoPath(ctx.RepoPath, cand) },
	})
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(out, w)
	}
	if err := adoptTransaction(ctx, items, bridges, out); err != nil {
		return err
	}

	fmt.Fprintf(out, "adopt: done. %s was not modified; run `ferry status` to verify, then delete its old sync script (e.g. %s) — ferry now manages the bridges. Other domains reconcile as usual with `ferry apply`.\n",
		dir, filepath.Join(dir, "bin", "sync.sh"))
	return nil
}

// adoptTransaction performs the bridge swap as ONE journalled engine run:
// every bridge symlink is removed via BackupAndRemove (its symlink state —
// link target, mtime — lands in the immutable baseline and this run's
// journal), then every agents item is written through the same run's
// Backuper. The ordering guarantee is transactional rather than per-file: a
// symlink and its replacing copy occupy the SAME path, so the copy cannot be
// written first — instead, ANY failure rolls the whole open run back inline,
// which recreates the removed symlinks and reverts every written copy, so
// there is no reachable state where a bridge is gone and unrecoverable.
// After success, `ferry restore` (full or `agents`-scoped) still reverses
// the swap from the recorded baselines.
func adoptTransaction(ctx *cmdContext, items []agents.Item, bridges []agents.Bridge, out io.Writer) (retErr error) {
	eng, err := ctx.Engine()
	if err != nil {
		return err
	}

	lock, err := eng.Lock()
	if err != nil {
		var held *backup.ErrLockHeld
		if errors.As(err, &held) {
			return fmt.Errorf("another ferry apply is in progress (pid %d); try again later", held.OwnerPID)
		}
		return fmt.Errorf("acquire apply lock: %w", err)
	}
	defer func() {
		if uErr := lock.Unlock(); uErr != nil {
			if retErr == nil {
				retErr = fmt.Errorf("release apply lock: %w (the lock may be stale; remove it before the next apply)", uErr)
			} else {
				fmt.Fprintf(out, "warning: failed to release apply lock: %v; the lock may be stale and block the next apply\n", uErr)
			}
		}
	}()

	// Mirror applyPlan: register the terminal resources so a PRIOR incomplete
	// run holding a resource entry can roll back, then clear it.
	if err := registerTerminalDomains(ctx); err != nil {
		return err
	}
	if _, err := eng.RollbackIncomplete(); err != nil {
		return fmt.Errorf("roll back incomplete run: %w", err)
	}

	run, err := eng.Begin()
	if err != nil {
		return fmt.Errorf("begin adopt run: %w", err)
	}
	b := backuperFunc(func(target string, content []byte, perm os.FileMode) error {
		return eng.BackupAndWrite(run, target, content, perm)
	})
	removeBridge := func(path string) error { return eng.BackupAndRemove(run, path) }

	lastApplied, err := dotfile.OpenStore()
	if err != nil {
		return fmt.Errorf("open last-applied store: %w", err)
	}

	deferred, agentsTargets, err := adoptMutate(removeBridge, b, lastApplied, items, bridges, out)
	if err != nil {
		// Roll THIS open run back inline: the removed bridge symlinks are
		// recreated from the journal and every written copy reverts, so a
		// failed adopt leaves the machine exactly as it was.
		if _, rbErr := eng.RollbackIncomplete(); rbErr != nil {
			return fmt.Errorf("adopt failed (%v); inline rollback also failed (machine may be partially migrated — run `ferry restore` to recover): %w", err, rbErr)
		}
		return err
	}
	if err := run.Commit(); err != nil {
		return fmt.Errorf("commit adopt run: %w", err)
	}

	// Persist last-applied and the agents target record ONLY after the journal
	// commit (mutate()'s ordering): a crash before the commit rolls the files
	// back, and neither store can then be ahead of the rolled-back state.
	if err := dotfile.CommitLastApplied(deferred, lastApplied); err != nil {
		return fmt.Errorf("commit last-applied: %w", err)
	}
	if len(agentsTargets) > 0 {
		stateDir, serr := paths.StateDir()
		if serr != nil {
			return fmt.Errorf("record agents targets: %w", serr)
		}
		if err := agents.RecordTargets(stateDir, agentsTargets); err != nil {
			return fmt.Errorf("record agents targets: %w", err)
		}
	}
	return nil
}

// adoptMutate is the per-target body of the adopt run: journalled bridge
// removals first (freeing the destination paths), then every item written
// through the run's Backuper. It returns the deferred last-applied results
// and the target-record entries for the caller to persist AFTER the run
// commits. Any error returns immediately — the caller rolls the open run back.
func adoptMutate(removeBridge func(string) error, b dotfile.Backuper, lastApplied *dotfile.Store, items []agents.Item, bridges []agents.Bridge, out io.Writer) ([]dotfile.Result, map[string]string, error) {
	for _, br := range bridges {
		if err := removeBridge(br.Path); err != nil {
			return nil, nil, fmt.Errorf("remove bridge symlink %s: %w", br.Path, err)
		}
		fmt.Fprintf(out, "removed:  %s -> %s (journalled; becomes a managed copy)\n", br.Path, br.Dest)
	}

	var deferred []dotfile.Result
	agentsTargets := map[string]string{}
	for _, it := range items {
		agentsTargets[it.Key] = it.Target.Home
		res, err := agents.ApplyItem(it, lastApplied, b, false)
		if err != nil {
			var conflict *dotfile.ConflictError
			if errors.As(err, &conflict) {
				fmt.Fprintf(out, "  %-22s CONFLICT: edited live AND in the repo; not overwritten (update the repo copy, or `ferry apply --force` later)\n", it.Label)
				continue
			}
			return nil, nil, fmt.Errorf("deploy %s: %w", it.Label, err)
		}
		if res.Action == dotfile.ActionSkipped {
			fmt.Fprintf(out, "  %-22s skipped (edited live; update the repo copy, or `ferry apply --force` later)\n", it.Label)
			continue
		}
		deferred = append(deferred, res)
		fmt.Fprintf(out, "  %-22s %s\n", it.Label, res.Action)
	}
	return deferred, agentsTargets, nil
}

// recordAdoptedBridges writes the "<link> -> <destination>" list of replaced
// bridge symlinks to a timestamped file under ferry's state dir, so the
// pre-adopt wiring stays reconstructable even though the symlinks are gone.
func recordAdoptedBridges(bridges []agents.Bridge) (string, error) {
	stateDir, err := paths.StateDir()
	if err != nil {
		return "", err
	}
	if err := paths.HardenStoreDir(stateDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(stateDir, fmt.Sprintf("agents-adopt-%s.txt", time.Now().Format("20060102-150405")))
	var b strings.Builder
	for _, br := range bridges {
		fmt.Fprintf(&b, "%s -> %s\n", br.Path, br.Dest)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
