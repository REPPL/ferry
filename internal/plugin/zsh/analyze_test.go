package zsh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/plugin"
	"github.com/REPPL/ferry/internal/secret"
)

// synthToken is a synthetic high-entropy value that trips the real gate
// (>=24 chars, base64-shaped, letters+digits, entropy >= 4.0). Never a real key.
const synthToken = "FERRYUNITq7W3e9R1t8Y2u0I6o4P5aSdKjZx"

const synthPEM = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"c3ludGhldGljVW5pdEtleUJvZHlMaW5lWlpaWlpaWlpaWlpaWlpaWlpaWlpa\n" +
	"-----END OPENSSH PRIVATE KEY-----"

func testPlugin(t *testing.T) *Plugin {
	t.Helper()
	return &Plugin{Home: t.TempDir()}
}

func parseBlocks(t *testing.T, p *Plugin, content string) []plugin.Block {
	t.Helper()
	blocks, err := p.Parse([]byte(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return blocks
}

func secretFindings(fs []plugin.Finding) []plugin.Finding {
	var out []plugin.Finding
	for _, f := range fs {
		if f.Kind == plugin.SecretLine {
			out = append(out, f)
		}
	}
	return out
}

func repairables(fs []plugin.Finding) []plugin.Finding {
	var out []plugin.Finding
	for _, f := range fs {
		if f.Repairable {
			out = append(out, f)
		}
	}
	return out
}

// Assignment-shaped extraction: Key from the sanitized lowercase var name,
// Value = the RHS, Replacement = the line with the RHS swapped for the
// placeholder. Routes are exactly {SecretStore, Drop}.
func TestAnalyzeAssignmentExtraction(t *testing.T) {
	p := testPlugin(t)
	blocks := parseBlocks(t, p, "# token\nexport GITHUB_TOKEN="+synthToken+"\n")
	secrets := secretFindings(p.Analyze(blocks))
	if len(secrets) != 1 {
		t.Fatalf("want 1 secret finding, got %d", len(secrets))
	}
	f := secrets[0]
	if f.Secret == nil {
		t.Fatal("SecretLine finding carries no extraction")
	}
	if f.Secret.Key != "github_token" {
		t.Errorf("Key = %q, want github_token", f.Secret.Key)
	}
	if f.Secret.Value != synthToken {
		t.Errorf("Value = %q, want the RHS", f.Secret.Value)
	}
	wantRepl := "# token\nexport GITHUB_TOKEN={{ferry.secret \"zsh.github_token\"}}\n"
	if string(f.Secret.Replacement) != wantRepl {
		t.Errorf("Replacement = %q, want %q", f.Secret.Replacement, wantRepl)
	}
	if len(f.Routes) != 2 || f.Routes[0] != plugin.SecretStore || f.Routes[1] != plugin.Drop {
		t.Errorf("Routes = %v, want [SecretStore Drop]", f.Routes)
	}
	if strings.Contains(f.Detail, synthToken) {
		t.Error("Detail leaks the secret value")
	}
}

// PEM material extracts SPAN-grained: Value is exactly the contiguous
// BEGIN..END run; the Replacement collapses it to ONE placeholder line under a
// positional secret_<line> key; surrounding lines stay in place.
func TestAnalyzePEMSpanExtraction(t *testing.T) {
	p := testPlugin(t)
	content := "# deploy key\n" + synthPEM + "\nexport AFTER=ok\n"
	blocks := parseBlocks(t, p, content)
	secrets := secretFindings(p.Analyze(blocks))
	if len(secrets) != 1 {
		t.Fatalf("want exactly 1 secret finding (span dedupe), got %d", len(secrets))
	}
	f := secrets[0]
	if f.Secret.Value != synthPEM {
		t.Errorf("Value is not the exact BEGIN..END span:\n%q", f.Secret.Value)
	}
	if f.Secret.Key != "secret_2" {
		t.Errorf("Key = %q, want positional secret_2", f.Secret.Key)
	}
	repl := string(f.Secret.Replacement)
	if strings.Contains(repl, "BEGIN OPENSSH") {
		t.Errorf("Replacement still carries key material:\n%s", repl)
	}
	if !strings.Contains(repl, "export AFTER=ok") || !strings.Contains(repl, "# deploy key") {
		t.Errorf("Replacement lost surrounding non-secret lines:\n%s", repl)
	}
	if got := strings.Count(repl, "{{ferry.secret"); got != 1 {
		t.Errorf("Replacement has %d placeholder lines, want 1", got)
	}
}

// Duplicate var names get deterministic _2 suffixes BEFORE Replacement is
// built, so placeholder and Put ref cannot diverge (r2-M1).
func TestAnalyzeDuplicateKeySuffixing(t *testing.T) {
	p := testPlugin(t)
	other := "FERRYUNITz8X4f0S2u9Z3v1J7p5Q6bTeLkWy"
	content := "export GITHUB_TOKEN=" + synthToken + "\n\nexport GITHUB_TOKEN=" + other + "\n"
	secrets := secretFindings(p.Analyze(parseBlocks(t, p, content)))
	if len(secrets) != 2 {
		t.Fatalf("want 2 secret findings, got %d", len(secrets))
	}
	if secrets[0].Secret.Key != "github_token" || secrets[1].Secret.Key != "github_token_2" {
		t.Errorf("keys = %q, %q; want github_token, github_token_2", secrets[0].Secret.Key, secrets[1].Secret.Key)
	}
	if !strings.Contains(string(secrets[1].Secret.Replacement), `zsh.github_token_2`) {
		t.Errorf("second Replacement does not embed the suffixed ref:\n%s", secrets[1].Secret.Replacement)
	}
}

// Machine-specific detection: brew prefix / hostname blocks suggest Local with
// {Local, Shared, Drop}; hardcoded-home paths are a REPAIR, not machine-specific.
func TestAnalyzeMachineSpecific(t *testing.T) {
	p := testPlugin(t)
	content := "eval \"$(/opt/homebrew/bin/brew shellenv)\"\n\nalias proj=\"cd /Users/testuser/projects\"\n"
	fs := p.Analyze(parseBlocks(t, p, content))
	var machines []plugin.Finding
	for _, f := range fs {
		if f.Kind == plugin.MachineSpecific {
			machines = append(machines, f)
		}
	}
	if len(machines) != 1 || machines[0].Block != 0 {
		t.Fatalf("want exactly 1 machine finding on block 0, got %+v", machines)
	}
	if len(machines[0].Routes) != 3 || machines[0].Routes[0] != plugin.Local {
		t.Errorf("machine Routes = %v, want Local suggested first of {Local, Shared, Drop}", machines[0].Routes)
	}
}

// Repairs detect in block order: hardcoded home -> $HOME; duplicate PATH;
// dead source (only unguarded, only resolvable targets).
func TestAnalyzeRepairs(t *testing.T) {
	p := testPlugin(t)
	// A file that EXISTS must not be flagged dead.
	alive := filepath.Join(p.Home, "alive.zsh")
	if err := os.WriteFile(alive, []byte("# ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content := "export PATH=\"$HOME/bin:$PATH\"\n\n" +
		"alias proj=\"cd /Users/testuser/projects\"\n\n" +
		"export PATH=\"$HOME/bin:$PATH\"\n\n" +
		"source ~/alive.zsh\n\n" +
		"source ~/nonexistent.zsh\n\n" +
		"[ -f ~/also-missing.zsh ] && source ~/also-missing.zsh\n"
	reps := repairables(p.Analyze(parseBlocks(t, p, content)))
	if len(reps) != 3 {
		t.Fatalf("want 3 repairable findings (home, dup-path, dead-source), got %d: %+v", len(reps), reps)
	}
	if reps[0].Kind != plugin.HardcodedHome || reps[0].Block != 1 {
		t.Errorf("repair 0 = %v on block %d, want HardcodedHome on 1", reps[0].Kind, reps[0].Block)
	}
	if !strings.Contains(reps[0].Suggested, "$HOME/projects") {
		t.Errorf("HardcodedHome Suggested = %q, want the $HOME form", reps[0].Suggested)
	}
	if reps[1].Kind != plugin.DuplicatePath || reps[1].Block != 2 {
		t.Errorf("repair 1 = %v on block %d, want DuplicatePath on 2", reps[1].Kind, reps[1].Block)
	}
	if reps[2].Kind != plugin.DeadSource || reps[2].Block != 4 {
		t.Errorf("repair 2 = %v on block %d, want DeadSource on 4 (guarded line must not flag)", reps[2].Kind, reps[2].Block)
	}
}

// ApplyRepairs: single deterministic writer; a block holding BOTH a secret and
// a repair gets both edits; never adds/removes/reorders blocks; a full-block
// removal EMPTIES Raw (dedupe keeps the first occurrence).
func TestApplyRepairsCombinedAndInvariant(t *testing.T) {
	p := testPlugin(t)
	content := "# combined\nexport GITHUB_TOKEN=" + synthToken + "\nalias proj=\"cd /Users/testuser/projects\"\n\n" +
		"export PATH=a:$PATH\n\nexport PATH=a:$PATH\n"
	blocks := parseBlocks(t, p, content)
	fs := p.Analyze(blocks)
	repaired, err := p.ApplyRepairs(blocks, fs) // accept everything
	if err != nil {
		t.Fatalf("ApplyRepairs: %v", err)
	}
	if len(repaired) != len(blocks) {
		t.Fatalf("block count changed: %d -> %d (never add/remove/reorder)", len(blocks), len(repaired))
	}
	b0 := string(repaired[0].Raw)
	if !strings.Contains(b0, `export GITHUB_TOKEN={{ferry.secret "zsh.github_token"}}`) {
		t.Errorf("secret substitution lost:\n%s", b0)
	}
	if !strings.Contains(b0, `alias proj="cd $HOME/projects"`) || strings.Contains(b0, "/Users/testuser") {
		t.Errorf("home repair lost (single-writer composition, r3-M2):\n%s", b0)
	}
	if len(repaired[1].Raw) == 0 {
		t.Error("FIRST PATH occurrence was removed; dedupe must keep it")
	}
	if len(repaired[2].Raw) != 0 {
		t.Errorf("duplicate PATH block not EMPTIED: %q", repaired[2].Raw)
	}
	// Declining everything leaves the blocks byte-identical.
	untouched, err := p.ApplyRepairs(blocks, nil)
	if err != nil {
		t.Fatalf("ApplyRepairs(none): %v", err)
	}
	if string(plugin.Reassemble(untouched)) != content {
		t.Error("ApplyRepairs with no accepted findings changed content")
	}
}

// plugin.DropSecretSpans removes exactly the span line(s) each finding's
// Replacement identifies (positionally), separators included, and empties a
// block left without content.
func TestDropSecretSpans(t *testing.T) {
	p := testPlugin(t)
	drop := func(content string) string {
		t.Helper()
		blocks := parseBlocks(t, p, content)
		if len(blocks) != 1 {
			t.Fatalf("fixture must be one block, got %d", len(blocks))
		}
		var repls [][]byte
		for _, f := range secretFindings(p.Analyze(blocks)) {
			repls = append(repls, f.Secret.Replacement)
		}
		return string(plugin.DropSecretSpans(blocks[0].Raw, repls))
	}

	got := drop("# token\nexport GITHUB_TOKEN=" + synthToken + "\nalias keep='me'\n")
	if got != "# token\nalias keep='me'\n" {
		t.Errorf("assignment drop = %q", got)
	}
	if out := drop("export GITHUB_TOKEN=" + synthToken + "\n\n"); out != "" {
		t.Errorf("a value-only block must empty entirely, got %q", out)
	}
	if out := drop("# key\n" + synthPEM + "\n"); out != "# key\n" {
		t.Errorf("PEM span drop = %q", out)
	}
}

// Starter: portable, commented, sidecar sourced last; unknown answers error.
func TestStarter(t *testing.T) {
	p := testPlugin(t)
	out, err := p.Starter(plugin.Answers{"prompt": "minimal"})
	if err != nil {
		t.Fatalf("Starter: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "/Users/") || !strings.Contains(s, "$HOME") {
		t.Errorf("starter is not portable:\n%s", s)
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, ".zshrc.local") {
		t.Errorf("starter's last line does not source ~/.zshrc.local: %q", last)
	}
	if _, err := p.Starter(plugin.Answers{"bogus": "x"}); err == nil {
		t.Error("unknown starter answer id did not error")
	}
	if _, err := p.Starter(plugin.Answers{"prompt": "bogus"}); err == nil {
		t.Error("unknown starter option value did not error")
	}
}

// Detect distinguishes OK / Absent / NearEmpty / Symlink / Unreadable.
func TestDetectReasons(t *testing.T) {
	p := testPlugin(t)

	if d, _ := p.Detect(p.Home); d.Reason != plugin.Absent || d.Present {
		t.Errorf("absent: %+v", d)
	}

	path := filepath.Join(p.Home, ".zshrc")
	if err := os.WriteFile(path, []byte("# only a comment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, _ := p.Detect(p.Home); d.Reason != plugin.NearEmpty {
		t.Errorf("near-empty: %+v", d)
	}

	if err := os.WriteFile(path, []byte("alias gs='git status'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, _ := p.Detect(p.Home); d.Reason != plugin.OK || !d.Present {
		t.Errorf("ok: %+v", d)
	}

	if os.Geteuid() != 0 {
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		if d, _ := p.Detect(p.Home); d.Reason != plugin.Unreadable {
			t.Errorf("unreadable: %+v", d)
		}
		_ = os.Chmod(path, 0o644)
	}

	link := filepath.Join(p.Home, "sub")
	if err := os.MkdirAll(link, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(p.Home, "sub"), path); err != nil {
		t.Fatal(err)
	}
	if d, _ := p.Detect(p.Home); d.Reason != plugin.Symlink {
		t.Errorf("symlink: %+v", d)
	}
}

// Ship-review round-2 #1 (Claude repro): a byte-equal occurrence of the value
// on an EARLIER line (an alias echoing it) must never be patched or dropped in
// the flagged line's stead — substitution and drop anchor to the finding's
// LINE RANGE. And routing every finding of the block to the store leaves no
// raw value behind, so the pre-mutation re-gate never aborts.
func TestApplyRepairsPositionalAnchoring(t *testing.T) {
	p := testPlugin(t)
	aliasLine := "alias showpw='echo " + synthToken + "'"
	content := aliasLine + "\nexport PASSWORD=" + synthToken + "\n"
	blocks := parseBlocks(t, p, content)
	secrets := secretFindings(p.Analyze(blocks))
	if len(secrets) != 2 {
		t.Fatalf("fixture must flag both lines, got %d findings", len(secrets))
	}
	var passwordFinding, aliasFinding plugin.Finding
	for _, f := range secrets {
		if f.Secret.Key == "password" {
			passwordFinding = f
		} else {
			aliasFinding = f
		}
	}
	if passwordFinding.Secret == nil || aliasFinding.Secret == nil {
		t.Fatalf("missing expected findings: %+v", secrets)
	}

	// Substitution of ONLY the assignment finding patches ONLY line 2.
	out, err := p.ApplyRepairs(blocks, []plugin.Finding{passwordFinding})
	if err != nil {
		t.Fatalf("ApplyRepairs: %v", err)
	}
	got := string(out[0].Raw)
	want := aliasLine + "\nexport PASSWORD={{ferry.secret \"zsh.password\"}}\n"
	if got != want {
		t.Errorf("positional substitution wrong (alias line must survive VERBATIM).\ngot:  %q\nwant: %q", got, want)
	}

	// Drop of ONLY the assignment finding removes ONLY line 2.
	dropped := string(plugin.DropSecretSpans(blocks[0].Raw, [][]byte{passwordFinding.Secret.Replacement}))
	if dropped != aliasLine+"\n" {
		t.Errorf("positional drop wrong (alias line must survive VERBATIM).\ngot: %q", dropped)
	}

	// Routing BOTH findings to the store (the real per-block route) leaves no
	// raw value anywhere: the re-gate must NOT abort.
	all, err := p.ApplyRepairs(blocks, secrets)
	if err != nil {
		t.Fatalf("ApplyRepairs(all): %v", err)
	}
	final := string(plugin.Reassemble(all))
	if strings.Contains(final, synthToken) {
		t.Errorf("raw value survived the full store route (re-gate would abort init):\n%s", final)
	}
	if secret.GateText(final).BlockedFromRepo {
		t.Errorf("final block still gate-blocked (init would hard-abort):\n%s", final)
	}
	// The alias line's own finding replaced its WHOLE line with its placeholder.
	if !strings.Contains(final, `{{ferry.secret "zsh.secret_1"}}`) || !strings.Contains(final, `{{ferry.secret "zsh.password"}}`) {
		t.Errorf("expected both placeholders in the final block:\n%s", final)
	}
}

// Ship-review round-3 #2: repair consent is PER FINDING and line-anchored —
// accepting one finding never rewrites a declined sibling of the same kind in
// the same block ("Declined = untouched").
func TestApplyRepairsPerFindingConsent(t *testing.T) {
	p := testPlugin(t)

	t.Run("hardcoded_home", func(t *testing.T) {
		content := "alias a=\"cd /Users/testuser/projects\"\nalias b=\"cd /Users/testuser/downloads\"\n"
		blocks := parseBlocks(t, p, content)
		reps := repairables(p.Analyze(blocks))
		if len(reps) != 2 || reps[0].Kind != plugin.HardcodedHome || reps[1].Kind != plugin.HardcodedHome {
			t.Fatalf("want 2 HardcodedHome findings, got %+v", reps)
		}
		out, err := p.ApplyRepairs(blocks, []plugin.Finding{reps[0]}) // accept FIRST only
		if err != nil {
			t.Fatalf("ApplyRepairs: %v", err)
		}
		got := string(out[0].Raw)
		want := "alias a=\"cd $HOME/projects\"\nalias b=\"cd /Users/testuser/downloads\"\n"
		if got != want {
			t.Errorf("declined sibling was rewritten (block-wide repair).\ngot:  %q\nwant: %q", got, want)
		}
		// Accepting only the SECOND repairs only the second line.
		out, err = p.ApplyRepairs(blocks, []plugin.Finding{reps[1]})
		if err != nil {
			t.Fatalf("ApplyRepairs: %v", err)
		}
		want = "alias a=\"cd /Users/testuser/projects\"\nalias b=\"cd $HOME/downloads\"\n"
		if got := string(out[0].Raw); got != want {
			t.Errorf("second-only acceptance wrong.\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("duplicate_path", func(t *testing.T) {
		dup := "export PATH=a:$PATH"
		content := dup + "\n\n# dups\n" + dup + "\n" + dup + "\n"
		blocks := parseBlocks(t, p, content)
		reps := repairables(p.Analyze(blocks))
		if len(reps) != 2 || reps[0].Kind != plugin.DuplicatePath || reps[1].Kind != plugin.DuplicatePath {
			t.Fatalf("want 2 DuplicatePath findings (occurrences beyond the first), got %+v", reps)
		}
		out, err := p.ApplyRepairs(blocks, []plugin.Finding{reps[0]}) // accept ONE removal only
		if err != nil {
			t.Fatalf("ApplyRepairs: %v", err)
		}
		if n := strings.Count(string(out[1].Raw), dup); n != 1 {
			t.Errorf("block 1 has %d duplicate lines after accepting ONE removal, want exactly 1 kept\n%s", n, out[1].Raw)
		}
		if n := strings.Count(string(out[0].Raw), dup); n != 1 {
			t.Errorf("the FIRST occurrence (block 0) was disturbed:\n%s", out[0].Raw)
		}
	})

	t.Run("dead_source", func(t *testing.T) {
		content := "source ~/gone-one.zsh\nsource ~/gone-two.zsh\n"
		blocks := parseBlocks(t, p, content)
		reps := repairables(p.Analyze(blocks))
		if len(reps) != 2 || reps[0].Kind != plugin.DeadSource || reps[1].Kind != plugin.DeadSource {
			t.Fatalf("want 2 DeadSource findings, got %+v", reps)
		}
		out, err := p.ApplyRepairs(blocks, []plugin.Finding{reps[1]}) // accept the SECOND only
		if err != nil {
			t.Fatalf("ApplyRepairs: %v", err)
		}
		if got := string(out[0].Raw); got != "source ~/gone-one.zsh\n" {
			t.Errorf("declined dead-source line disturbed (want first line byte-verbatim).\ngot: %q", got)
		}
	})
}

// Pins the REACHABILITY ASSUMPTION documented in ApplyRepairs pass 1: all
// shipping surfaces route secrets PER BLOCK, so a partial acceptance among
// byte-identical secret values in one block is unreachable today. If it ever
// happens, the FIRST occurrence is patched and the remainder keeps its raw
// value — which the pre-mutation re-gate then blocks (fails SAFE, never a
// silent misroute). A future per-finding secret UI must add span identity.
func TestApplyRepairsIdenticalSecretsPerBlockAssumption(t *testing.T) {
	p := testPlugin(t)
	line := "export GITHUB_TOKEN=" + synthToken
	content := line + "\n" + line + "\n"
	blocks := parseBlocks(t, p, content)
	secrets := secretFindings(p.Analyze(blocks))
	if len(secrets) != 2 {
		t.Fatalf("want 2 identical-value findings, got %d", len(secrets))
	}
	// Accept only the SECOND finding — unreachable from shipping surfaces
	// (per-block routing accepts both or neither).
	out, err := p.ApplyRepairs(blocks, []plugin.Finding{secrets[1]})
	if err != nil {
		t.Fatalf("ApplyRepairs: %v", err)
	}
	final := string(plugin.Reassemble(out))
	// Documented outcome: ONE occurrence patched (the first, by span order),
	// one raw value left — and the re-gate still BLOCKS the result.
	if n := strings.Count(final, synthToken); n != 1 {
		t.Errorf("want exactly 1 raw occurrence left, got %d:\n%s", n, final)
	}
	if !secret.GateText(final).BlockedFromRepo {
		t.Error("partial identical-value acceptance produced a gate-clean result — the fail-safe assumption no longer holds; per-finding span identity is now REQUIRED")
	}
	// Full per-block acceptance (the shipping shape) is clean.
	all, err := p.ApplyRepairs(blocks, secrets)
	if err != nil {
		t.Fatalf("ApplyRepairs(all): %v", err)
	}
	if secret.GateText(string(plugin.Reassemble(all))).BlockedFromRepo {
		t.Error("full per-block acceptance still gate-blocked")
	}
}

// Ship-review round-4 #1: repair identity ties must never let an accepted
// finding claim a DECLINED sibling's op. Three repro shapes: two DIFFERENT
// duplicate-PATH lines in one block; two hardcoded-home lines differing only
// in leading whitespace; two identical dead sources (byte-equivalent either
// way). Details also carry the line number for user disambiguation.
func TestApplyRepairsIdentityCollisions(t *testing.T) {
	p := testPlugin(t)

	t.Run("two_different_dup_path_lines_one_block", func(t *testing.T) {
		content := "export PATH=a:$PATH\nexport PATH=b:$PATH\n\n# dups\nexport PATH=a:$PATH\nexport PATH=b:$PATH\n"
		blocks := parseBlocks(t, p, content)
		reps := repairables(p.Analyze(blocks))
		if len(reps) != 2 || reps[0].Kind != plugin.DuplicatePath || reps[1].Kind != plugin.DuplicatePath {
			t.Fatalf("want 2 DuplicatePath findings on the second block, got %+v", reps)
		}
		if reps[0].Detail == reps[1].Detail {
			t.Fatalf("two DIFFERENT dup lines share one Detail — identity collision: %q", reps[0].Detail)
		}
		// Accept the /b dup (finding 1), DECLINE the /a dup (finding 0).
		out, err := p.ApplyRepairs(blocks, []plugin.Finding{reps[1]})
		if err != nil {
			t.Fatalf("ApplyRepairs: %v", err)
		}
		got := string(out[1].Raw)
		want := "# dups\nexport PATH=a:$PATH\n"
		if got != want {
			t.Errorf("the DECLINED /a dup was disturbed (or the accepted /b dup survived).\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("hardcoded_home_whitespace_tie", func(t *testing.T) {
		content := "alias p=\"cd /Users/testuser/x\"\n  alias p=\"cd /Users/testuser/x\"\n"
		blocks := parseBlocks(t, p, content)
		reps := repairables(p.Analyze(blocks))
		if len(reps) != 2 {
			t.Fatalf("want 2 HardcodedHome findings, got %+v", reps)
		}
		if reps[0].Suggested == reps[1].Suggested {
			t.Fatalf("whitespace-differing lines share one Suggested — identity collision: %q", reps[0].Suggested)
		}
		// Accept the SECOND (indented) finding only.
		out, err := p.ApplyRepairs(blocks, []plugin.Finding{reps[1]})
		if err != nil {
			t.Fatalf("ApplyRepairs: %v", err)
		}
		got := string(out[0].Raw)
		want := "alias p=\"cd /Users/testuser/x\"\n  alias p=\"cd $HOME/x\"\n"
		if got != want {
			t.Errorf("the DECLINED unindented line was edited (identity collision).\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("identical_dead_sources", func(t *testing.T) {
		content := "source ~/gone.zsh\nsource ~/gone.zsh\n"
		blocks := parseBlocks(t, p, content)
		reps := repairables(p.Analyze(blocks))
		if len(reps) != 2 {
			t.Fatalf("want 2 DeadSource findings, got %+v", reps)
		}
		out, err := p.ApplyRepairs(blocks, []plugin.Finding{reps[0]})
		if err != nil {
			t.Fatalf("ApplyRepairs: %v", err)
		}
		// Byte-identical lines: either removal is byte-equivalent — exactly one remains.
		if got := string(out[0].Raw); got != "source ~/gone.zsh\n" {
			t.Errorf("want exactly one identical dead source kept, got %q", got)
		}
	})

	t.Run("details_carry_line_numbers", func(t *testing.T) {
		content := "export PATH=a:$PATH\n\nexport PATH=a:$PATH\n\nsource ~/gone.zsh\n\nalias p=\"cd /Users/testuser/x\"\n"
		reps := repairables(p.Analyze(parseBlocks(t, p, content)))
		for _, f := range reps {
			if !strings.Contains(f.Detail, "(line ") {
				t.Errorf("%s Detail lacks the line number for user disambiguation: %q", f.Kind, f.Detail)
			}
		}
	})
}

// Ship-review round-5 #1: repair identity includes the ORIGINAL UNTRIMMED
// line, so findings that NORMALIZE to the same repaired form (or differ only
// in indentation) can never claim each other's op.
func TestApplyRepairsOriginalLineIdentity(t *testing.T) {
	p := testPlugin(t)

	// (a) Two DIFFERENT originals with the SAME repaired form:
	// /Users/testuser/dev and /home/testuser/dev both suggest $HOME/dev.
	t.Run("distinct_original_same_repair", func(t *testing.T) {
		lineA := "alias p=\"cd /Users/testuser/dev\""
		lineB := "alias p=\"cd /home/testuser/dev\""
		content := lineA + "\n" + lineB + "\n"
		blocks := parseBlocks(t, p, content)
		reps := repairables(p.Analyze(blocks))
		if len(reps) != 2 {
			t.Fatalf("want 2 HardcodedHome findings, got %+v", reps)
		}
		if reps[0].Suggested != reps[1].Suggested {
			t.Fatalf("fixture must repro the same-repaired-form collision (got distinct Suggested %q vs %q)", reps[0].Suggested, reps[1].Suggested)
		}
		if repairCoreDetail(reps[0].Detail) == repairCoreDetail(reps[1].Detail) {
			t.Fatalf("distinct originals share one content identity — collision: %q", reps[0].Detail)
		}
		// Accept ONLY finding 1 (the /home line): line A byte-verbatim.
		out, err := p.ApplyRepairs(blocks, []plugin.Finding{reps[1]})
		if err != nil {
			t.Fatalf("ApplyRepairs: %v", err)
		}
		want := lineA + "\nalias p=\"cd $HOME/dev\"\n"
		if got := string(out[0].Raw); got != want {
			t.Errorf("accept-1/decline-0 edited the wrong original.\ngot:  %q\nwant: %q", got, want)
		}
		// The other order: accept ONLY finding 0.
		out, err = p.ApplyRepairs(blocks, []plugin.Finding{reps[0]})
		if err != nil {
			t.Fatalf("ApplyRepairs: %v", err)
		}
		want = "alias p=\"cd $HOME/dev\"\n" + lineB + "\n"
		if got := string(out[0].Raw); got != want {
			t.Errorf("accept-0/decline-1 edited the wrong original.\ngot:  %q\nwant: %q", got, want)
		}
	})

	// (b) Indentation-only-differing DUPLICATE-PATH pair: the accepted
	// removal hits exactly the accepted line.
	t.Run("indentation_differing_dup_path", func(t *testing.T) {
		content := "export PATH=a:$PATH\n\n# dups\nexport PATH=a:$PATH\n  export PATH=a:$PATH\n"
		blocks := parseBlocks(t, p, content)
		reps := repairables(p.Analyze(blocks))
		if len(reps) != 2 {
			t.Fatalf("want 2 DuplicatePath findings, got %+v", reps)
		}
		if repairCoreDetail(reps[0].Detail) == repairCoreDetail(reps[1].Detail) {
			t.Fatalf("indentation-differing dups share one identity — collision: %q", reps[0].Detail)
		}
		// Accept the INDENTED dup (finding 1) only: the unindented dup survives.
		out, err := p.ApplyRepairs(blocks, []plugin.Finding{reps[1]})
		if err != nil {
			t.Fatalf("ApplyRepairs: %v", err)
		}
		want := "# dups\nexport PATH=a:$PATH\n"
		if got := string(out[1].Raw); got != want {
			t.Errorf("the DECLINED unindented dup was removed.\ngot:  %q\nwant: %q", got, want)
		}
	})

	// (c) Indentation-only-differing DEAD-SOURCE pair.
	t.Run("indentation_differing_dead_source", func(t *testing.T) {
		content := "source ~/gone.zsh\n  source ~/gone.zsh\n"
		blocks := parseBlocks(t, p, content)
		reps := repairables(p.Analyze(blocks))
		if len(reps) != 2 {
			t.Fatalf("want 2 DeadSource findings, got %+v", reps)
		}
		if repairCoreDetail(reps[0].Detail) == repairCoreDetail(reps[1].Detail) {
			t.Fatalf("indentation-differing dead sources share one identity: %q", reps[0].Detail)
		}
		// Accept the INDENTED one (finding 1) only.
		out, err := p.ApplyRepairs(blocks, []plugin.Finding{reps[1]})
		if err != nil {
			t.Fatalf("ApplyRepairs: %v", err)
		}
		if got := string(out[0].Raw); got != "source ~/gone.zsh\n" {
			t.Errorf("the DECLINED unindented dead source was removed.\ngot: %q", got)
		}
	})
}
