package dotfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeBackuper is a stand-in for the real backup engine. It records every
// backup (so tests can assert the prior content was preserved) and then writes
// the new content atomically (temp + rename), exactly like the real engine's
// contract. Wave 2 swaps in *backup.Engine.
type fakeBackuper struct {
	backups map[string][]byte // target -> prior content ("" sentinel handled via absent map entry)
	absent  map[string]bool   // target -> prior state was "did not exist"
}

func newFakeBackuper() *fakeBackuper {
	return &fakeBackuper{backups: map[string][]byte{}, absent: map[string]bool{}}
}

func (f *fakeBackuper) BackupAndWrite(target string, content []byte, perm os.FileMode) error {
	if prior, err := os.ReadFile(target); err == nil {
		f.backups[target] = prior
	} else if errors.Is(err, os.ErrNotExist) {
		f.absent[target] = true
	} else {
		return err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, content, perm); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// harness wires a fake repo, fake home, and fake state dir under t.TempDir().
type harness struct {
	t        *testing.T
	repoRoot string
	home     string
	store    *Store
	b        *fakeBackuper
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	repoRoot := t.TempDir()
	home := t.TempDir()
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, RepoSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStoreAt(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, repoRoot: repoRoot, home: home, store: store, b: newFakeBackuper()}
}

func (h *harness) target(name string) Target { return TargetFor(h.repoRoot, h.home, name) }

func (h *harness) writeRepo(name, content string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.repoRoot, RepoSubdir, name), []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) writeHome(name, content string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.home, "."+name), []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) readHome(name string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.home, "."+name))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(data)
}

// --- copy-not-symlink ---

func TestApplyCopiesNotSymlinks(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "export A=1\n")
	tgt := h.target("zshrc")

	res, err := Apply(tgt, h.store, h.b, false, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Action != ActionCreated {
		t.Fatalf("action = %q, want created", res.Action)
	}

	fi, err := os.Lstat(tgt.Home)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("home target is a symlink; must be a regular copy")
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("home target is not a regular file: %s", fi.Mode())
	}
	if got := h.readHome("zshrc"); got != "export A=1\n" {
		t.Fatalf("home content = %q", got)
	}

	// Distinct inode from the repo source: editing one must not touch the other.
	repoFI, err := os.Stat(tgt.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(fi, repoFI) {
		t.Fatal("home target and repo source are the same file (hardlink/symlink); must be distinct")
	}

	// Editing the live file must NOT change the repo source (the safety point).
	h.writeHome("zshrc", "export A=2\n")
	if data, _ := os.ReadFile(tgt.Repo); string(data) != "export A=1\n" {
		t.Fatalf("editing live file changed the repo source: %q", string(data))
	}
}

// --- update when live == last-applied and repo changed ---

func TestApplyUpdatesWhenLiveMatchesLastApplied(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "v1\n")
	tgt := h.target("zshrc")

	if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
		t.Fatal(err)
	}
	// Repo moves ahead; live is untouched (== last-applied).
	h.writeRepo("zshrc", "v2\n")

	res, err := Apply(tgt, h.store, h.b, false, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.State != StateRepoAhead || res.Action != ActionUpdated {
		t.Fatalf("state=%q action=%q, want repo-ahead/updated", res.State, res.Action)
	}
	if got := h.readHome("zshrc"); got != "v2\n" {
		t.Fatalf("home = %q, want v2", got)
	}
	// The prior content was backed up.
	if string(h.b.backups[tgt.Home]) != "v1\n" {
		t.Fatalf("backup = %q, want v1", h.b.backups[tgt.Home])
	}
}

// --- CONFLICT when live edited (and repo also moved) ---

func TestApplyConflictWhenLocallyEditedAndRepoMoved(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "v1\n")
	tgt := h.target("zshrc")
	if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
		t.Fatal(err)
	}
	// User edits live (uncaptured) AND the repo also moves ahead.
	h.writeHome("zshrc", "local-edit\n")
	h.writeRepo("zshrc", "v2\n")

	res, err := Apply(tgt, h.store, h.b, false, false)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *ConflictError", err)
	}
	if res.Action != ActionConflict || res.State != StateConflict {
		t.Fatalf("state=%q action=%q, want conflict/conflict", res.State, res.Action)
	}
	// Nothing written: live untouched.
	if got := h.readHome("zshrc"); got != "local-edit\n" {
		t.Fatalf("conflict overwrote live file: %q", got)
	}
}

// --- locally-drifted (repo unchanged) is a capture candidate, not a conflict ---

func TestApplySkipsLocallyDrifted(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "v1\n")
	tgt := h.target("zshrc")
	if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
		t.Fatal(err)
	}
	h.writeHome("zshrc", "local-edit\n") // repo still v1

	res, err := Apply(tgt, h.store, h.b, false, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.State != StateLocallyDrifted || res.Action != ActionSkipped {
		t.Fatalf("state=%q action=%q, want locally-drifted/skipped", res.State, res.Action)
	}
	if got := h.readHome("zshrc"); got != "local-edit\n" {
		t.Fatalf("apply touched a drifted file: %q", got)
	}
}

// --- no-op when live == repo ---

func TestApplyNoopWhenLiveMatchesRepo(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "same\n")
	tgt := h.target("zshrc")
	if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(tgt, h.store, h.b, false, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.State != StateClean || res.Action != ActionNoop {
		t.Fatalf("state=%q action=%q, want clean/noop", res.State, res.Action)
	}
}

// --- --force overwrites a conflict ---

func TestApplyForceOverwritesConflict(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "v1\n")
	tgt := h.target("zshrc")
	if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
		t.Fatal(err)
	}
	h.writeHome("zshrc", "local-edit\n")
	h.writeRepo("zshrc", "v2\n")

	res, err := Apply(tgt, h.store, h.b, true /*force*/, false)
	if err != nil {
		t.Fatalf("force apply: %v", err)
	}
	if res.Action != ActionUpdated {
		t.Fatalf("action=%q, want updated", res.Action)
	}
	if got := h.readHome("zshrc"); got != "v2\n" {
		t.Fatalf("home = %q, want v2", got)
	}
	// The local edit was backed up before being discarded.
	if string(h.b.backups[tgt.Home]) != "local-edit\n" {
		t.Fatalf("force did not back up the discarded edit: %q", h.b.backups[tgt.Home])
	}
}

// --- unmanaged-target refusal ---

func TestApplyRefusesUnmanagedTarget(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("gitconfig", "[user]\n")
	tgt := h.target("gitconfig")
	// A pre-existing home file ferry never applied (no last-applied record).
	h.writeHome("gitconfig", "[user]\n\tname = pre-existing\n")

	res, err := Apply(tgt, h.store, h.b, false, false)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *ConflictError for unmanaged target", err)
	}
	if res.State != StateConflict {
		t.Fatalf("state=%q, want conflict", res.State)
	}
	if got := h.readHome("gitconfig"); got != "[user]\n\tname = pre-existing\n" {
		t.Fatalf("refused apply overwrote unmanaged file: %q", got)
	}

	// --force adopts it.
	if _, err := Apply(tgt, h.store, h.b, true, false); err != nil {
		t.Fatalf("force apply: %v", err)
	}
	if got := h.readHome("gitconfig"); got != "[user]\n" {
		t.Fatalf("force apply home = %q", got)
	}
}

// An identical pre-existing file is adopted as clean, not flagged.
func TestApplyAdoptsIdenticalUnmanagedFile(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("gitconfig", "[user]\n")
	tgt := h.target("gitconfig")
	h.writeHome("gitconfig", "[user]\n") // identical, no last-applied

	res, err := Apply(tgt, h.store, h.b, false, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.State != StateClean || res.Action != ActionNoop {
		t.Fatalf("state=%q action=%q, want clean/noop", res.State, res.Action)
	}
}

// --- dry-run writes nothing ---

func TestApplyDryRunWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "v1\n")
	tgt := h.target("zshrc")

	res, err := Apply(tgt, h.store, h.b, false, true /*dryRun*/)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Action != ActionCreated {
		t.Fatalf("action=%q, want created (preview)", res.Action)
	}
	if _, err := os.Stat(tgt.Home); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run created the home file")
	}
	if _, ok := h.store.LastApplied("zshrc"); ok {
		t.Fatal("dry-run recorded last-applied")
	}
}

// --- status classification for each state ---

func TestClassifyAllStates(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness, tgt Target)
		want  State
	}{
		{"missing", func(h *harness, tgt Target) {
			h.writeRepo(tgt.Name, "v1\n")
		}, StateMissing},
		{"clean", func(h *harness, tgt Target) {
			h.writeRepo(tgt.Name, "v1\n")
			if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
				h.t.Fatal(err)
			}
		}, StateClean},
		{"repo-ahead", func(h *harness, tgt Target) {
			h.writeRepo(tgt.Name, "v1\n")
			if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
				h.t.Fatal(err)
			}
			h.writeRepo(tgt.Name, "v2\n")
		}, StateRepoAhead},
		{"locally-drifted", func(h *harness, tgt Target) {
			h.writeRepo(tgt.Name, "v1\n")
			if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
				h.t.Fatal(err)
			}
			h.writeHome(tgt.Name, "edit\n")
		}, StateLocallyDrifted},
		{"conflict", func(h *harness, tgt Target) {
			h.writeRepo(tgt.Name, "v1\n")
			if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
				h.t.Fatal(err)
			}
			h.writeHome(tgt.Name, "edit\n")
			h.writeRepo(tgt.Name, "v2\n")
		}, StateConflict},
		{"conflict-unmanaged", func(h *harness, tgt Target) {
			h.writeRepo(tgt.Name, "v1\n")
			h.writeHome(tgt.Name, "pre-existing\n")
		}, StateConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			tgt := h.target("zshrc")
			c.setup(h, tgt)
			st, err := Classify(tgt, h.store)
			if err != nil {
				t.Fatal(err)
			}
			if st.State != c.want {
				t.Fatalf("state = %q, want %q", st.State, c.want)
			}
		})
	}
}

// --- last-applied updates only on full reproduction ---

func TestUpdateLastAppliedFullReproduction(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "v1\n")
	tgt := h.target("zshrc")
	if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
		t.Fatal(err)
	}

	// Simulate a FULL capture: repo now reproduces the live file exactly.
	h.writeHome("zshrc", "captured\n")
	h.writeRepo("zshrc", "captured\n")
	updated, err := UpdateLastApplied(tgt, h.store)
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("full capture should advance last-applied")
	}
	st, err := Classify(tgt, h.store)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateClean {
		t.Fatalf("after full capture state = %q, want clean", st.State)
	}
}

func TestUpdateLastAppliedPartialKeepsDrift(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "v1\n")
	tgt := h.target("zshrc")
	if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
		t.Fatal(err)
	}
	appliedBefore, _ := h.store.LastApplied("zshrc")

	// PARTIAL capture: user has two local edits, only one routed to the repo.
	h.writeHome("zshrc", "edit-a\nedit-b\n")
	h.writeRepo("zshrc", "edit-a\n") // live still differs

	updated, err := UpdateLastApplied(tgt, h.store)
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("partial capture must NOT advance last-applied")
	}
	if got, _ := h.store.LastApplied("zshrc"); got != appliedBefore {
		t.Fatalf("last-applied moved on a partial capture: %q != %q", got, appliedBefore)
	}
	// status keeps reporting the remaining drift (repo moved + live moved -> conflict).
	st, err := Classify(tgt, h.store)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateConflict {
		t.Fatalf("after partial capture state = %q, want conflict (remaining drift)", st.State)
	}
}

// --- a symlink at the home target is rejected, never silently followed ---

func TestClassifyRejectsSymlinkTarget(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "v1\n")
	tgt := h.target("zshrc")
	decoy := filepath.Join(h.repoRoot, "decoy")
	if err := os.WriteFile(decoy, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, tgt.Home); err != nil {
		t.Fatal(err)
	}
	var uke *UnexpectedKindError
	if _, err := Classify(tgt, h.store); !errors.As(err, &uke) {
		t.Fatalf("err = %v, want *UnexpectedKindError", err)
	}
}

// --- mapping ---

func TestTargetFor(t *testing.T) {
	tgt := TargetFor("/repo", "/home", "zshrc")
	if tgt.Repo != filepath.Join("/repo", "dotfiles", "zshrc") {
		t.Fatalf("repo = %q", tgt.Repo)
	}
	if tgt.Home != filepath.Join("/home", ".zshrc") {
		t.Fatalf("home = %q", tgt.Home)
	}
}

// TestTargetForNameContract pins the config<->dotfile name contract: a dotfile
// declared with a leading dot in the manifest (`dotfiles = [".zshrc"]`, the
// documented form) and a bare "zshrc" must BOTH map to repo `dotfiles/zshrc`
// and home `~/.zshrc` — no double dots, no `dotfiles/.zshrc`.
func TestTargetForNameContract(t *testing.T) {
	wantRepo := filepath.Join("/repo", "dotfiles", "zshrc")
	wantHome := filepath.Join("/home", ".zshrc")
	for _, declared := range []string{".zshrc", "zshrc"} {
		tgt := TargetFor("/repo", "/home", declared)
		if tgt.Name != "zshrc" {
			t.Errorf("input %q: Name = %q, want bare \"zshrc\"", declared, tgt.Name)
		}
		if tgt.Repo != wantRepo {
			t.Errorf("input %q: repo = %q, want %q", declared, tgt.Repo, wantRepo)
		}
		if tgt.Home != wantHome {
			t.Errorf("input %q: home = %q, want %q", declared, tgt.Home, wantHome)
		}
	}
}

// Adopting an identical pre-existing file must record it in last-applied, so a
// later repo advance is a clean repo-ahead update, not a false conflict.
func TestApplyAdoptIdenticalThenRepoAdvanceIsUpdate(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "v1\n")
	tgt := h.target("zshrc")
	h.writeHome("zshrc", "v1\n") // identical, no last-applied record

	res, err := Apply(tgt, h.store, h.b, false, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.State != StateClean || res.Action != ActionNoop {
		t.Fatalf("state=%q action=%q, want clean/noop", res.State, res.Action)
	}
	if _, ok := h.store.LastApplied("zshrc"); !ok {
		t.Fatal("identical file was not adopted into last-applied")
	}

	// Repo advances; live is untouched -> repo-ahead update, NOT a conflict.
	h.writeRepo("zshrc", "v2\n")
	res, err = Apply(tgt, h.store, h.b, false, false)
	if err != nil {
		t.Fatalf("apply after advance: %v", err)
	}
	if res.State != StateRepoAhead || res.Action != ActionUpdated {
		t.Fatalf("state=%q action=%q, want repo-ahead/updated", res.State, res.Action)
	}
	if got := h.readHome("zshrc"); got != "v2\n" {
		t.Fatalf("home = %q, want v2", got)
	}
}

// --force resets a purely locally-drifted file (repo unchanged) to the repo
// content, honouring the documented "--force overwrites local edits".
func TestApplyForceResetsLocallyDrifted(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "v1\n")
	tgt := h.target("zshrc")
	if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
		t.Fatal(err)
	}
	h.writeHome("zshrc", "local-edit\n") // repo still v1 -> locally-drifted

	res, err := Apply(tgt, h.store, h.b, true /*force*/, false)
	if err != nil {
		t.Fatalf("force apply: %v", err)
	}
	if res.State != StateLocallyDrifted || res.Action != ActionUpdated {
		t.Fatalf("state=%q action=%q, want locally-drifted/updated", res.State, res.Action)
	}
	if got := h.readHome("zshrc"); got != "v1\n" {
		t.Fatalf("force did not reset live to repo: %q", got)
	}
	if string(h.b.backups[tgt.Home]) != "local-edit\n" {
		t.Fatalf("force did not back up the discarded edit: %q", h.b.backups[tgt.Home])
	}
}

// --- store persists across reopen ---

func TestStorePersists(t *testing.T) {
	dir := t.TempDir()
	s1, err := OpenStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.set("zshrc", "deadbeef"); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := s2.LastApplied("zshrc"); !ok || h != "deadbeef" {
		t.Fatalf("reopened store lost data: %q ok=%v", h, ok)
	}
}
