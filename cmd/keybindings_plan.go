package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/keybindings"
	"github.com/REPPL/ferry/internal/secret"
)

// planKeybindings builds the per-target plan for the macOS key-bindings domain:
// the single repo source (keybindings/DefaultKeyBinding.dict) validated for
// format hygiene and expanded to one hash-classified (content, target) planItem
// the shared apply/status/diff machinery acts on, carried like a dotfile but
// repo-authoritative (no capture pass) and with no per-machine .local overlay.
//
// A target that is currently a symlink (or any non-regular file) is SKIPPED with
// a clear warning rather than classified: ferry materialises regular-file copies
// only. Repo-side reads route through safeRepoPath, so a symlinked source in the
// config repo is refused exactly like a dotfile's. The domain carries no secrets
// (secretStore is unused), so there is no render-or-skip pass.
func planKeybindings(ctx *cmdContext, home string, _ *secret.Store, lastApplied *dotfile.Store) ([]planItem, []string, error) {
	kItems, warnings, err := keybindings.Plan(keybindings.PlanInput{
		RepoRoot: ctx.RepoPath,
		Home:     home,
		Guard:    func(cand string) (string, error) { return safeRepoPath(ctx.RepoPath, cand) },
		Linter:   keybindings.PlutilLinter{},
	})
	if err != nil {
		return nil, nil, err
	}

	var items []planItem
	planned := map[string]bool{}
	for _, ki := range kItems {
		planned[ki.Key] = true
		// Key-bindings content is never secret-rendered (secretRouted=false); its
		// risk comes from the three-way state alone (a first-touch adoption over an
		// existing file, or a conflict).
		state, risky, reason, cerr := classifyItem(ki.Target, ki.Content, false, lastApplied)
		if cerr != nil {
			var kind *dotfile.UnexpectedKindError
			if errors.As(cerr, &kind) {
				warnings = append(warnings, fmt.Sprintf(
					"%s skipped: %s is a symlink (or non-regular file) not managed by ferry — remove it, then re-run `ferry apply`",
					ki.Label, ki.Target.Home))
				continue
			}
			return nil, nil, cerr
		}
		items = append(items, planItem{
			kind:       kindFile,
			domain:     ki.Label,
			target:     ki.Target,
			content:    ki.Content,
			state:      state,
			risky:      risky,
			riskReason: reason,
		})
	}

	warnings = append(warnings, descopeKeybindingsWarnings(lastApplied, planned, true)...)
	return items, warnings, nil
}

// descopeKeybindingsWarnings warns about a key-bindings target ferry previously
// applied (recorded under the keybindings/ prefix in the last-applied store) that
// the current plan no longer covers. The file is left untouched (de-scope = warn,
// never auto-remove). With the domain unmanaged the record collapses into ONE
// warning; with the domain managed but the source removed, the dropped target
// warns. A full `ferry restore` reverts it to its pre-ferry baseline (the file
// deploys through the backup engine, like a dotfile).
func descopeKeybindingsWarnings(store *dotfile.Store, planned map[string]bool, managed bool) []string {
	var stale []string
	for _, name := range store.RecordedNames() {
		if !strings.HasPrefix(name, keybindings.KeyPrefix) {
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
			"warning: the keybindings domain is no longer managed; %d previously applied file(s) left as-is (now unmanaged). To revert them to their pre-ferry state: ferry restore",
			len(stale))}
	}
	out := make([]string, 0, len(stale))
	for _, name := range stale {
		out = append(out, fmt.Sprintf(
			"warning: %s is no longer part of the keybindings plan; existing file left as-is (now unmanaged). To revert: ferry restore",
			name))
	}
	// The keybindings/ namespace only ever holds one key, so this is a no-op today;
	// sorted anyway for byte-for-byte parity with the termcfg de-scope twin.
	sort.Strings(out)
	return out
}
