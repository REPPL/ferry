package cmd

// The huh-v2 TUI face of the first-run wizard. THIN BY DESIGN: it only
// populates the same wizardChoices struct the --wizard-answers file feeds —
// the seedPlan engine, routing rules, gates, preview, and confirm ordering all
// live in wizard.go and run identically on every surface. The answers-file
// path never calls into this file, so the data model works without a tty.
//
// PROGRAM COUNT IS A SAFETY PROPERTY (v0.3.2 field-critical fix): every
// bubbletea program emits terminal capability queries on start; real
// terminals reply on stdin, and with many short-lived sequential programs the
// replies of one land in the NEXT program's input and parse as keystrokes
// (bubbletea#1627 leak class — DECRPM replies end in `$y`, huh Confirm's
// Accept key). The wizard therefore runs exactly ONE multi-group form per
// pass (conditional groups via WithHideFunc — repairs and starter questions
// included, so no second program window ever opens) — and neither the WRITE
// decision nor the DROP consent rides a TUI keystroke at all: both are the
// typed, flushed, plain-line gates in wizard_gate.go.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	huh "charm.land/huh/v2"

	"github.com/REPPL/ferry/internal/plugin"
)

// newWizardForm builds a huh form with EXPLICIT stdin/stdout binding (never
// implicit defaults) — one call site so every wizard form is bound the same way.
func newWizardForm(groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).WithInput(os.Stdin).WithOutput(os.Stdout)
}

// runWizardTUI runs the interactive first-run wizard (both stdin and stdout
// are ttys). It loops preview -> confirm: a declined preview offers to revise
// the choices (back to the top-level step) or abort with nothing changed.
// The write confirm is the TYPED plain-line gate, not a huh keystroke.
func runWizardTUI(in *bufio.Reader, out io.Writer, opts freshInitOpts, p plugin.Plugin, det plugin.Detection) (*seedPlan, bool, error) {
	switch det.Reason {
	case plugin.Absent, plugin.NearEmpty:
		return tuiStarterOrSkip(in, out, p, det)
	}

	inputs, err := loadWizardInputs(p, det)
	if err != nil {
		return nil, false, err
	}
	for {
		ch, err := tuiCollectChoices(in, out, inputs, opts.repair)
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
		// SAFETY-CRITICAL: the write decision is a typed word on a drained
		// plain line — a leaked terminal reply can never phantom-confirm it.
		if confirmTypedWrite(in, wizardStdinTTY(), out,
			"Write this plan? (repo seed + secret store + a timestamped ~/.zshrc backup)") {
			return plan, false, nil
		}
		// Revising may stay a huh confirm: its worst phantom outcome is
		// another pass through the loop or an abort — never a write.
		again, err := tuiConfirm("Not written. Revise the choices? (No exits with nothing changed)")
		if err != nil || !again {
			fmt.Fprintln(out, "declined at the preview gate: nothing was changed.")
			return nil, true, nil
		}
	}
}

// tuiStarterOrSkip handles the Absent/NearEmpty detection: offer the
// from-scratch starter or continue with no managed source. The seed confirm
// is the same typed gate as the main flow.
func tuiStarterOrSkip(in *bufio.Reader, out io.Writer, p plugin.Plugin, det plugin.Detection) (*seedPlan, bool, error) {
	fmt.Fprintf(out, "no usable ~/.zshrc found (%s)\n", det.Reason)

	// ONE form: the build decision plus the starter questions (hidden until
	// the user opts in) — a single program, so no cross-program reply window.
	build := false
	qs := p.StarterQuestions()
	vals := make([]string, len(qs))
	groups := []*huh.Group{huh.NewGroup(
		huh.NewConfirm().Title("Build an opinionated starter ~/.zshrc?").Value(&build),
	)}
	for i, q := range qs {
		i, q := i, q
		vals[i] = q.Default
		groups = append(groups, huh.NewGroup(starterField(q, &vals[i])).
			WithHideFunc(func() bool { return !build }))
	}
	if err := newWizardForm(groups...).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return declareOnlyPlan(""), false, nil
		}
		return nil, false, err
	}
	if !build {
		return declareOnlyPlan(""), false, nil
	}
	answers := plugin.Answers{}
	for i, q := range qs {
		answers[q.ID] = vals[i]
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
	if !confirmTypedWrite(in, wizardStdinTTY(), out, "Seed this starter?") {
		fmt.Fprintln(out, "declined at the preview gate: nothing was changed.")
		return nil, true, nil
	}
	return plan, false, nil
}

// tuiCollectChoices collects one full wizard pass of choices in a SINGLE
// multi-group huh form (mode, per-block routes, forced secret routing,
// starter questions, and the opt-in repair review — conditional groups hidden
// via WithHideFunc). Drop consent runs AFTER the form as a typed plain-line
// gate (immediate, un-phantomable). The function only populates the
// wizardChoices struct; every decision is validated and applied by the shared
// engine in wizard.go.
func tuiCollectChoices(in *bufio.Reader, out io.Writer, inputs *wizardInputs, repair bool) (*wizardChoices, error) {
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

	// Bound values (the form writes into these; ch is populated after Run).
	mode := "keep-as-is"
	blockRoutes := make([]string, len(inputs.blocks))
	secretRoutes := make([]string, len(inputs.blocks))
	starterQs := inputs.p.StarterQuestions()
	starterVals := make([]string, len(starterQs))
	reps := repairableFindings(inputs.findings)
	repairAccepts := make([]bool, len(reps))

	var groups []*huh.Group

	// Step: top-level mode.
	groups = append(groups, huh.NewGroup(
		huh.NewSelect[string]().
			Title(fmt.Sprintf("found ~/.zshrc (%d block(s)) — how should ferry adopt it?", len(inputs.blocks))).
			Options(
				huh.NewOption(keepLabel, "keep-as-is"),
				huh.NewOption("Choose per block (shared / local / drop)", "per-block"),
				huh.NewOption("Start fresh with an opinionated starter (original kept in the backup)", "start-fresh"),
			).
			Value(&mode),
	))

	// Step: per-block routing (git add -p model) — per-block mode only.
	for i, b := range inputs.blocks {
		i := i
		blockRoutes[i] = "shared"
		if hasMachineFinding(inputs.findings, i) {
			blockRoutes[i] = "local" // the SUGGESTED default; freely declinable
		}
		caveat := ""
		if b.Kind == plugin.PathExport || b.Kind == plugin.Prompt {
			caveat = " (Local note: the sidecar is sourced LAST — order-sensitive PATH/prompt lines may behave differently)"
		}
		if _, isSecret := secretBlocks[i]; isSecret {
			caveat += " (its secret-shaped line is routed separately in the next step)"
		}
		// Describe over the MASKED block: a secret-bearing block titles with
		// its placeholder, never the raw value. The title prefers the first
		// INFORMATIVE line (banner-divider comments skipped) and the group
		// description shows the block CONTENT (git-add-p model: the user must
		// see WHAT they are routing, not a truncated divider).
		masked := maskBlockSecrets(b, inputs.findings, i, inputs.p.Domain())
		groups = append(groups, huh.NewGroup(
			huh.NewSelect[string]().
				Title(fmt.Sprintf("block %d — %s%s", i, tuiBlockTitle(inputs.p, masked), caveat)).
				Description(tuiBlockPreview(masked)).
				Options(
					huh.NewOption("Shared (committed, deploys everywhere)", "shared"),
					huh.NewOption("Local (per-machine sidecar ~/.zshrc.local)", "local"),
					huh.NewOption("Drop (removed from the deployed config)", "drop"),
				).
				Value(&blockRoutes[i]),
		).WithHideFunc(func() bool { return mode != "per-block" }))
	}

	// Step: SECRETS ARE ALWAYS ROUTED (keep-as-is included): forced store/drop
	// per secret-bearing block — never shared, never local, never silent.
	for i := range inputs.blocks {
		if _, ok := secretBlocks[i]; !ok {
			continue
		}
		i := i
		secretRoutes[i] = "store"
		// The preview shows the MASKED block (placeholders, never the value),
		// so the user sees which block they are routing to store/drop.
		maskedSecret := maskBlockSecrets(inputs.blocks[i], inputs.findings, i, inputs.p.Domain())
		groups = append(groups, huh.NewGroup(
			huh.NewSelect[string]().
				Title(fmt.Sprintf("block %d carries secret-shaped content — it is never committed. Route it:", i)).
				Description(tuiBlockPreview(maskedSecret)).
				Options(
					huh.NewOption("Secret store (out-of-repo, placeholder in the seed)", "store"),
					huh.NewOption("Drop (line removed; survives only in the backup)", "drop"),
				).
				Value(&secretRoutes[i]),
		).WithHideFunc(func() bool { return mode == "start-fresh" }))
	}

	// Step: starter questions — start-fresh only.
	for qi, q := range starterQs {
		qi, q := qi, q
		starterVals[qi] = q.Default
		groups = append(groups, huh.NewGroup(starterField(q, &starterVals[qi])).
			WithHideFunc(func() bool { return mode != "start-fresh" }))
	}

	// Step: opt-in repair review (--repair) — IN the same form (hidden for
	// start-fresh), so no second program ever runs: a straggler terminal
	// reply at a second program's startup could phantom-Accept a repair,
	// violating per-finding consent. Confirms default to Decline.
	if repair {
		for idx, f := range reps {
			idx := idx
			title := fmt.Sprintf("repair %d/%d — %s", idx+1, len(reps), f.Detail)
			if f.Suggested != "" {
				title += fmt.Sprintf("\nproposed: %s", f.Suggested)
			}
			groups = append(groups, huh.NewGroup(
				huh.NewConfirm().Title(title).Affirmative("Accept").Negative("Decline").Value(&repairAccepts[idx]),
			).WithHideFunc(func() bool { return mode == "start-fresh" }))
		}
	}

	// ONE program for the whole pass.
	if err := newWizardForm(groups...).Run(); err != nil {
		return nil, err
	}

	// Populate the choices struct from the bound values.
	ch.mode = mode
	ch.confirm = true
	if mode == "start-fresh" {
		for qi, q := range starterQs {
			ch.starter[q.ID] = starterVals[qi]
		}
		return ch, nil
	}
	if mode == "per-block" {
		dropped := 0
		for i := range inputs.blocks {
			ch.routes[i] = blockRoutes[i]
			if blockRoutes[i] == "drop" {
				dropped++
			}
		}
		// Drop consent is UN-PHANTOMABLE and PROMPT (immediately after the
		// form, before anything else): a typed "yes" on a drained plain line,
		// like the write gate. Decline aborts with nothing written.
		if dropped > 0 && !confirmTypedDrop(in, wizardStdinTTY(), out, dropped) {
			return nil, huh.ErrUserAborted
		}
	}
	for i := range inputs.blocks {
		if _, ok := secretBlocks[i]; ok {
			ch.secretRoutes[i] = secretRoutes[i]
		}
	}
	if repair {
		for idx := range reps {
			ch.repairs[idx] = repairAccepts[idx]
		}
		ch.repairsGiven = true
	}
	return ch, nil
}

// starterField builds the huh field for one starter Question.
func starterField(q plugin.Question, val *string) huh.Field {
	if q.Kind == plugin.Select {
		opts := make([]huh.Option[string], 0, len(q.Options))
		for _, o := range q.Options {
			opts = append(opts, huh.NewOption(o, o))
		}
		return huh.NewSelect[string]().Title(q.Prompt).Description(q.Description).Options(opts...).Value(val)
	}
	return huh.NewInput().Title(q.Prompt).Description(q.Description).Value(val)
}

// tuiPreviewMaxLines / tuiPreviewMaxCols bound the block-content preview: a
// terminal-friendly snippet, never a full-file dump.
const (
	tuiPreviewMaxLines = 12
	tuiPreviewMaxCols  = 100
)

// tuiBlockPreview renders a MASKED block's content as a quoted snippet for a
// group description: first tuiPreviewMaxLines lines, prefix-indented, long
// lines clipped, with a "… (+N more lines)" marker when truncated. Callers
// pass the maskBlockSecrets output, so a secret value never reaches it.
func tuiBlockPreview(b plugin.Block) string {
	lines := strings.Split(strings.TrimRight(string(b.Raw), "\n"), "\n")
	total := len(lines)
	shown := lines
	if total > tuiPreviewMaxLines {
		shown = lines[:tuiPreviewMaxLines]
	}
	var sb strings.Builder
	for _, l := range shown {
		if runes := []rune(l); len(runes) > tuiPreviewMaxCols {
			l = string(runes[:tuiPreviewMaxCols-1]) + "…"
		}
		sb.WriteString("  │ ")
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	if total > len(shown) {
		sb.WriteString(fmt.Sprintf("  … (+%d more lines)", total-len(shown)))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// tuiBlockTitle titles a block by its first INFORMATIVE line: banner-divider
// comments (a comment carrying only punctuation/box-drawing characters, e.g.
// `# ═══════`) and blank lines are skipped so the title says WHAT the block
// is, not how it is decorated. The plugin's Describe contract is untouched —
// this shifts the (already MASKED) block to the informative line and lets
// Describe render it; an all-banner block falls back to Describe unchanged.
func tuiBlockTitle(p plugin.Plugin, masked plugin.Block) string {
	lines := strings.Split(string(masked.Raw), "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || isBannerCommentLine(t) {
			continue
		}
		if i == 0 {
			break // first line is already informative
		}
		shifted := plugin.Block{Kind: masked.Kind, Raw: []byte(strings.Join(lines[i:], "\n")), Start: masked.Start + i}
		return p.Describe(shifted)
	}
	return p.Describe(masked)
}

// isBannerCommentLine reports whether a trimmed line is a banner/divider
// comment: it starts with '#' and its body carries NO letter or digit (only
// punctuation/box-drawing/whitespace, or nothing at all).
func isBannerCommentLine(t string) bool {
	if !strings.HasPrefix(t, "#") {
		return false
	}
	body := strings.TrimSpace(strings.TrimLeft(t, "#"))
	for _, r := range body {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return false
		}
	}
	return true
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

// tuiConfirm shows a yes/no confirm (default No — the safe side). NEVER used
// for the write decision (see confirmTypedWrite): its phantom-input worst
// case must always be a loop or an abort, never a mutation.
func tuiConfirm(title string) (bool, error) {
	v := false
	if err := newWizardForm(huh.NewGroup(
		huh.NewConfirm().Title(title).Value(&v),
	)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	return v, nil
}
