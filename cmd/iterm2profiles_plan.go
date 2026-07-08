package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/iterm2profiles"
	"github.com/REPPL/ferry/internal/secret"
)

// planIterm2Profiles builds the per-target plan for the iTerm2 Dynamic Profiles
// domain: the repo's iterm2/DynamicProfiles/ tree fanned out file-by-file into
// hash-classified (content, target) planItems under
// ~/Library/Application Support/iTerm2/DynamicProfiles/, carried like a dotfile
// with the per-machine local/iterm2-profiles/ overlay winning per file, but
// repo-authoritative on capture (no capture pass). A malformed profile JSON is
// refused (never deployed) with a warning, so one bad file can never disable all of
// iTerm2's dynamic profiles.
//
// A target that is currently a symlink (or any non-regular file) is SKIPPED with a
// clear warning rather than classified: ferry materialises regular-file copies
// only. Repo-side reads route through safeRepoPath, so a symlinked source in the
// config repo is refused exactly like a dotfile's. Each target's bytes carry
// {{ferry.secret ...}} placeholders through the SAME render-or-SKIP as dotfiles.
func planIterm2Profiles(ctx *cmdContext, home string, secretStore *secret.Store, lastApplied *dotfile.Store) ([]planItem, []string, error) {
	pItems, warnings, err := iterm2profiles.Plan(iterm2profiles.PlanInput{
		RepoRoot: ctx.RepoPath,
		Home:     home,
		Guard:    func(cand string) (string, error) { return safeRepoPath(ctx.RepoPath, cand) },
		Linter:   iterm2profiles.PlutilLinter{},
	})
	if err != nil {
		return nil, nil, err
	}

	var items []planItem
	planned := map[string]bool{}
	for _, pi := range pItems {
		planned[pi.Key] = true
		rendered, missing, skip, rerr := renderSecrets(secretStore, pi.Content)
		if rerr != nil {
			return nil, nil, rerr
		}
		secretRouted := isSecretRouted(pi.Content)
		if skip {
			items = append(items, planItem{
				kind:         kindFile,
				domain:       pi.Label,
				target:       pi.Target,
				action:       "skipped",
				skip:         true,
				missing:      missing,
				secretRouted: secretRouted,
			})
			continue
		}
		state, risky, reason, cerr := classifyItem(pi.Target, rendered, secretRouted, lastApplied)
		if cerr != nil {
			var kind *dotfile.UnexpectedKindError
			if errors.As(cerr, &kind) {
				warnings = append(warnings, fmt.Sprintf(
					"%s skipped: %s is a symlink (or non-regular file) not managed by ferry — remove it, then re-run `ferry apply`",
					pi.Label, pi.Target.Home))
				continue
			}
			return nil, nil, cerr
		}
		items = append(items, planItem{
			kind:         kindFile,
			domain:       pi.Label,
			target:       pi.Target,
			content:      rendered,
			state:        state,
			risky:        risky,
			riskReason:   reason,
			secretRouted: secretRouted,
		})
	}

	warnings = append(warnings, descopeIterm2ProfilesWarnings(lastApplied, planned, true)...)
	return items, warnings, nil
}

// descopeIterm2ProfilesWarnings warns about Dynamic Profile targets ferry
// previously applied (recorded under the iterm2-profiles/ prefix in the last-applied
// store) that the current plan no longer covers. Files are left untouched (de-scope
// = warn, never auto-remove). With the whole domain unmanaged the records collapse
// into ONE warning; with the domain managed, each dropped target (a file removed
// from the tree) warns individually. A full `ferry restore` reverts them to their
// pre-ferry baseline.
func descopeIterm2ProfilesWarnings(store *dotfile.Store, planned map[string]bool, managed bool) []string {
	var stale []string
	for _, name := range store.RecordedNames() {
		if !strings.HasPrefix(name, iterm2profiles.KeyPrefix) {
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
			"warning: the iterm2-profiles domain is no longer managed; %d previously applied file(s) left as-is (now unmanaged). To revert them to their pre-ferry state: ferry restore",
			len(stale))}
	}
	out := make([]string, 0, len(stale))
	for _, name := range stale {
		out = append(out, fmt.Sprintf(
			"warning: %s is no longer part of the iterm2-profiles plan; existing file left as-is (now unmanaged). To revert: ferry restore",
			name))
	}
	sort.Strings(out)
	return out
}
