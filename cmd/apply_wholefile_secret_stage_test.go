package cmd

// WS4(a): applyTarget must deploy a WHOLE-FILE-REPLACE dotfile (e.g. .gitconfig)
// through the IN-MEMORY apply core too, never staging its effective bytes to a
// $TMPDIR temp file. A generic dotfile defaults to OverlayWholeFileReplace and is
// arguably the most likely to carry a {{ferry.secret}} token (a GitHub token in
// .gitconfig), so for a secret-routed target those bytes are ALREADY-RENDERED
// plaintext; a crash between staging and cleanup would leave the secret at rest in
// /tmp. The test points $TMPDIR at an empty dir and inspects it at the moment of
// the deploy write (inside the Backuper), when the pre-fix stageContent temp would
// still exist (its cleanup was deferred until applyTarget returned).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/REPPL/ferry/internal/dotfile"
)

func TestApplyTargetWholeFileSecretRoutedStagesNoTempFile(t *testing.T) {
	// t.Setenv forces non-parallel; point os.TempDir() at a throwaway dir so any
	// staging temp lands here and is observable.
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	home := t.TempDir()
	target := dotfile.Target{
		Name:    "gitconfig",
		Home:    filepath.Join(home, ".gitconfig"),
		Overlay: dotfile.OverlayWholeFileReplace,
	}

	plaintext := []byte("[github]\n\ttoken = super-secret-rendered-value\n")
	it := &planItem{target: target, content: plaintext, secretRouted: true}

	// The Backuper stands in for the transactional engine. At the instant it is
	// called to materialise the file, no staging temp for the effective content
	// must exist in TMPDIR.
	var tmpEntriesAtWrite []string
	b := backuperFunc(func(dest string, content []byte, perm os.FileMode) error {
		entries, err := os.ReadDir(tmpDir)
		if err != nil {
			t.Fatalf("read TMPDIR at write time: %v", err)
		}
		for _, e := range entries {
			tmpEntriesAtWrite = append(tmpEntriesAtWrite, e.Name())
		}
		return os.WriteFile(dest, content, perm)
	})

	store, err := dotfile.OpenStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	res, err := applyTarget(it, store, b, false)
	if err != nil {
		t.Fatalf("applyTarget: %v", err)
	}
	if res.Action != dotfile.ActionCreated {
		t.Fatalf("Action = %q, want created (fresh missing target should be written)", res.Action)
	}

	if len(tmpEntriesAtWrite) != 0 {
		t.Fatalf("applyTarget staged %d file(s) in $TMPDIR during a whole-file secret-routed deploy: %v; "+
			"the rendered plaintext must never touch /tmp", len(tmpEntriesAtWrite), tmpEntriesAtWrite)
	}

	// The effective plaintext still reaches the home destination (behaviour preserved).
	got, err := os.ReadFile(target.Home)
	if err != nil {
		t.Fatalf("read deployed target: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("deployed content = %q, want %q", got, plaintext)
	}
}
