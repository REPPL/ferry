package cmd

// End-to-end coverage for the import post-error ROLLBACK finding: after the staging
// tree is renamed into place, the only remaining ferry-owned write is
// SaveMachineConfig. If THAT fails, the just-placed target must be rolled back
// (os.RemoveAll) so a re-run starts clean and no populated-but-unconfigured repo is
// left behind. We force the config write to fail by planting a REGULAR FILE at
// $HOME/.config so creating $HOME/.config/ferry/config.toml cannot succeed.
//
// This drives the REAL runImport over a REAL bundle (git init/add/commit run inside
// staging before the rename), so it also exercises the staging-before-rename order.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/bundle"
)

// newTestImportCmd returns a standalone cobra command carrying the same flags
// runImport reads, so a test can drive runImport directly without the global
// importCmd (which the root command owns).
func newTestImportCmd() *cobra.Command {
	c := &cobra.Command{Use: "import"}
	c.Flags().String("out", "", "")
	c.Flags().String("expect-sha256", "", "")
	c.Flags().Bool("include-local", false, "")
	return c
}

func TestImportRollsBackTargetOnConfigWriteFailure(t *testing.T) {
	// t.Setenv forces non-parallel and points ferry's config dir at a throwaway HOME.
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Build a real, valid bundle: a required ferry.toml plus one shared file.
	bundleDir := t.TempDir()
	toml := filepath.Join(bundleDir, "ferry.toml")
	shared := filepath.Join(bundleDir, "shared.txt")
	if err := os.WriteFile(toml, []byte("# ferry manifest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "b.zip")
	if _, err := bundle.Write(bundlePath, "0.2.0-test", false, []bundle.Source{
		{RelPath: "ferry.toml", AbsPath: toml, Data: []byte("# ferry manifest\n")},
		{RelPath: "shared.txt", AbsPath: shared, Data: []byte("hello\n")},
	}); err != nil {
		t.Fatalf("build bundle: %v", err)
	}

	// Sabotage the config write: a regular file at $HOME/.config makes MkdirAll of
	// $HOME/.config/ferry fail, so SaveMachineConfig errors AFTER the rename.
	if err := os.WriteFile(filepath.Join(home, ".config"), []byte("not a dir\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// An absent target under a temp parent so the import creates it fresh.
	target := filepath.Join(t.TempDir(), "repo")

	c := newTestImportCmd()
	_ = c.Flags().Set("out", target)
	err := runImport(c, []string{bundlePath})
	if err == nil {
		t.Fatalf("expected runImport to fail on the sabotaged config write")
	}

	// ROLLBACK: the target must be gone (or empty) — no populated-but-unconfigured
	// repo left behind, so a re-run starts clean.
	if ents, rerr := os.ReadDir(target); rerr == nil && len(ents) != 0 {
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("config-write failure left a populated target %q: %v (must roll back)", target, names)
	}

	// No orphan staging dir should linger in the target's parent either.
	parent := filepath.Dir(target)
	pents, _ := os.ReadDir(parent)
	for _, e := range pents {
		if e.IsDir() && len(e.Name()) >= len("ferry-import-") && e.Name()[:len("ferry-import-")] == "ferry-import-" {
			t.Errorf("orphan staging dir left behind: %s", filepath.Join(parent, e.Name()))
		}
	}
}
