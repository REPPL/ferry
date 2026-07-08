package cmd

// The v0.5.0 guided apply (W1). `apply` is no longer a terse reconciler: on
// every run that has changes it walks the pending work, GROUPED BY DOMAIN
// (dotfiles / agents), and is QUIET WHEN SAFE / STOPS WHEN RISKY.
//
//   - A clean, in-sync apply prints ONE line and does not walk.
//   - SAFE changes (create a file where none exists, update a target whose live
//     content still matches the last-deployed baseline) auto-apply.
//   - RISKY changes — an overwrite of a locally-modified/pre-existing file, a
//     conflict, or a secret-routed target — HALT for confirmation.
//   - Non-interactively (EOF on stdin) or under --skip-wizard, risky changes
//     FAIL CLOSED: they are listed and refused, never applied unattended, and
//     apply exits non-zero. The safe subset still applies ("quiet when safe").
//
// The risk gate CONSUMES the Foundation baseline (Store.LastDeployedSnapshot /
// the recorded deployed hash) to tell a locally-modified file from an in-sync
// one — that is what wires the baseline READ side.
//
// SAFETY: the confirmation is a TYPED plain-line gate (the same un-phantomable
// consent pattern the init wizard uses — see wizard_gate.go), not a TUI
// keystroke, so it is safe over a real tty AND drivable over piped stdin (which
// is what makes each path eval-provable). EOF on the gate is treated as
// "non-interactive" and fails closed.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/dotfile"
)

// guidedOpts carries the apply-time flags the guided walkthrough consults.
type guidedOpts struct {
	// skipWizard suppresses the interactive walkthrough (expert opt-out). Safe
	// changes still auto-apply; risky changes are refused (fail closed), never
	// prompted — nothing risky ever happens unattended.
	skipWizard bool
}

// guidedOutcome is decideGuided's verdict: the subset to hand mutate, the risky
// changes refused (for the fail-closed report), and the clean short-circuit.
type guidedOutcome struct {
	toApply     []planItem // safe + confirmed items (plus pass-through preference/blocked)
	refused     []planItem // risky items NOT applied (non-interactive / --skip-wizard)
	nothingToDo bool       // no pending change anywhere: print one line, do not walk
	cleanCount  int        // in-sync target count (for the one-line message)
}

// isGuidedKind reports whether a plan item is inside the dotfiles/agents grouping
// the walkthrough and risk gate cover. Terminal preference domains (kindPreference)
// are NOT: they auto-apply through their own reversible backup-first path.
func isGuidedKind(k planKind) bool {
	return k == kindFile
}

// itemKey is the stable per-machine identity used for skip-always exclusions: a
// target's bare/namespaced name (unique across dotfiles, overlays, and the
// agents/ prefix).
func itemKey(it planItem) string { return it.target.Name }

// sha256Hex is the lowercase hex sha256 of content — the same content-address the
// dotfile store keys the last-deployed baseline by, so a snapshot's hash compares
// directly against a Status.LiveHash.
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// assessRisk is the guided-apply RISK GATE: it classifies a planned change as safe
// (auto-apply) or risky (halt for confirmation / fail closed). It consumes the
// Foundation baseline via liveMatchesDeployedBaseline to distinguish a
// locally-modified file from an in-sync one.
//
// Risky when the change would:
//   - deploy a value from the secret store (secret-routed — secrets-adjacent);
//   - overwrite a file that is not provably what ferry last deployed (a
//     first-touch adoption of a pre-existing file, or an overwrite of local
//     modifications) — the OVERWRITE-A-LOCALLY-MODIFIED-FILE case;
//   - resolve to a conflict (edited live AND in the repo).
//
// Safe when the change:
//   - creates a target where nothing exists (StateMissing — nothing is
//     overwritten, so there is nothing to lose);
//   - updates a target whose live content still equals the last-deployed
//     baseline (a clean repo-ahead update);
//   - re-creates a target ferry deployed before and that is now absent.
//
// A locally-drifted target is not "applied" at all (apply skips it), so it is not
// risky here. Removals are never produced by apply (de-scope = warn, never
// auto-remove), so there is no removal action to gate.
//
// NOTE ON "brand-new target": creating a file where none exists is classified
// SAFE — nothing is overwritten. The risky, surprising case the plan guards is
// bringing a target under management whose home path ALREADY holds content ferry
// never wrote (first-touch adoption); that is caught by the StateRepoAhead branch
// below. This keeps ferry's fresh-machine first apply usable while still halting
// on every overwrite of existing content.
func assessRisk(t dotfile.Target, st dotfile.Status, secretRouted bool, store *dotfile.Store) (bool, string) {
	if secretRouted {
		return true, "deploys a value from the secret store"
	}
	switch st.State {
	case dotfile.StateConflict:
		return true, "conflict: edited locally AND in the repo (run `ferry capture` first, or `ferry apply --force` to overwrite)"
	case dotfile.StateRepoAhead:
		if !st.LiveExists {
			// Nothing on disk to overwrite — a safe (re-)create.
			return false, ""
		}
		if liveMatchesDeployedBaseline(t, st, store) {
			// The live bytes are exactly what ferry last deployed: a clean update.
			return false, ""
		}
		if !st.HasApplied {
			return true, "would adopt and overwrite a pre-existing file ferry has never managed here"
		}
		return true, "would overwrite local changes (the live file differs from what ferry last deployed)"
	default:
		// StateClean (nothing to do), StateMissing (create where absent),
		// StateLocallyDrifted (apply skips it) are all safe.
		return false, ""
	}
}

// liveMatchesDeployedBaseline reports whether the live file is byte-for-byte what
// ferry last deployed for this target, using the Foundation baseline as the
// authority. It reads the last-deployed snapshot (Store.LastDeployedSnapshot) and
// compares its content-address to the live hash. When there is NO recorded
// snapshot — a target ferry never applied, a secret-routed target (recorded
// hash-only), or a record migrated forward before any re-apply — it returns false
// (the match cannot be PROVEN, so the caller fails safe and treats the overwrite
// as risky).
func liveMatchesDeployedBaseline(t dotfile.Target, st dotfile.Status, store *dotfile.Store) bool {
	snap, ok := store.LastDeployedSnapshot(t.Name)
	if !ok {
		return false
	}
	return sha256Hex(snap) == st.LiveHash
}

// decideGuided partitions the freshly-recomputed plan into what should actually be
// written this run. It runs UNDER the apply lock and NEVER mutates the live
// machine — it only decides, reports, and (when the user chooses skip-always)
// persists an exclusion to the .local layer.
func decideGuided(ctx *cmdContext, plan []planItem, force bool, gopts guidedOpts, in *bufio.Reader, out io.Writer) (guidedOutcome, error) {
	skipSet, err := loadSkipAlways(ctx.RepoPath)
	if err != nil {
		return guidedOutcome{}, err
	}
	// Open the MUTATING last-applied store (decideGuided only ever runs on the
	// mutating apply path, under the lock). This both provides the last-deployed
	// baseline the risk gate and detail-view diff read AND performs the on-open
	// state-file migration (v0.3.x / v1 -> v2), so even a clean, in-sync apply
	// upgrades the store format rather than leaving it stale.
	store, err := dotfile.OpenStore()
	if err != nil {
		return guidedOutcome{}, fmt.Errorf("open last-applied store: %w", err)
	}

	var res guidedOutcome
	var risky []planItem

	for _, it := range plan {
		if isGuidedKind(it.kind) && skipSet[itemKey(it)] {
			fmt.Fprintf(out, "  %-22s skipped (skip-always on this machine; delete it from %s to re-enable)\n", it.domain, skipAlwaysRel)
			continue
		}
		if !planItemPending(it) {
			res.cleanCount++
			// A clean guided target with NO last-applied record still needs its
			// last-deployed baseline recorded on this (first) apply, so later
			// capture/drift detection has a sync point. That recording happens
			// inside mutate's clean branch, so pass it through — it is content-free
			// (the file already matches) and never risky.
			if isGuidedKind(it.kind) {
				if _, recorded := store.LastApplied(it.target.Name); !recorded {
					res.toApply = append(res.toApply, it)
				}
			}
			continue // otherwise clean + recorded: nothing to write (quiet).
		}
		// Terminal preference domains and missing-secret-blocked items are outside
		// the risk-gated dotfiles/agents grouping: they pass straight through to
		// mutate with their existing (reversible / clearly reported) behaviour.
		if !isGuidedKind(it.kind) || it.skip {
			res.toApply = append(res.toApply, it)
			continue
		}
		if !it.risky {
			res.toApply = append(res.toApply, it) // safe change: auto-apply.
			continue
		}
		risky = append(risky, it)
	}

	// Nothing to write AND nothing risky pending: a clean, in-sync apply.
	if len(res.toApply) == 0 && len(risky) == 0 {
		res.nothingToDo = true
		return res, nil
	}

	if len(risky) == 0 {
		if n := countPendingApply(res.toApply); n > 0 {
			fmt.Fprintf(out, "applying %d change(s) automatically (all safe); run `ferry diff` to preview\n", n)
		}
		return res, nil
	}

	// --force is an explicit "just do it" override: the user has already decided to
	// overwrite, so the risk gate does not halt (the downstream apply still honours
	// its own conflict / empty-over-substantial guards and warns). --force is an
	// explicit decision, not an unattended run, so this does not violate the
	// fail-closed contract.
	if force {
		res.toApply = append(res.toApply, risky...)
		return res, nil
	}

	if gopts.skipWizard {
		// Expert opt-out: apply the safe subset, refuse every risky change.
		res.refused = risky
		return res, nil
	}

	apply, refused, skipAlways, err := walkRisky(in, out, risky, store)
	if err != nil {
		return guidedOutcome{}, err
	}
	res.toApply = append(res.toApply, apply...)
	res.refused = refused
	if len(skipAlways) > 0 {
		if err := addSkipAlways(ctx.RepoPath, skipAlways); err != nil {
			return guidedOutcome{}, err
		}
	}
	return res, nil
}

// countPendingApply counts the items in toApply that will actually write (pending
// and not a missing-secret skip) — the number the "all safe" summary reports.
func countPendingApply(items []planItem) int {
	n := 0
	for _, it := range items {
		if planItemPending(it) && !it.skip {
			n++
		}
	}
	return n
}

// riskyGroup is one domain's risky changes for the walkthrough.
type riskyGroup struct {
	name  string
	items []planItem
}

// groupRisky buckets risky items by domain (dotfiles, then agents, then
// terminals) so the walkthrough confirms a domain wholesale or drills into it.
func groupRisky(risky []planItem) []riskyGroup {
	var dot, ag, term []planItem
	for _, it := range risky {
		switch it.fileDomain {
		case "agents":
			ag = append(ag, it)
		case "terminals":
			term = append(term, it)
		default:
			dot = append(dot, it)
		}
	}
	var groups []riskyGroup
	if len(dot) > 0 {
		groups = append(groups, riskyGroup{"dotfiles", dot})
	}
	if len(ag) > 0 {
		groups = append(groups, riskyGroup{"agents", ag})
	}
	if len(term) > 0 {
		groups = append(groups, riskyGroup{"terminals", term})
	}
	return groups
}

// walkRisky is the interactive walkthrough over the risky changes, grouped by
// domain. For each group the user confirms the whole group ("yes"), skips it this
// run (anything else), or drills into per-item review ("details"). Every decision
// is a TYPED plain-line gate, so it is un-phantomable and drivable over piped
// stdin. EOF (an empty/exhausted stdin) means NON-INTERACTIVE — the current and
// all remaining groups FAIL CLOSED (refused, never applied).
func walkRisky(in *bufio.Reader, out io.Writer, risky []planItem, store *dotfile.Store) (apply, refused []planItem, skipAlways []string, err error) {
	groups := groupRisky(risky)
	for gi := range groups {
		g := groups[gi]
		fmt.Fprintf(out, "\n%s — %d change(s) need review:\n", g.name, len(g.items))
		for _, it := range g.items {
			fmt.Fprintf(out, "  - %-22s %s\n", it.domain, it.riskReason)
		}
		ans, eof := readGuidedLine(in, out, fmt.Sprintf(
			"Apply all %d change(s) in %s? Type \"yes\" to apply, \"details\" to review each, anything else to skip this run: ", len(g.items), g.name))
		if eof {
			refused = append(refused, g.items...)
			refused = append(refused, remainingItems(groups[gi+1:])...)
			return apply, refused, skipAlways, nil
		}
		switch ans {
		case "yes", "y":
			apply = append(apply, g.items...)
		case "details", "d":
			a, r, sa, detEOF := walkDetails(in, out, g.items, store)
			apply = append(apply, a...)
			refused = append(refused, r...)
			skipAlways = append(skipAlways, sa...)
			if detEOF {
				refused = append(refused, remainingItems(groups[gi+1:])...)
				return apply, refused, skipAlways, nil
			}
		default:
			// Skip this run: neither applied nor refused (a deliberate user choice,
			// so the run still exits 0).
		}
	}
	return apply, refused, skipAlways, nil
}

// walkDetails drills into a group item by item: it shows each change's full diff,
// then offers apply / skip-this-run / skip-always. On EOF it refuses the current
// and every remaining item in the group and signals the caller to fail the rest
// closed.
func walkDetails(in *bufio.Reader, out io.Writer, items []planItem, store *dotfile.Store) (apply, refused []planItem, skipAlways []string, eof bool) {
	for i := 0; i < len(items); i++ {
		it := items[i]
		showItemDiff(out, it, store)
		ans, e := readGuidedLine(in, out, "  [a]pply · [s]kip this run · [x] skip-always on this machine: ")
		if e {
			refused = append(refused, items[i:]...)
			return apply, refused, skipAlways, true
		}
		switch ans {
		case "a", "apply", "yes", "y":
			apply = append(apply, it)
		case "x", "always", "skip-always":
			skipAlways = append(skipAlways, itemKey(it))
			fmt.Fprintf(out, "  %s will be skipped on this machine from now on (%s).\n", it.domain, skipAlwaysRel)
		default:
			// "s" / skip / anything else: skip this run.
		}
	}
	return apply, refused, skipAlways, false
}

// remainingItems flattens the items of the groups not yet reached, so a
// non-interactive stop refuses ALL outstanding risky work in one report.
func remainingItems(groups []riskyGroup) []planItem {
	var out []planItem
	for _, g := range groups {
		out = append(out, g.items...)
	}
	return out
}

// showItemDiff renders a risky change's full diff. For a secret-routed target the
// content diff is HIDDEN (it would print the rendered secret value). Otherwise it
// shows the user's local modifications since ferry last deployed (baseline vs
// live — consuming the Foundation snapshot) followed by what apply would write
// (live vs the repo content).
func showItemDiff(out io.Writer, it planItem, store *dotfile.Store) {
	fmt.Fprintf(out, "\n  --- %s: %s ---\n", it.domain, it.riskReason)
	if it.secretRouted {
		fmt.Fprintln(out, "  (secret-routed: diff hidden so no secret value is printed)")
		return
	}
	live, _ := os.ReadFile(it.target.Home)
	if snap, ok := store.LastDeployedSnapshot(it.target.Name); ok && !bytes.Equal(snap, live) {
		fmt.Fprintln(out, "  local changes since ferry last deployed here (ferry deployed → your live file):")
		for _, h := range diffHunks(string(snap), string(live)) {
			fmt.Fprint(out, renderHunk(h))
		}
	}
	fmt.Fprintln(out, "  apply would change the live file to the repo content (your live file → repo):")
	for _, h := range diffHunks(string(live), string(it.content)) {
		fmt.Fprint(out, renderHunk(h))
	}
}

// readGuidedLine prints the prompt and reads ONE trimmed, lowercased line. It
// returns eof=true when stdin is exhausted with nothing typed — the
// NON-INTERACTIVE signal the caller fails closed on. A typed answer with a
// trailing EOF (no newline) is still honoured.
func readGuidedLine(in *bufio.Reader, out io.Writer, prompt string) (answer string, eof bool) {
	fmt.Fprint(out, prompt)
	line, err := in.ReadString('\n')
	ans := strings.TrimSpace(line)
	if ans == "" && err != nil {
		return "", true
	}
	return strings.ToLower(ans), false
}

// riskyRefusedError prints the fail-closed report (what needs a human) and returns
// the non-zero error. It is used both when nothing safe was applied and after the
// safe subset committed — in either case nothing risky was applied.
func riskyRefusedError(out io.Writer, refused []planItem) error {
	fmt.Fprintf(out, "\nrefused: %d risky change(s) need review — nothing risky was applied:\n", len(refused))
	for _, it := range refused {
		fmt.Fprintf(out, "  - %-22s %s\n", it.domain, it.riskReason)
	}
	fmt.Fprintln(out, "Re-run `ferry apply` interactively and confirm each change, or remove them from scope. Non-interactive runs and --skip-wizard never apply risky changes unattended.")
	return fmt.Errorf("%d risky change(s) refused; nothing risky applied — re-run `ferry apply` interactively to confirm", len(refused))
}

// -----------------------------------------------------------------------------
// skip-always exclusions (the per-machine .local layer)
// -----------------------------------------------------------------------------

// skipAlwaysRel is the repo-relative, gitignored file that records the targets the
// user chose to skip ALWAYS on this machine. It lives in the existing `.local`
// layer (local/), so it is never committed — one mechanism, consistent with how
// `.local` already wins per machine.
const skipAlwaysRel = "local/skip-always.txt"

// skipAlwaysPath resolves and guards the skip-always file inside the repo's local
// layer. safeRepoPath refuses a symlinked/escaping path, so a poisoned local/
// component can never redirect the read or write.
func skipAlwaysPath(repo string) (string, error) {
	return safeRepoPath(repo, filepath.Join(repo, "local", "skip-always.txt"))
}

// loadSkipAlways reads the per-machine skip-always target set. An absent file is a
// normal empty set (nothing skipped), not an error.
func loadSkipAlways(repo string) (map[string]bool, error) {
	set := map[string]bool{}
	path, err := skipAlwaysPath(repo)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return set, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", skipAlwaysRel, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if k := strings.TrimSpace(line); k != "" {
			set[k] = true
		}
	}
	return set, nil
}

// addSkipAlways unions keys into the per-machine skip-always file (creating it and
// gitignoring the local layer first). Entries are sorted for a stable file.
func addSkipAlways(repo string, keys []string) error {
	existing, err := loadSkipAlways(repo)
	if err != nil {
		return err
	}
	for _, k := range keys {
		existing[k] = true
	}
	// Make sure the per-machine .local layer is gitignored before writing into it,
	// so a skip-always choice is never committed.
	if err := ensureLocalLayerIgnored(repo); err != nil {
		return err
	}
	path, err := skipAlwaysPath(repo)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create local layer dir: %w", err)
	}
	lines := make([]string, 0, len(existing))
	for k := range existing {
		lines = append(lines, k)
	}
	sort.Strings(lines)
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", skipAlwaysRel, err)
	}
	return nil
}
