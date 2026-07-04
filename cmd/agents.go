package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/agents"
	"github.com/REPPL/ferry/internal/config"
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
// import its sources into the config repo, record + remove the $HOME bridge
// symlinks that point into it, then run a normal apply so the managed copies
// materialise through the usual backup/journal machinery. <dir> is never
// modified.
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
	if err := agents.ImportSST(dir, destDir, out); err != nil {
		return err
	}

	// 2. Find the $HOME bridge symlinks pointing into <dir> (harness targets,
	// the optional devtree file, and the ~/.claude asset locations), record
	// them to a timestamped file, then remove them so apply can deploy
	// regular-file copies in their place.
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
		for _, br := range bridges {
			if err := os.Remove(br.Path); err != nil {
				return fmt.Errorf("remove bridge symlink %s: %w", br.Path, err)
			}
			fmt.Fprintf(out, "removed:  %s -> %s (becomes a managed copy)\n", br.Path, br.Dest)
		}
	} else {
		fmt.Fprintln(out, "adopt: no bridge symlinks into that directory found at the managed locations")
	}
	if cfg.Devtree == "" {
		fmt.Fprintln(out, "note: no [agents] devtree is configured — if the old setup linked a workspace CLAUDE.md, set devtree in ferry.toml and re-run adopt (or remove that symlink yourself)")
	}

	// 3. Materialise the managed copies through the normal apply transaction
	// (idempotent for every other in-scope domain).
	fmt.Fprintln(out, "adopt: running apply to materialise the managed copies")
	if err := applyPlan(ctx, nil, false, out); err != nil {
		return err
	}

	fmt.Fprintf(out, "adopt: done. %s was not modified; once you are satisfied, delete its old sync script (e.g. %s) — ferry now manages the bridges.\n",
		dir, filepath.Join(dir, "bin", "sync.sh"))
	return nil
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
