package cmd

import (
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
