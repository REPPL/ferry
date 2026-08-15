package cmd

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/dotfile"
)

// TestAssessRisk pins the pure risk-gate decision table: which planned changes
// halt for confirmation (risky) and which auto-apply (safe). It uses an EMPTY
// last-deployed store, so any overwrite of an existing live file cannot be proven
// to match the baseline and must fail safe (risky).
func TestAssessRisk(t *testing.T) {
	store, err := dotfile.OpenStoreAtReadOnly(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	tgt := dotfile.Target{Name: "gitconfig"}

	cases := []struct {
		name         string
		st           dotfile.Status
		secretRouted bool
		wantRisky    bool
	}{
		{"secret-routed clean is risky", dotfile.Status{State: dotfile.StateClean}, true, true},
		{"secret-routed missing is risky", dotfile.Status{State: dotfile.StateMissing}, true, true},
		{"clean is safe", dotfile.Status{State: dotfile.StateClean}, false, false},
		{"missing (create where absent) is safe", dotfile.Status{State: dotfile.StateMissing}, false, false},
		{"locally-drifted is safe (apply skips it)", dotfile.Status{State: dotfile.StateLocallyDrifted}, false, false},
		{"conflict is risky", dotfile.Status{State: dotfile.StateConflict}, false, true},
		{
			"repo-ahead re-create (no live file) is safe",
			dotfile.Status{State: dotfile.StateRepoAhead, LiveExists: false, HasApplied: true},
			false, false,
		},
		{
			"repo-ahead first-touch adoption (live exists, never applied) is risky",
			dotfile.Status{State: dotfile.StateRepoAhead, LiveExists: true, HasApplied: false, LiveHash: "abc"},
			false, true,
		},
		{
			"repo-ahead overwrite with no provable baseline is risky",
			dotfile.Status{State: dotfile.StateRepoAhead, LiveExists: true, HasApplied: true, LiveHash: "abc", AppliedHash: "abc"},
			false, true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			risky, reason := assessRisk(tgt, tc.st, tc.secretRouted, store)
			if risky != tc.wantRisky {
				t.Errorf("assessRisk = %v (%q), want risky=%v", risky, reason, tc.wantRisky)
			}
			if risky && reason == "" {
				t.Errorf("a risky verdict must carry a human reason")
			}
		})
	}
}

// --- skip-always: a clean target is not "skipped", it is already in sync (A10) ---

// seedSkipAlways writes a repo-local skip-always file naming keys and returns the
// repo root, plus points $HOME at a throwaway dir (decideGuided opens the
// last-applied store there).
func seedSkipAlways(t *testing.T, keys ...string) *cmdContext {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "local"), 0o755); err != nil {
		t.Fatalf("make local layer: %v", err)
	}
	body := strings.Join(keys, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(repo, "local", "skip-always.txt"), []byte(body), 0o644); err != nil {
		t.Fatalf("seed skip-always: %v", err)
	}
	return &cmdContext{RepoPath: repo}
}

// TestDecideGuidedCleanSkipAlwaysTargetIsSilentAndCounted pins A10: a skip-always
// target that already matches the repo has nothing to skip. Announcing "skipped
// (skip-always…)" every run is noise, and leaving it out of cleanCount makes an
// otherwise in-sync machine report "0 target(s) already match".
func TestDecideGuidedCleanSkipAlwaysTargetIsSilentAndCounted(t *testing.T) {
	ctx := seedSkipAlways(t, "zshrc")
	plan := []planItem{{
		kind: kindFile, fileDomain: "dotfiles", domain: ".zshrc",
		target: dotfile.Target{Name: "zshrc", Home: filepath.Join(t.TempDir(), ".zshrc")},
		state:  dotfile.StateClean,
	}}

	var out bytes.Buffer
	dec, err := decideGuided(ctx, plan, false, guidedOpts{}, bufio.NewReader(strings.NewReader("")), &out)
	if err != nil {
		t.Fatalf("decideGuided: %v", err)
	}
	if strings.Contains(out.String(), "skip-always") {
		t.Errorf("a clean skip-always target must not announce a skip every run:\n%s", out.String())
	}
	if dec.cleanCount != 1 {
		t.Errorf("cleanCount = %d, want 1 (the clean skip-always target is in sync)", dec.cleanCount)
	}
	if !dec.nothingToDo {
		t.Errorf("nothing pending anywhere: decideGuided must short-circuit as in-sync")
	}
	if len(dec.toApply) != 0 {
		t.Errorf("a skip-always target must never be handed to mutate, got %d item(s)", len(dec.toApply))
	}
}

// TestDecideGuidedPendingSkipAlwaysTargetStillReportsTheSkip is the other half:
// when the target IS pending, the skip line is the honest report of work the user
// excluded — it must still print, and the item must still never be applied.
func TestDecideGuidedPendingSkipAlwaysTargetStillReportsTheSkip(t *testing.T) {
	ctx := seedSkipAlways(t, "zshrc")
	plan := []planItem{{
		kind: kindFile, fileDomain: "dotfiles", domain: ".zshrc",
		target: dotfile.Target{Name: "zshrc", Home: filepath.Join(t.TempDir(), ".zshrc")},
		state:  dotfile.StateRepoAhead,
	}}

	var out bytes.Buffer
	dec, err := decideGuided(ctx, plan, false, guidedOpts{}, bufio.NewReader(strings.NewReader("")), &out)
	if err != nil {
		t.Fatalf("decideGuided: %v", err)
	}
	if !strings.Contains(out.String(), "skip-always") {
		t.Errorf("a PENDING skip-always target must still be reported:\n%s", out.String())
	}
	if dec.cleanCount != 0 {
		t.Errorf("cleanCount = %d, want 0 (the target is pending, not in sync)", dec.cleanCount)
	}
	if len(dec.toApply) != 0 || len(dec.refused) != 0 {
		t.Errorf("a skip-always target is neither applied nor refused: toApply=%d refused=%d", len(dec.toApply), len(dec.refused))
	}
}

// --- walkthrough honesty: confirming a conflict does not overwrite it (B4) ---

// TestWalkRiskyConflictListingSaysYesWillNotOverwrite pins B4: the group prompt
// offers to "Apply all N change(s)", but a conflict item is reported and left
// unchanged even when confirmed (apply refuses without --force). The listing must
// say so, so the prompt cannot be read as a promise to overwrite.
func TestWalkRiskyConflictListingSaysYesWillNotOverwrite(t *testing.T) {
	store, err := dotfile.OpenStoreAtReadOnly(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	risky := []planItem{
		{
			kind: kindFile, fileDomain: "dotfiles", domain: ".zshrc",
			target: dotfile.Target{Name: "zshrc", Home: filepath.Join(t.TempDir(), ".zshrc")},
			state:  dotfile.StateConflict, risky: true,
			riskReason: "conflict: edited locally AND in the repo (run `ferry capture` first, or `ferry apply --force` to overwrite)",
		},
		{
			kind: kindFile, fileDomain: "dotfiles", domain: ".gitconfig",
			target: dotfile.Target{Name: "gitconfig", Home: filepath.Join(t.TempDir(), ".gitconfig")},
			state:  dotfile.StateRepoAhead, risky: true,
			riskReason: "would overwrite local changes (the live file differs from what ferry last deployed)",
		},
	}

	var out bytes.Buffer
	if _, _, _, err := walkRisky(bufio.NewReader(strings.NewReader("skip\n")), &out, risky, store); err != nil {
		t.Fatalf("walkRisky: %v", err)
	}
	got := out.String()
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, ".zshrc") && !strings.Contains(line, "will NOT overwrite") {
			t.Errorf("the conflict listing must say confirming will not overwrite it:\n%s", got)
		}
		if strings.Contains(line, ".gitconfig") && strings.Contains(line, "will NOT overwrite") {
			t.Errorf("a non-conflict risky item must NOT carry the no-overwrite suffix:\n%s", got)
		}
	}
}
