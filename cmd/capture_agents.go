package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/REPPL/ferry/internal/agents"
	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/secret"
)

// captureAgents is the agents-domain capture-back pass: live edits to deployed
// agent files (a harness CLAUDE.md sourced from general.md, a skill, a hook,
// …) flow back into the config repo through the SAME approve + route flow as
// dotfiles, instead of being reported as drift forever.
//
// It classifies every deployed agents target with the shared three-way machine
// (dotfile.ClassifyContent) against the last-deployed baseline:
//   - locally-drifted (the deployed file changed, the derived repo source did
//     NOT move past the baseline) is a capture candidate — reviewed hunk by
//     hunk and routed shared/local;
//   - true DIVERGENCE (the deployed file AND its repo source both moved past
//     the baseline = dotfile.StateConflict) is REFUSED with a diff — capture
//     never auto-merges and never guesses a winner;
//   - a SourceCombined target (a derived general+coding render) cannot be
//     decomposed back into two files, so a drift there is reported and the user
//     is pointed at general.md / coding.md — again, never guess.
//
// It then offers to ADOPT new agent-shaped files: regular files sitting under a
// resolved asset mapping's $HOME target directory that ferry never deployed.
// "Agent-shaped" is defined by the asset-mapping registry, not a hand list.
//
// Every write goes through writeRepoFile (guard-checked, copy-only) and the
// mandatory secret gate; capture never commits or pushes. Returns
// (captured, offered): a divergence refusal counts as offered (it surfaced) but
// not captured, so the run summary reflects it.
func captureAgents(ctx *cmdContext, home string, in *bufio.Reader, out io.Writer, lastApplied *dotfile.Store) (captured, offered int, err error) {
	cfg, err := config.LoadAgents(ctx.RepoPath)
	if err != nil {
		return 0, 0, err
	}
	guard := func(cand string) (string, error) { return safeRepoPath(ctx.RepoPath, cand) }
	pin := agents.PlanInput{RepoRoot: ctx.RepoPath, Home: home, Config: cfg, Guard: guard}

	targets, warnings, err := agents.CaptureTargets(pin)
	if err != nil {
		return 0, 0, err
	}
	for _, w := range warnings {
		fmt.Fprintln(out, w)
	}

	for _, ct := range targets {
		st, cerr := dotfile.ClassifyContent(ct.Target, ct.Content, lastApplied)
		if cerr != nil {
			// A live target that is a symlink / non-regular file is reported and
			// skipped, never captured (mirrors the planner's refusal).
			fmt.Fprintf(out, "agents: skipping %s (%v)\n", ct.Label, cerr)
			continue
		}
		switch st.State {
		case dotfile.StateLocallyDrifted:
			wrote, oerr := captureAgentsOne(agentsCaptureCtx{
				ctx: ctx, in: in, out: out,
				lastApplied: lastApplied,
			}, ct)
			if oerr != nil {
				return captured, offered, oerr
			}
			offered++
			if wrote {
				captured++
			}
		case dotfile.StateConflict:
			// TRUE DIVERGENCE: the deployed file AND the repo source both moved
			// past the last-deployed baseline. Refuse and SHOW the diff (repo
			// source vs live). Never write, never merge.
			offered++
			reportAgentsDivergence(out, ct)
		default:
			continue
		}
	}

	// Adopt-new: offer any new agent-shaped file the domain never deployed.
	cands, awarn, aerr := agents.AdoptCandidates(pin)
	if aerr != nil {
		return captured, offered, aerr
	}
	for _, w := range awarn {
		fmt.Fprintln(out, w)
	}
	for _, cand := range cands {
		wrote, oerr := adoptAgentsOne(agentsCaptureCtx{
			ctx: ctx, in: in, out: out,
			lastApplied: lastApplied,
		}, cand)
		if oerr != nil {
			return captured, offered, oerr
		}
		offered++
		if wrote {
			captured++
		}
	}

	return captured, offered, nil
}

// agentsCaptureCtx bundles the shared inputs for one agents capture/adopt.
type agentsCaptureCtx struct {
	ctx         *cmdContext
	in          *bufio.Reader
	out         io.Writer
	lastApplied *dotfile.Store
}

// captureAgentsOne reviews and routes ONE locally-drifted agents target. It runs
// the mandatory secret gate, a hunk-by-hunk review against the derived repo
// content, and routes the accepted result to the shared repo source or (assets
// only) the per-machine local overlay. Returns whether it wrote anything.
func captureAgentsOne(cc agentsCaptureCtx, ct agents.CaptureTarget) (bool, error) {
	fmt.Fprintf(cc.out, "\n=== %s (drifted) ===\n", ct.Label)

	// A combined target cannot be decomposed into general.md/coding.md, so a
	// live edit there is not capturable — never guess which source it belongs
	// to. Report and point at the sources.
	if ct.Kind == agents.CaptureCombined {
		fmt.Fprintf(cc.out, "  %s: this file is DERIVED from agents/general.md + agents/coding.md and cannot be split back automatically. Edit those sources in the config repo, then `ferry apply`.\n", ct.Label)
		return false, nil
	}

	liveBytes, err := os.ReadFile(ct.Target.Home)
	if err != nil {
		return false, err
	}

	// MANDATORY secret gate BEFORE any write. Agents content is never rendered
	// from the secret store (no placeholders), so a high-confidence secret in the
	// live file is BLOCKED from the repo entirely — only reject is offered (there
	// is no placeholder mechanism to route it through).
	if secret.GateText(string(liveBytes)).BlockedFromRepo {
		reportAgentsSecretBlock(cc.out, ct.Label)
		return false, nil
	}

	hunks := diffHunks(string(ct.Content), string(liveBytes))
	accepted := make([]bool, len(hunks))
	anyAccepted := false
	for i, h := range hunks {
		fmt.Fprintf(cc.out, "\n--- hunk %d/%d ---\n%s", i+1, len(hunks), renderHunk(h))
		ans := prompt(cc.in, cc.out, "accept this hunk? [y]es / [n]o (default n): ")
		if ans == "y" || ans == "yes" {
			accepted[i] = true
			anyAccepted = true
		}
	}
	if !anyAccepted {
		fmt.Fprintf(cc.out, "  %s: no hunks accepted; nothing written\n", ct.Label)
		return false, nil
	}
	allAccepted := true
	for _, a := range accepted {
		if !a {
			allAccepted = false
			break
		}
	}

	composed := applyHunks(string(ct.Content), hunks, accepted, endsWithNewline(liveBytes))
	// Re-scan the composed content: never write secret material to the repo.
	if secret.IsBlockedFromRepo(composed) {
		reportAgentsSecretBlock(cc.out, ct.Label)
		return false, nil
	}

	// Route the accepted result. Assets support shared + local; instruction
	// sources (general/coding) support shared only — a per-target local override
	// of general.md/coding.md would silently change every harness that shares it.
	var route secret.Route
	if ct.Kind == agents.CaptureAsset {
		route = promptRoute(cc.in, cc.out)
	} else {
		ans := prompt(cc.in, cc.out, "route this change? [s]hared / [r]eject (default r): ")
		if ans == "s" || ans == "shared" {
			route = secret.RouteShared
		} else {
			route = secret.RouteReject
		}
	}

	switch route {
	case secret.RouteShared:
		if err := writeAgentsRepoFile(cc.ctx.RepoPath, ct.RepoDest, []byte(composed), ct.Exec); err != nil {
			return false, err
		}
		fmt.Fprintf(cc.out, "  %s: captured -> shared (%s)\n", ct.Label, relTo(cc.ctx.RepoPath, ct.RepoDest))
	case secret.RouteLocal:
		if ct.LocalDest == "" {
			fmt.Fprintf(cc.out, "  %s: no per-machine local route for this target; re-run and route [s]hared or reject\n", ct.Label)
			return false, nil
		}
		if err := ensureLocalLayerIgnored(cc.ctx.RepoPath); err != nil {
			return false, err
		}
		if err := writeAgentsRepoFile(cc.ctx.RepoPath, ct.LocalDest, []byte(composed), ct.Exec); err != nil {
			return false, err
		}
		fmt.Fprintf(cc.out, "  %s: captured -> local (%s, gitignored)\n", ct.Label, relTo(cc.ctx.RepoPath, ct.LocalDest))
	default:
		fmt.Fprintf(cc.out, "  %s: rejected; nothing written\n", ct.Label)
		return false, nil
	}

	// Advance the last-applied baseline ONLY on a full reproduction (every hunk
	// accepted, so the newly written source now equals the live file): status then
	// reads clean and a later repo edit classifies repo-ahead, not a false
	// conflict. A partial capture leaves the record put so the remaining drift
	// keeps being reported. UpdateLastAppliedContent re-checks live == composed
	// itself, so this is safe even on a partial accept.
	if allAccepted {
		if _, err := dotfile.UpdateLastAppliedContent(ct.Target, []byte(composed), cc.lastApplied); err != nil {
			return false, err
		}
	}
	return true, nil
}

// adoptAgentsOne offers ONE new agent-shaped file for adoption: secret-gate it,
// confirm, and route the whole file to the shared repo source or the per-machine
// local overlay. A brand-new file has no repo source yet, so there is nothing to
// diff — the whole file is the change.
func adoptAgentsOne(cc agentsCaptureCtx, cand agents.AdoptCandidate) (bool, error) {
	fmt.Fprintf(cc.out, "\n=== %s (new, not yet managed) ===\n", cand.Label)

	liveBytes, err := os.ReadFile(cand.Home)
	if err != nil {
		return false, err
	}
	if secret.GateText(string(liveBytes)).BlockedFromRepo {
		reportAgentsSecretBlock(cc.out, cand.Label)
		return false, nil
	}

	ans := prompt(cc.in, cc.out, "adopt this new file into the agents domain? [y]es / [n]o (default n): ")
	if ans != "y" && ans != "yes" {
		fmt.Fprintf(cc.out, "  %s: not adopted; nothing written\n", cand.Label)
		return false, nil
	}

	switch promptRoute(cc.in, cc.out) {
	case secret.RouteShared:
		if err := writeAgentsRepoFile(cc.ctx.RepoPath, cand.RepoDest, liveBytes, cand.Exec); err != nil {
			return false, err
		}
		fmt.Fprintf(cc.out, "  %s: adopted -> shared (%s)\n", cand.Label, relTo(cc.ctx.RepoPath, cand.RepoDest))
		return true, nil
	case secret.RouteLocal:
		localDest := agentsLocalDestFor(cc.ctx.RepoPath, cand.RepoDest)
		if localDest == "" {
			fmt.Fprintf(cc.out, "  %s: could not resolve a local overlay path; re-run and route [s]hared\n", cand.Label)
			return false, nil
		}
		if err := ensureLocalLayerIgnored(cc.ctx.RepoPath); err != nil {
			return false, err
		}
		if err := writeAgentsRepoFile(cc.ctx.RepoPath, localDest, liveBytes, cand.Exec); err != nil {
			return false, err
		}
		fmt.Fprintf(cc.out, "  %s: adopted -> local (%s, gitignored)\n", cand.Label, relTo(cc.ctx.RepoPath, localDest))
		return true, nil
	default:
		fmt.Fprintf(cc.out, "  %s: not adopted; nothing written\n", cand.Label)
		return false, nil
	}
}

// reportAgentsDivergence prints the refusal + diff for a truly divergent agents
// target (both the deployed file and the repo source moved past the baseline).
// Capture writes NOTHING and never merges — the user reconciles by hand.
func reportAgentsDivergence(out io.Writer, ct agents.CaptureTarget) {
	fmt.Fprintf(out, "\n=== %s (DIVERGED — not captured) ===\n", ct.Label)
	fmt.Fprintf(out, "  Both the deployed file and its config-repo source changed since ferry last deployed it.\n")
	fmt.Fprintf(out, "  ferry refuses to auto-merge or guess a winner. Reconcile by hand, then `ferry apply`.\n")
	live, err := os.ReadFile(ct.Target.Home)
	if err != nil {
		return
	}
	// repo source (what apply would deploy) on the '-' side, live on the '+' side.
	hunks := diffHunks(string(ct.Content), string(live))
	fmt.Fprintf(out, "  diff (repo source vs live):\n")
	for _, h := range hunks {
		fmt.Fprint(out, renderHunk(h))
	}
}

// reportAgentsSecretBlock prints the standard secret-blocked refusal for an
// agents capture/adopt. Agents content has no placeholder mechanism, so a secret
// is blocked from the repo entirely (both shared and local) — only reject.
func reportAgentsSecretBlock(out io.Writer, label string) {
	fmt.Fprintf(out, "  %s: SECRET / credential material detected (e.g. a private key or token).\n", label)
	fmt.Fprintf(out, "  This change is BLOCKED from the repo entirely (both shared and local) and is never committed.\n")
}

// writeAgentsRepoFile writes a captured/adopted agents source into the repo
// worktree through the guard-checked writeRepoFile, preserving the executable
// bit for a hook script so the re-deployed copy stays runnable. It is the ONLY
// write this pass makes — it never runs git commit or git push.
func writeAgentsRepoFile(repoRoot, path string, content []byte, exec bool) error {
	if err := writeRepoFile(repoRoot, path, content); err != nil {
		return err
	}
	if exec {
		safe, err := safeRepoPath(repoRoot, path)
		if err != nil {
			return err
		}
		if err := os.Chmod(safe, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// agentsLocalDestFor maps a shared agents repo source
// (<repo>/agents/<source>/<rel>) to its per-machine overlay
// (<repo>/local/agents/<source>/<rel>) for an adopt LOCAL route. Returns "" when
// repoDest is not under <repo>/agents (defensive).
func agentsLocalDestFor(repoRoot, repoDest string) string {
	agentsRoot := filepath.Join(repoRoot, agents.RepoSubdir)
	rel, err := filepath.Rel(agentsRoot, repoDest)
	if err != nil || rel == ".." || filepath.IsAbs(rel) {
		return ""
	}
	return filepath.Join(repoRoot, agents.LocalSubdir, agents.RepoSubdir, rel)
}
