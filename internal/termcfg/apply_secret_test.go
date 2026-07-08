package termcfg

// fn-4.5 / F2: a secret-routed terminal target must be materialised mode 0600 —
// its Content is ALREADY-RENDERED plaintext (a {{ferry.secret}} placeholder was
// substituted), so a world-/group-readable file would leak the credential. A
// plain terminal file keeps 0644; an executable one keeps its exec bit but, when
// secret-routed, drops group/other access (0700).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/REPPL/ferry/internal/dotfile"
)

// permBackuper writes through to disk, honouring the requested mode, so a test
// can inspect the deployed file's permissions.
type permBackuper struct{}

func (permBackuper) BackupAndWrite(target string, content []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(target, content, perm); err != nil {
		return err
	}
	return os.Chmod(target, perm)
}

func applyOne(t *testing.T, home string, it Item) os.FileMode {
	t.Helper()
	store, err := dotfile.OpenStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Production reaches dotfile.ApplyContentDeferred directly via the converged
	// kindFile apply arm; drive that same core here with the terminal domain's
	// first-write mode policy (filePerm/execPerm keyed on Item.Exec).
	perm := filePerm
	if it.Exec {
		perm = execPerm
	}
	if _, err := dotfile.ApplyContentDeferred(it.Target, it.Content, perm, store, permBackuper{}, false, it.SecretRouted); err != nil {
		t.Fatalf("ApplyContentDeferred: %v", err)
	}
	fi, err := os.Stat(it.Target.Home)
	if err != nil {
		t.Fatalf("stat deployed target: %v", err)
	}
	return fi.Mode().Perm()
}

func TestApplyItemSecretRoutedWrites0600(t *testing.T) {
	home := t.TempDir()

	secretMode := applyOne(t, home, Item{
		Key:          "terminals/wezterm",
		Label:        "terminals:wezterm",
		Target:       dotfile.Target{Name: "terminals/wezterm", Home: filepath.Join(home, ".wezterm.lua")},
		Content:      []byte("token = REAL-RENDERED-SECRET\n"),
		SecretRouted: true,
	})
	if secretMode != 0o600 {
		t.Fatalf("secret-routed terminal mode = %o, want 0600", secretMode)
	}

	plainMode := applyOne(t, home, Item{
		Key:     "terminals/kitty",
		Label:   "terminals:kitty",
		Target:  dotfile.Target{Name: "terminals/kitty", Home: filepath.Join(home, ".config/kitty/kitty.conf")},
		Content: []byte("font_size 12\n"),
	})
	if plainMode != 0o644 {
		t.Fatalf("plain terminal mode = %o, want 0644", plainMode)
	}

	execSecretMode := applyOne(t, home, Item{
		Key:          "terminals/hook",
		Label:        "terminals:hook",
		Target:       dotfile.Target{Name: "terminals/hook", Home: filepath.Join(home, ".config/term/hook.sh")},
		Content:      []byte("#!/bin/sh\necho REAL-RENDERED-SECRET\n"),
		Exec:         true,
		SecretRouted: true,
	})
	if execSecretMode != 0o700 {
		t.Fatalf("secret-routed executable terminal mode = %o, want 0700 (exec bit kept, group/other stripped)", execSecretMode)
	}
}

// TestApplyItemSecretRoutedClampsExistingMode is the regression for the
// preserve-existing-mode leak (the finding's ~/.wezterm.lua case): adopting an
// existing world-readable 0644 terminal config whose repo source is now
// secret-routed must NOT keep 0644. Mode preservation is overridden for a secret
// target, so the rendered plaintext lands 0600 instead of group-/world-readable.
func TestApplyItemSecretRoutedClampsExistingMode(t *testing.T) {
	home := t.TempDir()
	dst := filepath.Join(home, ".wezterm.lua")

	// A pre-existing, world-readable terminal config the user already had on disk.
	if err := os.WriteFile(dst, []byte("-- old config\n"), 0o644); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	store, err := dotfile.OpenStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// No last-applied recorded and live differs from the repo content, so this is a
	// first-touch adoption: force it through (a default apply would refuse as a
	// capture candidate) to exercise the write-over-existing-file branch. Drive the
	// shared apply core directly, exactly as the converged kindFile arm does.
	it := Item{
		Key:          "terminals/wezterm",
		Label:        "terminals:wezterm",
		Target:       dotfile.Target{Name: "terminals/wezterm", Home: dst},
		Content:      []byte("token = REAL-RENDERED-SECRET\n"),
		SecretRouted: true,
	}
	perm := filePerm
	if it.Exec {
		perm = execPerm
	}
	if _, err := dotfile.ApplyContentDeferred(it.Target, it.Content, perm, store, permBackuper{}, true, it.SecretRouted); err != nil {
		t.Fatalf("ApplyContentDeferred: %v", err)
	}

	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat deployed target: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret-routed adoption over an existing 0644 file = %o, want 0600 (group/other stripped, not preserved)", fi.Mode().Perm())
	}
}
