package cmd

// A8 (preview fidelity + abort honesty): the empty-over-substantial data-loss
// guard aborts apply at WRITE time, but the preview used to render the same
// target as a plain "would update" — promising a change that cannot happen. The
// preview now predicts the refusal from the SAME predicate the guard enforces
// (dotfile.WouldRefuseEmptyOverSubstantial), and an aborted run says out loud
// that the changes it already reported were rolled back.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/dotfile"
)

// substantialLive is a live file well over the guard's 64-significant-byte bar.
const substantialLive = "export PATH=/usr/local/bin:$PATH\nalias gs='git status'\nalias gd='git diff'\n"

// seedLive writes content to a fresh temp home path and returns it.
func seedLive(t *testing.T, name, content string) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(home, []byte(content), 0o644); err != nil {
		t.Fatalf("seed live %s: %v", name, err)
	}
	return home
}

// TestPrintPlanPredictsEmptyOverSubstantialRefusal pins the preview: a repo
// source that is empty/near-empty over a substantial live file must be rendered
// as a refusal, never as a "would update" apply will abort on. Every file domain
// shares the prediction (dotfiles, agents, and the repo-authoritative domains).
func TestPrintPlanPredictsEmptyOverSubstantialRefusal(t *testing.T) {
	for _, domain := range []string{"dotfiles", "agents", "terminals"} {
		t.Run(domain, func(t *testing.T) {
			home := seedLive(t, ".zshrc", substantialLive)
			plan := []planItem{{
				kind:       kindFile,
				fileDomain: domain,
				domain:     ".zshrc",
				target:     dotfile.Target{Name: "zshrc", Home: home},
				content:    []byte("# nothing here\n"),
				state:      dotfile.StateRepoAhead,
			}}
			var out bytes.Buffer
			printPlan(&out, plan)
			got := out.String()
			if !strings.Contains(got, "would refuse") {
				t.Errorf("preview must predict the guard's refusal:\n%s", got)
			}
			if strings.Contains(got, "would update") {
				t.Errorf("preview must NOT promise an update the guard aborts:\n%s", got)
			}
			if !strings.Contains(got, "1 would refuse") {
				t.Errorf("summary must count the predicted refusal:\n%s", got)
			}
		})
	}
}

// TestPrintPlanKeepsWouldUpdateForRealContent guards the negative side: an
// ordinary repo-ahead update with real content is untouched by the prediction.
func TestPrintPlanKeepsWouldUpdateForRealContent(t *testing.T) {
	home := seedLive(t, ".zshrc", substantialLive)
	plan := []planItem{{
		kind:       kindFile,
		fileDomain: "dotfiles",
		domain:     ".zshrc",
		target:     dotfile.Target{Name: "zshrc", Home: home},
		content:    []byte(substantialLive + "alias gl='git log'\n"),
		state:      dotfile.StateRepoAhead,
	}}
	var out bytes.Buffer
	printPlan(&out, plan)
	got := out.String()
	if !strings.Contains(got, "would update") {
		t.Errorf("a real-content repo-ahead update must still read as an update:\n%s", got)
	}
	if strings.Contains(got, "would refuse") {
		t.Errorf("a real-content update must never be predicted as a refusal:\n%s", got)
	}
}

// TestPlanSummaryCountsRefusals pins the footer's new category and its ordering.
func TestPlanSummaryCountsRefusals(t *testing.T) {
	t.Parallel()
	if got := planSummary(1, 2, 3, 4); got != "1 would create, 2 would update, 4 conflict, 3 would refuse" {
		t.Errorf("planSummary = %q", got)
	}
	if got := planSummary(0, 0, 0, 0); got != "" {
		t.Errorf("an empty summary must stay empty, got %q", got)
	}
	if got := planSummary(0, 0, 1, 0); got != "1 would refuse" {
		t.Errorf("planSummary = %q", got)
	}
}

// TestRolledBackNoticeIsHonestAboutRevertedWork pins the abort-honesty line: when
// the guard (or any in-process error) aborts a run and the inline rollback
// succeeds, scrollback must not be left asserting writes that were reverted.
func TestRolledBackNoticeIsHonestAboutRevertedWork(t *testing.T) {
	t.Parallel()
	if got := rolledBackNotice(0); got != "" {
		t.Errorf("nothing reported means nothing to retract, got %q", got)
	}
	got := rolledBackNotice(3)
	for _, want := range []string{"3 change(s)", "rolled back"} {
		if !strings.Contains(got, want) {
			t.Errorf("rollback notice must contain %q: %q", want, got)
		}
	}
}

// TestMutateCountsReportedWritesWhenTheGuardAborts pins the count the rollback
// retraction is built on: the data-loss guard aborts the run mid-plan, and mutate
// reports how many changes it had ALREADY printed as written (the ones the
// caller's inline rollback then reverts). Noops are not counted — they assert no
// change to take back.
func TestMutateCountsReportedWritesWhenTheGuardAborts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// 1: a fresh create (counts). 2: already in sync (noop, does not count).
	// 3: an empty repo source over a substantial live file — the guard aborts here.
	created := filepath.Join(home, ".gitconfig")
	clean := filepath.Join(home, ".tmux.conf")
	if err := os.WriteFile(clean, []byte("set -g mouse on\n"), 0o644); err != nil {
		t.Fatalf("seed clean: %v", err)
	}
	guarded := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(guarded, []byte(substantialLive), 0o644); err != nil {
		t.Fatalf("seed guarded: %v", err)
	}

	plan := []planItem{
		{kind: kindFile, fileDomain: "dotfiles", domain: ".gitconfig",
			target: dotfile.Target{Name: "gitconfig", Home: created}, content: []byte("[user]\n\tname = a\n")},
		{kind: kindFile, fileDomain: "dotfiles", domain: ".tmux.conf",
			target: dotfile.Target{Name: "tmux.conf", Home: clean}, content: []byte("set -g mouse on\n")},
		{kind: kindFile, fileDomain: "dotfiles", domain: ".zshrc",
			target: dotfile.Target{Name: "zshrc", Home: guarded}, content: []byte("")},
	}

	b := backuperFunc(func(dest string, content []byte, perm os.FileMode) error {
		return os.WriteFile(dest, content, perm)
	})
	committed := false
	var out bytes.Buffer
	applied, err := mutate(nil, b, func(string) error { return nil }, func() error { committed = true; return nil }, plan, false, &out)
	if err == nil {
		t.Fatalf("the data-loss guard must abort the run; out:\n%s", out.String())
	}
	if committed {
		t.Errorf("an aborted run must never commit the journal")
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1 (the create; the noop asserts no change)\n%s", applied, out.String())
	}
	// The guarded live file is untouched — the guard writes nothing.
	if got, _ := os.ReadFile(guarded); string(got) != substantialLive {
		t.Errorf("the guard must leave the live file byte-identical, got %q", got)
	}
}
