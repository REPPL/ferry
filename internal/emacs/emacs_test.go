package emacs

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// mkfile writes body to root/rel, creating parents.
func mkfile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func planKeys(items []Item) []string {
	keys := make([]string, len(items))
	for i, it := range items {
		keys[i] = it.Key
	}
	sort.Strings(keys)
	return keys
}

// TestExcluded pins the carry/exclude predicate: every volatile path the domain
// must never deploy is excluded, and the carry set is included.
func TestExcluded(t *testing.T) {
	excludedCases := []string{
		"elpa/foo.el",
		"elpa",
		"eln-cache/native.eln",
		"inits/repp.elc",
		"init.elc",
		"inits/repp.el", // the tangled output specifically
		"auto-save-list",
		"auto-save-list/.saves-123",
		"transient/history.el",
		"url/cookies",
		"network-security.data",
		"recentf",
		"savehist",
		"saveplace",
	}
	for _, rel := range excludedCases {
		if !excluded(rel) {
			t.Errorf("excluded(%q) = false, want true (volatile path must be pruned)", rel)
		}
	}
	carryCases := []string{
		"init.el",
		"early-init.el",
		"inits/repp.org", // the literate source is carried; only the tangled .el is not
		"docs/README.md",
		"README",
		"LICENSE",
		"inits/custom.el", // an overlay-friendly file, not itself excluded
	}
	for _, rel := range carryCases {
		if excluded(rel) {
			t.Errorf("excluded(%q) = true, want false (carry-set file must deploy)", rel)
		}
	}
}

// TestPlan_treeMapping proves the emacs/ tree fans out to per-file targets under
// ~/.emacs.d/, preserving the relpath: init.el maps to ~/.emacs.d/init.el and a
// nested inits/repp.org to ~/.emacs.d/inits/repp.org.
func TestPlan_treeMapping(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkfile(t, repo, "emacs/init.el", "shared-init")
	mkfile(t, repo, "emacs/early-init.el", "shared-early")
	mkfile(t, repo, "emacs/inits/repp.org", "shared-literate")

	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}

	got := planKeys(items)
	want := []string{"emacs/early-init.el", "emacs/init.el", "emacs/inits/repp.org"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}

	byKey := map[string]Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	if h := byKey["emacs/init.el"].Target.Home; h != filepath.Join(home, ".emacs.d/init.el") {
		t.Errorf("init.el home = %s, want ~/.emacs.d/init.el", h)
	}
	if h := byKey["emacs/inits/repp.org"].Target.Home; h != filepath.Join(home, ".emacs.d/inits/repp.org") {
		t.Errorf("repp.org home = %s, want ~/.emacs.d/inits/repp.org", h)
	}
	if string(byKey["emacs/init.el"].Content) != "shared-init" {
		t.Errorf("init.el content = %q", byKey["emacs/init.el"].Content)
	}
}

// TestPlan_excludesVolatilePaths proves the excluded volatile paths are pruned
// during the walk and never become items, while the carry set does.
func TestPlan_excludesVolatilePaths(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkfile(t, repo, "emacs/init.el", "keep")
	mkfile(t, repo, "emacs/inits/repp.org", "keep")
	mkfile(t, repo, "emacs/inits/repp.el", "tangled-drop")
	mkfile(t, repo, "emacs/init.elc", "bytecode-drop")
	mkfile(t, repo, "emacs/elpa/some-pkg/foo.el", "pkg-drop")
	mkfile(t, repo, "emacs/eln-cache/x.eln", "eln-drop")
	mkfile(t, repo, "emacs/recentf", "session-drop")
	mkfile(t, repo, "emacs/transient/history.el", "session-drop")

	items, _, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := planKeys(items)
	want := []string{"emacs/init.el", "emacs/inits/repp.org"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v (only the carry set deploys)", got, want)
	}
}

// TestPlan_localOverlayWins proves the per-machine overlay at
// local/emacs/<relpath> overrides the shared emacs/<relpath> for just that file,
// while a non-overridden file still deploys the shared content.
func TestPlan_localOverlayWins(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkfile(t, repo, "emacs/init.el", "shared-init")
	mkfile(t, repo, "emacs/inits/custom.el", "shared-custom")
	// Per-machine override of just custom.el.
	mkfile(t, repo, "local/emacs/inits/custom.el", "MACHINE-custom")

	items, _, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	byKey := map[string]Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	if got := string(byKey["emacs/inits/custom.el"].Content); got != "MACHINE-custom" {
		t.Errorf("custom.el content = %q, want the local overlay to win", got)
	}
	if got := string(byKey["emacs/init.el"].Content); got != "shared-init" {
		t.Errorf("non-overridden init.el = %q, want shared", got)
	}
}

// TestPlan_localOnlyFileDeploysAsMachineOnly proves a file present ONLY under
// local/emacs/ (no shared counterpart) is enumerated by the union walk and
// deploys as a machine-only file to ~/.emacs.d/<rel>, alongside the shared tree.
func TestPlan_localOnlyFileDeploysAsMachineOnly(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkfile(t, repo, "emacs/init.el", "shared-init")
	// A machine-only file with NO shared counterpart.
	mkfile(t, repo, "local/emacs/init.local.el", "MACHINE-only")

	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	got := planKeys(items)
	want := []string{"emacs/init.el", "emacs/init.local.el"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v (local-only file must deploy)", got, want)
	}
	byKey := map[string]Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	if h := byKey["emacs/init.local.el"].Target.Home; h != filepath.Join(home, ".emacs.d/init.local.el") {
		t.Errorf("init.local.el home = %s, want ~/.emacs.d/init.local.el", h)
	}
	if got := string(byKey["emacs/init.local.el"].Content); got != "MACHINE-only" {
		t.Errorf("init.local.el content = %q, want the local-only bytes", got)
	}
}

// TestPlan_excludesApplyToLocalOnlyFiles proves the volatile-path exclude filter
// runs over the local overlay tree too: a machine-only volatile path under
// local/emacs/ is pruned exactly like one under emacs/.
func TestPlan_excludesApplyToLocalOnlyFiles(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkfile(t, repo, "emacs/init.el", "keep")
	// Machine-only carry file plus machine-only volatile decoys.
	mkfile(t, repo, "local/emacs/init.local.el", "keep-local")
	mkfile(t, repo, "local/emacs/init.local.elc", "bytecode-drop")
	mkfile(t, repo, "local/emacs/elpa/pkg/foo.el", "pkg-drop")
	mkfile(t, repo, "local/emacs/recentf", "session-drop")

	items, _, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := planKeys(items)
	want := []string{"emacs/init.el", "emacs/init.local.el"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v (local-only volatile paths must be pruned)", got, want)
	}
}

// TestPlan_refusesLocalOnlySymlink proves a symlinked file present ONLY in the
// local overlay tree is refused with a warning and skipped, so ferry never reads
// a machine-only config through a symlink.
func TestPlan_refusesLocalOnlySymlink(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkfile(t, repo, "emacs/init.el", "real")
	// Real overlay dir so the tree exists, plus a symlinked file inside it.
	mkfile(t, repo, "local/emacs/keep.el", "real-local")
	linkPath := filepath.Join(repo, "local", "emacs", "init.local.el")
	if err := os.Symlink(filepath.Join(repo, "emacs", "init.el"), linkPath); err != nil {
		t.Fatal(err)
	}

	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := planKeys(items); !reflect.DeepEqual(got, []string{"emacs/init.el", "emacs/keep.el"}) {
		t.Errorf("keys = %v, want the two real files (local symlink refused)", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one symlink refusal", warnings)
	}
	if !strings.Contains(warnings[0], "symlink not allowed") ||
		!strings.Contains(warnings[0], "local/emacs/init.local.el") {
		t.Errorf("warning = %q, want a local overlay symlink refusal", warnings[0])
	}
}

// TestPlan_absentSourceDeploysNothing proves an absent emacs/ tree deploys
// nothing without warning.
func TestPlan_absentSourceDeploysNothing(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 0 || len(warnings) != 0 {
		t.Errorf("items=%v warnings=%v, want both empty for a repo with no emacs/ tree", items, warnings)
	}
}

// TestPlan_refusesSymlinkInTree proves a symlinked file inside the managed tree
// is refused with a warning and skipped (a symlinked directory prunes its
// subtree), so ferry never reads a config through a symlink.
func TestPlan_refusesSymlinkInTree(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	mkfile(t, repo, "emacs/init.el", "real")
	// A symlinked file inside the tree.
	linkPath := filepath.Join(repo, "emacs", "linked.el")
	if err := os.Symlink(filepath.Join(repo, "emacs", "init.el"), linkPath); err != nil {
		t.Fatal(err)
	}
	// A symlinked directory inside the tree.
	linkDir := filepath.Join(repo, "emacs", "linkdir")
	if err := os.Symlink(filepath.Join(repo, "emacs"), linkDir); err != nil {
		t.Fatal(err)
	}

	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Only the real init.el deploys; both symlinks are refused.
	if got := planKeys(items); !reflect.DeepEqual(got, []string{"emacs/init.el"}) {
		t.Errorf("keys = %v, want only emacs/init.el (symlinks refused)", got)
	}
	if len(warnings) != 2 {
		t.Errorf("warnings = %v, want two symlink refusals (file + dir)", warnings)
	}
}
