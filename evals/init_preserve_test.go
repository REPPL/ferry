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
	"path/filepath"
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

// MAJOR: `ferry init <path>` naming the SAME repo config.toml already records is a
// reuse of that repo, whatever route resolves it — `managed` describes the repo's
// ferry-owned remote, so re-declaring the identical repo must keep it. A positional
// path to a DIFFERENT repo must still drop it (no ferry-owned remote there).
func TestInitSameRepoPositionalPreservesManaged(t *testing.T) {
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

	repo := s.HomePath(".config/ferry/repo")
	if _, errOut, code := s.FerryWithInput("", "init", repo); code != 0 {
		t.Fatalf("positional same-repo init exited %d\n%s", code, errOut)
	}
	after, err := os.ReadFile(s.ConfigTOMLPath())
	if err != nil {
		t.Fatalf("config.toml missing after positional re-init: %v", err)
	}
	got := string(after)
	if !strings.Contains(got, "managed = true") {
		t.Errorf("`ferry init <same-repo-path>` dropped `managed`; `ferry sync` would now refuse:\n%s", got)
	}
	if !strings.Contains(got, "[work]") || !strings.Contains(got, store) {
		t.Errorf("`ferry init <same-repo-path>` dropped the [work] cargo store:\n%s", got)
	}

	// Direction guard: pointing init at a DIFFERENT existing repo is a re-target,
	// not a reuse — `managed` must not survive onto a repo ferry never pushed.
	other := s.HomePath("other-repo")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", other).CombinedOutput(); err != nil {
		t.Fatalf("git init other repo: %v\n%s", err, out)
	}
	manifest, err := os.ReadFile(filepath.Join(repo, "ferry.toml"))
	if err != nil {
		t.Fatalf("read first repo's manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(other, "ferry.toml"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := s.FerryWithInput("", "init", other); code != 0 {
		t.Fatalf("positional other-repo init exited %d\n%s", code, errOut)
	}
	after, err = os.ReadFile(s.ConfigTOMLPath())
	if err != nil {
		t.Fatalf("config.toml missing after re-target: %v", err)
	}
	if strings.Contains(string(after), "managed = true") {
		t.Errorf("`ferry init <different-repo>` carried `managed` onto a repo with no ferry-owned remote:\n%s", after)
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
