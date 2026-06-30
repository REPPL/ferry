package dotfile

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
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

// failingBackuper always fails the backup-and-write, simulating a backup engine
// that cannot secure the prior state. Apply must then refuse to deploy.
type failingBackuper struct{}

func (failingBackuper) BackupAndWrite(target string, content []byte, perm os.FileMode) error {
	return errors.New("backup failed")
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

func (h *harness) target(name string) Target {
	h.t.Helper()
	tgt, err := TargetFor(h.repoRoot, h.home, name)
	if err != nil {
		h.t.Fatalf("TargetFor(%q): %v", name, err)
	}
	return tgt
}

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

// --- first-touch adoption of a pre-existing, differing, unmanaged file ---

// On the FIRST-EVER apply of an in-scope dotfile whose home file PRE-EXISTS,
// DIFFERS from the repo, and has NO last-applied record, ferry ADOPTS it: it
// backs the live file up to the baseline (via the Backuper) and deploys the
// repo content. This is NOT a conflict.
func TestApplyAdoptsPreExistingDifferingFile(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("gitconfig", "[user]\n") // repo content Y
	tgt := h.target("gitconfig")
	const preExisting = "[user]\n\tname = pre-existing\n" // home content X
	h.writeHome("gitconfig", preExisting)

	res, err := Apply(tgt, h.store, h.b, false /*no force*/, false)
	if err != nil {
		t.Fatalf("first-touch adoption should not error: %v", err)
	}
	if res.State != StateRepoAhead || res.Action != ActionUpdated {
		t.Fatalf("state=%q action=%q, want repo-ahead/updated (adoption, not conflict)", res.State, res.Action)
	}
	// The repo content Y was deployed.
	if got := h.readHome("gitconfig"); got != "[user]\n" {
		t.Fatalf("home = %q, want repo content deployed", got)
	}
	// The pre-existing content X was backed up first — restore would recover it.
	if string(h.b.backups[tgt.Home]) != preExisting {
		t.Fatalf("pre-existing content was not backed up before deploy: %q", h.b.backups[tgt.Home])
	}
	// last-applied now records the repo hash.
	if got, ok := h.store.LastApplied("gitconfig"); !ok || got != hashBytes([]byte("[user]\n")) {
		t.Fatalf("last-applied = %q ok=%v, want hash of repo content", got, ok)
	}
}

// If the pre-existing file cannot be backed up (the Backuper errors), apply
// REFUSES rather than overwrite — never deploy without first securing the
// original in the baseline.
func TestApplyAdoptionRefusesWhenBackupFails(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("gitconfig", "[user]\n")
	tgt := h.target("gitconfig")
	const preExisting = "[user]\n\tname = pre-existing\n"
	h.writeHome("gitconfig", preExisting)

	failing := &failingBackuper{}
	if _, err := Apply(tgt, h.store, failing, false, false); err == nil {
		t.Fatal("apply should propagate the backup failure, refusing to deploy")
	}
	// Nothing deployed: the pre-existing file is untouched.
	if got := h.readHome("gitconfig"); got != preExisting {
		t.Fatalf("backup failure still overwrote the live file: %q", got)
	}
	if _, ok := h.store.LastApplied("gitconfig"); ok {
		t.Fatal("backup failure still recorded last-applied")
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
		{"first-touch-adopt-unmanaged", func(h *harness, tgt Target) {
			// Pre-existing, differing, NO last-applied record -> first-touch
			// adoption (repo-ahead deploy), NOT a conflict.
			h.writeRepo(tgt.Name, "v1\n")
			h.writeHome(tgt.Name, "pre-existing\n")
		}, StateRepoAhead},
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
	tgt, err := TargetFor("/repo", "/home", "zshrc")
	if err != nil {
		t.Fatalf("TargetFor: %v", err)
	}
	if tgt.Repo != filepath.Join("/repo", "dotfiles", "zshrc") {
		t.Fatalf("repo = %q", tgt.Repo)
	}
	if tgt.Home != filepath.Join("/home", ".zshrc") {
		t.Fatalf("home = %q", tgt.Home)
	}
	if tgt.Overlay != OverlayWholeFileReplace {
		t.Fatalf("overlay = %q, want whole-file-replace (default)", tgt.Overlay)
	}
}

// TestTargetForRefusesSSH pins the top security contract: NO declared dotfile
// name may resolve to a target under ~/.ssh/ (or .ssh itself). TargetFor is the
// single enforcement point, so apply/capture/status can never obtain such a
// target. A FAKE ~/.ssh under a temp HOME is used — the real ~/.ssh is untouched.
func TestTargetForRefusesSSH(t *testing.T) {
	home := t.TempDir() // fake $HOME; its .ssh is purely hypothetical
	for _, name := range []string{".ssh/config", "ssh/config", ".ssh/id_ed25519", ".ssh", "ssh"} {
		if _, err := TargetFor("/repo", home, name); !errors.Is(err, ErrForbiddenSSHPath) {
			t.Errorf("TargetFor(%q): err = %v, want ErrForbiddenSSHPath", name, err)
		}
	}
}

// TestTargetForRefusesTraversal: a declared name must resolve strictly within
// $HOME. `..` climbs and absolute paths are path-traversal and are refused.
func TestTargetForRefusesTraversal(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"../evil", "../../etc/passwd", "/etc/passwd", "foo/../../escape"} {
		if _, err := TargetFor("/repo", home, name); !errors.Is(err, ErrPathEscapesHome) {
			t.Errorf("TargetFor(%q): err = %v, want ErrPathEscapesHome", name, err)
		}
	}
}

// TestTargetForNormalStillWorks: an ordinary dotfile (with or without the
// leading dot) maps cleanly and is NOT refused.
func TestTargetForNormalStillWorks(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{".zshrc", "zshrc", ".gitconfig"} {
		tgt, err := TargetFor("/repo", home, name)
		if err != nil {
			t.Fatalf("TargetFor(%q): %v", name, err)
		}
		bare := name
		if bare[0] == '.' {
			bare = bare[1:]
		}
		if tgt.Name != bare {
			t.Errorf("name = %q, want %q", tgt.Name, bare)
		}
		if tgt.Home != filepath.Join(home, "."+bare) {
			t.Errorf("home = %q", tgt.Home)
		}
	}
}

// TestIncludeSidecarTargetMode: the include-style constructor yields the same
// validated mapping but with OverlayIncludeSidecar, and still refuses ~/.ssh.
func TestIncludeSidecarTargetMode(t *testing.T) {
	home := t.TempDir()
	tgt, err := IncludeSidecarTarget("/repo", home, "zshrc")
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Overlay != OverlayIncludeSidecar {
		t.Fatalf("overlay = %q, want include-sidecar", tgt.Overlay)
	}
	if _, err := IncludeSidecarTarget("/repo", home, ".ssh/config"); !errors.Is(err, ErrForbiddenSSHPath) {
		t.Fatalf("include-sidecar still must refuse ~/.ssh: %v", err)
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
		tgt, err := TargetFor("/repo", "/home", declared)
		if err != nil {
			t.Fatalf("TargetFor(%q): %v", declared, err)
		}
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

// --- deferred last-applied: write happens, but last-applied is NOT persisted
//     until CommitLastApplied (Codex#3 ordering fix) ---

func TestApplyDeferredDoesNotPersistUntilCommit(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "v1\n")
	tgt := h.target("zshrc")

	res, err := ApplyDeferred(tgt, h.store, h.b, false, false)
	if err != nil {
		t.Fatalf("apply deferred: %v", err)
	}
	if res.Action != ActionCreated {
		t.Fatalf("action=%q, want created", res.Action)
	}
	// The file WAS written (the journal would commit it)...
	if got := h.readHome("zshrc"); got != "v1\n" {
		t.Fatalf("home = %q, want v1 (deferred apply still writes the file)", got)
	}
	// ...but last-applied is NOT yet recorded (would be ahead of an uncommitted
	// journal).
	if _, ok := h.store.LastApplied("zshrc"); ok {
		t.Fatal("ApplyDeferred persisted last-applied before commit")
	}
	if res.PendingHash == "" {
		t.Fatal("ApplyDeferred did not record a PendingHash to commit")
	}

	// Caller commits last-applied AFTER the journal commit.
	if err := CommitLastApplied([]Result{res}, h.store); err != nil {
		t.Fatalf("commit last-applied: %v", err)
	}
	if hsh, ok := h.store.LastApplied("zshrc"); !ok || hsh != res.PendingHash {
		t.Fatalf("after commit last-applied = %q ok=%v, want %q", hsh, ok, res.PendingHash)
	}
}

// CommitLastApplied ignores results with no pending hash (noop/skip/conflict),
// so passing the full slice is safe.
func TestCommitLastAppliedIgnoresNonWrites(t *testing.T) {
	h := newHarness(t)
	noop := Result{Target: h.target("zshrc"), Action: ActionSkipped} // no PendingHash
	if err := CommitLastApplied([]Result{noop}, h.store); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, ok := h.store.LastApplied("zshrc"); ok {
		t.Fatal("CommitLastApplied recorded a result with no PendingHash")
	}
}

// --- read-only store: no mkdir, refuses to persist ---

func TestOpenStoreAtReadOnlyDoesNotCreateDir(t *testing.T) {
	parent := t.TempDir()
	stateDir := filepath.Join(parent, "state") // deliberately absent

	s, err := OpenStoreAtReadOnly(stateDir)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only open created the state dir")
	}
	// Absent dir reads as "no last-applied records" (everything first-touch).
	if _, ok := s.LastApplied("zshrc"); ok {
		t.Fatal("read-only store reported a record for an absent state dir")
	}
	// It refuses to persist.
	if err := s.set("zshrc", "deadbeef"); err == nil {
		t.Fatal("read-only store allowed set()")
	}
}

// --- Fix 2: whole-file-replace overlay (generic dotfiles, e.g. .gitconfig) ---

// writeLocal writes a per-machine overlay copy under repo/local/<domain>/<bare>.
func (h *harness) writeLocal(domain, name, content string) string {
	h.t.Helper()
	p := LocalOverlayPath(h.repoRoot, domain, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return p
}

// When a local copy is present, ApplyWholeFileOverlay deploys IT, not shared.
func TestApplyWholeFileOverlayLocalPresentReplacesShared(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("gitconfig", "[user]\n\tname = shared\n")
	local := h.writeLocal("git", "gitconfig", "[user]\n\tname = local\n")
	tgt := h.target("gitconfig") // default overlay = whole-file-replace

	res, err := ApplyWholeFileOverlay(tgt, local, h.store, h.b, false, false)
	if err != nil {
		t.Fatalf("apply overlay: %v", err)
	}
	if res.Action != ActionCreated {
		t.Fatalf("action = %q, want created", res.Action)
	}
	if got := h.readHome("gitconfig"); got != "[user]\n\tname = local\n" {
		t.Fatalf("home = %q, want the LOCAL copy deployed", got)
	}
}

// With no local copy, ApplyWholeFileOverlay deploys the shared content.
func TestApplyWholeFileOverlayAbsentDeploysShared(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("gitconfig", "[user]\n\tname = shared\n")
	tgt := h.target("gitconfig")

	// localSource path that does not exist -> shared deploys.
	missing := LocalOverlayPath(h.repoRoot, "git", "gitconfig")
	res, err := ApplyWholeFileOverlay(tgt, missing, h.store, h.b, false, false)
	if err != nil {
		t.Fatalf("apply overlay: %v", err)
	}
	if res.Action != ActionCreated {
		t.Fatalf("action = %q, want created", res.Action)
	}
	if got := h.readHome("gitconfig"); got != "[user]\n\tname = shared\n" {
		t.Fatalf("home = %q, want SHARED content (no local copy)", got)
	}
}

// An include-sidecar (zsh) target is REFUSED by ApplyWholeFileOverlay — it must
// stay sidecar; it can never be silently whole-file-replaced.
func TestApplyWholeFileOverlayRefusesIncludeSidecar(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "export A=1\n")
	tgt, err := IncludeSidecarTarget(h.repoRoot, h.home, "zshrc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyWholeFileOverlay(tgt, "", h.store, h.b, false, false); err == nil {
		t.Fatal("ApplyWholeFileOverlay must refuse an include-sidecar target")
	}
	// And a plain Apply of the same target still works (sidecar stays Apply's job).
	if _, err := Apply(tgt, h.store, h.b, false, false); err != nil {
		t.Fatalf("zsh sidecar Apply: %v", err)
	}
	if got := h.readHome("zshrc"); got != "export A=1\n" {
		t.Fatalf("zsh shared not deployed: %q", got)
	}
}

// --- deferred whole-file overlay: write happens, but last-applied is NOT
//     persisted until CommitLastApplied (Codex#3 ordering fix, overlay path) ---

// The deferred overlay writes the LOCAL copy (local wins) but does not persist
// last-applied until CommitLastApplied.
func TestApplyWholeFileOverlayDeferredLocalPresentDefersLastApplied(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("gitconfig", "[user]\n\tname = shared\n")
	local := h.writeLocal("git", "gitconfig", "[user]\n\tname = local\n")
	tgt := h.target("gitconfig")

	res, err := ApplyWholeFileOverlayDeferred(tgt, local, h.store, h.b, false, false)
	if err != nil {
		t.Fatalf("apply overlay deferred: %v", err)
	}
	if res.Action != ActionCreated {
		t.Fatalf("action = %q, want created", res.Action)
	}
	// The file WAS written with the LOCAL copy (the journal would commit it)...
	if got := h.readHome("gitconfig"); got != "[user]\n\tname = local\n" {
		t.Fatalf("home = %q, want the LOCAL copy deployed", got)
	}
	// ...but last-applied is NOT yet recorded.
	if _, ok := h.store.LastApplied("gitconfig"); ok {
		t.Fatal("deferred overlay persisted last-applied before commit")
	}
	if res.PendingHash == "" {
		t.Fatal("deferred overlay did not record a PendingHash to commit")
	}

	// Caller commits last-applied AFTER the journal commit.
	if err := CommitLastApplied([]Result{res}, h.store); err != nil {
		t.Fatalf("commit last-applied: %v", err)
	}
	if hsh, ok := h.store.LastApplied("gitconfig"); !ok || hsh != res.PendingHash {
		t.Fatalf("after commit last-applied = %q ok=%v, want %q", hsh, ok, res.PendingHash)
	}
}

// With no local copy, the deferred overlay deploys SHARED, still deferring.
func TestApplyWholeFileOverlayDeferredAbsentDeploysShared(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("gitconfig", "[user]\n\tname = shared\n")
	tgt := h.target("gitconfig")

	missing := LocalOverlayPath(h.repoRoot, "git", "gitconfig")
	res, err := ApplyWholeFileOverlayDeferred(tgt, missing, h.store, h.b, false, false)
	if err != nil {
		t.Fatalf("apply overlay deferred: %v", err)
	}
	if got := h.readHome("gitconfig"); got != "[user]\n\tname = shared\n" {
		t.Fatalf("home = %q, want SHARED content (no local copy)", got)
	}
	if _, ok := h.store.LastApplied("gitconfig"); ok {
		t.Fatal("deferred overlay persisted last-applied before commit")
	}
	if res.PendingHash == "" {
		t.Fatal("deferred overlay did not record a PendingHash to commit")
	}
}

// The deferred overlay REFUSES an include-sidecar target, same as the eager one.
func TestApplyWholeFileOverlayDeferredRefusesIncludeSidecar(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("zshrc", "export A=1\n")
	tgt, err := IncludeSidecarTarget(h.repoRoot, h.home, "zshrc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyWholeFileOverlayDeferred(tgt, "", h.store, h.b, false, false); err == nil {
		t.Fatal("ApplyWholeFileOverlayDeferred must refuse an include-sidecar target")
	}
}

// Dry-run defers AND writes nothing.
func TestApplyWholeFileOverlayDeferredDryRunWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.writeRepo("gitconfig", "[user]\n\tname = shared\n")
	local := h.writeLocal("git", "gitconfig", "[user]\n\tname = local\n")
	tgt := h.target("gitconfig")

	res, err := ApplyWholeFileOverlayDeferred(tgt, local, h.store, h.b, false, true)
	if err != nil {
		t.Fatalf("apply overlay deferred dry-run: %v", err)
	}
	if res.Action != ActionCreated {
		t.Fatalf("action = %q, want created (planned)", res.Action)
	}
	if _, err := os.Stat(filepath.Join(h.home, ".gitconfig")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote the home file")
	}
	if res.PendingHash != "" {
		t.Fatalf("dry-run recorded a PendingHash %q", res.PendingHash)
	}
	if _, ok := h.store.LastApplied("gitconfig"); ok {
		t.Fatal("dry-run persisted last-applied")
	}
}

// A read-only store still READS an existing last-applied file (status/diff need
// the hashes) without creating anything.
func TestOpenStoreAtReadOnlyReadsExisting(t *testing.T) {
	dir := t.TempDir()
	rw, err := OpenStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := rw.set("zshrc", "cafef00d"); err != nil {
		t.Fatal(err)
	}

	ro, err := OpenStoreAtReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := ro.LastApplied("zshrc"); !ok || h != "cafef00d" {
		t.Fatalf("read-only store lost existing data: %q ok=%v", h, ok)
	}
}

// RecordedNames enumerates the keys of the last-applied store, sorted; an empty
// store yields an empty slice (no error). It reads identically through a
// read-only store, since enumeration never writes.
func TestRecordedNames(t *testing.T) {
	dir := t.TempDir()

	empty, err := OpenStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := empty.RecordedNames(); len(got) != 0 {
		t.Fatalf("empty store: want no names, got %v", got)
	}

	// Record two targets (out of sorted order) via the normal set path.
	if err := empty.set("zshrc", "1111"); err != nil {
		t.Fatal(err)
	}
	if err := empty.set("gitconfig", "2222"); err != nil {
		t.Fatal(err)
	}

	want := []string{"gitconfig", "zshrc"} // sorted
	if got := empty.RecordedNames(); !equalStrings(got, want) {
		t.Fatalf("RecordedNames = %v, want %v", got, want)
	}

	// Same enumeration through a read-only store reopened on the same dir.
	ro, err := OpenStoreAtReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := ro.RecordedNames(); !equalStrings(got, want) {
		t.Fatalf("read-only RecordedNames = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- in-memory classification (ClassifyContent) ---

// TestClassifyContentMatchesClassify pins the core invariant: for identical
// content + live + last-applied, file-based Classify and bytes-based
// ClassifyContent yield IDENTICAL states. The "effective" bytes handed to
// ClassifyContent are exactly the repo file's bytes, so the two MUST agree —
// they share the same pure classify decision table.
func TestClassifyContentMatchesClassify(t *testing.T) {
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
		{"first-touch-adopt-unmanaged", func(h *harness, tgt Target) {
			h.writeRepo(tgt.Name, "v1\n")
			h.writeHome(tgt.Name, "pre-existing\n")
		}, StateRepoAhead},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			tgt := h.target("zshrc")
			c.setup(h, tgt)

			// The effective bytes ClassifyContent gets are exactly the repo
			// source's bytes, so it MUST agree with file-based Classify.
			effective, err := os.ReadFile(tgt.Repo)
			if err != nil {
				t.Fatal(err)
			}

			fileSt, err := Classify(tgt, h.store)
			if err != nil {
				t.Fatal(err)
			}
			memSt, err := ClassifyContent(tgt, effective, h.store)
			if err != nil {
				t.Fatal(err)
			}

			if memSt.State != c.want {
				t.Fatalf("ClassifyContent state = %q, want %q", memSt.State, c.want)
			}
			if memSt.State != fileSt.State {
				t.Fatalf("ClassifyContent state %q diverges from Classify %q", memSt.State, fileSt.State)
			}
			if memSt.RepoHash != fileSt.RepoHash || memSt.LiveHash != fileSt.LiveHash ||
				memSt.AppliedHash != fileSt.AppliedHash || memSt.HasApplied != fileSt.HasApplied ||
				memSt.LiveExists != fileSt.LiveExists {
				t.Fatalf("ClassifyContent status %+v diverges from Classify %+v", memSt, fileSt)
			}
		})
	}
}

// TestClassifyContentRejectsSymlinkTarget mirrors the live-file symlink check:
// ClassifyContent must reject a symlink at the home target exactly as Classify
// does (the check is on the LIVE file, shared by both).
func TestClassifyContentRejectsSymlinkTarget(t *testing.T) {
	h := newHarness(t)
	tgt := h.target("zshrc")
	decoy := filepath.Join(h.repoRoot, "decoy")
	if err := os.WriteFile(decoy, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, tgt.Home); err != nil {
		t.Fatal(err)
	}
	var uke *UnexpectedKindError
	if _, err := ClassifyContent(tgt, []byte("v1\n"), h.store); !errors.As(err, &uke) {
		t.Fatalf("err = %v, want *UnexpectedKindError", err)
	}
}

// TestClassifyContentWritesNothing asserts the read-only contract: classifying
// from in-memory bytes creates NO file anywhere — not in the state dir, not in
// the home dir, not in the repo dir. This is the whole point of the in-memory
// path: a preview must never stage (possibly secret-rendered) content to disk.
func TestClassifyContentWritesNothing(t *testing.T) {
	h := newHarness(t)
	tgt := h.target("zshrc")
	h.writeHome("zshrc", "live\n") // a live file to classify against

	stateDir := filepath.Dir(h.store.path)
	before := snapshotDirs(t, stateDir, h.home, filepath.Join(h.repoRoot, RepoSubdir))

	if _, err := ClassifyContent(tgt, []byte("desired\n"), h.store); err != nil {
		t.Fatal(err)
	}

	after := snapshotDirs(t, stateDir, h.home, filepath.Join(h.repoRoot, RepoSubdir))
	if !equalStrings(before, after) {
		t.Fatalf("ClassifyContent wrote to disk: before %v, after %v", before, after)
	}
}

// snapshotDirs returns the sorted set of entry names across the given dirs (a
// missing dir contributes nothing). Used to assert no file was created.
func snapshotDirs(t *testing.T, dirs ...string) []string {
	t.Helper()
	var names []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			names = append(names, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(names)
	return names
}
