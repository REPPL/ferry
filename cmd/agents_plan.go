package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/agents"
	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/paths"
)

// agentsKeyPrefix namespaces the agents domain's records inside the shared
// last-applied store, so the dotfile de-scope pass can tell them apart from
// declared dotfiles.
const agentsKeyPrefix = "agents/"

// planAgents builds the per-target plan for the agents domain: the harness
// registry (built-ins overlaid with manifest declarations) expanded against
// the repo's instruction sources, the optional devtree workspace file, and
// the recursive asset trees — each as one hash-classified (content, target)
// planItem the shared apply/status/diff machinery acts on.
//
// A target that is currently a symlink (or any non-regular file) is SKIPPED
// with a clear warning rather than classified: ferry materialises regular-file
// copies only, and a lingering bridge symlink must be adopted or removed, not
// silently overwritten. Repo-side reads are routed through safeRepoPath, so a
// symlinked source in the config repo is refused exactly like a dotfile's.
func planAgents(ctx *cmdContext, home string, lastApplied *dotfile.Store) ([]planItem, []string, error) {
	cfg, err := config.LoadAgents(ctx.RepoPath)
	if err != nil {
		return nil, nil, err
	}

	aItems, warnings, err := agents.Plan(agents.PlanInput{
		RepoRoot: ctx.RepoPath,
		Home:     home,
		Config:   cfg,
		Guard:    func(cand string) (string, error) { return safeRepoPath(ctx.RepoPath, cand) },
	})
	if err != nil {
		return nil, nil, err
	}

	var items []planItem
	planned := map[string]bool{}
	for _, ai := range aItems {
		planned[ai.Key] = true
		// Agents content is never secret-rendered (secretRouted=false); its risk
		// comes from the three-way state alone (a brand-new adoption over an
		// existing file, or a conflict).
		state, risky, reason, cerr := classifyItem(ai.Target, ai.Content, false, lastApplied)
		if cerr != nil {
			var kind *dotfile.UnexpectedKindError
			if errors.As(cerr, &kind) {
				warnings = append(warnings, fmt.Sprintf(
					"%s skipped: %s is a symlink (or non-regular file) not managed by ferry — remove it, or run `ferry agents adopt <dir>` to migrate an existing symlink-based setup",
					ai.Label, ai.Target.Home))
				continue
			}
			return nil, nil, cerr
		}
		items = append(items, planItem{
			kind:       kindAgents,
			domain:     ai.Label,
			target:     ai.Target,
			content:    ai.Content,
			execBit:    ai.Exec,
			state:      state,
			risky:      risky,
			riskReason: reason,
		})
	}

	warnings = append(warnings, descopeAgentsWarnings(lastApplied, planned, true)...)
	return items, warnings, nil
}

// descopeAgentsWarnings warns about agents targets ferry previously applied
// (recorded under the agents/ prefix in the last-applied store) that the
// current plan no longer covers. Files are left untouched (de-scope = warn,
// never auto-remove). With the whole domain unmanaged the individual records
// collapse into ONE warning; with the domain managed, each dropped target
// (e.g. a harness removed from the manifest) warns individually.
func descopeAgentsWarnings(store *dotfile.Store, planned map[string]bool, managed bool) []string {
	var stale []string
	for _, name := range store.RecordedNames() {
		if !strings.HasPrefix(name, agentsKeyPrefix) {
			continue
		}
		if planned[name] {
			continue
		}
		stale = append(stale, name)
	}
	if len(stale) == 0 {
		return nil
	}
	if !managed {
		return []string{fmt.Sprintf(
			"warning: the agents domain is no longer managed; %d previously applied file(s) left as-is (now unmanaged). To fully revert them: ferry restore agents",
			len(stale))}
	}
	out := make([]string, 0, len(stale))
	for _, name := range stale {
		out = append(out, fmt.Sprintf(
			"warning: %s is no longer part of the agents plan; existing file left as-is (now unmanaged). To fully revert: ferry restore agents",
			name))
	}
	sort.Strings(out)
	return out
}

// agentsRestorePaths resolves the absolute $HOME destinations `ferry restore
// agents` should revert: the PERSISTED record of every target the domain has
// applied on this machine (agents-targets.json, unioned at each apply). The
// record — not the live manifest — is authoritative, for two reasons:
//   - a DE-SCOPED target (a harness removed from the manifest, a changed
//     devtree) is exactly what the de-scope warning tells the user to revert,
//     and the manifest no longer names it;
//   - restore must keep working with the config repo deleted or its manifest
//     unreadable (runRestore's repo-independence guarantee).
//
// The engine skips recorded paths without a baseline, so the cumulative
// record can never revert more than ferry actually touched. An empty record
// (domain never applied) is an error so the caller reports it clearly.
func agentsRestorePaths() ([]string, error) {
	stateDir, err := paths.StateDir()
	if err != nil {
		return nil, err
	}
	recorded, err := agents.RecordedTargetPaths(stateDir)
	if err != nil {
		return nil, err
	}
	if len(recorded) == 0 {
		return nil, errors.New("no agents targets recorded on this machine (the domain was never applied)")
	}
	return recorded, nil
}
