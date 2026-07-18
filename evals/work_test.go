package evals

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// workFixture is one account's side of a work handover: a sandbox whose HOME
// holds a git project with a handover note, configured against a shared
// cargo store.
type workFixture struct {
	sb      *Sandbox
	project string
}

// gitProject runs git in dir with the eval-isolated environment.
func gitProject(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitIsolatedEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newWorkFixture builds an account: sandbox HOME, a committed project repo
// under it with .abcd/.work.local/NEXT.md, and a config.toml naming the
// shared store.
func newWorkFixture(t *testing.T, store, hostname string) *workFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	sb := NewSandbox(t)
	project := filepath.Join(sb.Home, "src", "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	gitProject(t, project, "init", "-q")
	sb.WriteHomeFile(t, "src/proj/main.go", "package main\n", 0o644)
	gitProject(t, project, "add", ".")
	gitProject(t, project, "commit", "-q", "-m", "root")
	sb.WriteHomeFile(t, "src/proj/.abcd/.work.local/NEXT.md", "# NEXT\neval handoff\n", 0o644)
	writeWorkConfig(t, sb, store, hostname)
	return &workFixture{sb: sb, project: project}
}

func writeWorkConfig(t *testing.T, sb *Sandbox, store, hostname string) {
	t.Helper()
	cfg := "hostname = \"" + hostname + "\"\nrepo = \"" + sb.Repo + "\"\n\n[work]\nstore = \"" + store + "\"\n"
	if err := os.WriteFile(sb.ConfigTOMLPath(), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// cloneInto clones src's project into a second account's HOME.
func (f *workFixture) cloneInto(t *testing.T, store, hostname string) *workFixture {
	t.Helper()
	sb := NewSandbox(t)
	project := filepath.Join(sb.Home, "src", "proj")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	gitProject(t, filepath.Dir(project), "clone", "-q", "file://"+f.project, project)
	writeWorkConfig(t, sb, store, hostname)
	return &workFixture{sb: sb, project: project}
}

func TestWorkRoundTrip_AC_work_pack_receive_roundtrip(t *testing.T) {
	requireBin(t)
	store := t.TempDir()
	alice := newWorkFixture(t, store, "alicebox")
	alice.sb.SSHTripwire(t)

	stdout, stderr, code := alice.sb.Ferry("work", "pack", alice.project)
	if code != 0 {
		t.Fatalf("pack: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if _, ok := containsAllFold(stdout, "packed bundle", "handover marker"); !ok {
		t.Errorf("pack output missing expectations:\n%s", stdout)
	}

	bob := alice.cloneInto(t, store, "bobbox")
	stdout, stderr, code = bob.sb.Ferry("work", "receive", bob.project)
	if code != 0 {
		t.Fatalf("receive: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	note, err := os.ReadFile(filepath.Join(bob.project, ".abcd", ".work.local", "NEXT.md"))
	if err != nil || string(note) != "# NEXT\neval handoff\n" {
		t.Errorf("received NEXT.md = %q, %v", note, err)
	}

	// The status verbs on both sides see a consistent picture.
	stdout, _, code = bob.sb.Ferry("work", "status", bob.project)
	if code != 0 || !strings.Contains(stdout, "cargo") {
		t.Errorf("bob status: exit %d\n%s", code, stdout)
	}
	alice.sb.AssertSSHUntouched(t)
}

func TestWorkRestore_AC_work_restore_reverts_receive(t *testing.T) {
	requireBin(t)
	store := t.TempDir()
	alice := newWorkFixture(t, store, "alicebox")
	if _, se, code := alice.sb.Ferry("work", "pack", alice.project); code != 0 {
		t.Fatalf("pack failed: %s", se)
	}
	bob := alice.cloneInto(t, store, "bobbox")
	if _, se, code := bob.sb.Ferry("work", "receive", bob.project); code != 0 {
		t.Fatalf("receive failed: %s", se)
	}
	notePath := filepath.Join(bob.project, ".abcd", ".work.local", "NEXT.md")
	if _, err := os.Stat(notePath); err != nil {
		t.Fatal("receive did not land the note")
	}

	stdout, stderr, code := bob.sb.Ferry("work", "restore", bob.project)
	if code != 0 {
		t.Fatalf("work restore: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if _, err := os.Stat(notePath); err == nil {
		t.Error("NEXT.md survived work restore, want reverted (absent)")
	}
}

func TestWorkGuards_AC_work_divergence_refused_force_overrides(t *testing.T) {
	requireBin(t)
	store := t.TempDir()
	alice := newWorkFixture(t, store, "alicebox")
	if _, se, code := alice.sb.Ferry("work", "pack", alice.project); code != 0 {
		t.Fatalf("pack failed: %s", se)
	}
	bob := alice.cloneInto(t, store, "bobbox")
	if _, se, code := bob.sb.Ferry("work", "receive", bob.project); code != 0 {
		t.Fatalf("receive failed: %s", se)
	}

	// Bob edits; alice packs v2; bob's receive refuses, then --force lands it.
	bob.sb.WriteHomeFile(t, "src/proj/.abcd/.work.local/NEXT.md", "bob's edits\n", 0o644)
	alice.sb.WriteHomeFile(t, "src/proj/.abcd/.work.local/NEXT.md", "# NEXT\nv2\n", 0o644)
	if _, se, code := alice.sb.Ferry("work", "pack", alice.project); code != 0 {
		t.Fatalf("pack v2 failed: %s", se)
	}

	_, stderr, code := bob.sb.Ferry("work", "receive", bob.project)
	if code == 0 {
		t.Fatal("diverged receive succeeded, want refusal")
	}
	if _, ok := containsAllFold(stderr, "refusing to overwrite", "--force"); !ok {
		t.Errorf("refusal message unhelpful:\n%s", stderr)
	}
	if _, se, code := bob.sb.Ferry("work", "receive", "--force", bob.project); code != 0 {
		t.Fatalf("forced receive failed: %s", se)
	}
	note, _ := os.ReadFile(filepath.Join(bob.project, ".abcd", ".work.local", "NEXT.md"))
	if string(note) != "# NEXT\nv2\n" {
		t.Errorf("after force, NEXT.md = %q", note)
	}
}

func TestWorkSecretGate_AC_work_pack_secret_abort_and_escapes(t *testing.T) {
	requireBin(t)
	store := t.TempDir()
	alice := newWorkFixture(t, store, "alicebox")
	secretLine := "api_key = \"sk-ferrytest-FAKE1234567890abcdefghijklmnopqrstuv\"\n"
	alice.sb.WriteHomeFile(t, "src/proj/.abcd/.work.local/NEXT.md", secretLine, 0o644)

	// Abort, and nothing lands in the store.
	_, stderr, code := alice.sb.Ferry("work", "pack", alice.project)
	if code == 0 {
		t.Fatal("pack with a secret succeeded, want abort")
	}
	if _, ok := containsAllFold(stderr, "secret gate", "nothing was written"); !ok {
		t.Errorf("abort message unhelpful:\n%s", stderr)
	}
	entries, _ := os.ReadDir(store)
	for _, e := range entries {
		files, _ := os.ReadDir(filepath.Join(store, e.Name()))
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".ferrywork") {
				t.Errorf("aborted pack left bundle %s in the store", f.Name())
			}
		}
	}

	// Escape hatch 1: acknowledge, pinned to content.
	stdout, stderr, code := alice.sb.Ferry("work", "pack", "--acknowledge", "next/NEXT.md", alice.project)
	if code != 0 {
		t.Fatalf("acknowledged pack: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if _, ok := containsAllFold(stdout, "acknowledged"); !ok {
		t.Errorf("acknowledged pack output:\n%s", stdout)
	}

	// Escape hatch 2: exclude the item entirely (needs --allow-empty since
	// the note is the required item, and something else must remain to pack).
	alice.sb.WriteHomeFile(t, "src/proj/.abcd/.work.local/run-journal.json", `{"runs":[]}`, 0o644)
	stdout, stderr, code = alice.sb.Ferry("work", "pack", "--exclude", "next", "--allow-empty", alice.project)
	if code != 0 {
		t.Fatalf("excluded pack: exit %d\nstderr: %s", code, stderr)
	}
	if _, ok := containsAllFold(stdout, "not packed (excluded)"); !ok {
		t.Errorf("exclusion not surfaced:\n%s", stdout)
	}
}

func TestWorkIdentityGuards_AC_work_shallow_and_worktree_refused(t *testing.T) {
	requireBin(t)
	store := t.TempDir()
	alice := newWorkFixture(t, store, "alicebox")

	// A second commit so a depth-1 clone truncates.
	alice.sb.WriteHomeFile(t, "src/proj/main.go", "package main // v2\n", 0o644)
	gitProject(t, alice.project, "commit", "-aqm", "second")

	shallow := filepath.Join(alice.sb.Home, "src", "shallow")
	gitProject(t, filepath.Dir(shallow), "clone", "-q", "--depth", "1", "file://"+alice.project, shallow)
	_, stderr, code := alice.sb.Ferry("work", "pack", shallow)
	if code == 0 || !strings.Contains(strings.ToLower(stderr), "shallow") {
		t.Errorf("shallow pack: exit %d, stderr:\n%s", code, stderr)
	}

	wt := filepath.Join(alice.sb.Home, "src", "wt")
	gitProject(t, alice.project, "worktree", "add", "-q", wt)
	_, stderr, code = alice.sb.Ferry("work", "pack", wt)
	if code == 0 || !strings.Contains(strings.ToLower(stderr), "worktree") {
		t.Errorf("worktree pack: exit %d, stderr:\n%s", code, stderr)
	}
}

func TestWorkTakeBack_AC_work_takeback_and_superseded(t *testing.T) {
	requireBin(t)
	store := t.TempDir()
	alice := newWorkFixture(t, store, "alicebox")
	if _, se, code := alice.sb.Ferry("work", "pack", alice.project); code != 0 {
		t.Fatalf("pack failed: %s", se)
	}
	bob := alice.cloneInto(t, store, "bobbox")

	// Alice reclaims her own baton: nothing restored, marker cleared.
	alice.sb.WriteHomeFile(t, "src/proj/.abcd/.work.local/NEXT.md", "alice kept working\n", 0o644)
	stdout, stderr, code := alice.sb.Ferry("work", "receive", alice.project)
	if code != 0 {
		t.Fatalf("take-back: exit %d\nstderr: %s", code, stderr)
	}
	if _, ok := containsAllFold(stdout, "taken back", "nothing was restored"); !ok {
		t.Errorf("take-back output:\n%s", stdout)
	}
	note, _ := os.ReadFile(filepath.Join(alice.project, ".abcd", ".work.local", "NEXT.md"))
	if string(note) != "alice kept working\n" {
		t.Errorf("take-back changed local work: %q", note)
	}

	// Bob's receive of the taken-back bundle refuses without --force.
	_, stderr, code = bob.sb.Ferry("work", "receive", bob.project)
	if code == 0 || !strings.Contains(strings.ToLower(stderr), "taken back") {
		t.Errorf("superseded receive: exit %d, stderr:\n%s", code, stderr)
	}
}

func TestWorkStoreGuard_AC_work_store_in_worktree_refused(t *testing.T) {
	requireBin(t)
	alice := newWorkFixture(t, t.TempDir(), "alicebox")
	// Point the store INSIDE the project worktree: refused.
	inRepo := filepath.Join(alice.project, "cargo")
	if err := os.MkdirAll(inRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkConfig(t, alice.sb, inRepo, "alicebox")
	_, stderr, code := alice.sb.Ferry("work", "pack", alice.project)
	if code == 0 || !strings.Contains(strings.ToLower(stderr), "worktree") {
		t.Errorf("in-worktree store: exit %d, stderr:\n%s", code, stderr)
	}
}
