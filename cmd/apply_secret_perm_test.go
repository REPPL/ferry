package cmd

// fn-4.5 / F2: a secret-routed deployed target must land mode 0600 — its bytes
// are ALREADY-RENDERED plaintext secrets, so a world-/group-readable file would
// leave a credential readable by every account on the machine. A plain
// (non-secret) target keeps the ordinary 0644 fresh-write mode.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/REPPL/ferry/internal/dotfile"
)

func TestApplyTargetSecretRoutedWrites0600(t *testing.T) {
	home := t.TempDir()

	secretTarget := dotfile.Target{
		Name:    "gitconfig",
		Home:    filepath.Join(home, ".gitconfig"),
		Overlay: dotfile.OverlayWholeFileReplace,
	}
	plainTarget := dotfile.Target{
		Name:    "vimrc",
		Home:    filepath.Join(home, ".vimrc"),
		Overlay: dotfile.OverlayWholeFileReplace,
	}

	b := backuperFunc(func(dest string, content []byte, perm os.FileMode) error {
		if err := os.WriteFile(dest, content, perm); err != nil {
			return err
		}
		return os.Chmod(dest, perm)
	})
	store, err := dotfile.OpenStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	secretItem := &planItem{
		target:       secretTarget,
		content:      []byte("[github]\n\ttoken = super-secret-rendered-value\n"),
		secretRouted: true,
	}
	if _, err := dotfile.ApplyContentDeferred(secretItem.target, secretItem.content, dotfile.DefaultPerm(), store, b, false, secretItem.secretRouted); err != nil {
		t.Fatalf("ApplyContentDeferred (secret): %v", err)
	}
	fi, err := os.Stat(secretTarget.Home)
	if err != nil {
		t.Fatalf("stat secret target: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("secret-routed target mode = %o, want 0600 (rendered plaintext must not be world/group readable)", perm)
	}

	plainItem := &planItem{
		target:       plainTarget,
		content:      []byte("set number\n"),
		secretRouted: false,
	}
	if _, err := dotfile.ApplyContentDeferred(plainItem.target, plainItem.content, dotfile.DefaultPerm(), store, b, false, plainItem.secretRouted); err != nil {
		t.Fatalf("ApplyContentDeferred (plain): %v", err)
	}
	fi, err = os.Stat(plainTarget.Home)
	if err != nil {
		t.Fatalf("stat plain target: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o644 {
		t.Fatalf("plain target mode = %o, want 0644 (unchanged default fresh-write mode)", perm)
	}
}
