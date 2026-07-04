package agents

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scaffoldFixture builds a templates dir with the standard templates and a
// project repo dir (with a .git/ marker when git is true).
func scaffoldFixture(t *testing.T, git bool) (templates, repo string) {
	t.Helper()
	templates = t.TempDir()
	for name, content := range map[string]string{
		"AGENTS.md":              "# {{PROJECT}}\n\nCreated {{DATE}}.\n",
		"NEXT.md":                "# Next — {{PROJECT}}\n",
		"DECISIONS.md":           "# Decisions — {{PROJECT}} ({{DATE}})\n",
		"ISSUES.md":              "# Issues — {{PROJECT}}\n",
		"pre-commit-config.yaml": "repos: []\n",
	} {
		if err := os.WriteFile(filepath.Join(templates, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo = t.TempDir()
	if git {
		if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return templates, repo
}

func runScaffold(t *testing.T, opts ScaffoldOptions) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Scaffold(opts, &buf); err != nil {
		t.Fatalf("Scaffold: %v\noutput:\n%s", err, buf.String())
	}
	return buf.String()
}

func TestScaffoldTrackedMode(t *testing.T) {
	templates, repo := scaffoldFixture(t, true)
	out := runScaffold(t, ScaffoldOptions{
		RepoDir: repo, TemplatesDir: templates, Date: "2026-07-04",
	})

	name := filepath.Base(repo)
	agentsMD, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "# " + name + "\n\nCreated 2026-07-04.\n"; string(agentsMD) != want {
		t.Errorf("AGENTS.md = %q, want %q", agentsMD, want)
	}

	for _, rel := range []string{".work/NEXT.md", ".work/DECISIONS.md", ".work/scratch", ".work/logs", ".pre-commit-config.yaml"} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Errorf("%s missing: %v", rel, err)
		}
	}

	for _, link := range []string{"CLAUDE.md", "GEMINI.md"} {
		target, err := os.Readlink(filepath.Join(repo, link))
		if err != nil {
			t.Errorf("%s is not a symlink: %v", link, err)
			continue
		}
		if target != "AGENTS.md" {
			t.Errorf("%s -> %q, want the RELATIVE target AGENTS.md", link, target)
		}
	}

	gi, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gi), ".work/scratch/") || !strings.Contains(string(gi), ".work/logs/") {
		t.Errorf(".gitignore missing scratch entries: %q", gi)
	}
	if !strings.Contains(out, "done: "+name) {
		t.Errorf("output missing done line: %q", out)
	}
}

func TestScaffoldSubstitutionIsMetacharSafe(t *testing.T) {
	templates, repo := scaffoldFixture(t, false)
	// A name full of sed/regex metacharacters must land verbatim.
	name := `a&b/c\d|e$1`
	runScaffold(t, ScaffoldOptions{
		RepoDir: repo, Name: name, TemplatesDir: templates, Date: "2026-07-04",
	})
	agentsMD, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agentsMD), "# "+name+"\n") {
		t.Errorf("metacharacter name corrupted: %q", agentsMD)
	}
}

func TestScaffoldIsIdempotentAndNeverOverwrites(t *testing.T) {
	templates, repo := scaffoldFixture(t, true)
	runScaffold(t, ScaffoldOptions{RepoDir: repo, TemplatesDir: templates, Date: "2026-07-04"})

	// User content added after the first run must survive a re-run untouched.
	custom := []byte("user edits\n")
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), custom, 0o644); err != nil {
		t.Fatal(err)
	}
	giBefore, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}

	out := runScaffold(t, ScaffoldOptions{RepoDir: repo, TemplatesDir: templates, Date: "2026-07-05"})
	after, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, custom) {
		t.Errorf("re-run overwrote AGENTS.md: %q", after)
	}
	if !strings.Contains(out, "exists:") {
		t.Errorf("re-run did not report skips: %q", out)
	}
	giAfter, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(giBefore, giAfter) {
		t.Errorf(".gitignore grew on re-run:\n%q\nvs\n%q", giBefore, giAfter)
	}
}

func TestScaffoldSkipsRealFileInLinkPosition(t *testing.T) {
	templates, repo := scaffoldFixture(t, false)
	real := []byte("a real CLAUDE.md\n")
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), real, 0o644); err != nil {
		t.Fatal(err)
	}
	out := runScaffold(t, ScaffoldOptions{RepoDir: repo, TemplatesDir: templates, Date: "2026-07-04"})
	got, err := os.ReadFile(filepath.Join(repo, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, real) {
		t.Errorf("real CLAUDE.md was replaced: %q", got)
	}
	if !strings.Contains(out, "CLAUDE.md is a real file (skipped") {
		t.Errorf("output missing the real-file skip: %q", out)
	}
}

func TestScaffoldPrivateMode(t *testing.T) {
	templates, repo := scaffoldFixture(t, true)
	out := runScaffold(t, ScaffoldOptions{
		RepoDir: repo, Private: true, TemplatesDir: templates, Date: "2026-07-04",
	})

	for _, rel := range []string{".work.local/NEXT.md", ".work.local/DECISIONS.md", ".work.local/ISSUES.md", ".work.local/scratch", ".work.local/logs"} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Errorf("%s missing: %v", rel, err)
		}
	}
	// Zero tracked trace: none of the tracked-mode artefacts may exist.
	for _, rel := range []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md", ".gitignore", ".work", ".pre-commit-config.yaml"} {
		if _, err := os.Lstat(filepath.Join(repo, rel)); err == nil {
			t.Errorf("private mode created tracked artefact %s", rel)
		}
	}
	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exclude), ".work.local/") {
		t.Errorf(".git/info/exclude missing entry: %q", exclude)
	}
	if !strings.Contains(out, "private mode") {
		t.Errorf("output missing private-mode done line: %q", out)
	}

	// Re-run: the exclude entry must not duplicate.
	runScaffold(t, ScaffoldOptions{RepoDir: repo, Private: true, TemplatesDir: templates, Date: "2026-07-04"})
	exclude2, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(exclude2), ".work.local/") != 1 {
		t.Errorf("exclude entry duplicated: %q", exclude2)
	}
}

func TestScaffoldPrivateModeWarnsWithoutGit(t *testing.T) {
	templates, repo := scaffoldFixture(t, false)
	out := runScaffold(t, ScaffoldOptions{
		RepoDir: repo, Private: true, TemplatesDir: templates, Date: "2026-07-04",
	})
	if !strings.Contains(out, "WARN: not a git repo") {
		t.Errorf("output missing the no-git warning: %q", out)
	}
}

func TestScaffoldMissingTemplateIsClearError(t *testing.T) {
	templates := t.TempDir() // empty: no templates at all
	repo := t.TempDir()
	var buf bytes.Buffer
	err := Scaffold(ScaffoldOptions{RepoDir: repo, TemplatesDir: templates, Date: "2026-07-04"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "missing from the config repo") {
		t.Errorf("err = %v, want a missing-template error naming the config repo", err)
	}
}
