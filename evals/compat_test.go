package evals

// Cross-version compatibility eval. It drives the CURRENT ferry binary against a
// REAL state directory produced by the v0.3.2 binary (tag v0.3.2, commit
// cd648ae) and asserts the pre-1.0 on-disk compatibility contract end to end:
//
//   - current code reads the v0.3.x state, migrates the last-applied store
//     forward (preserving the pre-migration file in a sibling backup), and
//   - `ferry restore` still reverses correctly from that state, returning the
//     managed file to its true pre-ferry content recorded in the v0.3.x baseline.
//
// The committed fixture (evals/testdata/compat/v032-state) is scrubbed of the
// generating machine's HOME: baseline/journal metadata carry a "__HOME__"
// placeholder that this eval reconstructs against the sandbox HOME (and re-keys
// the baseline metadata filename, which is the sha256 of the absolute path).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// versionField is the observable marker of ferry's versioned state-file envelope:
// a top-level "version" key. The eval is black-box (it never imports ferry's
// source), so it checks for the field's presence in the raw JSON rather than
// reusing the internal parser.
var versionField = []byte(`"version"`)

const (
	// The content the v0.3.2 run applied to ~/.zshrc (its recorded last-applied).
	compatAppliedZshrc = "export EDITOR=vim_from_repo\n"
	// The TRUE pre-ferry content the v0.3.2 baseline captured; restore reverses here.
	compatOriginalZshrc = "export EDITOR=ORIGINAL_v032\n"
)

// installV032State copies the committed v0.3.x state fixture into the sandbox
// state dir, substituting the sandbox HOME for the "__HOME__" placeholder in
// every JSON metadata file and re-keying each baseline metadata file to the
// sha256 of its (now absolute) path, exactly as the current engine expects.
func installV032State(t *testing.T, s *Sandbox) {
	t.Helper()
	src := filepath.Join("testdata", "compat", "v032-state")
	dst := s.StateDir()
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		outPath := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
			return err
		}
		if strings.HasSuffix(rel, ".json") {
			data = []byte(strings.ReplaceAll(string(data), "__HOME__", s.Home))
			// A baseline metadata file's name is the sha256 of the absolute path
			// it records; re-key it so the current restore engine finds it.
			if strings.HasPrefix(rel, "baseline"+string(os.PathSeparator)) {
				var meta struct {
					Path string `json:"path"`
				}
				if json.Unmarshal(data, &meta) == nil && meta.Path != "" {
					key := sha256.Sum256([]byte(filepath.Clean(meta.Path)))
					outPath = filepath.Join(dst, "baseline", hex.EncodeToString(key[:])+".json")
				}
			}
		}
		return os.WriteFile(outPath, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("install v0.3.2 state: %v", err)
	}
}

// TestCompatV032StateMigratesAndRestores is the W4 end-to-end gate.
func TestCompatV032StateMigratesAndRestores(t *testing.T) {
	requireBin(t)
	t.Parallel()
	s := NewSandbox(t)

	// A coherent manifest so `ferry apply` is a clean no-op content-wise: the repo
	// source and the live file both equal the v0.3.x last-applied content.
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", compatAppliedZshrc)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), compatAppliedZshrc)
	zshrc := s.HomePath(".zshrc")
	if err := os.WriteFile(zshrc, []byte(compatAppliedZshrc), 0o644); err != nil {
		t.Fatalf("seed live .zshrc: %v", err)
	}

	installV032State(t, s)

	lastApplied := filepath.Join(s.StateDir(), "dotfile-last-applied.json")
	before, err := os.ReadFile(lastApplied)
	if err != nil {
		t.Fatalf("read seeded last-applied: %v", err)
	}
	if bytes.Contains(before, versionField) {
		t.Fatal("precondition: the v0.3.x fixture must be UNVERSIONED before apply")
	}

	// (1) apply must succeed and migrate the last-applied store forward.
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply over v0.3.x state exited %d; stderr:\n%s", code, errOut)
	}
	after, err := os.ReadFile(lastApplied)
	if err != nil {
		t.Fatalf("read migrated last-applied: %v", err)
	}
	if !bytes.Contains(after, versionField) {
		t.Fatalf("apply did not migrate the last-applied store to the versioned form:\n%s", after)
	}
	// The pre-migration file is preserved in a sibling backup, byte-for-byte.
	bak, err := os.ReadFile(lastApplied + ".pre-v1.bak")
	if err != nil {
		t.Fatalf("migration backup missing: %v", err)
	}
	if string(bak) != string(before) {
		t.Fatalf("migration backup is not the original v0.3.x file")
	}

	// (2) restore must still reverse correctly from the v0.3.x baseline.
	if _, errOut, code := s.Ferry("restore"); code != 0 {
		t.Fatalf("restore over v0.3.x state exited %d; stderr:\n%s", code, errOut)
	}
	got, err := os.ReadFile(zshrc)
	if err != nil {
		t.Fatalf("read .zshrc after restore: %v", err)
	}
	if string(got) != compatOriginalZshrc {
		t.Fatalf("restore did not reverse from v0.3.x baseline: .zshrc = %q, want %q", got, compatOriginalZshrc)
	}
}
