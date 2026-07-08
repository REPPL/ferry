package cmd

// The v0.3.0 first-run wizard (PLAN-v0.3.0-plugin-wizard.md): consent-driven
// SEED-PLANNING for `ferry init`'s fresh path. The wizard (interactive TUI,
// --wizard-answers data model, or the non-interactive gate-and-extract
// fallback) produces ONE in-memory seedPlan BEFORE any filesystem or network
// mutation; initFresh and `init --github`'s pre-commit gate consume the SAME
// plan, so the preview cannot lie about what gets seeded or committed.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/plugin"
	"github.com/REPPL/ferry/internal/plugin/zsh"
	"github.com/REPPL/ferry/internal/secret"
)

// wizardRegistry holds the built-in plugins. v0.3.0 registers exactly zsh.
var wizardRegistry = plugin.NewRegistry()

func init() {
	wizardRegistry.Register(zsh.New())
}

// sharedManifestBody is the minimal shared manifest every fresh seed declares
// (identical to the v0.2.1 initFresh manifest).
const sharedManifestBody = "[manage]\ndotfiles = [\".zshrc\"]\n"

// wizardScaffoldShared is the MINIMUM SHARED SCAFFOLD (F2-2/F3-1): seeded when
// routing/dropping leaves the shared text near-empty, so apply's sidecar
// branch still runs and the empty-over-substantial guard never fires on
// ferry's own output. The source line is BARE (never preceded by the ferry
// overlay marker — marker shape would be stripped back to near-empty).
const wizardScaffoldShared = `# ~/.zshrc — managed by ferry.
# Your shell content was routed to the per-machine sidecar (~/.zshrc.local)
# or dropped at init; the original file is preserved in the timestamped
# ~/.zshrc.ferry-<ts>.bak backup next to it.

[ -f ~/.zshrc.local ] && source ~/.zshrc.local
`

// backupTimestamp names the visible .bak's timestamp component
// (YYYYMMDD-HHMMSS). A variable so the collision-arm unit tests can pin the
// candidate name deterministically.
var backupTimestamp = func() string { return time.Now().Format("20060102-150405") }

// secretPut is one pending out-of-repo secret store write.
type secretPut struct{ ref, value string }

// maskPair masks one extraction value with its placeholder in preview output.
type maskPair struct{ value, placeholder string }

// seedPlan is the single source of truth for a fresh init: what gets seeded,
// stored, and backed up. It is built PURELY (no filesystem or network
// mutation) and executed only after the preview confirm.
type seedPlan struct {
	shared    []byte      // shared seed bytes; nil = no deployable shared seed
	local     []byte      // per-machine sidecar seed; empty = no local seed file
	puts      []secretPut // out-of-repo secret store writes
	manifest  string      // ferry.toml body
	backup    []byte      // original ~/.zshrc bytes to back up; nil = no .bak
	original  []byte      // original live bytes (preview diff); nil if none
	maskPairs []maskPair  // extraction values -> placeholders (preview masking)
	note      string      // detection note (symlink/irregular/unreadable), if any
}

// wizardChoices is the plain data model every wizard surface populates: the
// huh TUI, the --wizard-answers file, and the non-interactive fallback all
// feed the SAME struct into buildPlanFromChoices.
type wizardChoices struct {
	mode         string         // "keep-as-is" | "per-block" | "start-fresh"
	confirm      bool           // false = decline at the preview gate
	routes       map[int]string // per-block: block index -> shared|local|drop
	secretRoutes map[int]string // block index of a SecretLine finding -> store|drop
	repairs      map[int]bool   // repairable-finding index -> accepted
	starter      plugin.Answers // start-fresh: Question.ID -> answer
	repairsGiven bool           // a [repairs] table (or TUI repair step) ran
}

// --- answers file (--wizard-answers) ----------------------------------------

// answersFile is the documented TOML schema for `ferry init --wizard-answers`.
type answersFile struct {
	Mode         string            `toml:"mode"`
	Confirm      *bool             `toml:"confirm"`
	Routes       map[string]string `toml:"routes"`
	SecretRoutes map[string]string `toml:"secret-routes"`
	Repairs      map[string]string `toml:"repairs"`
	Starter      map[string]string `toml:"starter"`
}

// parseWizardAnswers loads and strictly validates a --wizard-answers file.
// Unknown or malformed keys error out loudly — an eval (or user) must never
// silently get a default route.
func parseWizardAnswers(path string) (*wizardChoices, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read --wizard-answers file: %w", err)
	}
	var af answersFile
	md, err := toml.Decode(string(data), &af)
	if err != nil {
		return nil, fmt.Errorf("parse --wizard-answers file %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("--wizard-answers file %s has unknown key(s) %v — the schema is: mode, confirm, [routes], [secret-routes], [repairs], [starter]", path, undecoded)
	}
	switch af.Mode {
	case "keep-as-is", "per-block", "start-fresh":
	case "":
		return nil, fmt.Errorf("--wizard-answers file %s is missing `mode` (one of keep-as-is, per-block, start-fresh)", path)
	default:
		return nil, fmt.Errorf("--wizard-answers file %s has unknown mode %q (want keep-as-is, per-block, or start-fresh)", path, af.Mode)
	}
	if af.Confirm == nil {
		return nil, fmt.Errorf("--wizard-answers file %s is missing `confirm` (true seeds, false declines at the preview gate)", path)
	}

	ch := &wizardChoices{
		mode:         af.Mode,
		confirm:      *af.Confirm,
		routes:       map[int]string{},
		secretRoutes: map[int]string{},
		repairs:      map[int]bool{},
		starter:      plugin.Answers{},
		repairsGiven: af.Repairs != nil,
	}
	parseIndexTable := func(table map[string]string, name string, allowed []string, into func(i int, v string)) error {
		for k, v := range table {
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 {
				return fmt.Errorf("[%s] key %q is not a 0-based index", name, k)
			}
			ok := false
			for _, a := range allowed {
				if v == a {
					ok = true
					break
				}
			}
			if !ok {
				return fmt.Errorf("[%s] %q = %q is not one of %v", name, k, v, allowed)
			}
			into(i, v)
		}
		return nil
	}
	if err := parseIndexTable(af.Routes, "routes", []string{"shared", "local", "drop"}, func(i int, v string) { ch.routes[i] = v }); err != nil {
		return nil, err
	}
	if err := parseIndexTable(af.SecretRoutes, "secret-routes", []string{"store", "drop"}, func(i int, v string) { ch.secretRoutes[i] = v }); err != nil {
		return nil, err
	}
	if err := parseIndexTable(af.Repairs, "repairs", []string{"accept", "decline"}, func(i int, v string) { ch.repairs[i] = v == "accept" }); err != nil {
		return nil, err
	}
	for k, v := range af.Starter {
		ch.starter[k] = v
	}
	return ch, nil
}

// validateChoicesApplicability enforces the answers-file schema contract of
// LOUD failure on inapplicable keys: each mode rejects the tables that do not
// apply to it, and [repairs] requires --repair in every mode — an eval (or
// user) must never have a table silently ignored.
func validateChoicesApplicability(ch *wizardChoices, repairFlag bool) error {
	if ch.repairsGiven && !repairFlag {
		return fmt.Errorf("a [repairs] table was given but --repair was not: repairs are opt-in — re-run with `ferry init --repair --wizard-answers <file>`")
	}
	switch ch.mode {
	case "start-fresh":
		if len(ch.routes) > 0 {
			return fmt.Errorf("[routes] does not apply to start-fresh mode: block routing exists only when adopting an existing file")
		}
		if len(ch.secretRoutes) > 0 {
			return fmt.Errorf("[secret-routes] does not apply to start-fresh mode: nothing is adopted, so there is no detected secret to route")
		}
		if ch.repairsGiven {
			return fmt.Errorf("[repairs] does not apply to start-fresh mode: repairs edit adopted content")
		}
	default:
		if len(ch.starter) > 0 {
			return fmt.Errorf("[starter] only applies to start-fresh mode (mode is %q)", ch.mode)
		}
	}
	return nil
}

// --- finding validation (routing rules enforced against the plugin) ---------

// validatePluginFindings enforces the wizard-layer routing rules the plugin is
// never trusted with: a SecretLine finding may offer ONLY {SecretStore, Drop}
// (the existing gate contract — never Shared, never Local), must carry an
// extraction, and extraction keys must be unique (r2-M1: the wizard only
// VALIDATES; it never renames, so placeholder and Put ref cannot diverge).
func validatePluginFindings(findings []plugin.Finding, blockCount int) error {
	seenKeys := map[string]bool{}
	for _, f := range findings {
		if f.Block < 0 || f.Block >= blockCount {
			return fmt.Errorf("internal error: plugin finding block index %d out of range (%d blocks)", f.Block, blockCount)
		}
		if f.Kind != plugin.SecretLine {
			continue
		}
		if f.Secret == nil {
			return fmt.Errorf("internal error: plugin SecretLine finding on block %d has no extraction", f.Block)
		}
		if len(f.Routes) == 0 {
			return fmt.Errorf("internal error: plugin SecretLine finding on block %d offers no routes", f.Block)
		}
		for _, r := range f.Routes {
			if r != plugin.SecretStore && r != plugin.Drop {
				return fmt.Errorf("internal error: plugin offered route %q for a secret-shaped line (block %d) — secret routes are only {store, drop}; refusing to seed", r, f.Block)
			}
		}
		if seenKeys[f.Secret.Key] {
			return fmt.Errorf("internal error: plugin produced duplicate secret key %q — refusing to seed (placeholder and stored ref could diverge)", f.Secret.Key)
		}
		seenKeys[f.Secret.Key] = true
	}
	return nil
}

// --- plan building -----------------------------------------------------------

// wizardInputs bundles the parsed/analyzed state of an adoptable ~/.zshrc.
type wizardInputs struct {
	p        plugin.Plugin
	det      plugin.Detection
	original []byte
	blocks   []plugin.Block
	findings []plugin.Finding
}

// loadWizardInputs reads and analyzes the detected config (Reason OK only).
func loadWizardInputs(p plugin.Plugin, det plugin.Detection) (*wizardInputs, error) {
	original, err := os.ReadFile(det.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", det.Path, err)
	}
	blocks, err := p.Parse(original)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", det.Path, err)
	}
	findings := p.Analyze(blocks)
	if err := validatePluginFindings(findings, len(blocks)); err != nil {
		return nil, err
	}
	return &wizardInputs{p: p, det: det, original: original, blocks: blocks, findings: findings}, nil
}

// secretFindingsByBlock groups SecretLine findings by block index.
func secretFindingsByBlock(findings []plugin.Finding) map[int][]plugin.Finding {
	out := map[int][]plugin.Finding{}
	for _, f := range findings {
		if f.Kind == plugin.SecretLine {
			out[f.Block] = append(out[f.Block], f)
		}
	}
	return out
}

// repairableFindings returns the repairable findings in Analyze order — the
// pinned 0-based [repairs] index space.
func repairableFindings(findings []plugin.Finding) []plugin.Finding {
	var out []plugin.Finding
	for _, f := range findings {
		if f.Repairable {
			out = append(out, f)
		}
	}
	return out
}

// nearEmptyOriginal returns the ORIGINAL bytes of a NEAR-EMPTY detected file
// (which EXISTS, unlike Absent) so a start-fresh replacement still writes the
// visible .bak and previews the replacement diff — the .bak rule is "only when
// an original exists", and a near-empty original exists.
func nearEmptyOriginal(det plugin.Detection) []byte {
	if det.Reason != plugin.NearEmpty {
		return nil
	}
	data, err := os.ReadFile(det.Path)
	if err != nil {
		return nil
	}
	return data
}

// maskPairsFromSpans builds preview mask pairs for original content the
// wizard did NOT Parse/Analyze (the NearEmpty start-fresh short-circuit):
// every gate-flagged span masks as a positional placeholder, so the preview
// diff never prints a secret byte — even a commented-out token in a
// comments-only rc (the answers-file surface writes the preview to stdout
// regardless of tty, so scrollback AND redirected files are output). Only
// OUTPUT is masked: the .bak keeps the original bytes by contract (it is the
// user's own file, 0600, in their HOME).
func maskPairsFromSpans(original []byte, domain string) []maskPair {
	var out []maskPair
	for _, sp := range secret.FlaggedSpans(string(original)) {
		out = append(out, maskPair{
			value:       sp.Value,
			placeholder: secret.Placeholder(fmt.Sprintf("%s.secret_%d", domain, sp.StartLine)),
		})
	}
	return out
}

// declareOnlyPlan is the "declared, no seed" v0.2.1-parity plan: manifest only.
func declareOnlyPlan(note string) *seedPlan {
	return &seedPlan{manifest: sharedManifestBody, note: note}
}

// buildPlanFromChoices turns the analyzed inputs plus a populated choices
// struct into the seedPlan. PURE: no filesystem or network mutation.
func buildPlanFromChoices(in *wizardInputs, ch *wizardChoices, repairFlag bool) (*seedPlan, error) {
	plan := &seedPlan{manifest: sharedManifestBody}
	domain := in.p.Domain()

	// Every extraction masks its value in preview/diff output — routed,
	// dropped, or start-fresh-discarded alike (DROP-LINE MASKING pin).
	for _, f := range in.findings {
		if f.Kind == plugin.SecretLine && f.Secret != nil {
			plan.maskPairs = append(plan.maskPairs, maskPair{
				value:       f.Secret.Value,
				placeholder: secret.Placeholder(domain + "." + f.Secret.Key),
			})
		}
	}

	if ch.mode == "start-fresh" {
		starterBytes, err := in.p.Starter(ch.starter)
		if err != nil {
			return nil, err
		}
		plan.shared = starterBytes
		plan.backup = in.original
		plan.original = in.original
		return plan, nil
	}

	secretBlocks := secretFindingsByBlock(in.findings)
	repairables := repairableFindings(in.findings)

	// [repairs] validation: opt-in via --repair; indices within range.
	if ch.repairsGiven && !repairFlag {
		return nil, fmt.Errorf("a [repairs] table was given but --repair was not: repairs are opt-in — re-run with `ferry init --repair --wizard-answers <file>`")
	}
	for idx := range ch.repairs {
		if idx < 0 || idx >= len(repairables) {
			return nil, fmt.Errorf("[repairs] index %d is out of range: %d repairable finding(s) detected", idx, len(repairables))
		}
	}

	// Route validation.
	switch ch.mode {
	case "per-block":
		for i := range in.blocks {
			if _, ok := ch.routes[i]; !ok {
				return nil, fmt.Errorf("per-block mode: block %d has no [routes] entry — every block needs an explicit shared/local/drop route (no silent defaults)", i)
			}
		}
		for i := range ch.routes {
			if i >= len(in.blocks) {
				return nil, fmt.Errorf("[routes] index %d is out of range: the file parsed into %d block(s)", i, len(in.blocks))
			}
		}
	case "keep-as-is":
		if len(ch.routes) > 0 {
			return nil, fmt.Errorf("[routes] only applies to per-block mode (mode is %q)", ch.mode)
		}
	}

	// SECRETS ARE ALWAYS ROUTED (never optional, never behind --repair): every
	// block carrying a SecretLine finding needs an explicit store/drop choice,
	// and a secret block can never be routed shared or local.
	for i := range secretBlocks {
		route, ok := ch.secretRoutes[i]
		if !ok {
			return nil, fmt.Errorf("block %d contains a detected secret but has no [secret-routes] entry: a secret-shaped line is never seeded silently — route it \"store\" (out-of-repo secret store) or \"drop\"", i)
		}
		if route != "store" && route != "drop" {
			return nil, fmt.Errorf("[secret-routes] %d = %q: a secret line routes only to \"store\" or \"drop\" — never shared, never local", i, route)
		}
	}
	for i := range ch.secretRoutes {
		if _, ok := secretBlocks[i]; !ok {
			return nil, fmt.Errorf("[secret-routes] names block %d, but no secret was detected there", i)
		}
	}

	// Compose the SINGLE ApplyRepairs pass (r3-M2): store-routed secret
	// substitutions + accepted repairs together.
	var accepted []plugin.Finding
	blockRoute := func(i int) string {
		if ch.mode == "per-block" {
			return ch.routes[i]
		}
		return "shared"
	}
	for i, fs := range secretBlocks {
		if ch.secretRoutes[i] != "store" {
			continue
		}
		if blockRoute(i) == "drop" {
			continue // the whole block is dropped; nothing to substitute or store
		}
		for _, f := range fs {
			accepted = append(accepted, f)
			plan.puts = append(plan.puts, secretPut{
				ref:   domain + "." + f.Secret.Key,
				value: f.Secret.Value,
			})
		}
	}
	if repairFlag {
		for idx, f := range repairables {
			if ch.repairs[idx] && blockRoute(f.Block) != "drop" {
				// A repair inside a DROP-routed block is moot: the content
				// never seeds, and its op vanishes once the block empties below.
				accepted = append(accepted, f)
			}
		}
	}

	// Drop-routed secret lines FIRST, on the pristine blocks: each finding's
	// span is located POSITIONALLY by diffing the block against its
	// Replacement (never a first-byte-occurrence value match — an earlier
	// byte-equal occurrence on another line must survive), all windows
	// computed against the same original raw and removed in descending order.
	// The value survives only in the .bak. Repairs are content-anchored, so
	// running them after the drops is order-safe.
	edited := cloneBlocks(in.blocks)
	for i, fs := range secretBlocks {
		if ch.secretRoutes[i] != "drop" {
			continue
		}
		var replacements [][]byte
		for _, f := range fs {
			replacements = append(replacements, f.Secret.Replacement)
		}
		edited[i].Raw = plugin.DropSecretSpans(edited[i].Raw, replacements)
	}
	// Block-route DROPs empty the Raw BEFORE ApplyRepairs (indices preserved
	// per the never-add/remove/reorder invariant), so the repair detection
	// re-run inside ApplyRepairs sees only SURVIVING content: a PATH line in a
	// dropped block no longer makes a kept block's identical line "a
	// duplicate" whose accepted removal would strip the seed's only surviving
	// PATH export. LOCAL-routed blocks deliberately STAY in the set — they
	// deploy via the sidecar, so a shared+local duplicate is still an
	// effective duplicate. (A [repairs] acceptance whose finding depended on
	// dropped content no longer matches any op and fails LOUDLY in
	// ApplyRepairs — the answers combo is contradictory, and nothing seeds.)
	for i := range edited {
		if blockRoute(i) == "drop" {
			edited[i].Raw = nil
		}
	}
	edited, err := in.p.ApplyRepairs(edited, accepted)
	if err != nil {
		return nil, err
	}

	// SEED ASSEMBLY (pinned): byte-concat of the selected blocks' Raw.
	var sharedBuf, localBuf bytes.Buffer
	for i, b := range edited {
		switch blockRoute(i) {
		case "shared":
			sharedBuf.Write(b.Raw)
		case "local":
			localBuf.Write(b.Raw)
		case "drop":
			// dropped: preserved only in the .bak
		}
	}
	plan.shared = sharedBuf.Bytes()
	plan.local = localBuf.Bytes()
	plan.backup = in.original
	plan.original = in.original

	// Deterministic put order (map iteration above is unordered).
	sort.Slice(plan.puts, func(a, b int) bool { return plan.puts[a].ref < plan.puts[b].ref })

	// MINIMUM SHARED SCAFFOLD (F2-2/F3-1): the shared seed must survive the
	// apply-path guards — non-near-empty AFTER stripFerryOverlayDirective.
	if dotfile.IsNearEmpty(stripOverlayForGuard(plan.shared)) {
		plan.shared = []byte(wizardScaffoldShared)
	}
	return plan, nil
}

// maskBlockSecrets returns a COPY of block i with every SecretLine finding's
// value swapped for its placeholder — the GENERIC wizard-layer mask applied
// before any block text (Describe titles, previews) reaches the terminal. The
// plugin's Describe is never trusted with raw secret bytes: an
// `export TOKEN=<value>` block must paint its placeholder, not the value,
// into scrollback.
func maskBlockSecrets(b plugin.Block, findings []plugin.Finding, blockIdx int, domain string) plugin.Block {
	raw := b.Raw
	for _, f := range findings {
		if f.Kind != plugin.SecretLine || f.Block != blockIdx || f.Secret == nil {
			continue
		}
		raw = bytes.ReplaceAll(raw, []byte(f.Secret.Value), []byte(secret.Placeholder(domain+"."+f.Secret.Key)))
	}
	return plugin.Block{Kind: b.Kind, Raw: raw, Start: b.Start}
}

// cloneBlocks deep-copies blocks so plan building never mutates the parsed originals.
func cloneBlocks(blocks []plugin.Block) []plugin.Block {
	out := make([]plugin.Block, len(blocks))
	for i, b := range blocks {
		out[i] = plugin.Block{Kind: b.Kind, Raw: append([]byte(nil), b.Raw...), Start: b.Start}
	}
	return out
}

// stripOverlayForGuard mirrors the apply guard's judgment of the USER's real
// managed content (internal/dotfile strips ferry's own overlay directive
// before judging near-emptiness).
func stripOverlayForGuard(content []byte) []byte {
	return dotfile.StripFerryOverlayDirective(content)
}

// buildFallbackSeedPlan is the NON-INTERACTIVE fallback (non-tty / --yes /
// --no-wizard): keep-everything-Shared whole-file adopt PLUS always-on secret
// extraction. Every SecretLine finding is auto-routed to the SecretStore
// (Drop is NEVER auto-selected); machine-specific suggestions and repairs are
// declined. A stderr NOTICE lists the extracted ref names (never values);
// stdout is unchanged. A secret-free zshrc seeds byte-identical to the v0.2.1
// adopt.
func buildFallbackSeedPlan(p plugin.Plugin, det plugin.Detection, errOut io.Writer) (*seedPlan, error) {
	switch det.Reason {
	case plugin.Symlink, plugin.Irregular, plugin.Unreadable:
		return declareOnlyPlan(fmt.Sprintf("~/.zshrc is %s; ferry will not manage the file (declared, no seed)", det.Reason)), nil
	case plugin.Absent, plugin.NearEmpty:
		return declareOnlyPlan(""), nil
	}
	in, err := loadWizardInputs(p, det)
	if err != nil {
		return nil, err
	}
	ch := &wizardChoices{
		mode:         "keep-as-is",
		confirm:      true,
		secretRoutes: map[int]string{},
	}
	for i := range secretFindingsByBlock(in.findings) {
		ch.secretRoutes[i] = "store"
	}
	plan, err := buildPlanFromChoices(in, ch, false)
	if err != nil {
		return nil, err
	}
	if len(plan.puts) > 0 {
		refs := make([]string, 0, len(plan.puts))
		for _, put := range plan.puts {
			refs = append(refs, put.ref)
		}
		fmt.Fprintf(errOut, "notice: %d secret-shaped value(s) in ~/.zshrc were extracted to the out-of-repo secret store (~/.config/ferry/secrets-local) and replaced with placeholders in the seed: %s\n",
			len(refs), strings.Join(refs, ", "))
	}
	return plan, nil
}

// --- preview -----------------------------------------------------------------

// maskSecrets masks every extraction value with its placeholder — the
// AC-preview-masking rule: a routed, dropped, or context-adjacent secret line
// never prints its value (both diff sides show the placeholder).
func (plan *seedPlan) maskSecrets(text string) string {
	for _, mp := range plan.maskPairs {
		text = strings.ReplaceAll(text, mp.value, mp.placeholder)
	}
	return text
}

// printSeedPlanPreview renders the full seed bytes plus the first-apply diff,
// with every secret value masked on BOTH sides. All three consumers (initFresh
// seeding, the --github pre-commit gate, and this preview) render the same
// seedPlan, so the preview cannot lie.
func printSeedPlanPreview(out io.Writer, plan *seedPlan) {
	fmt.Fprintln(out, "\n=== preview — nothing has been written yet ===")
	if plan.shared == nil {
		fmt.Fprintln(out, "no shared seed: .zshrc stays declared with no managed source")
	} else {
		fmt.Fprintln(out, "--- shared seed (dotfiles/zshrc) ---")
		fmt.Fprint(out, plan.maskSecrets(string(plan.shared)))
		if !bytes.HasSuffix(plan.shared, []byte("\n")) {
			fmt.Fprintln(out)
		}
	}
	if len(plan.local) > 0 {
		fmt.Fprintln(out, "--- per-machine seed (local/zsh/zshrc.local, gitignored) ---")
		fmt.Fprint(out, plan.maskSecrets(string(plan.local)))
		if !bytes.HasSuffix(plan.local, []byte("\n")) {
			fmt.Fprintln(out)
		}
	}
	if len(plan.puts) > 0 {
		refs := make([]string, 0, len(plan.puts))
		for _, put := range plan.puts {
			refs = append(refs, put.ref)
		}
		fmt.Fprintf(out, "--- secret store (out-of-repo, ~/.config/ferry/secrets-local) ---\n%s\n", strings.Join(refs, "\n"))
	}
	if plan.original != nil && plan.shared != nil {
		future := plan.shared
		if len(plan.local) > 0 {
			future = appendSourceDirective(plan.shared, ".zshrc.local", shellDirective)
		}
		fmt.Fprintln(out, "--- diff: ~/.zshrc after first apply vs current (dropped lines removed; secrets masked) ---")
		hunks := diffHunks(plan.maskSecrets(string(plan.original)), plan.maskSecrets(string(future)))
		if len(hunks) == 0 {
			fmt.Fprintln(out, "(no changes: first apply leaves ~/.zshrc byte-identical)")
		}
		for _, h := range hunks {
			fmt.Fprint(out, renderHunk(h))
		}
	}
	fmt.Fprintln(out, "=== end preview ===")
}

// --- execution (post-confirm, strictly ordered per F2-14) ---------------------

// regateSeedPlan is the defense-in-depth pre-mutation re-gate: after ALL
// routing/repairs, the final shared AND local texts (starter included) are
// re-scanned. A hit aborts BEFORE anything is written (no .bak, no puts, no
// repo dir — this helper is PURE; the old gate helper's MkdirAll-on-abort was
// removed, F3-6). It can only fire on a plugin bug.
func regateSeedPlan(plan *seedPlan) error {
	if plan.shared != nil && secret.GateText(string(plan.shared)).BlockedFromRepo {
		return fmt.Errorf("internal error: a secret would have entered the SHARED seed after routing — aborting; nothing was written")
	}
	if len(plan.local) > 0 && secret.GateText(string(plan.local)).BlockedFromRepo {
		return fmt.Errorf("internal error: a secret would have entered the LOCAL seed after routing — aborting; nothing was written")
	}
	return nil
}

// executeSeedPlan performs the post-confirm mutations in the pinned order:
// (a) re-gate (pure; abort writes NOTHING) -> (b) visible timestamped .bak ->
// (c) secret store puts -> (d) seed the repo (git init + files). It returns
// the repo path for the normal init tail (config.toml, plan display).
func executeSeedPlan(out io.Writer, plan *seedPlan, freshDir string) (string, error) {
	// (a) defense-in-depth re-gate — pure, before any write.
	if err := regateSeedPlan(plan); err != nil {
		return "", err
	}

	// (b) visible timestamped backup of the original ~/.zshrc.
	if plan.backup != nil {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		bak, err := writeVisibleBackup(home, plan.backup)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(out, "backed up the original ~/.zshrc to %s\n", bak)
	}

	// (c) secret store puts (out-of-repo, 0600; overwrites are retry-idempotent).
	if len(plan.puts) > 0 {
		store, err := secret.Open()
		if err != nil {
			return "", err
		}
		for _, put := range plan.puts {
			if err := store.Put(put.ref, put.value); err != nil {
				return "", fmt.Errorf("store secret %s: %w", put.ref, err)
			}
		}
	}

	// (d) seed the repo from the plan.
	dest, err := resolveFreshRepoDest(freshDir)
	if err != nil {
		return "", err
	}
	if err := createFreshRepo(out, dest); err != nil {
		return "", err
	}
	if err := seedRepoFromPlan(dest, plan); err != nil {
		return "", err
	}
	return dest, nil
}

// writeVisibleBackup writes ~/.zshrc.ferry-<YYYYMMDD-HHMMSS>.bak with the
// ORIGINAL bytes: candidates are opened O_CREATE|O_EXCL|O_WRONLY 0600; a
// SYMLINK or other non-regular entry at any candidate name ABORTS (attack
// shape — never sidestepped); an existing REGULAR file (same-second re-run)
// retries with a -2..-9 suffix then aborts (F2-7/F3-4). The .bak is never a
// declared dotfile, so capture/sync ignore it structurally.
func writeVisibleBackup(home string, content []byte) (string, error) {
	base := filepath.Join(home, ".zshrc.ferry-"+backupTimestamp()+".bak")
	for i := 1; i <= 9; i++ {
		cand := base
		if i > 1 {
			cand = fmt.Sprintf("%s-%d", base, i)
		}
		exists, perr := backupCandidateState(cand)
		if perr != nil {
			return "", perr
		}
		if exists {
			continue // regular-file collision: try the next suffix
		}
		f, err := os.OpenFile(cand, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			if os.IsExist(err) {
				// Raced into existence AFTER the Lstat probe: RE-CLASSIFY the
				// entry. A symlink or non-regular file ABORTS (attack shape —
				// never sidestepped to a suffix); only a regular file retries.
				if _, rerr := backupCandidateState(cand); rerr != nil {
					return "", rerr
				}
				continue
			}
			return "", fmt.Errorf("create backup %s: %w", cand, err)
		}
		if _, werr := f.Write(content); werr != nil {
			f.Close()
			return "", fmt.Errorf("write backup %s: %w", cand, werr)
		}
		if cerr := f.Close(); cerr != nil {
			return "", fmt.Errorf("close backup %s: %w", cand, cerr)
		}
		return cand, nil
	}
	return "", fmt.Errorf("could not create a backup at %s (all -2..-9 suffixes taken) — remove stale backups and re-run", base)
}

// backupCandidateState Lstat-classifies a backup candidate path: (false, nil)
// = free to O_EXCL-create; (true, nil) = an existing REGULAR file (a
// same-second collision — the caller tries the next suffix); any symlink or
// other non-regular entry is the attack shape and returns the abort error.
// Used both for the pre-create probe and the EEXIST re-classification.
func backupCandidateState(cand string) (exists bool, err error) {
	fi, lerr := os.Lstat(cand)
	if lerr != nil {
		if os.IsNotExist(lerr) {
			return false, nil
		}
		return false, fmt.Errorf("probe backup path %s: %w", cand, lerr)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return true, fmt.Errorf("refusing to write the backup: %s already exists and is not a regular file (symlink/special) — remove it and re-run", cand)
	}
	return true, nil
}
