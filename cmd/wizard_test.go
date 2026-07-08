package cmd

// UNIT-PHASE wizard tests (accounted in evals/wizard_test.go's header): the
// arms that need direct API access or an injected plugin bug —
// AC-route-enforcement (synthetic plugin emitting Routes:[Shared] for a
// SecretLine), AC-gate-defense-in-depth (injected leaking plugin -> the
// pre-mutation re-gate aborts writing NOTHING), and AC-wizard-backup's
// symlink-abort + `-2` collision arms (the timestamped candidate name cannot
// be deterministically pre-planted from outside the process).

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/plugin"
)

// synthWizardSecret trips ferry's REAL gate (high-entropy rule): 36 chars,
// base64-shaped, letters+digits, entropy above the 4.0 floor. Never real.
const synthWizardSecret = "FERRYUNITq7W3e9R1t8Y2u0I6o4P5aSdKjZx"

// leakyPlugin is a SYNTHETIC plugin with injectable bugs: route -> the Routes
// it emits for its SecretLine finding; leak -> ApplyRepairs "forgets" the
// substitution, leaving the raw secret in the shared text.
type leakyPlugin struct {
	routes []plugin.Route
	leak   bool
}

func (l *leakyPlugin) Domain() string { return "zsh" }
func (l *leakyPlugin) Detect(home string) (plugin.Detection, error) {
	return plugin.Detection{Path: filepath.Join(home, ".zshrc"), Present: true, Reason: plugin.OK}, nil
}
func (l *leakyPlugin) Parse(content []byte) ([]plugin.Block, error) {
	return []plugin.Block{{Kind: plugin.Other, Raw: append([]byte(nil), content...), Start: 1}}, nil
}
func (l *leakyPlugin) Analyze(blocks []plugin.Block) []plugin.Finding {
	return []plugin.Finding{{
		Kind:   plugin.SecretLine,
		Block:  0,
		Detail: "synthetic secret finding",
		Routes: l.routes,
		Secret: &plugin.SecretExtraction{
			Key:         "leaked",
			Value:       synthWizardSecret,
			Replacement: []byte("masked"),
		},
	}}
}
func (l *leakyPlugin) ApplyRepairs(blocks []plugin.Block, accepted []plugin.Finding) ([]plugin.Block, error) {
	if l.leak {
		return blocks, nil // BUG under test: substitution silently dropped
	}
	out := make([]plugin.Block, len(blocks))
	copy(out, blocks)
	for _, f := range accepted {
		if f.Kind == plugin.SecretLine {
			out[f.Block].Raw = []byte(strings.ReplaceAll(string(out[f.Block].Raw), synthWizardSecret, "{{ferry.secret \"zsh.leaked\"}}"))
		}
	}
	return out, nil
}
func (l *leakyPlugin) StarterQuestions() []plugin.Question      { return nil }
func (l *leakyPlugin) Starter(a plugin.Answers) ([]byte, error) { return []byte("# starter\n"), nil }
func (l *leakyPlugin) Describe(b plugin.Block) string           { return "synthetic block" }

func leakyInputs(t *testing.T, p plugin.Plugin) *wizardInputs {
	t.Helper()
	original := []byte("export GITHUB_TOKEN=" + synthWizardSecret + "\nalias keep='me'\n")
	blocks, err := p.Parse(original)
	if err != nil {
		t.Fatal(err)
	}
	return &wizardInputs{p: p, original: original, blocks: blocks, findings: p.Analyze(blocks)}
}

// AC-route-enforcement: a synthetic plugin returning Routes:[Shared] for a
// SecretLine is rejected by the wizard-layer validation with an internal
// error — plugin-supplied routes are validated, never trusted.
func TestRouteEnforcement_SharedSecretRouteRejected(t *testing.T) {
	p := &leakyPlugin{routes: []plugin.Route{plugin.Shared}}
	findings := p.Analyze(nil)
	err := validatePluginFindings(findings, 1)
	if err == nil {
		t.Fatal("Routes:[Shared] on a SecretLine finding was accepted — the wizard must reject it")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Errorf("rejection does not explain the secret routing rule: %v", err)
	}
	// The legal route set passes.
	ok := &leakyPlugin{routes: []plugin.Route{plugin.SecretStore, plugin.Drop}}
	if err := validatePluginFindings(ok.Analyze(nil), 1); err != nil {
		t.Errorf("legal {SecretStore, Drop} routes rejected: %v", err)
	}
}

// AC-gate-defense-in-depth: an injected plugin bug leaking the secret into the
// shared text is caught by the pre-mutation re-gate; the abort writes NOTHING
// (no .bak, no store put, no repo dir).
func TestGateDefenseInDepth_LeakingPluginAborts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	p := &leakyPlugin{routes: []plugin.Route{plugin.SecretStore, plugin.Drop}, leak: true}
	in := leakyInputs(t, p)
	ch := &wizardChoices{
		mode:         "keep-as-is",
		confirm:      true,
		secretRoutes: map[int]string{0: "store"},
	}
	plan, err := buildPlanFromChoices(in, ch, false)
	if err != nil {
		t.Fatalf("plan build errored before the re-gate could be exercised: %v", err)
	}
	// The leaked plan must be caught by the pure re-gate...
	if gerr := regateSeedPlan(plan); gerr == nil {
		t.Fatal("re-gate passed a shared seed carrying the raw secret (defense-in-depth broken)")
	}
	// ...and executeSeedPlan must abort BEFORE any mutation: nothing under HOME.
	if _, xerr := executeSeedPlan(discardWriter{}, plan, ""); xerr == nil {
		t.Fatal("executeSeedPlan did not abort on a leaking plan")
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Errorf("gate-abort wrote into HOME (must write NOTHING): %v", entries)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// AC-wizard-backup, UNIT-PHASE arms (F2-7/F3-4): a pre-planted SYMLINK at the
// candidate name aborts (nothing written through it); a pre-existing REGULAR
// file yields the `-2` suffix; -2..-9 all taken aborts.
func TestWriteVisibleBackup_SymlinkAndCollisionArms(t *testing.T) {
	content := []byte("# original\nalias gs='git status'\n")

	// Pin the timestamp so the candidate name is deterministic across arms.
	orig := backupTimestamp
	backupTimestamp = func() string { return "20260101-120000" }
	t.Cleanup(func() { backupTimestamp = orig })

	// Baseline: a fresh HOME gets exactly one 0600 O_EXCL regular file with the
	// original bytes, at the timestamped name.
	home := t.TempDir()
	bak, err := writeVisibleBackup(home, content)
	if err != nil {
		t.Fatalf("writeVisibleBackup: %v", err)
	}
	if want := filepath.Join(home, ".zshrc.ferry-20260101-120000.bak"); bak != want {
		t.Errorf("backup path = %q, want %q", bak, want)
	}
	info, err := os.Lstat(bak)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Errorf("backup %s: mode %v err %v — want a 0600 regular file", bak, info.Mode(), err)
	}
	data, _ := os.ReadFile(bak)
	if string(data) != string(content) {
		t.Errorf("backup content != original bytes")
	}

	// Pre-existing REGULAR file at the candidate name (same-second re-run):
	// the next backup takes the `-2` suffix (F3-4).
	bak2, err := writeVisibleBackup(home, content)
	if err != nil {
		t.Fatalf("second writeVisibleBackup: %v", err)
	}
	if bak2 != bak+"-2" {
		t.Errorf("same-second collision path = %q, want %q", bak2, bak+"-2")
	}

	// SYMLINK pre-planted at the candidate name: ABORT, nothing written
	// through it (attack shape — never sidestepped by suffixing).
	linkHome := t.TempDir()
	target := filepath.Join(linkHome, "attack-target")
	if err := os.WriteFile(target, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	cand := filepath.Join(linkHome, ".zshrc.ferry-20260101-120000.bak")
	if err := os.Symlink(target, cand); err != nil {
		t.Fatal(err)
	}
	if _, err := writeVisibleBackup(linkHome, content); err == nil {
		t.Fatal("a symlink at the backup candidate name did not abort")
	} else if !strings.Contains(err.Error(), "regular") {
		t.Errorf("symlink-abort error does not explain the non-regular refusal: %v", err)
	}
	victim, _ := os.ReadFile(target)
	if string(victim) != "victim" {
		t.Errorf("the symlink target was written through: %q", victim)
	}

	// All -2..-9 suffixes taken: abort rather than loop forever.
	fullHome := t.TempDir()
	base := filepath.Join(fullHome, ".zshrc.ferry-20260101-120000.bak")
	if err := os.WriteFile(base, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 9; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s-%d", base, i), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writeVisibleBackup(fullHome, content); err == nil {
		t.Error("exhausted -2..-9 suffixes did not abort")
	}
}

// Claude CRITICAL: the TUI titles blocks via Describe — a secret-bearing
// block must be masked (value swapped for its placeholder) BEFORE any block
// text reaches the terminal.
func TestMaskBlockSecrets(t *testing.T) {
	p := &leakyPlugin{routes: []plugin.Route{plugin.SecretStore, plugin.Drop}}
	in := leakyInputs(t, p)
	masked := maskBlockSecrets(in.blocks[0], in.findings, 0, p.Domain())
	if strings.Contains(string(masked.Raw), synthWizardSecret) {
		t.Errorf("masked block still carries the secret value:\n%s", masked.Raw)
	}
	if !strings.Contains(string(masked.Raw), `{{ferry.secret "zsh.leaked"}}`) {
		t.Errorf("masked block lacks the placeholder:\n%s", masked.Raw)
	}
	if desc := p.Describe(masked); strings.Contains(desc, synthWizardSecret) {
		t.Errorf("Describe over the masked block leaks the value: %q", desc)
	}
	// The ORIGINAL block is untouched (mask returns a copy).
	if !strings.Contains(string(in.blocks[0].Raw), synthWizardSecret) {
		t.Error("maskBlockSecrets mutated the original block")
	}
	// A block without secret findings passes through byte-identical.
	plain := plugin.Block{Kind: plugin.Alias, Raw: []byte("alias gs='git status'\n"), Start: 1}
	if got := maskBlockSecrets(plain, in.findings, 5, "zsh"); string(got.Raw) != string(plain.Raw) {
		t.Errorf("secret-free block changed: %q", got.Raw)
	}
}

// Claude MINOR: each answers-file mode LOUDLY rejects tables that do not
// apply to it; [repairs] requires --repair in every mode.
func TestValidateChoicesApplicability(t *testing.T) {
	cases := []struct {
		name    string
		ch      wizardChoices
		repair  bool
		wantErr string
	}{
		{"repairs without --repair (keep-as-is)", wizardChoices{mode: "keep-as-is", repairsGiven: true}, false, "--repair"},
		{"repairs without --repair (start-fresh)", wizardChoices{mode: "start-fresh", repairsGiven: true}, false, "--repair"},
		{"routes in start-fresh", wizardChoices{mode: "start-fresh", routes: map[int]string{0: "shared"}}, false, "[routes]"},
		{"secret-routes in start-fresh", wizardChoices{mode: "start-fresh", secretRoutes: map[int]string{0: "store"}}, false, "[secret-routes]"},
		{"repairs in start-fresh even with --repair", wizardChoices{mode: "start-fresh", repairsGiven: true}, true, "[repairs]"},
		{"starter in keep-as-is", wizardChoices{mode: "keep-as-is", starter: plugin.Answers{"prompt": "minimal"}}, false, "[starter]"},
		{"starter in per-block", wizardChoices{mode: "per-block", starter: plugin.Answers{"prompt": "minimal"}}, false, "[starter]"},
		{"clean keep-as-is", wizardChoices{mode: "keep-as-is"}, false, ""},
		{"clean start-fresh with starter", wizardChoices{mode: "start-fresh", starter: plugin.Answers{"prompt": "minimal"}}, false, ""},
		{"clean per-block with repairs and --repair", wizardChoices{mode: "per-block", repairsGiven: true}, true, ""},
	}
	for _, c := range cases {
		err := validateChoicesApplicability(&c.ch, c.repair)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error: %v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: inapplicable table accepted silently", c.name)
		} else if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: error does not name %s: %v", c.name, c.wantErr, err)
		}
	}
}

// Claude MINOR: a NEAR-EMPTY original ~/.zshrc EXISTS — start-fresh over it
// still writes the visible .bak and previews the replacement diff.
func TestStartFreshOverNearEmptyBacksUpOriginal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	nearEmpty := "# just a comment\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(nearEmpty), 0o644); err != nil {
		t.Fatal(err)
	}
	answers := filepath.Join(t.TempDir(), "answers.toml")
	if err := os.WriteFile(answers, []byte("mode = \"start-fresh\"\nconfirm = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := wizardRegistry.Get("zsh")
	if err != nil {
		t.Fatal(err)
	}
	det, err := p.Detect(home)
	if err != nil {
		t.Fatal(err)
	}
	if det.Reason != plugin.NearEmpty {
		t.Fatalf("setup: Detect reason = %v, want NearEmpty", det.Reason)
	}
	var out bytes.Buffer
	plan, declined, err := buildAnswersSeedPlan(&out, freshInitOpts{answersPath: answers}, p, det)
	if err != nil || declined {
		t.Fatalf("buildAnswersSeedPlan: declined=%v err=%v", declined, err)
	}
	if string(plan.backup) != nearEmpty {
		t.Errorf("plan.backup = %q, want the near-empty ORIGINAL bytes (it exists, so it must be backed up)", plan.backup)
	}
	if string(plan.original) != nearEmpty {
		t.Errorf("plan.original = %q, want the original for the preview diff", plan.original)
	}
	if !strings.Contains(out.String(), "diff: ~/.zshrc after first apply") {
		t.Errorf("preview lacks the replacement diff over the near-empty original:\n%s", out.String())
	}
	// An ABSENT original still yields no backup.
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	detAbsent, _ := p.Detect(emptyHome)
	planAbsent, _, err := buildAnswersSeedPlan(&out, freshInitOpts{answersPath: answers}, p, detAbsent)
	if err != nil {
		t.Fatal(err)
	}
	if planAbsent.backup != nil {
		t.Error("an ABSENT original produced a backup")
	}
}

// Ship-review round-2 #2: the NearEmpty start-fresh short-circuit skips
// Parse/Analyze, so the preview's mask pairs must come from the gate's own
// span scan — a comments-only original carrying a commented token must never
// print the value in the preview diff (the answers surface writes the preview
// to stdout regardless of tty). The .bak still keeps the ORIGINAL bytes.
func TestStartFreshOverNearEmptyMasksSecretsInPreview(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	nearEmpty := "# disabled for now:\n# export GITHUB_TOKEN=" + synthWizardSecret + "\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(nearEmpty), 0o644); err != nil {
		t.Fatal(err)
	}
	answers := filepath.Join(t.TempDir(), "answers.toml")
	if err := os.WriteFile(answers, []byte("mode = \"start-fresh\"\nconfirm = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := wizardRegistry.Get("zsh")
	if err != nil {
		t.Fatal(err)
	}
	det, err := p.Detect(home)
	if err != nil {
		t.Fatal(err)
	}
	if det.Reason != plugin.NearEmpty {
		t.Fatalf("setup: Detect reason = %v, want NearEmpty", det.Reason)
	}
	var out bytes.Buffer
	plan, declined, err := buildAnswersSeedPlan(&out, freshInitOpts{answersPath: answers}, p, det)
	if err != nil || declined {
		t.Fatalf("buildAnswersSeedPlan: declined=%v err=%v", declined, err)
	}
	if strings.Contains(out.String(), synthWizardSecret) {
		t.Errorf("preview printed the raw secret value from the near-empty original:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "diff: ~/.zshrc after first apply") {
		t.Errorf("preview lacks the replacement diff:\n%s", out.String())
	}
	// The diff still shows the original SIDE — masked with a placeholder.
	if !strings.Contains(out.String(), `{{ferry.secret "zsh.secret_2"}}`) {
		t.Errorf("masked original-side line missing from the preview:\n%s", out.String())
	}
	// The .bak content contract is unchanged: original bytes, value included.
	if string(plan.backup) != nearEmpty {
		t.Errorf("plan.backup must keep the ORIGINAL bytes (only OUTPUT is masked), got %q", plan.backup)
	}
}

// Claude MINOR: the EEXIST race re-classifies via Lstat — a symlink/non-regular
// entry ABORTS; only a regular file proceeds to the suffix.
func TestBackupCandidateState(t *testing.T) {
	dir := t.TempDir()

	// Absent: free to create.
	if exists, err := backupCandidateState(filepath.Join(dir, "absent")); exists || err != nil {
		t.Errorf("absent: exists=%v err=%v, want free", exists, err)
	}
	// Regular file: a suffix-retry collision, not an abort.
	reg := filepath.Join(dir, "regular")
	if err := os.WriteFile(reg, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if exists, err := backupCandidateState(reg); !exists || err != nil {
		t.Errorf("regular: exists=%v err=%v, want collision without abort", exists, err)
	}
	// Symlink: the attack shape — abort.
	link := filepath.Join(dir, "link")
	if err := os.Symlink(reg, link); err != nil {
		t.Fatal(err)
	}
	if _, err := backupCandidateState(link); err == nil {
		t.Error("symlink candidate did not abort")
	} else if !strings.Contains(err.Error(), "regular") {
		t.Errorf("symlink abort does not explain the non-regular refusal: %v", err)
	}
	// Directory (non-regular): abort too.
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := backupCandidateState(sub); err == nil {
		t.Error("directory candidate did not abort")
	}
}

// Ship-review round-2 #4: keep-as-is/per-block answers with a POPULATED
// [routes]/[secret-routes] table over an Absent or NearEmpty ~/.zshrc fail
// LOUDLY (zero blocks — every index is out of range by definition); empty
// tables still proceed declare-only.
func TestAnswersRoutesOverAbsentOrNearEmptyRefused(t *testing.T) {
	p, err := wizardRegistry.Get("zsh")
	if err != nil {
		t.Fatal(err)
	}
	writeAnswers := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "answers.toml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// Arm 1: ABSENT + per-block [routes].
	absentHome := t.TempDir()
	t.Setenv("HOME", absentHome)
	detAbsent, _ := p.Detect(absentHome)
	if detAbsent.Reason != plugin.Absent {
		t.Fatalf("setup: reason = %v, want Absent", detAbsent.Reason)
	}
	var out bytes.Buffer
	answers := writeAnswers(t, "mode = \"per-block\"\nconfirm = true\n\n[routes]\n\"0\" = \"shared\"\n")
	_, _, err = buildAnswersSeedPlan(&out, freshInitOpts{answersPath: answers}, p, detAbsent)
	if err == nil {
		t.Error("populated [routes] over an ABSENT rc was silently accepted")
	} else if !strings.Contains(err.Error(), "absent") || !strings.Contains(err.Error(), "[routes]") {
		t.Errorf("error does not name the condition and the table: %v", err)
	}

	// Arm 2: NEAR-EMPTY + keep-as-is [secret-routes].
	neHome := t.TempDir()
	t.Setenv("HOME", neHome)
	if err := os.WriteFile(filepath.Join(neHome, ".zshrc"), []byte("# only a comment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	detNE, _ := p.Detect(neHome)
	if detNE.Reason != plugin.NearEmpty {
		t.Fatalf("setup: reason = %v, want NearEmpty", detNE.Reason)
	}
	answers = writeAnswers(t, "mode = \"keep-as-is\"\nconfirm = true\n\n[secret-routes]\n\"0\" = \"store\"\n")
	_, _, err = buildAnswersSeedPlan(&out, freshInitOpts{answersPath: answers}, p, detNE)
	if err == nil {
		t.Error("populated [secret-routes] over a NEAR-EMPTY rc was silently accepted")
	} else if !strings.Contains(err.Error(), "near-empty") || !strings.Contains(err.Error(), "[secret-routes]") {
		t.Errorf("error does not name the condition and the table: %v", err)
	}

	// Arm 3: NEAR-EMPTY + populated [repairs] (with --repair): same loud refusal.
	answers = writeAnswers(t, "mode = \"keep-as-is\"\nconfirm = true\n\n[repairs]\n\"0\" = \"accept\"\n")
	_, _, err = buildAnswersSeedPlan(&out, freshInitOpts{answersPath: answers, repair: true}, p, detNE)
	if err == nil {
		t.Error("populated [repairs] over a NEAR-EMPTY rc was silently accepted")
	} else if !strings.Contains(err.Error(), "near-empty") || !strings.Contains(err.Error(), "[repairs]") {
		t.Errorf("error does not name the condition and the [repairs] table: %v", err)
	}

	// Empty tables still proceed declare-only (exit clean, nothing to seed).
	answers = writeAnswers(t, "mode = \"keep-as-is\"\nconfirm = true\n")
	plan, declined, err := buildAnswersSeedPlan(&out, freshInitOpts{answersPath: answers}, p, detNE)
	if err != nil || declined {
		t.Fatalf("empty-table keep-as-is over NearEmpty must proceed declare-only: declined=%v err=%v", declined, err)
	}
	if plan.shared != nil || plan.backup != nil {
		t.Errorf("declare-only plan seeded content: shared=%q backup=%q", plan.shared, plan.backup)
	}
}

// Ship-review round-4 #2: duplicate-PATH detection must not count content the
// user is DROPPING — dropped blocks are emptied BEFORE ApplyRepairs, so a
// dropped first occurrence no longer makes the kept block's line "a
// duplicate"; a LOCAL-routed first occurrence still does (the sidecar deploys
// it, so shared+local is still an effective duplicate).
func TestDedupeIgnoresDroppedBlocksButSeesLocal(t *testing.T) {
	p, err := wizardRegistry.Get("zsh")
	if err != nil {
		t.Fatal(err)
	}
	const pathLine = "export PATH=dup:$PATH"
	content := pathLine + "\n\nalias gs='git status'\n\n" + pathLine + "\n"
	blocks, err := p.Parse([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	findings := p.Analyze(blocks)
	in := &wizardInputs{p: p, original: []byte(content), blocks: blocks, findings: findings}
	if len(repairableFindings(findings)) != 1 {
		t.Fatalf("fixture must yield exactly one DuplicatePath finding, got %+v", repairableFindings(findings))
	}

	// Arm 1: FIRST occurrence DROPPED, stale dup repair NOT accepted — the
	// kept block's line survives into the shared seed.
	ch := &wizardChoices{
		mode:    "per-block",
		confirm: true,
		routes:  map[int]string{0: "drop", 1: "shared", 2: "shared"},
		repairs: map[int]bool{},
	}
	plan, err := buildPlanFromChoices(in, ch, true)
	if err != nil {
		t.Fatalf("drop arm: %v", err)
	}
	if !strings.Contains(string(plan.shared), pathLine) {
		t.Errorf("the kept block's PATH line was removed despite its 'duplicate' living in a DROPPED block:\n%s", plan.shared)
	}

	// Arm 2: FIRST occurrence DROPPED and the now-stale dup repair ACCEPTED —
	// the contradiction fails LOUDLY (never a silent removal of the seed's
	// only surviving PATH export).
	ch.repairs = map[int]bool{0: true}
	if _, err := buildPlanFromChoices(in, ch, true); err == nil {
		t.Error("accepting the dropped-block-dependent dup repair succeeded silently (must fail loudly or be moot)")
	} else if !strings.Contains(err.Error(), "repair") {
		t.Errorf("the loud failure does not name the repair: %v", err)
	}

	// Arm 3: FIRST occurrence routed LOCAL — still an effective duplicate;
	// the accepted repair removes the kept block's line from the SHARED seed
	// while the local sidecar carries the original.
	ch = &wizardChoices{
		mode:    "per-block",
		confirm: true,
		routes:  map[int]string{0: "local", 1: "shared", 2: "shared"},
		repairs: map[int]bool{0: true},
	}
	plan, err = buildPlanFromChoices(in, ch, true)
	if err != nil {
		t.Fatalf("local arm: %v", err)
	}
	if strings.Contains(string(plan.shared), pathLine) {
		t.Errorf("shared+local duplicate survived the accepted dedupe in the SHARED seed:\n%s", plan.shared)
	}
	if !strings.Contains(string(plan.local), pathLine) {
		t.Errorf("the local sidecar lost the first occurrence:\n%s", plan.local)
	}

	// A repair accepted INSIDE a drop-routed block is moot and filtered, not
	// an error: route the dup block itself to drop.
	ch = &wizardChoices{
		mode:    "per-block",
		confirm: true,
		routes:  map[int]string{0: "shared", 1: "shared", 2: "drop"},
		repairs: map[int]bool{0: true},
	}
	plan, err = buildPlanFromChoices(in, ch, true)
	if err != nil {
		t.Fatalf("moot-repair arm: %v", err)
	}
	if n := strings.Count(string(plan.shared), pathLine); n != 1 {
		t.Errorf("want the first occurrence alone in the shared seed, got %d copies:\n%s", n, plan.shared)
	}
}
