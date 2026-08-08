package evals

// ~/.config/ferry/config.toml is replaced WHOLESALE on every write, so any writer
// that does not re-supply a field silently drops it. Two fields are not the
// writer's to drop: `managed` (set by `init --github`, and the gate `ferry sync`
// refuses without) and the `[work]` table (this machine's cargo store, which the
// reference documents as machine-scoped and never repo-side). Re-running the
// documented, eval-covered `ferry init` must not un-configure either.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// seedManagedAndWork rewrites the sandbox's config.toml with the two fields a
// route-2 machine with a configured cargo store carries, keeping the repo path
// init recorded.
func seedManagedAndWork(t *testing.T, s *Sandbox, store string) {
	t.Helper()
	data, err := os.ReadFile(s.ConfigTOMLPath())
	if err != nil {
		t.Fatalf("config.toml not written by init: %v", err)
	}
	body := strings.TrimRight(string(data), "\n") + "\nmanaged = true\n\n[work]\nstore = \"" + store + "\"\nkeep = 7\n"
	if err := os.WriteFile(s.ConfigTOMLPath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// MAJOR: a plain `ferry init` re-run on a configured machine keeps `managed` and
// the whole [work] table. Losing `managed` makes `ferry sync` refuse ("this repo
// is not marked managed"); losing [work] makes every `ferry work` verb refuse
// with setup guidance.
func TestInitRerunPreservesManagedAndWork(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: fresh init needs git")
	}
	s := NewSandbox(t)
	if _, errOut, code := s.FerryWithInput("", "init"); code != 0 {
		t.Fatalf("first init exited %d\n%s", code, errOut)
	}
	store := s.HomePath("cargo")
	seedManagedAndWork(t, s, store)

	if _, errOut, code := s.FerryWithInput("", "init"); code != 0 {
		t.Fatalf("re-run init exited %d\n%s", code, errOut)
	}

	after, err := os.ReadFile(s.ConfigTOMLPath())
	if err != nil {
		t.Fatalf("config.toml missing after re-run: %v", err)
	}
	got := string(after)
	if !strings.Contains(got, "managed = true") {
		t.Errorf("re-running `ferry init` dropped `managed`; `ferry sync` would now refuse:\n%s", got)
	}
	if !strings.Contains(got, "[work]") || !strings.Contains(got, store) {
		t.Errorf("re-running `ferry init` dropped the [work] cargo store; the work verbs would now refuse:\n%s", got)
	}
	if !strings.Contains(got, "keep = 7") {
		t.Errorf("re-running `ferry init` dropped the [work] retention:\n%s", got)
	}
}

// A fresh init on a machine that has a cargo store but NO configured repo still
// keeps [work]: the table is machine state, independent of which repo is set up.
func TestInitFreshPreservesWorkTable(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: fresh init needs git")
	}
	s := NewSandbox(t)
	if _, errOut, code := s.FerryWithInput("", "init"); code != 0 {
		t.Fatalf("first init exited %d\n%s", code, errOut)
	}
	store := s.HomePath("cargo")
	seedManagedAndWork(t, s, store)

	// `--fresh <dir>` establishes a DIFFERENT repo: `managed` must NOT carry over
	// (the new repo has no ferry-owned remote), but [work] must.
	fresh := s.HomePath("second-repo")
	if _, errOut, code := s.FerryWithInput("", "init", "--fresh", fresh); code != 0 {
		t.Fatalf("fresh init exited %d\n%s", code, errOut)
	}
	after, err := os.ReadFile(s.ConfigTOMLPath())
	if err != nil {
		t.Fatalf("config.toml missing: %v", err)
	}
	got := string(after)
	if !strings.Contains(got, "[work]") || !strings.Contains(got, store) {
		t.Errorf("`init --fresh` dropped the machine-scoped [work] cargo store:\n%s", got)
	}
	if strings.Contains(got, "managed = true") {
		t.Errorf("`init --fresh` carried `managed` onto a different repo with no ferry-owned remote:\n%s", got)
	}
}
