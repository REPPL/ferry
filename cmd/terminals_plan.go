package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/secret"
	"github.com/REPPL/ferry/internal/termcfg"
)

// planTerminals builds the per-target plan for the config-file terminal domain:
// the built-in terminal registry (trimmed by `enabled`, overlaid with manifest
// declarations) expanded against the repo's terminals/ area — each config file
// (or a single-file terminal's one file) as one hash-classified (content,
// target) planItem the shared apply/status/diff machinery acts on, carried like
// a dotfile with the per-machine .local overlay winning per file.
//
// A target that is currently a symlink (or any non-regular file) is SKIPPED
// with a clear warning rather than classified: ferry materialises regular-file
// copies only. Repo-side reads route through safeRepoPath, so a symlinked
// source in the config repo is refused exactly like a dotfile's.
//
// Each target's bytes carry {{ferry.secret ...}} placeholders through the SAME
// render-or-SKIP as dotfiles (renderSecrets): a present secret is substituted and
// the target flagged secretRouted (deployed 0600, its plaintext kept out of the
// last-applied snapshot); a MISSING secret SKIPS the whole target rather than
// deploy the literal placeholder to the terminal's live config. A real secret
// dropped in terminals/ is caught by the same repo commit/push gate as any other
// managed file (sync/export scan every changed and committed blob path-agnostically).
func planTerminals(ctx *cmdContext, home string, secretStore *secret.Store, lastApplied *dotfile.Store) ([]planItem, []string, error) {
	cfg, err := config.LoadTerminals(ctx.RepoPath)
	if err != nil {
		return nil, nil, err
	}

	tItems, warnings, err := termcfg.Plan(termcfg.PlanInput{
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
	for _, ti := range tItems {
		planned[ti.Key] = true
		// Render secrets EXACTLY like a dotfile: substitute present placeholders, or
		// SKIP the whole target (never deploy a literal {{ferry.secret}}) when a
		// referenced secret is missing. secretRouted is judged on the PRE-render bytes.
		rendered, missing, skip, rerr := renderSecrets(secretStore, ti.Content)
		if rerr != nil {
			return nil, nil, rerr
		}
		secretRouted := isSecretRouted(ti.Content)
		if skip {
			items = append(items, planItem{
				kind:         kindFile,
				domain:       ti.Label,
				target:       ti.Target,
				action:       "skipped",
				skip:         true,
				missing:      missing,
				secretRouted: secretRouted,
			})
			continue
		}
		// A secret-routed terminal target is always risky (its rendered plaintext is
		// deployed); otherwise risk comes from the three-way state alone (a
		// first-touch adoption over an existing file, an overwrite of local edits, or
		// a conflict). classifyItem sees the EFFECTIVE (rendered) bytes.
		state, risky, reason, cerr := classifyItem(ti.Target, rendered, secretRouted, lastApplied)
		if cerr != nil {
			var kind *dotfile.UnexpectedKindError
			if errors.As(cerr, &kind) {
				warnings = append(warnings, fmt.Sprintf(
					"%s skipped: %s is a symlink (or non-regular file) not managed by ferry — remove it, then re-run `ferry apply`",
					ti.Label, ti.Target.Home))
				continue
			}
			return nil, nil, cerr
		}
		items = append(items, planItem{
			kind:         kindFile,
			domain:       ti.Label,
			target:       ti.Target,
			content:      rendered,
			execBit:      ti.Exec,
			state:        state,
			risky:        risky,
			riskReason:   reason,
			secretRouted: secretRouted,
		})
	}

	warnings = append(warnings, descopeTerminalConfigWarnings(lastApplied, planned, true)...)
	return items, warnings, nil
}

// descopeTerminalConfigWarnings warns about config-file terminal targets ferry
// previously applied (recorded under the terminals/ prefix in the last-applied
// store) that the current plan no longer covers. Files are left untouched
// (de-scope = warn, never auto-remove). With the whole domain unmanaged the
// records collapse into ONE warning; with the domain managed, each dropped
// target (a terminal removed from `enabled`, a deleted config file) warns
// individually. A full `ferry restore` reverts them to their pre-ferry baseline
// (terminal files deploy through the backup engine, like dotfiles).
func descopeTerminalConfigWarnings(store *dotfile.Store, planned map[string]bool, managed bool) []string {
	var stale []string
	for _, name := range store.RecordedNames() {
		if !strings.HasPrefix(name, termcfg.KeyPrefix) {
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
			"warning: the terminals domain is no longer managed; %d previously applied file(s) left as-is (now unmanaged). To revert them to their pre-ferry state: ferry restore",
			len(stale))}
	}
	out := make([]string, 0, len(stale))
	for _, name := range stale {
		out = append(out, fmt.Sprintf(
			"warning: %s is no longer part of the terminals plan; existing file left as-is (now unmanaged). To revert: ferry restore",
			name))
	}
	sort.Strings(out)
	return out
}
