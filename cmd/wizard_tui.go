package cmd

// The huh-v2 TUI face of the first-run wizard. THIN BY DESIGN: it only
// populates the same wizardChoices struct the --wizard-answers file feeds —
// the seedPlan engine, routing rules, gates, preview, and confirm ordering all
// live in wizard.go and run identically on every surface. The answers-file
// path never calls into this file, so the data model works without a tty.

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	huh "charm.land/huh/v2"

	"github.com/REPPL/ferry/internal/plugin"
)

// runWizardTUI runs the interactive first-run wizard (both stdin and stdout
// are ttys). It loops preview -> confirm: a declined preview offers to revise
// the choices (back to the top-level step) or abort with nothing changed.
func runWizardTUI(in *bufio.Reader, out io.Writer, opts freshInitOpts, p plugin.Plugin, det plugin.Detection) (*seedPlan, bool, error) {
	switch det.Reason {
	case plugin.Absent, plugin.NearEmpty:
		return tuiStarterOrSkip(out, p, det)
	}

	inputs, err := loadWizardInputs(p, det)
	if err != nil {
		return nil, false, err
	}
	for {
		ch, err := tuiCollectChoices(out, inputs, opts.repair)
		if err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				fmt.Fprintln(out, "wizard aborted: nothing was changed.")
				return nil, true, nil
			}
			return nil, false, err
		}
		plan, err := buildPlanFromChoices(inputs, ch, opts.repair)
		if err != nil {
			return nil, false, err
		}
		printSeedPlanPreview(out, plan)
		confirm, err := tuiConfirm("Write this plan? (repo seed + secret store + a timestamped ~/.zshrc backup)")
		if err != nil {
			return nil, false, err
		}
		if confirm {
			return plan, false, nil
		}
		again, err := tuiConfirm("Revise the choices? (No exits with nothing changed)")
		if err != nil || !again {
			fmt.Fprintln(out, "declined at the preview gate: nothing was changed.")
			return nil, true, nil
		}
	}
}

// tuiStarterOrSkip handles the Absent/NearEmpty detection: offer the
// from-scratch starter or continue with no managed source.
func tuiStarterOrSkip(out io.Writer, p plugin.Plugin, det plugin.Detection) (*seedPlan, bool, error) {
	fmt.Fprintf(out, "no usable ~/.zshrc found (%s)\n", det.Reason)
	build, err := tuiConfirm("Build an opinionated starter ~/.zshrc?")
	if err != nil {
		return nil, false, err
	}
	if !build {
		return declareOnlyPlan(""), false, nil
	}
	answers, err := tuiStarterAnswers(p)
	if err != nil {
		return nil, false, err
	}
	starter, err := p.Starter(answers)
	if err != nil {
		return nil, false, err
	}
	plan := &seedPlan{manifest: sharedManifestBody, shared: starter}
	// A NEAR-EMPTY original still EXISTS: back it up and preview the diff.
	if orig := nearEmptyOriginal(det); orig != nil {
		plan.backup = orig
		plan.original = orig
		plan.maskPairs = maskPairsFromSpans(orig, p.Domain())
	}
	printSeedPlanPreview(out, plan)
	confirm, err := tuiConfirm("Seed this starter?")
	if err != nil {
		return nil, false, err
	}
	if !confirm {
		fmt.Fprintln(out, "declined at the preview gate: nothing was changed.")
		return nil, true, nil
	}
	return plan, false, nil
}

// tuiCollectChoices walks the wizard steps, populating the choices struct.
func tuiCollectChoices(out io.Writer, inputs *wizardInputs, repair bool) (*wizardChoices, error) {
	secretBlocks := secretFindingsByBlock(inputs.findings)
	ch := &wizardChoices{
		routes:       map[int]string{},
		secretRoutes: map[int]string{},
		repairs:      map[int]bool{},
		starter:      plugin.Answers{},
	}

	keepLabel := "Keep everything as-is"
	if n := len(secretBlocks); n > 0 {
		keepLabel = fmt.Sprintf("Keep everything as-is (%d secret-shaped block(s) still need routing)", n)
	}
	mode := "keep-as-is"
	if err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(fmt.Sprintf("found ~/.zshrc (%d block(s)) — how should ferry adopt it?", len(inputs.blocks))).
			Options(
				huh.NewOption(keepLabel, "keep-as-is"),
				huh.NewOption("Choose per block (shared / local / drop)", "per-block"),
				huh.NewOption("Start fresh with an opinionated starter (original kept in the backup)", "start-fresh"),
			).
			Value(&mode),
	)).Run(); err != nil {
		return nil, err
	}
	ch.mode = mode
	ch.confirm = true

	if mode == "start-fresh" {
		answers, err := tuiStarterAnswers(inputs.p)
		if err != nil {
			return nil, err
		}
		ch.starter = answers
		return ch, nil
	}

	// Per-block routing (git add -p model).
	if mode == "per-block" {
		droppedAny := false
		for i, b := range inputs.blocks {
			if _, isSecret := secretBlocks[i]; isSecret {
				// The secret line itself is routed in the forced mini-step below;
				// the block's REMAINDER still needs a shared/local/drop route.
				fmt.Fprintf(out, "block %d carries a secret-shaped line (routed separately below)\n", i)
			}
			route := "shared"
			if hasMachineFinding(inputs.findings, i) {
				route = "local" // the SUGGESTED default; freely declinable
			}
			caveat := ""
			if b.Kind == plugin.PathExport || b.Kind == plugin.Prompt {
				caveat = " (Local note: the sidecar is sourced LAST — order-sensitive PATH/prompt lines may behave differently)"
			}
			// Describe over the MASKED block: a secret-bearing block titles
			// with its placeholder, never the raw value (generic wizard-layer
			// masking — the plugin is not trusted with output hygiene).
			masked := maskBlockSecrets(b, inputs.findings, i, inputs.p.Domain())
			if err := huh.NewForm(huh.NewGroup(
				huh.NewSelect[string]().
					Title(fmt.Sprintf("block %d — %s%s", i, inputs.p.Describe(masked), caveat)).
					Options(
						huh.NewOption("Shared (committed, deploys everywhere)", "shared"),
						huh.NewOption("Local (per-machine sidecar ~/.zshrc.local)", "local"),
						huh.NewOption("Drop (removed from the deployed config)", "drop"),
					).
					Value(&route),
			)).Run(); err != nil {
				return nil, err
			}
			ch.routes[i] = route
			if route == "drop" {
				droppedAny = true
			}
		}
		if droppedAny {
			ok, err := tuiConfirm("Dropped blocks are REMOVED from the deployed config (they remain in your ~/.zshrc backup). Continue?")
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, huh.ErrUserAborted
			}
		}
	}

	// SECRETS ARE ALWAYS ROUTED (keep-as-is included): forced store/drop per
	// secret-bearing block — never shared, never local, never silent.
	for i := range inputs.blocks {
		if _, ok := secretBlocks[i]; !ok {
			continue
		}
		route := "store"
		if err := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title(fmt.Sprintf("block %d carries secret-shaped content — it is never committed. Route it:", i)).
				Options(
					huh.NewOption("Secret store (out-of-repo, placeholder in the seed)", "store"),
					huh.NewOption("Drop (line removed; survives only in the backup)", "drop"),
				).
				Value(&route),
		)).Run(); err != nil {
			return nil, err
		}
		ch.secretRoutes[i] = route
	}

	// Opt-in repair review (--repair): accept/decline each finding.
	if repair {
		reps := repairableFindings(inputs.findings)
		for idx, f := range reps {
			accept := false
			title := fmt.Sprintf("repair %d/%d — %s", idx+1, len(reps), f.Detail)
			if f.Suggested != "" {
				title += fmt.Sprintf("\nproposed: %s", f.Suggested)
			}
			if err := huh.NewForm(huh.NewGroup(
				huh.NewConfirm().Title(title).Affirmative("Accept").Negative("Decline").Value(&accept),
			)).Run(); err != nil {
				return nil, err
			}
			ch.repairs[idx] = accept
		}
		ch.repairsGiven = true
	}
	return ch, nil
}

// tuiStarterAnswers asks the plugin's StarterQuestions.
func tuiStarterAnswers(p plugin.Plugin) (plugin.Answers, error) {
	answers := plugin.Answers{}
	for _, q := range p.StarterQuestions() {
		v := q.Default
		switch q.Kind {
		case plugin.Select:
			opts := make([]huh.Option[string], 0, len(q.Options))
			for _, o := range q.Options {
				opts = append(opts, huh.NewOption(o, o))
			}
			if err := huh.NewForm(huh.NewGroup(
				huh.NewSelect[string]().Title(q.Prompt).Description(q.Description).Options(opts...).Value(&v),
			)).Run(); err != nil {
				return nil, err
			}
		default:
			if err := huh.NewForm(huh.NewGroup(
				huh.NewInput().Title(q.Prompt).Description(q.Description).Value(&v),
			)).Run(); err != nil {
				return nil, err
			}
		}
		answers[q.ID] = v
	}
	return answers, nil
}

// hasMachineFinding reports whether block i carries a MachineSpecific finding.
func hasMachineFinding(findings []plugin.Finding, i int) bool {
	for _, f := range findings {
		if f.Kind == plugin.MachineSpecific && f.Block == i {
			return true
		}
	}
	return false
}

// tuiConfirm shows a yes/no confirm (default No — the safe side).
func tuiConfirm(title string) (bool, error) {
	v := false
	if err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(title).Value(&v),
	)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	return v, nil
}
