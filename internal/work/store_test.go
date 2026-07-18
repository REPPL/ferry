package work

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenStore_InsideWorktreeRefused(t *testing.T) {
	repo := newRepo(t)
	sub := filepath.Join(repo, "cargo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := OpenStore(sub, false)
	var wte *StoreInWorktreeError
	if !errors.As(err, &wte) {
		t.Fatalf("err = %v, want *StoreInWorktreeError", err)
	}
}

func TestOpenStore_SyncRootRefusedWithoutOverride(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	base := t.TempDir()
	for _, seg := range []string{"Dropbox", "Mobile Documents", "CloudStorage", "Google Drive"} {
		dir := filepath.Join(base, seg, "cargo")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := OpenStore(dir, false)
		var sre *StoreSyncRootError
		if !errors.As(err, &sre) {
			t.Errorf("OpenStore under %q: err = %v, want *StoreSyncRootError", seg, err)
		}
		if _, err := OpenStore(dir, true); err != nil {
			t.Errorf("OpenStore under %q with override: %v", seg, err)
		}
	}
}

func TestOpenStore_PlainDirAccepted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if _, err := OpenStore(t.TempDir(), false); err != nil {
		t.Fatalf("OpenStore on plain dir: %v", err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	st, err := OpenStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStore_WriteBundleAllocatesSequences(t *testing.T) {
	st := openTestStore(t)
	key := testKey()

	fixed := func(data string) func(uint64) ([]byte, error) {
		return func(uint64) ([]byte, error) { return []byte(data), nil }
	}
	r1, err := st.WriteBundle(key, fixed("cargo-one"))
	if err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	r2, err := st.WriteBundle(key, fixed("cargo-two"))
	if err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	if r1.Seq != 1 || r2.Seq != 2 {
		t.Errorf("seqs = %d, %d, want 1, 2", r1.Seq, r2.Seq)
	}
	if r1.SHA256 == r2.SHA256 {
		t.Error("different cargo produced identical bundle hashes")
	}
	if got, err := os.ReadFile(r2.Path); err != nil || string(got) != "cargo-two" {
		t.Errorf("bundle content = %q, %v", got, err)
	}

	refs, err := st.Bundles(key)
	if err != nil {
		t.Fatalf("Bundles: %v", err)
	}
	if len(refs) != 2 || refs[0].Seq != 1 || refs[1].Seq != 2 {
		t.Errorf("Bundles = %+v, want seqs [1 2] ascending", refs)
	}
}

func TestStore_BundlesSurfacesEqualSeqForks(t *testing.T) {
	st := openTestStore(t)
	key := testKey()
	if _, err := st.WriteBundle(key, func(uint64) ([]byte, error) { return []byte("alice"), nil }); err != nil {
		t.Fatal(err)
	}
	// Simulate Bob's concurrent pack that allocated the SAME seq on other
	// media: drop a same-seq, different-hash file in directly.
	forkName := "000001-" + strings.Repeat("9", 64) + ".ferrywork"
	if err := os.WriteFile(filepath.Join(st.ProjectDir(key), forkName), []byte("bob"), 0o644); err != nil {
		t.Fatal(err)
	}
	refs, err := st.Bundles(key)
	if err != nil {
		t.Fatalf("Bundles: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("Bundles = %+v, want both fork files", refs)
	}
	if refs[0].Seq != 1 || refs[1].Seq != 1 {
		t.Errorf("fork seqs = %d, %d, want 1, 1", refs[0].Seq, refs[1].Seq)
	}

	// The next pack must skip past the fork, not collide with it, and the
	// build callback must be told the seq it actually got.
	var builtSeq uint64
	r, err := st.WriteBundle(key, func(seq uint64) ([]byte, error) {
		builtSeq = seq
		return []byte("carol"), nil
	})
	if err != nil {
		t.Fatalf("WriteBundle after fork: %v", err)
	}
	if r.Seq != 2 || builtSeq != 2 {
		t.Errorf("post-fork seq = %d (built with %d), want 2", r.Seq, builtSeq)
	}
}

func TestStore_BundlesIgnoresJunk(t *testing.T) {
	st := openTestStore(t)
	key := testKey()
	dir := st.ProjectDir(key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, junk := range []string{"README.txt", "claim.alice@studio.json", ".DS_Store", "nonseq-xyz.ferrywork"} {
		if err := os.WriteFile(filepath.Join(dir, junk), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := st.Bundles(key)
	if err != nil {
		t.Fatalf("Bundles: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("Bundles = %+v, want none (junk ignored)", refs)
	}
}

func TestStore_ClaimsRoundTripAndMerge(t *testing.T) {
	st := openTestStore(t)
	key := testKey()

	if err := st.AppendClaim(key, "alice@studio", ClaimEvent{Op: OpPack, Seq: 1, At: "2026-07-18T10:00:00Z"}); err != nil {
		t.Fatalf("AppendClaim: %v", err)
	}
	if err := st.AppendClaim(key, "alice@studio", ClaimEvent{Op: OpTakeBack, Seq: 1}); err != nil {
		t.Fatalf("AppendClaim: %v", err)
	}
	if err := st.AppendClaim(key, "bob@laptop", ClaimEvent{Op: OpReceive, Seq: 1}); err != nil {
		t.Fatalf("AppendClaim: %v", err)
	}

	claims, err := st.Claims(key)
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("Claims = %+v, want 2 accounts", claims)
	}
	byAccount := map[string][]ClaimEvent{}
	for _, c := range claims {
		byAccount[c.Account] = c.Events
	}
	if evs := byAccount["alice@studio"]; len(evs) != 2 || evs[0].Op != OpPack || evs[1].Op != OpTakeBack {
		t.Errorf("alice events = %+v", evs)
	}
	if evs := byAccount["bob@laptop"]; len(evs) != 1 || evs[0].Op != OpReceive {
		t.Errorf("bob events = %+v", evs)
	}
}

func TestStore_ClaimAccountNameValidated(t *testing.T) {
	st := openTestStore(t)
	for _, bad := range []string{"", "a/b@host", "..@host", "a b@host"} {
		if err := st.AppendClaim(testKey(), bad, ClaimEvent{Op: OpPack, Seq: 1}); err == nil {
			t.Errorf("AppendClaim accepted account %q, want refusal", bad)
		}
	}
}
