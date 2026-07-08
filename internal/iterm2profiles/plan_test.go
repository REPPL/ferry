package iterm2profiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLinter records the paths it was asked to lint and can be told to fail a
// specific base name, so the plutil-lint gate is exercised without shelling out.
type fakeLinter struct {
	failBase string
	linted   []string
}

func (f *fakeLinter) Lint(path string) error {
	f.linted = append(f.linted, path)
	if f.failBase != "" && filepath.Base(path) == f.failBase {
		return errFakeLint
	}
	return nil
}

var errFakeLint = &lintErr{"fake plutil failure"}

type lintErr struct{ msg string }

func (e *lintErr) Error() string { return e.msg }

const workProfile = `{"Profiles":[{"Name":"Work","Guid":"WORK-GUID-1234","Rewritable":false}]}`

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPlanCarriesValidProfilePreservingGUID(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	writeFile(t, filepath.Join(repo, RepoSubdir, "Work.json"), workProfile)

	items, warns, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	if it.Key != KeyPrefix+"Work.json" {
		t.Errorf("key = %q", it.Key)
	}
	if !strings.HasSuffix(it.Target.Home, filepath.Join(TargetHome, "Work.json")) {
		t.Errorf("target home = %q, want under %s", it.Target.Home, TargetHome)
	}
	// GUID and the whole body are byte-preserved (ferry never rewrites the JSON).
	if string(it.Content) != workProfile {
		t.Errorf("content mutated:\n got: %s\nwant: %s", it.Content, workProfile)
	}
}

func TestPlanSkipsMalformedJSON(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	writeFile(t, filepath.Join(repo, RepoSubdir, "Good.json"), workProfile)
	writeFile(t, filepath.Join(repo, RepoSubdir, "Bad.json"), `{ this is not json`)

	items, warns, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Key != KeyPrefix+"Good.json" {
		t.Fatalf("expected only Good.json to be carried, got %+v", items)
	}
	if !containsSubstr(warns, "Bad.json") || !containsSubstr(warns, "not valid JSON") {
		t.Errorf("expected a refusal warning for Bad.json, got %v", warns)
	}
}

func TestPlanLocalOverlayWinsAndAddsMachineOnly(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	writeFile(t, filepath.Join(repo, RepoSubdir, "Work.json"), workProfile)
	// Override Work.json for this machine.
	local := `{"Profiles":[{"Name":"Work","Guid":"WORK-GUID-1234","Rewritable":false,"Normal Font":"Menlo 14"}]}`
	writeFile(t, filepath.Join(repo, LocalSubdir, LocalName, "Work.json"), local)
	// A machine-only child profile present ONLY in the local overlay.
	child := `{"Profiles":[{"Name":"Work-laptop","Guid":"CHILD-1","Dynamic Profile Parent GUID":"WORK-GUID-1234"}]}`
	writeFile(t, filepath.Join(repo, LocalSubdir, LocalName, "Work-laptop.json"), child)

	items, warns, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil || len(warns) != 0 {
		t.Fatalf("Plan err=%v warns=%v", err, warns)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Key] = string(it.Content)
	}
	if got[KeyPrefix+"Work.json"] != local {
		t.Errorf("local overlay did not win for Work.json: %q", got[KeyPrefix+"Work.json"])
	}
	if got[KeyPrefix+"Work-laptop.json"] != child {
		t.Errorf("machine-only child profile not deployed: %q", got[KeyPrefix+"Work-laptop.json"])
	}
}

func TestPlanPlutilLintGates(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	// Valid JSON but plutil rejects it (simulated).
	writeFile(t, filepath.Join(repo, RepoSubdir, "Work.json"), workProfile)
	fl := &fakeLinter{failBase: "Work.json"}

	items, warns, err := Plan(PlanInput{RepoRoot: repo, Home: home, Linter: fl})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("a plutil-rejected file was carried: %+v", items)
	}
	if !containsSubstr(warns, "lint failed") {
		t.Errorf("expected a lint-failure warning, got %v", warns)
	}
	if len(fl.linted) == 0 {
		t.Errorf("linter was never invoked")
	}
}

func TestPlanRefusesSymlinkedFile(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	dir := filepath.Join(repo, RepoSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repo, "outside.json")
	if err := os.WriteFile(target, []byte(workProfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "Work.json")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	items, warns, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("a symlinked profile was carried: %+v", items)
	}
	if !containsSubstr(warns, "symlink not allowed") {
		t.Errorf("expected a symlink refusal, got %v", warns)
	}
}

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
