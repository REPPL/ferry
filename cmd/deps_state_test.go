package cmd

// Unit coverage for the CUMULATIVE deps-installed-set persistence that backs
// `restore --packages`. persistInstalledSet unions each run's newly-installed
// packages into ~/.local/state/ferry/deps-installed.txt, so two `apply --deps`
// runs that each install a different package leave BOTH recorded — restore then
// uninstalls everything ferry ever installed, not just the last run's set.
//
// This is the RIGHT layer for the "cumulative across two apply --deps runs"
// concern: the union is pure state logic (no package manager needed), so a
// deterministic unit test asserting the persisted set is stronger and more
// honest than driving a stub `brew list` diff end-to-end (whose recorded set is
// empty unless the stub simulates a package appearing — the Layer-2-deferred leg).
//
// Traceability: AC-deps-install-attempted (recorded self-installed set) +
// AC-cmd-restore (`--packages` uninstalls only ferry-installed packages).

import (
	"sort"
	"testing"
)

// TestPersistInstalledSetCumulative asserts the recorded set is the UNION across
// runs, deduped and sorted — not overwritten each run.
func TestPersistInstalledSetCumulative(t *testing.T) {
	// t.Setenv forces non-parallel; HOME points the state dir at a throwaway dir.
	t.Setenv("HOME", t.TempDir())

	// Run 1 installs depA.
	if err := persistInstalledSet([]string{"depA"}); err != nil {
		t.Fatalf("persistInstalledSet run1: %v", err)
	}
	// Run 2 installs depB (a DIFFERENT package). The record must UNION, not replace.
	if err := persistInstalledSet([]string{"depB"}); err != nil {
		t.Fatalf("persistInstalledSet run2: %v", err)
	}

	got, err := readInstalledSet()
	if err != nil {
		t.Fatalf("readInstalledSet: %v", err)
	}
	sort.Strings(got)
	want := []string{"depA", "depB"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("cumulative set = %v, want %v (the record was overwritten, not accumulated)", got, want)
	}
}

// TestPersistInstalledSetDedupes asserts an idempotent re-record of the same
// package does not duplicate it, and an empty run preserves the prior set (so a
// later idempotent `apply --deps` that installs nothing new never erases records).
func TestPersistInstalledSetDedupes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := persistInstalledSet([]string{"depA", "depB"}); err != nil {
		t.Fatalf("persistInstalledSet run1: %v", err)
	}
	// Re-record depA (duplicate) plus a NEW depC.
	if err := persistInstalledSet([]string{"depA", "depC"}); err != nil {
		t.Fatalf("persistInstalledSet run2: %v", err)
	}
	// An empty run (idempotent apply --deps installing nothing new) preserves all.
	if err := persistInstalledSet(nil); err != nil {
		t.Fatalf("persistInstalledSet run3 (empty): %v", err)
	}

	got, err := readInstalledSet()
	if err != nil {
		t.Fatalf("readInstalledSet: %v", err)
	}
	sort.Strings(got)
	want := []string{"depA", "depB", "depC"}
	if len(got) != len(want) {
		t.Fatalf("deduped set = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("deduped set = %v, want %v", got, want)
		}
	}
}

// TestClearInstalledSetAfterRestore asserts that clearing the record (what
// restore --packages does once it has uninstalled) empties the set, so a later
// restore does not re-uninstall already-removed packages.
func TestClearInstalledSetAfterRestore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := persistInstalledSet([]string{"depA", "depB"}); err != nil {
		t.Fatalf("persistInstalledSet: %v", err)
	}
	if err := clearInstalledSet(); err != nil {
		t.Fatalf("clearInstalledSet: %v", err)
	}
	got, err := readInstalledSet()
	if err != nil {
		t.Fatalf("readInstalledSet after clear: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("set after clear = %v, want empty", got)
	}
	// Clearing an already-absent record is a no-op, not an error.
	if err := clearInstalledSet(); err != nil {
		t.Fatalf("clearInstalledSet (absent) should be a no-op: %v", err)
	}
}
