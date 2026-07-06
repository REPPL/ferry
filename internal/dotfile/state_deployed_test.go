package dotfile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/REPPL/ferry/internal/statefile"
)

// TestApplyRecordsAndRereadsDeployedBaseline proves the load-bearing Foundation
// primitive: the deferred apply path (ApplyContentDeferred + CommitLastApplied,
// the exact pair the apply command drives) records the last-deployed content
// baseline for a target, and it re-reads byte-for-byte from a freshly reopened
// store on disk.
func TestApplyRecordsAndRereadsDeployedBaseline(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	store, err := OpenStoreAt(dir)
	if err != nil {
		t.Fatalf("OpenStoreAt: %v", err)
	}

	content := []byte("export EDITOR=vim\n")
	target := Target{Name: "zshrc", Home: filepath.Join(home, ".zshrc")}
	b := &contentBackuper{}

	// A brand-new target is created; the deferred path carries the deployed bytes
	// on the Result but writes NOTHING to the store yet.
	res, err := ApplyContentDeferred(target, content, 0o644, store, b, false, false)
	if err != nil {
		t.Fatalf("ApplyContentDeferred: %v", err)
	}
	if res.Action != ActionCreated {
		t.Fatalf("action = %q, want %q", res.Action, ActionCreated)
	}
	if res.PendingHash != hashBytes(content) {
		t.Fatalf("PendingHash = %q, want %q", res.PendingHash, hashBytes(content))
	}
	if !bytes.Equal(res.PendingContent, content) {
		t.Fatalf("PendingContent = %q, want %q", res.PendingContent, content)
	}
	// Nothing is committed until CommitLastApplied runs (post-journal ordering).
	if _, ok := store.LastDeployedSnapshot("zshrc"); ok {
		t.Fatal("snapshot recorded before CommitLastApplied; it must ride PendingContent until the journal commit")
	}

	// The post-journal commit records both the hash and the deployed snapshot.
	if err := CommitLastApplied([]Result{res}, store); err != nil {
		t.Fatalf("CommitLastApplied: %v", err)
	}
	got, ok := store.LastDeployedSnapshot("zshrc")
	if !ok || !bytes.Equal(got, content) {
		t.Fatalf("in-memory snapshot = %q,%v; want %q,true", got, ok, content)
	}

	// Re-read from disk: a fresh store opened over the same dir reproduces the
	// baseline byte-for-byte.
	reopened, err := OpenStoreAt(dir)
	if err != nil {
		t.Fatalf("reopen OpenStoreAt: %v", err)
	}
	got, ok = reopened.LastDeployedSnapshot("zshrc")
	if !ok || !bytes.Equal(got, content) {
		t.Fatalf("reread snapshot = %q,%v; want %q,true", got, ok, content)
	}
	if h, ok := reopened.LastApplied("zshrc"); !ok || h != hashBytes(content) {
		t.Fatalf("reread LastApplied = %q,%v; want %q,true", h, ok, hashBytes(content))
	}

	// The on-disk file is the version-2 envelope carrying the deployed map.
	raw, _ := os.ReadFile(filepath.Join(dir, stateFileName))
	if v, versioned := statefile.PeekVersion(raw); !versioned || v != lastAppliedVersion {
		t.Fatalf("on-disk version = %d,%v; want %d", v, versioned, lastAppliedVersion)
	}
	var env lastAppliedFile
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal on-disk state: %v", err)
	}
	if snap, ok := env.Deployed[hashBytes(content)]; !ok || !bytes.Equal(snap, content) {
		t.Fatalf("on-disk deployed[%s] = %q,%v; want the deployed bytes", hashBytes(content), snap, ok)
	}
}

// TestSecretRoutedTargetRecordsHashOnly proves the secret-at-rest fix: a
// SecretRouted result records ONLY its content hash — its rendered plaintext bytes
// (which carry substituted secret-store values) are NEVER written into the
// last-applied state file — while a non-secret target in the SAME commit still gets
// its full byte snapshot. Drift/baseline detection for the secret target survives
// via the recorded hash.
func TestSecretRoutedTargetRecordsHashOnly(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	store, err := OpenStoreAt(dir)
	if err != nil {
		t.Fatalf("OpenStoreAt: %v", err)
	}

	// The secret-routed target's DEPLOYED bytes are the rendered form: the real
	// secret value substituted in. This plaintext must never reach the state file.
	const secretPlaintext = "sk-live-DO-NOT-PERSIST-0123456789"
	secretContent := []byte("export API_KEY=" + secretPlaintext + "\n")
	secretTarget := Target{Name: "secretrc", Home: filepath.Join(home, ".secretrc")}

	// A plain, non-secret target that must KEEP its byte snapshot.
	plainContent := []byte("export EDITOR=vim\n")
	plainTarget := Target{Name: "zshrc", Home: filepath.Join(home, ".zshrc")}

	b := &contentBackuper{}

	// Declare the secret routing to the apply core (last arg); it stamps
	// res.SecretRouted so CommitLastApplied takes the hash-only path — no caller
	// assignment required.
	secretRes, err := ApplyContentDeferred(secretTarget, secretContent, 0o644, store, b, false, true)
	if err != nil {
		t.Fatalf("ApplyContentDeferred (secret): %v", err)
	}
	if !secretRes.SecretRouted {
		t.Fatal("apply core did not stamp res.SecretRouted for a secret-routed target")
	}

	plainRes, err := ApplyContentDeferred(plainTarget, plainContent, 0o644, store, b, false, false)
	if err != nil {
		t.Fatalf("ApplyContentDeferred (plain): %v", err)
	}

	if err := CommitLastApplied([]Result{secretRes, plainRes}, store); err != nil {
		t.Fatalf("CommitLastApplied: %v", err)
	}

	// (a) The rendered plaintext secret must appear NOWHERE in the on-disk state
	// file bytes — not under "deployed", not anywhere.
	raw, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if bytes.Contains(raw, []byte(secretPlaintext)) {
		t.Fatalf("SECRET LEAK: rendered plaintext %q found in the on-disk state file:\n%s", secretPlaintext, raw)
	}
	// And the secret target has NO stored snapshot (hash-only), on disk and in memory.
	if _, ok := store.LastDeployedSnapshot("secretrc"); ok {
		t.Fatal("secret-routed target recorded a byte snapshot; it must be hash-only")
	}

	// (b) Drift/baseline detection still works for the secret target via its hash:
	// the last-applied hash is recorded and re-reads from a fresh store on disk.
	reopened, err := OpenStoreAt(dir)
	if err != nil {
		t.Fatalf("reopen OpenStoreAt: %v", err)
	}
	if h, ok := reopened.LastApplied("secretrc"); !ok || h != hashBytes(secretContent) {
		t.Fatalf("secret target LastApplied = %q,%v; want %q,true (hash-based drift detection lost)", h, ok, hashBytes(secretContent))
	}
	if _, ok := reopened.LastDeployedSnapshot("secretrc"); ok {
		t.Fatal("secret-routed target has a snapshot after reopen; it must stay hash-only")
	}

	// (c) The non-secret target still gets its full byte snapshot.
	got, ok := reopened.LastDeployedSnapshot("zshrc")
	if !ok || !bytes.Equal(got, plainContent) {
		t.Fatalf("non-secret snapshot = %q,%v; want %q,true", got, ok, plainContent)
	}
}

// TestMigrateV1ToV2PreservesEveryHash rehearses the version-1 -> version-2
// migration (the transition this change introduces) on a synthetic old-format
// state file and asserts ZERO data loss: every recorded hash survives on the
// live file, the pre-migration bytes are preserved verbatim in a write-once
// sibling backup, and no snapshot is fabricated for a hash whose bytes the
// migration cannot know (the first-apply-after-upgrade bootstrap case).
func TestMigrateV1ToV2PreservesEveryHash(t *testing.T) {
	// A synthetic version-1 envelope with several recorded hashes: exactly the
	// shape a v0.4.x ferry wrote before the deployed baseline existed.
	original := []byte(`{
  "version": 1,
  "applied": {
    "gitconfig": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "zshrc": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  }
}`)
	dir, statePath := seedStateBytes(t, string(original))

	store, err := OpenStoreAt(dir)
	if err != nil {
		t.Fatalf("OpenStoreAt (migrate v1->v2): %v", err)
	}

	// Zero data loss: every recorded hash survives the migration unchanged.
	want := map[string]string{
		"gitconfig": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"zshrc":     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for name, hash := range want {
		if got, ok := store.LastApplied(name); !ok || got != hash {
			t.Fatalf("LastApplied(%q) = %q,%v; want %q,true", name, got, ok, hash)
		}
		// The migration cannot know the deployed bytes for a pre-baseline hash, so
		// it must NOT fabricate a snapshot — the target reads as "no baseline yet".
		if _, ok := store.LastDeployedSnapshot(name); ok {
			t.Fatalf("migration fabricated a deployed snapshot for %q; it must read as no-baseline until the next apply", name)
		}
	}

	// The live file is now the version-2 envelope.
	migrated, _ := os.ReadFile(statePath)
	if v, versioned := statefile.PeekVersion(migrated); !versioned || v != lastAppliedVersion {
		t.Fatalf("migrated version = %d,%v; want %d", v, versioned, lastAppliedVersion)
	}

	// The pre-migration bytes are preserved verbatim in the write-once sibling
	// backup, keyed by the version being migrated away from (v1).
	bak := statePath + ".pre-v1.bak"
	bakData, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("read backup %s: %v", bak, err)
	}
	if !bytes.Equal(bakData, original) {
		t.Fatalf("backup is not byte-identical to the pre-migration file:\n got %q\nwant %q", bakData, original)
	}

	// And the reopened store still reproduces exactly the same applied map — no
	// entry was silently dropped by the rewrite.
	reopened, err := OpenStoreAt(dir)
	if err != nil {
		t.Fatalf("reopen after migration: %v", err)
	}
	for name, hash := range want {
		if got, ok := reopened.LastApplied(name); !ok || got != hash {
			t.Fatalf("post-migration reopen LastApplied(%q) = %q,%v; want %q,true", name, got, ok, hash)
		}
	}
}

// TestSupersededSnapshotIsPruned proves the deployed map stays bounded: when a
// target is redeployed with new content, the old snapshot (now referenced by no
// applied hash) is pruned on save, so the baseline never grows without limit.
func TestSupersededSnapshotIsPruned(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStoreAt(dir)
	if err != nil {
		t.Fatalf("OpenStoreAt: %v", err)
	}

	first := []byte("v1\n")
	second := []byte("v2\n")
	if err := store.setDeployed("zshrc", hashBytes(first), first); err != nil {
		t.Fatalf("setDeployed first: %v", err)
	}
	if err := store.setDeployed("zshrc", hashBytes(second), second); err != nil {
		t.Fatalf("setDeployed second: %v", err)
	}

	got, ok := store.LastDeployedSnapshot("zshrc")
	if !ok || !bytes.Equal(got, second) {
		t.Fatalf("snapshot = %q,%v; want the latest %q", got, ok, second)
	}

	// The superseded first snapshot must be gone from disk after the prune.
	raw, _ := os.ReadFile(filepath.Join(dir, stateFileName))
	var env lastAppliedFile
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := env.Deployed[hashBytes(first)]; present {
		t.Fatalf("superseded snapshot for hash %s was not pruned", hashBytes(first))
	}
	if len(env.Deployed) != 1 {
		t.Fatalf("deployed map has %d entries, want 1 (only the current content)", len(env.Deployed))
	}
}
