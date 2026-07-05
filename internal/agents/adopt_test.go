package agents

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/config"
)

func TestImportSST(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()

	write := func(rel, content string, perm os.FileMode) {
		t.Helper()
		path := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), perm); err != nil {
			t.Fatal(err)
		}
	}
	write("general.md", "GENERAL\n", 0o644)
	write("coding.md", "CODING\n", 0o644)
	write("templates/AGENTS.md", "# {{PROJECT}}\n", 0o644)
	write("skills/demo/SKILL.md", "skill\n", 0o644)
	write("hooks/guard.sh", "#!/bin/sh\n", 0o755)
	// Pieces that must NOT be imported: a generated combined.md and the old
	// scripts live outside the imported set.
	write("combined.md", "GENERATED\n", 0o644)
	write("bin/sync.sh", "#!/bin/sh\n", 0o755)

	var buf bytes.Buffer
	if err := ImportSST(src, dest, nil, &buf); err != nil {
		t.Fatalf("ImportSST: %v", err)
	}

	for rel, want := range map[string]string{
		"general.md":           "GENERAL\n",
		"coding.md":            "CODING\n",
		"templates/AGENTS.md":  "# {{PROJECT}}\n",
		"skills/demo/SKILL.md": "skill\n",
		"hooks/guard.sh":       "#!/bin/sh\n",
	} {
		got, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", rel, got, err, want)
		}
	}
	fi, err := os.Stat(filepath.Join(dest, "hooks", "guard.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("imported hook lost its executable bit: %v", fi.Mode())
	}
	for _, rel := range []string{"combined.md", "bin/sync.sh"} {
		if _, err := os.Lstat(filepath.Join(dest, rel)); err == nil {
			t.Errorf("%s was imported but must not be", rel)
		}
	}

	// Source must be untouched (non-destructive).
	if _, err := os.Stat(filepath.Join(src, "general.md")); err != nil {
		t.Errorf("source file went missing: %v", err)
	}
}

func TestImportSSTNeverOverwritesDifferingRepoFile(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "general.md"), []byte("from src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoVersion := []byte("repo edit\n")
	if err := os.WriteFile(filepath.Join(dest, "general.md"), repoVersion, 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := ImportSST(src, dest, nil, &buf); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "general.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, repoVersion) {
		t.Errorf("repo file was overwritten: %q", got)
	}
	if !strings.Contains(buf.String(), "differs — reconcile manually") {
		t.Errorf("output missing the differs skip: %q", buf.String())
	}

	// Idempotence: identical files re-import silently.
	buf.Reset()
	if err := os.WriteFile(filepath.Join(dest, "general.md"), []byte("from src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ImportSST(src, dest, nil, &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "general.md") {
		t.Errorf("identical file produced output: %q", buf.String())
	}
}

// refuseSymlinkComponents mimics the cmd layer's safeRepoPath for tests: it
// walks each component from root down to the candidate and refuses when any
// existing component is a symlink.
func refuseSymlinkComponents(t *testing.T, root string) func(string) (string, error) {
	t.Helper()
	return func(cand string) (string, error) {
		rel, err := filepath.Rel(root, cand)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", os.ErrPermission
		}
		cur := root
		for _, seg := range strings.Split(rel, string(os.PathSeparator)) {
			cur = filepath.Join(cur, seg)
			fi, lerr := os.Lstat(cur)
			if lerr != nil {
				break // not-yet-existing tail: nothing to traverse
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				return "", os.ErrPermission
			}
		}
		return cand, nil
	}
}

// TestImportSSTRefusesGuardedDestinations pins the destination guard: a
// symlink already inside the config repo's agents/ area (e.g. hooks ->
// somewhere outside) must be refused with a loud skip — never written
// THROUGH — while the untainted pieces still import.
func TestImportSSTRefusesGuardedDestinations(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	outside := t.TempDir()

	for rel, content := range map[string]string{
		"general.md":     "GENERAL\n",
		"hooks/guard.sh": "#!/bin/sh\n",
	} {
		path := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The POISONED repo destination: agents/hooks is a symlink out of the repo.
	if err := os.Symlink(outside, filepath.Join(dest, "hooks")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := ImportSST(src, dest, refuseSymlinkComponents(t, dest), &buf); err != nil {
		t.Fatalf("ImportSST: %v", err)
	}

	// Nothing may have been written through the symlink.
	ents, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Errorf("import wrote THROUGH the symlinked destination: %v", ents)
	}
	if !strings.Contains(buf.String(), "refused destination") {
		t.Errorf("output missing the loud refusal: %q", buf.String())
	}
	// The clean piece still imports.
	if got, err := os.ReadFile(filepath.Join(dest, "general.md")); err != nil || string(got) != "GENERAL\n" {
		t.Errorf("general.md = %q, %v; want imported", got, err)
	}
}

func TestFindBridges(t *testing.T) {
	home := t.TempDir()
	adopted := t.TempDir()

	// Populate the adopted dir so the links have real destinations.
	for _, rel := range []string{"general.md", "combined.md", "coding.md"} {
		if err := os.WriteFile(filepath.Join(adopted, rel), []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(adopted, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(adopted, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}

	link := func(target, linkPath string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, linkPath); err != nil {
			t.Fatal(err)
		}
	}

	// sync.sh-era bridges: harness files, a devtree file, a skill dir link,
	// and a whole-dir hooks link.
	link(filepath.Join(adopted, "general.md"), filepath.Join(home, ".claude", "CLAUDE.md"))
	link(filepath.Join(adopted, "combined.md"), filepath.Join(home, ".codex", "AGENTS.md"))
	link(filepath.Join(adopted, "coding.md"), filepath.Join(home, "Workspace", "CLAUDE.md"))
	link(filepath.Join(adopted, "skills", "demo"), filepath.Join(home, ".claude", "skills", "demo"))
	link(filepath.Join(adopted, "hooks"), filepath.Join(home, ".claude", "hooks"))
	// Distractors: a symlink to somewhere else, and a real file.
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "other.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link(filepath.Join(elsewhere, "other.md"), filepath.Join(home, ".gemini", "GEMINI.md"))
	if err := os.MkdirAll(filepath.Join(home, ".companion"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".companion", "COMPANION.md"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}

	bridges, err := FindBridges(home, adopted, config.AgentsConfig{Devtree: "Workspace"})
	if err != nil {
		t.Fatalf("FindBridges: %v", err)
	}
	got := map[string]bool{}
	gotDir := map[string]bool{}
	for _, b := range bridges {
		got[b.Path] = true
		gotDir[b.Path] = b.Dir
	}
	// Dir marks the directory-level links (a whole skill dir, the hooks dir);
	// file bridges stay migratable.
	want := map[string]bool{
		filepath.Join(home, ".claude", "CLAUDE.md"):      false,
		filepath.Join(home, ".codex", "AGENTS.md"):       false,
		filepath.Join(home, "Workspace", "CLAUDE.md"):    false,
		filepath.Join(home, ".claude", "skills", "demo"): true,
		filepath.Join(home, ".claude", "hooks"):          true,
	}
	if len(bridges) != len(want) {
		t.Errorf("found %d bridges (%v), want %d", len(bridges), got, len(want))
	}
	for p, wantDir := range want {
		if !got[p] {
			t.Errorf("bridge %s not found", p)
			continue
		}
		if gotDir[p] != wantDir {
			t.Errorf("bridge %s Dir = %v, want %v", p, gotDir[p], wantDir)
		}
	}
	if got[filepath.Join(home, ".gemini", "GEMINI.md")] {
		t.Error("a symlink pointing OUTSIDE the adopted dir was reported as a bridge")
	}
	if got[filepath.Join(home, ".companion", "COMPANION.md")] {
		t.Error("a REAL file was reported as a bridge")
	}
}

// TestFindBridgesDetectsDirectoryLevelBridges pins the ancestor scan: a setup
// that symlinked ~/.claude ITSELF (or another managed ancestor) into the
// instruction directory must surface as ONE directory-level bridge — never be
// written through — with anything nested under it dropped (removing the outer
// link retires the whole subtree).
func TestFindBridgesDetectsDirectoryLevelBridges(t *testing.T) {
	home := t.TempDir()
	adopted := t.TempDir()

	// The adopted dir doubles as the "claude home": it holds the files the
	// directory bridge exposes.
	if err := os.MkdirAll(filepath.Join(adopted, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adopted, "CLAUDE.md"), []byte("g"), 0o644); err != nil {
		t.Fatal(err)
	}
	// ~/.claude -> <adopted> (a DIRECTORY-level bridge).
	if err := os.Symlink(adopted, filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}

	bridges, err := FindBridges(home, adopted, config.AgentsConfig{})
	if err != nil {
		t.Fatalf("FindBridges: %v", err)
	}
	if len(bridges) != 1 {
		t.Fatalf("bridges = %+v, want exactly the one directory-level bridge", bridges)
	}
	if bridges[0].Path != filepath.Join(home, ".claude") {
		t.Errorf("bridge path = %s, want %s", bridges[0].Path, filepath.Join(home, ".claude"))
	}
	if !bridges[0].Dir {
		t.Error("directory-level bridge not marked Dir — the caller could not refuse it")
	}
}

// TestFindBridgesDropsNestedBridges: when both an ancestor and something the
// ancestor exposes would match, only the outermost link is reported.
func TestFindBridgesDropsNestedBridges(t *testing.T) {
	bridges := dropNestedBridges([]Bridge{
		{Path: "/home/u/.claude/skills/demo", Dest: "/sst/skills/demo"},
		{Path: "/home/u/.claude", Dest: "/sst"},
		{Path: "/home/u/.codex/AGENTS.md", Dest: "/sst/combined.md"},
	})
	if len(bridges) != 2 {
		t.Fatalf("bridges = %+v, want 2 (nested skills entry dropped)", bridges)
	}
	if bridges[0].Path != "/home/u/.claude" || bridges[1].Path != "/home/u/.codex/AGENTS.md" {
		t.Errorf("unexpected bridge set: %+v", bridges)
	}
}

// TestFindBridgesScansCustomAssetMappingTargets: a user-declared asset
// mapping's target directory is scanned for bridges exactly like the
// built-in ~/.claude locations — the scan set comes from the same resolved
// registry the planner deploys.
func TestFindBridgesScansCustomAssetMappingTargets(t *testing.T) {
	home := t.TempDir()
	adopted := t.TempDir()
	if err := os.MkdirAll(filepath.Join(adopted, "githooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The old setup symlinked ~/.githooks at the adopted dir's githooks tree.
	if err := os.Symlink(filepath.Join(adopted, "githooks"), filepath.Join(home, ".githooks")); err != nil {
		t.Fatal(err)
	}

	cfg := config.AgentsConfig{
		Asset: map[string]config.AgentsAsset{
			"githooks": {Source: "githooks", Target: ".githooks"},
		},
	}
	bridges, err := FindBridges(home, adopted, cfg)
	if err != nil {
		t.Fatalf("FindBridges: %v", err)
	}
	if len(bridges) != 1 || bridges[0].Path != filepath.Join(home, ".githooks") {
		t.Fatalf("bridges = %+v, want the ~/.githooks mapping bridge", bridges)
	}
	if !bridges[0].Dir {
		t.Error("directory-level mapping bridge not marked Dir")
	}

	// Without the mapping declared, the location is not scanned at all.
	none, err := FindBridges(home, adopted, config.AgentsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("undeclared mapping target was scanned: %+v", none)
	}
}

func TestFindBridgesResolvesRelativeLinks(t *testing.T) {
	home := t.TempDir()
	adopted := filepath.Join(home, "Workspace", ".agents")
	if err := os.MkdirAll(adopted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adopted, "general.md"), []byte("g"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A RELATIVE symlink into the adopted dir must be resolved from the
	// link's own directory.
	if err := os.Symlink(filepath.Join("..", "Workspace", ".agents", "general.md"),
		filepath.Join(home, ".claude", "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}

	bridges, err := FindBridges(home, adopted, config.AgentsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(bridges) != 1 || bridges[0].Path != filepath.Join(home, ".claude", "CLAUDE.md") {
		t.Errorf("bridges = %+v, want the relative-linked CLAUDE.md", bridges)
	}
}
