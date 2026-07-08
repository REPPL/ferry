package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/emacs"
	"github.com/REPPL/ferry/internal/secret"
)

// planEmacs builds the per-target plan for the Emacs configuration domain: the
// repo's emacs/ tree fanned out file-by-file (volatile, machine-generated paths
// pruned) into hash-classified (content, target) planItems under ~/.emacs.d/,
// carried like a dotfile with the per-machine .local overlay winning per file,
// but repo-authoritative on capture (no capture pass).
//
// A target that is currently a symlink (or any non-regular file) is SKIPPED with
// a clear warning rather than classified: ferry materialises regular-file copies
// only. Repo-side reads route through safeRepoPath, so a symlinked source in the
// config repo is refused exactly like a dotfile's.
//
// Each target's bytes carry {{ferry.secret ...}} placeholders through the SAME
// render-or-SKIP as dotfiles and terminals (renderSecrets): a present secret is
// substituted and the target flagged secretRouted (deployed 0600, its plaintext
// kept out of the last-applied snapshot); a MISSING secret SKIPS the whole target
// rather than deploy the literal placeholder into the live Emacs config.
func planEmacs(ctx *cmdContext, home string, secretStore *secret.Store, lastApplied *dotfile.Store) ([]planItem, []string, error) {
	eItems, warnings, err := emacs.Plan(emacs.PlanInput{
		RepoRoot: ctx.RepoPath,
		Home:     home,
		Guard:    func(cand string) (string, error) { return safeRepoPath(ctx.RepoPath, cand) },
	})
	if err != nil {
		return nil, nil, err
	}

	var items []planItem
	planned := map[string]bool{}
	for _, ei := range eItems {
		planned[ei.Key] = true
		// Render secrets EXACTLY like a dotfile/terminal: substitute present
		// placeholders, or SKIP the whole target (never deploy a literal
		// {{ferry.secret}}) when a referenced secret is missing. secretRouted is
		// judged on the PRE-render bytes.
		rendered, missing, skip, rerr := renderSecrets(secretStore, ei.Content)
		if rerr != nil {
			return nil, nil, rerr
		}
		secretRouted := isSecretRouted(ei.Content)
		if skip {
			items = append(items, planItem{
				kind:         kindFile,
				domain:       ei.Label,
				target:       ei.Target,
				action:       "skipped",
				skip:         true,
				missing:      missing,
				secretRouted: secretRouted,
			})
			continue
		}
		state, risky, reason, cerr := classifyItem(ei.Target, rendered, secretRouted, lastApplied)
		if cerr != nil {
			var kind *dotfile.UnexpectedKindError
			if errors.As(cerr, &kind) {
				warnings = append(warnings, fmt.Sprintf(
					"%s skipped: %s is a symlink (or non-regular file) not managed by ferry — remove it, then re-run `ferry apply`",
					ei.Label, ei.Target.Home))
				continue
			}
			return nil, nil, cerr
		}
		items = append(items, planItem{
			kind:         kindFile,
			domain:       ei.Label,
			target:       ei.Target,
			content:      rendered,
			execBit:      ei.Exec,
			state:        state,
			risky:        risky,
			riskReason:   reason,
			secretRouted: secretRouted,
		})
	}

	warnings = append(warnings, descopeEmacsConfigWarnings(lastApplied, planned, true)...)
	return items, warnings, nil
}

// descopeEmacsConfigWarnings warns about Emacs targets ferry previously applied
// (recorded under the emacs/ prefix in the last-applied store) that the current
// plan no longer covers. Files are left untouched (de-scope = warn, never
// auto-remove). With the whole domain unmanaged the records collapse into ONE
// warning; with the domain managed, each dropped target (a file removed from the
// tree) warns individually. A full `ferry restore` reverts them to their
// pre-ferry baseline (Emacs files deploy through the backup engine, like
// dotfiles).
func descopeEmacsConfigWarnings(store *dotfile.Store, planned map[string]bool, managed bool) []string {
	var stale []string
	for _, name := range store.RecordedNames() {
		if !strings.HasPrefix(name, emacs.KeyPrefix) {
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
			"warning: the emacs domain is no longer managed; %d previously applied file(s) left as-is (now unmanaged). To revert them to their pre-ferry state: ferry restore",
			len(stale))}
	}
	out := make([]string, 0, len(stale))
	for _, name := range stale {
		out = append(out, fmt.Sprintf(
			"warning: %s is no longer part of the emacs plan; existing file left as-is (now unmanaged). To revert: ferry restore",
			name))
	}
	sort.Strings(out)
	return out
}
