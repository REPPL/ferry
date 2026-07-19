package work

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/backup"
)

// handoffFixture is a two-account setup sharing one cargo store: alice (the
// packFixture) hands over, bob picks up in his own home with his own clone.
type handoffFixture struct {
	alice *packFixture
	// bob's side
	home  string
	repo  string
	lc    Locator
	state *State
	eng   *backup.Engine
}

func newHandoffFixture(t *testing.T) *handoffFixture {
	t.Helper()
	fx := newPackFixture(t)
	if _, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts()); err != nil {
		t.Fatalf("alice pack: %v", err)
	}

	bobHome := t.TempDir()
	bobRepo := filepath.Join(bobHome, "src", "proj")
	if err := os.MkdirAll(filepath.Dir(bobRepo), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, filepath.Dir(bobRepo), "clone", "-q", "file://"+fx.repo, bobRepo)

	id, err := ProjectIdentity(bobRepo)
	if err != nil {
		t.Fatal(err)
	}
	if id.Key != fx.id.Key {
		t.Fatalf("clone changed identity: %s vs %s", id.Key, fx.id.Key)
	}
	state, err := LoadStateAt(filepath.Join(bobHome, ".local", "state", "ferry"), id.Key)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := backup.NewAt(filepath.Join(bobHome, ".local", "state", "ferry"))
	if err != nil {
		t.Fatal(err)
	}
	return &handoffFixture{
		alice: fx,
		home:  bobHome,
		repo:  bobRepo,
		lc:    Locator{Home: bobHome, ProjectDir: bobRepo, StoreKey: id.Key},
		state: state,
		eng:   eng,
	}
}

func bobOpts() ReceiveOptions {
	return ReceiveOptions{Account: "bob@laptop", Now: "2026-07-18T11:00:00Z"}
}

func (h *handoffFixture) receive(t *testing.T, opts ReceiveOptions) (*ReceiveResult, error) {
	t.Helper()
	id, err := ProjectIdentity(h.lc.ProjectDir)
	if err != nil {
		t.Fatal(err)
	}
	return Receive(h.alice.st, h.eng, h.lc, id, h.state, opts)
}

func TestReceive_HappyPath(t *testing.T) {
	h := newHandoffFixture(t)
	res, err := h.receive(t, bobOpts())
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if res.TakeBack {
		t.Fatal("receive on bob reported take-back")
	}
	if res.Ref.Seq != 1 {
		t.Errorf("seq = %d, want 1", res.Ref.Seq)
	}

	got, err := os.ReadFile(filepath.Join(h.repo, ".abcd", ".work.local", "NEXT.md"))
	if err != nil || string(got) != "# NEXT\ncontinue here\n" {
		t.Errorf("NEXT.md = %q, %v", got, err)
	}
	mem := filepath.Join(h.home, ".claude", "projects", ClaudeProjectsKey(h.repo), "memory", "MEMORY.md")
	if _, err := os.Stat(mem); err != nil {
		t.Errorf("memory not landed: %v", err)
	}
	tr := filepath.Join(h.home, ".abcd", "history", h.lc.StoreKey, "sess-1.jsonl")
	if _, err := os.Stat(tr); err != nil {
		t.Errorf("transcript not landed: %v", err)
	}

	if h.state.Baseline == nil || h.state.Baseline.Op != OpReceive || h.state.Baseline.Seq != 1 {
		t.Errorf("baseline = %+v", h.state.Baseline)
	}
	if h.state.LastReceive == nil || h.state.LastReceive.SnapshotID == "" {
		t.Errorf("last receive = %+v", h.state.LastReceive)
	}
	claims, err := h.alice.st.Claims(h.lc.StoreKey)
	if err != nil {
		t.Fatal(err)
	}
	var sawReceive bool
	for _, c := range claims {
		for _, ev := range c.Events {
			if c.Account == "bob@laptop" && ev.Op == OpReceive && ev.Seq == 1 {
				sawReceive = true
			}
		}
	}
	if !sawReceive {
		t.Errorf("no bob receive claim in %+v", claims)
	}
}

func TestReceive_UnionMergeNeverOverwritesOrDeletes(t *testing.T) {
	h := newHandoffFixture(t)
	trDir := filepath.Join(h.home, ".abcd", "history", h.lc.StoreKey)
	writeFileT(t, filepath.Join(trDir, "sess-1.jsonl"), "bob's own copy")
	writeFileT(t, filepath.Join(trDir, "sess-bob.jsonl"), "bob only")

	res, err := h.receive(t, bobOpts())
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(trDir, "sess-1.jsonl")); string(got) != "bob's own copy" {
		t.Errorf("union-merge overwrote an existing transcript: %q", got)
	}
	if _, err := os.Stat(filepath.Join(trDir, "sess-bob.jsonl")); err != nil {
		t.Error("union-merge deleted a destination-only transcript")
	}
	var skipped bool
	for _, s := range res.Skipped {
		if strings.HasSuffix(s, "sess-1.jsonl") {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("skip not reported: %+v", res.Skipped)
	}
}

func TestReceive_GuardedOverwriteRefusesDivergence(t *testing.T) {
	h := newHandoffFixture(t)
	if _, err := h.receive(t, bobOpts()); err != nil {
		t.Fatal(err)
	}
	// Bob edits the note, then alice packs again and bob tries to receive:
	// the destination changed since bob last held the baton.
	writeFileT(t, filepath.Join(h.repo, ".abcd", ".work.local", "NEXT.md"), "bob's local edits\n")
	writeFileT(t, filepath.Join(h.alice.repo, ".abcd", ".work.local", "NEXT.md"), "alice v2\n")
	if _, err := Pack(h.alice.st, h.alice.lc, h.alice.id, h.alice.state, defaultOpts()); err != nil {
		t.Fatal(err)
	}

	_, err := h.receive(t, bobOpts())
	var de *DivergedError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DivergedError", err)
	}

	opts := bobOpts()
	opts.Force = true
	if _, err := h.receive(t, opts); err != nil {
		t.Fatalf("forced receive: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(h.repo, ".abcd", ".work.local", "NEXT.md")); string(got) != "alice v2\n" {
		t.Errorf("NEXT.md after force = %q", got)
	}
}

func TestReceive_FirstReceiveIntoPopulatedRefused(t *testing.T) {
	h := newHandoffFixture(t)
	writeFileT(t, filepath.Join(h.repo, ".abcd", ".work.local", "NEXT.md"), "bob's pre-existing note\n")
	_, err := h.receive(t, bobOpts())
	var de *DivergedError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DivergedError (first receive into populated dest)", err)
	}
	opts := bobOpts()
	opts.Force = true
	if _, err := h.receive(t, opts); err != nil {
		t.Fatalf("forced: %v", err)
	}
}

func TestReceive_IdenticalContentIsNotDivergence(t *testing.T) {
	h := newHandoffFixture(t)
	// Destination already holds exactly the incoming content: no refusal.
	writeFileT(t, filepath.Join(h.repo, ".abcd", ".work.local", "NEXT.md"), "# NEXT\ncontinue here\n")
	if _, err := h.receive(t, bobOpts()); err != nil {
		t.Fatalf("identical-content receive refused: %v", err)
	}
}

func TestReceive_TakeBackOnPackerAccount(t *testing.T) {
	h := newHandoffFixture(t)
	fx := h.alice
	id, err := ProjectIdentity(fx.repo)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := backup.NewAt(filepath.Join(fx.home, ".local", "state", "ferry-eng"))
	if err != nil {
		t.Fatal(err)
	}
	// Alice edits after packing, then reclaims her baton.
	writeFileT(t, filepath.Join(fx.repo, ".abcd", ".work.local", "NEXT.md"), "alice kept working\n")
	res, err := Receive(fx.st, eng, fx.lc, id, fx.state, ReceiveOptions{Account: "alice@studio", Now: "2026-07-18T11:00:00Z"})
	if err != nil {
		t.Fatalf("take-back: %v", err)
	}
	if !res.TakeBack {
		t.Fatal("packer-account receive did not report take-back")
	}
	// It restores nothing…
	if got, _ := os.ReadFile(filepath.Join(fx.repo, ".abcd", ".work.local", "NEXT.md")); string(got) != "alice kept working\n" {
		t.Errorf("take-back overwrote local work: %q", got)
	}
	// …clears the marker, and records the claim.
	if _, ok, _ := ReadHandoverMarker(fx.repo); ok {
		t.Error("handover marker survived the take-back")
	}
	claims, _ := fx.st.Claims(id.Key)
	var sawTakeBack bool
	for _, c := range claims {
		for _, ev := range c.Events {
			if c.Account == "alice@studio" && ev.Op == OpTakeBack {
				sawTakeBack = true
			}
		}
	}
	if !sawTakeBack {
		t.Errorf("no take-back claim in %+v", claims)
	}

	// Bob's receive of the taken-back bundle now refuses without force.
	_, err = h.receive(t, bobOpts())
	var se *SupersededError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *SupersededError", err)
	}
	opts := bobOpts()
	opts.Force = true
	if _, err := h.receive(t, opts); err != nil {
		t.Fatalf("forced receive of superseded bundle: %v", err)
	}
}

func TestReceive_EqualSeqTieRefused(t *testing.T) {
	h := newHandoffFixture(t)
	// A same-seq fork from another account.
	forkName := "000001-" + strings.Repeat("9", 64) + ".ferrywork"
	if err := os.WriteFile(filepath.Join(h.alice.st.ProjectDir(h.lc.StoreKey), forkName), []byte("fork"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := h.receive(t, bobOpts())
	var te *TieError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want *TieError", err)
	}
	if len(te.Refs) != 2 {
		t.Errorf("tie refs = %+v", te.Refs)
	}

	// Naming one bundle explicitly resolves the tie.
	opts := bobOpts()
	opts.BundleSHA256 = te.Refs[0].SHA256
	if te.Refs[0].SHA256 == strings.Repeat("9", 64) {
		opts.BundleSHA256 = te.Refs[1].SHA256
	}
	if _, err := h.receive(t, opts); err != nil {
		t.Fatalf("explicit-bundle receive: %v", err)
	}
}

func TestReceive_MemoryTargetGuard(t *testing.T) {
	h := newHandoffFixture(t)
	memDir := filepath.Join(h.home, ".claude", "projects", ClaudeProjectsKey(h.repo), "memory")
	writeFileT(t, filepath.Join(memDir, "OTHER.md"), "someone else's memory\n")
	_, err := h.receive(t, bobOpts())
	var de *DivergedError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DivergedError (populated memory target)", err)
	}
	opts := bobOpts()
	opts.Force = true
	if _, err := h.receive(t, opts); err != nil {
		t.Fatalf("forced: %v", err)
	}
}

func TestReceive_TranscriptLockRefuses(t *testing.T) {
	h := newHandoffFixture(t)
	trDir := filepath.Join(h.home, ".abcd", "history", h.lc.StoreKey)
	writeFileT(t, filepath.Join(trDir, ".lock"), "held\n")
	if _, err := h.receive(t, bobOpts()); err == nil {
		t.Fatal("receive proceeded under a held transcript-store lock")
	}
}

func TestReceive_NoCargoIsClearError(t *testing.T) {
	h := newHandoffFixture(t)
	// An unrelated project (different identity) finds no cargo.
	otherRepo := newRepo(t)
	id, err := ProjectIdentity(otherRepo)
	if err != nil {
		t.Fatal(err)
	}
	lc := Locator{Home: h.home, ProjectDir: otherRepo, StoreKey: id.Key}
	if _, err := Receive(h.alice.st, h.eng, lc, id, h.state, bobOpts()); err == nil {
		t.Fatal("receive for a project with no cargo succeeded")
	}
}

func TestWorkRestore_RevertsLastReceive(t *testing.T) {
	h := newHandoffFixture(t)
	if _, err := h.receive(t, bobOpts()); err != nil {
		t.Fatal(err)
	}
	notePath := filepath.Join(h.repo, ".abcd", ".work.local", "NEXT.md")
	if _, err := os.Stat(notePath); err != nil {
		t.Fatal("receive did not land the note")
	}

	snapID, err := WorkRestore(h.eng, h.state)
	if err != nil {
		t.Fatalf("WorkRestore: %v", err)
	}
	if snapID == "" {
		t.Error("empty snapshot id")
	}
	// The note did not exist before the receive; the revert removes it.
	if _, err := os.Stat(notePath); err == nil {
		t.Error("NEXT.md survived the work restore")
	}
	if h.state.LastReceive != nil {
		t.Error("LastReceive not cleared")
	}
	if _, err := WorkRestore(h.eng, h.state); err == nil {
		t.Error("second WorkRestore succeeded, want 'nothing to revert'")
	}
}

func TestLocateProject_MatchesOnRootIntersection(t *testing.T) {
	h := newHandoffFixture(t)
	st := h.alice.st
	id := h.alice.id

	// Rename the store dir to a DIFFERENT root of the same project (as a
	// subtree import that reordered rev-list would produce): the manifest
	// scan must still find the cargo by set intersection.
	altKey := strings.Repeat("b", 40)
	if err := os.Rename(st.ProjectDir(id.Key), st.ProjectDir(altKey)); err != nil {
		t.Fatal(err)
	}
	key, refs, err := st.LocateProject(id)
	if err != nil {
		t.Fatalf("LocateProject: %v", err)
	}
	if key != altKey || len(refs) != 1 {
		t.Errorf("LocateProject = %q with %d refs, want %q with 1", key, len(refs), altKey)
	}
}

func TestReceive_FailedPlanReleasesTranscriptLock(t *testing.T) {
	h := newHandoffFixture(t)
	st := h.alice.st
	key := h.lc.StoreKey

	// A crafted bundle whose manifest lists transcripts FIRST (lock taken)
	// and then an item this registry does not know (plan fails after the
	// lock): the failed receive must not leave the advisory lock behind.
	files := []packedFile{
		newPackedFile(ItemTranscripts, "sess-x.jsonl", []byte("x"), 0o644),
		newPackedFile("bogus-item", "y", []byte("y"), 0o644),
	}
	m := &Manifest{
		FerryVersion: "test",
		Key:          key,
		Roots:        []string{key},
		RepoPath:     h.alice.repo,
		Home:         h.alice.home,
		PackedBy:     "alice@studio",
		ScanVerdict:  ScanVerdictClean,
		Items: []ManifestItem{
			{Name: ItemTranscripts, Included: true, Files: []ManifestFile{
				{Path: "sess-x.jsonl", Size: 1, SHA256: files[0].sha},
			}},
			{Name: "bogus-item", Included: true, Files: []ManifestFile{
				{Path: "y", Size: 1, SHA256: files[1].sha},
			}},
		},
	}
	if _, err := st.WriteBundle(key, func(seq uint64) ([]byte, error) {
		m.Seq = seq
		return buildCargoZip(m, files)
	}); err != nil {
		t.Fatal(err)
	}

	_, err := h.receive(t, bobOpts())
	if err == nil || !strings.Contains(err.Error(), "bogus-item") {
		t.Fatalf("err = %v, want unknown-item refusal", err)
	}
	lock := filepath.Join(h.home, ".abcd", "history", key, ".lock")
	if _, statErr := os.Stat(lock); statErr == nil {
		t.Error("failed receive left the transcript advisory lock behind")
	}
}

func TestReceive_MidWriteFailureLeavesRevertibleState(t *testing.T) {
	h := newHandoffFixture(t)
	// Make the memory target directory unwritable: the guarded file items
	// (written first, in manifest order) land, then the memory write fails —
	// a genuine partial receive.
	memDir := filepath.Join(h.home, ".claude", "projects", ClaudeProjectsKey(h.repo), "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(memDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(memDir, 0o755) })

	_, err := h.receive(t, bobOpts())
	if err == nil {
		t.Fatal("receive into an unwritable memory target succeeded, want failure")
	}
	notePath := filepath.Join(h.repo, ".abcd", ".work.local", "NEXT.md")
	if _, statErr := os.Stat(notePath); statErr != nil {
		t.Fatal("test setup: the partial receive did not land the note before failing")
	}

	// The persisted state must already carry THIS receive's snapshot, so the
	// advertised recovery (`ferry work restore`) genuinely reverts the
	// partial landing.
	reloaded, err := LoadStateAt(filepath.Join(h.home, ".local", "state", "ferry"), h.lc.StoreKey)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.LastReceive == nil || reloaded.LastReceive.SnapshotID == "" {
		t.Fatalf("LastReceive after mid-write failure = %+v, want the pre-write snapshot recorded", reloaded.LastReceive)
	}

	if err := os.Chmod(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := WorkRestore(h.eng, reloaded); err != nil {
		t.Fatalf("WorkRestore after partial receive: %v", err)
	}
	if _, statErr := os.Stat(notePath); statErr == nil {
		t.Error("NEXT.md survived the revert of the partial receive")
	}
}
