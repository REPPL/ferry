package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This file is a CHARACTERIZATION (golden) test: it pins the EXACT observable
// output of `ferry agents scaffold` for each of the three modes — tracked
// (default), private (--private), and --attribution — so that a later
// behaviour-preserving refactor of scaffold.go can be proven not to change
// what scaffold produces. It asserts three things per mode:
//
//  1. the EXACT set of filesystem paths the run creates (every dir, file, and
//     symlink under the repo, excluding .git/), and
//  2. the EXACT stdout, including every created:/exists:/linked:/… line and
//     their order, with the repo's absolute path normalised to <repo>, and
//  3. the EXACT line excludeWorkLocal writes into .git/info/exclude
//     (.abcd/.work.local/).
//
// Every mode runs in a REAL git repo so the exclude write and (for
// --attribution) the core.hooksPath config are exercised for real; the tests
// skip when git is unavailable. If any of these goldens change, the refactor
// changed behaviour — which this phase forbids.

// goldenRepo builds the standard templates dir plus a REAL git repo (needed so
// excludeWorkLocal writes a real info/exclude and --attribution can set
// core.hooksPath). It returns the templates dir and the initialised repo.
func goldenRepo(t *testing.T) (templates, repo string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	templates = scaffoldTemplatesOnly(t)
	repo = t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return templates, repo
}

// createdPaths walks repo and returns every path it contains, relative to repo
// and slash-separated, EXCLUDING the .git/ tree (git's own bookkeeping is not
// scaffold's output). Symlinks are recorded as leaves, never followed.
func createdPaths(t *testing.T, repo string) []string {
	t.Helper()
	var got []string
	err := filepath.WalkDir(repo, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(repo, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" {
			return filepath.SkipDir
		}
		got = append(got, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	sort.Strings(got)
	return got
}

// normaliseOut replaces the repo's absolute path with <repo> so the golden
// output is stable across temp dirs.
func normaliseOut(out, repo string) string {
	return strings.ReplaceAll(out, repo, "<repo>")
}

// excludeWorkLocalLines returns the count of exact ".abcd/.work.local/" lines
// in the repo's .git/info/exclude — the line excludeWorkLocal is contracted to
// write exactly once.
func excludeWorkLocalLines(t *testing.T, repo string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("read info/exclude: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if line == ".abcd/.work.local/" {
			n++
		}
	}
	return n
}

func assertGolden(t *testing.T, repo, gotOut string, wantPaths []string, wantOut string) {
	t.Helper()
	gotPaths := createdPaths(t, repo)
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Errorf("created path set changed (behaviour NOT preserved):\n got: %v\nwant: %v", gotPaths, wantPaths)
	}
	if norm := normaliseOut(gotOut, repo); norm != wantOut {
		t.Errorf("stdout changed (behaviour NOT preserved):\n got:\n%s\nwant:\n%s", norm, wantOut)
	}
	if n := excludeWorkLocalLines(t, repo); n != 1 {
		t.Errorf("info/exclude has %d %q lines, want exactly 1", n, ".abcd/.work.local/")
	}
}

// TestScaffoldGolden_Tracked pins the default (tracked) mode.
func TestScaffoldGolden_Tracked(t *testing.T) {
	templates, repo := goldenRepo(t)
	out := runScaffold(t, ScaffoldOptions{
		RepoDir: repo, Name: "goldenproj", TemplatesDir: templates, Date: "2026-07-05",
	})

	wantPaths := []string{
		".abcd",
		".abcd/.work.local",
		".abcd/.work.local/NEXT.md",
		".abcd/.work.local/logs",
		".abcd/.work.local/scratch",
		".abcd/development",
		".abcd/development/decisions",
		".abcd/development/plans",
		".abcd/development/research",
		".abcd/work",
		".abcd/work/CONTEXT.md",
		".abcd/work/DECISIONS.md",
		".pre-commit-config.yaml",
		"AGENTS.md",
		"CLAUDE.md",
		"GEMINI.md",
		"docs",
		"docs/README.md",
	}
	wantOut := "excluded: .abcd/.work.local/ via git info/exclude (local-only, not committed)\n" +
		"created:  <repo>/.abcd/.work.local/NEXT.md\n" +
		"created:  <repo>/.abcd/work/DECISIONS.md\n" +
		"created:  <repo>/.abcd/work/CONTEXT.md\n" +
		"created:  <repo>/AGENTS.md\n" +
		"created:  <repo>/docs/README.md\n" +
		"linked:   CLAUDE.md -> AGENTS.md\n" +
		"linked:   GEMINI.md -> AGENTS.md\n" +
		"created:  .pre-commit-config.yaml (activate with: pre-commit install)\n" +
		"done: goldenproj — now fill in the placeholder sections of AGENTS.md\n"

	assertGolden(t, repo, out, wantPaths, wantOut)
}

// TestScaffoldGolden_Private pins --private (the .abcd/.work.local/ layer only).
func TestScaffoldGolden_Private(t *testing.T) {
	templates, repo := goldenRepo(t)
	out := runScaffold(t, ScaffoldOptions{
		RepoDir: repo, Name: "goldenproj", Private: true, TemplatesDir: templates, Date: "2026-07-05",
	})

	wantPaths := []string{
		".abcd",
		".abcd/.work.local",
		".abcd/.work.local/CONTEXT.md",
		".abcd/.work.local/DECISIONS.md",
		".abcd/.work.local/ISSUES.md",
		".abcd/.work.local/NEXT.md",
		".abcd/.work.local/logs",
		".abcd/.work.local/scratch",
	}
	wantOut := "excluded: .abcd/.work.local/ via git info/exclude (local-only, not committed)\n" +
		"created:  <repo>/.abcd/.work.local/NEXT.md\n" +
		"created:  <repo>/.abcd/.work.local/DECISIONS.md\n" +
		"created:  <repo>/.abcd/.work.local/CONTEXT.md\n" +
		"created:  <repo>/.abcd/.work.local/ISSUES.md\n" +
		"done: goldenproj (private mode — no tracked files were created or modified)\n"

	assertGolden(t, repo, out, wantPaths, wantOut)
}

// TestScaffoldGolden_Attribution pins --attribution (tracked layer plus the
// prepare-commit-msg hook, core.hooksPath, and the AGENTS.md policy section).
func TestScaffoldGolden_Attribution(t *testing.T) {
	templates, repo := goldenRepo(t)
	out := runScaffold(t, ScaffoldOptions{
		RepoDir: repo, Name: "goldenproj", Attribution: true, TemplatesDir: templates, Date: "2026-07-05",
	})

	wantPaths := []string{
		".abcd",
		".abcd/.work.local",
		".abcd/.work.local/NEXT.md",
		".abcd/.work.local/logs",
		".abcd/.work.local/scratch",
		".abcd/development",
		".abcd/development/decisions",
		".abcd/development/plans",
		".abcd/development/research",
		".abcd/work",
		".abcd/work/CONTEXT.md",
		".abcd/work/DECISIONS.md",
		".githooks",
		".githooks/prepare-commit-msg",
		".pre-commit-config.yaml",
		"AGENTS.md",
		"CLAUDE.md",
		"GEMINI.md",
		"docs",
		"docs/README.md",
	}
	wantOut := "excluded: .abcd/.work.local/ via git info/exclude (local-only, not committed)\n" +
		"created:  <repo>/.abcd/.work.local/NEXT.md\n" +
		"created:  <repo>/.abcd/work/DECISIONS.md\n" +
		"created:  <repo>/.abcd/work/CONTEXT.md\n" +
		"created:  <repo>/AGENTS.md\n" +
		"created:  <repo>/docs/README.md\n" +
		"linked:   CLAUDE.md -> AGENTS.md\n" +
		"linked:   GEMINI.md -> AGENTS.md\n" +
		"created:  .pre-commit-config.yaml (activate with: pre-commit install)\n" +
		"created:  <repo>/.githooks/prepare-commit-msg\n" +
		"enabled:  core.hooksPath .githooks (re-run this config per clone)\n" +
		"updated:  AGENTS.md (AI attribution section)\n" +
		"done: goldenproj — now fill in the placeholder sections of AGENTS.md\n"

	assertGolden(t, repo, out, wantPaths, wantOut)
}
