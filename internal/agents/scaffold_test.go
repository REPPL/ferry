package agents

import (
	"bytes"
	"os"
	"os/exec"
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
		"CONTEXT.md":             "# Context — {{PROJECT}}\n",
		"ISSUES.md":              "# Issues — {{PROJECT}}\n",
		"docs-README.md":         "# docs — map for {{PROJECT}}\n",
		"prepare-commit-msg":     "#!/bin/sh\n# hook for {{PROJECT}}\n",
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

// layoutDests derives the repo-relative put destinations the shared
// scaffoldLayout table produces for a scaffold mode, in table order — so the
// existence assertions track the single source of truth instead of re-listing
// paths by hand.
func layoutDests(private bool) []string {
	var dests []string
	for _, f := range scaffoldLayout {
		if dest := f.destFor(private); dest != "" {
			dests = append(dests, dest)
		}
	}
	return dests
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

	// Committed memory in .work/, runtime artefacts in .work.local/ — both
	// modes share the .work.local layout. docs/ carries the user-facing map;
	// the dated developer records live under .abcd/development/. The tracked put
	// files come from the shared scaffoldLayout table (the single source of
	// truth), so this loop tracks the layout automatically.
	want := layoutDests(false) // tracked dests: .work.local/NEXT.md, .work/DECISIONS.md, .work/CONTEXT.md, AGENTS.md, docs/README.md
	want = append(want, ".work.local/scratch", ".work.local/logs", ".pre-commit-config.yaml")
	for _, d := range scaffoldDevDocsDirs {
		want = append(want, ".abcd/development/"+d)
	}
	for _, rel := range want {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Errorf("%s missing: %v", rel, err)
		}
	}
	docsMap, err := os.ReadFile(filepath.Join(repo, "docs", "README.md"))
	if err != nil {
		t.Fatalf("docs/README.md missing: %v", err)
	}
	if want := "# docs — map for " + name + "\n"; string(docsMap) != want {
		t.Errorf("docs/README.md = %q, want %q (substituted)", docsMap, want)
	}
	// .work/ holds ONLY the committed standing-facts memory (DECISIONS.md,
	// CONTEXT.md): no scratch/logs, and NEXT.md now lives in .work.local/.
	for _, rel := range []string{".work/scratch", ".work/logs", ".work/NEXT.md"} {
		if _, err := os.Lstat(filepath.Join(repo, rel)); err == nil {
			t.Errorf("%s exists; runtime artefacts belong in .work.local/", rel)
		}
	}
	// The volatile handoff note and the committed CONTEXT.md landed in their
	// new homes (layoutDests above already asserts them; this pins the split).
	if _, err := os.Stat(filepath.Join(repo, ".work.local", "NEXT.md")); err != nil {
		t.Errorf(".work.local/NEXT.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".work", "CONTEXT.md")); err != nil {
		t.Errorf(".work/CONTEXT.md missing: %v", err)
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

	// Tracked mode never touches .gitignore; .work.local/ is hidden via the
	// checkout-local git info/exclude instead.
	if _, err := os.Lstat(filepath.Join(repo, ".gitignore")); err == nil {
		t.Error(".gitignore was created; scaffold must not touch it")
	}
	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("info/exclude not written: %v", err)
	}
	if !strings.Contains(string(exclude), ".work.local/") {
		t.Errorf("info/exclude missing the .work.local/ entry: %q", exclude)
	}
	if !strings.Contains(out, "done: "+name) {
		t.Errorf("output missing done line: %q", out)
	}
}

// TestScaffoldAttribution pins the --attribution behaviour end to end in a
// REAL git repo: the prepare-commit-msg hook is stamped (substituted,
// executable), core.hooksPath points at .githooks (per clone), and AGENTS.md
// gains the AI-attribution section exactly once — a re-run appends nothing
// and changes nothing.
func TestScaffoldAttribution(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	templates := scaffoldTemplatesOnly(t)
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	run := func(date string) string {
		return runScaffold(t, ScaffoldOptions{
			RepoDir: repo, Name: "attrproj", Attribution: true,
			TemplatesDir: templates, Date: date,
		})
	}
	out := run("2026-07-05")

	// Hook stamped, substituted, executable.
	hook := filepath.Join(repo, ".githooks", "prepare-commit-msg")
	content, err := os.ReadFile(hook)
	if err != nil {
		t.Fatalf("hook missing: %v", err)
	}
	if want := "#!/bin/sh\n# hook for attrproj\n"; string(content) != want {
		t.Errorf("hook = %q, want %q", content, want)
	}
	fi, err := os.Stat(hook)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("hook is not executable: %v", fi.Mode())
	}

	// core.hooksPath set on this clone.
	hooksPath, err := exec.Command("git", "-C", repo, "config", "core.hooksPath").Output()
	if err != nil {
		t.Fatalf("read core.hooksPath: %v", err)
	}
	if got := strings.TrimSpace(string(hooksPath)); got != ".githooks" {
		t.Errorf("core.hooksPath = %q, want .githooks", got)
	}
	if !strings.Contains(out, "re-run this config per clone") {
		t.Errorf("output missing the per-clone note: %q", out)
	}

	// AGENTS.md carries the policy section exactly once.
	agentsMD, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(agentsMD), "## AI attribution"); n != 1 {
		t.Fatalf("AGENTS.md has %d attribution sections, want 1:\n%s", n, agentsMD)
	}
	if !strings.Contains(string(agentsMD), "Never use `Co-Authored-By` for AI") {
		t.Errorf("AGENTS.md missing the policy body:\n%s", agentsMD)
	}

	// Idempotent re-run: section still appears once, AGENTS.md byte-identical.
	run("2026-07-06")
	again, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(agentsMD, again) {
		t.Errorf("re-run changed AGENTS.md:\n%q\nvs\n%q", agentsMD, again)
	}
}

// TestScaffoldAttributionRefusedWithPrivate: policy cannot be set in a repo
// we do not own — the combination is refused before anything is touched.
func TestScaffoldAttributionRefusedWithPrivate(t *testing.T) {
	templates := scaffoldTemplatesOnly(t)
	repo := t.TempDir()
	var buf bytes.Buffer
	err := Scaffold(ScaffoldOptions{
		RepoDir: repo, Private: true, Attribution: true,
		TemplatesDir: templates, Date: "2026-07-05",
	}, &buf)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want the mutually-exclusive refusal", err)
	}
	// Refused BEFORE anything was touched.
	ents, rerr := os.ReadDir(repo)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(ents) != 0 {
		t.Errorf("refused scaffold still created entries: %v", ents)
	}
}

// TestScaffoldWithoutAttributionCreatesNoPolicy: the default run must not
// install any part of the attribution machinery.
func TestScaffoldWithoutAttributionCreatesNoPolicy(t *testing.T) {
	templates, repo := scaffoldFixture(t, true)
	runScaffold(t, ScaffoldOptions{RepoDir: repo, TemplatesDir: templates, Date: "2026-07-05"})

	if _, err := os.Lstat(filepath.Join(repo, ".githooks")); err == nil {
		t.Error(".githooks created without --attribution")
	}
	agentsMD, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(agentsMD), "## AI attribution") {
		t.Error("attribution section appended without --attribution")
	}
}

// scaffoldTemplatesOnly returns a templates dir with the standard set (no
// project repo), for tests that build their own repo.
func scaffoldTemplatesOnly(t *testing.T) string {
	t.Helper()
	templates, _ := scaffoldFixture(t, false)
	return templates
}

// TestScaffoldNeverOverwritesDocsReadme pins the docs map's never-overwrite
// rule: an existing docs/README.md (the project's own map) is skipped, and
// the dated-record directories are still ensured.
func TestScaffoldNeverOverwritesDocsReadme(t *testing.T) {
	templates, repo := scaffoldFixture(t, true)
	custom := []byte("my own docs map\n")
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs", "README.md"), custom, 0o644); err != nil {
		t.Fatal(err)
	}

	out := runScaffold(t, ScaffoldOptions{RepoDir: repo, TemplatesDir: templates, Date: "2026-07-05"})

	got, err := os.ReadFile(filepath.Join(repo, "docs", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, custom) {
		t.Errorf("existing docs/README.md was overwritten: %q", got)
	}
	if !strings.Contains(out, "exists:") {
		t.Errorf("output missing the exists skip: %q", out)
	}
	for _, rel := range []string{".abcd/development/decisions", ".abcd/development/research", ".abcd/development/plans"} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Errorf("%s missing: %v", rel, err)
		}
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
	excludePath := filepath.Join(repo, ".git", "info", "exclude")
	exBefore, err := os.ReadFile(excludePath)
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
	exAfter, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(exBefore, exAfter) {
		t.Errorf("info/exclude grew on re-run:\n%q\nvs\n%q", exBefore, exAfter)
	}
	if _, err := os.Lstat(filepath.Join(repo, ".gitignore")); err == nil {
		t.Error(".gitignore was created on re-run; scaffold must never touch it")
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

// TestScaffoldNeverRepointsForeignSymlinks pins the never-overwrite rule for
// link positions: a pre-existing CLAUDE.md/GEMINI.md symlink pointing
// anywhere OTHER than AGENTS.md is the user's own wiring and must be reported
// and left untouched — never deleted and repointed. A link already at
// AGENTS.md stays as-is on re-run.
func TestScaffoldNeverRepointsForeignSymlinks(t *testing.T) {
	templates, repo := scaffoldFixture(t, false)
	// The user's own wiring: CLAUDE.md points at their docs file.
	if err := os.WriteFile(filepath.Join(repo, "OTHER.md"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("OTHER.md", filepath.Join(repo, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}

	out := runScaffold(t, ScaffoldOptions{RepoDir: repo, TemplatesDir: templates, Date: "2026-07-04"})

	target, err := os.Readlink(filepath.Join(repo, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md is no longer a symlink: %v", err)
	}
	if target != "OTHER.md" {
		t.Errorf("foreign symlink was repointed: CLAUDE.md -> %q, want OTHER.md", target)
	}
	if !strings.Contains(out, "CLAUDE.md is a symlink to OTHER.md (skipped") {
		t.Errorf("output missing the foreign-symlink skip: %q", out)
	}
	// GEMINI.md had nothing in the way and is linked normally.
	if target, err := os.Readlink(filepath.Join(repo, "GEMINI.md")); err != nil || target != "AGENTS.md" {
		t.Errorf("GEMINI.md -> %q, %v; want AGENTS.md", target, err)
	}

	// Re-run: the AGENTS.md link is recognised, not churned.
	out = runScaffold(t, ScaffoldOptions{RepoDir: repo, TemplatesDir: templates, Date: "2026-07-04"})
	if !strings.Contains(out, "GEMINI.md -> AGENTS.md (already)") {
		t.Errorf("output missing the already-linked line: %q", out)
	}
}

func TestScaffoldPrivateMode(t *testing.T) {
	templates, repo := scaffoldFixture(t, true)
	out := runScaffold(t, ScaffoldOptions{
		RepoDir: repo, Private: true, TemplatesDir: templates, Date: "2026-07-04",
	})

	// The private put files come from the shared scaffoldLayout table (the
	// single source of truth), alongside the shared runtime dirs.
	want := layoutDests(true) // private dests: .work.local/{NEXT,DECISIONS,CONTEXT,ISSUES}.md
	want = append(want, ".work.local/scratch", ".work.local/logs")
	for _, rel := range want {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Errorf("%s missing: %v", rel, err)
		}
	}
	// Zero tracked trace: none of the tracked-mode artefacts may exist.
	for _, rel := range []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md", ".gitignore", ".work", "docs", ".pre-commit-config.yaml"} {
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

// TestResolveGitDir covers the three layouts git produces: a plain .git
// directory, a gitfile pointing at a separate git dir (submodule-style), and
// a linked worktree whose gitdir names the shared common dir — info/exclude
// must land in the COMMON dir there, because that is where git reads it.
func TestResolveGitDir(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, repo string) (wantGitDir string, wantOK bool)
	}{
		{
			name: "no .git at all",
			setup: func(t *testing.T, repo string) (string, bool) {
				return "", false
			},
		},
		{
			name: "plain .git directory",
			setup: func(t *testing.T, repo string) (string, bool) {
				gitDir := filepath.Join(repo, ".git")
				mustMkdirAll(t, filepath.Join(gitDir, "info"))
				return gitDir, true
			},
		},
		{
			name: "gitfile pointing at a separate git dir (submodule)",
			setup: func(t *testing.T, repo string) (string, bool) {
				gitDir := filepath.Join(t.TempDir(), "modules", "sub")
				mustMkdirAll(t, gitDir)
				mustWrite(t, filepath.Join(repo, ".git"), "gitdir: "+gitDir+"\n")
				return gitDir, true
			},
		},
		{
			name: "linked worktree resolves to the COMMON git dir",
			setup: func(t *testing.T, repo string) (string, bool) {
				mainGit := filepath.Join(t.TempDir(), ".git")
				wtGit := filepath.Join(mainGit, "worktrees", "wt")
				mustMkdirAll(t, filepath.Join(mainGit, "info"))
				mustMkdirAll(t, wtGit)
				// git writes a RELATIVE commondir ("../..") in worktree git dirs.
				mustWrite(t, filepath.Join(wtGit, "commondir"), "../..\n")
				mustWrite(t, filepath.Join(repo, ".git"), "gitdir: "+wtGit+"\n")
				return mainGit, true
			},
		},
		{
			name: "gitfile with a RELATIVE gitdir pointer",
			setup: func(t *testing.T, repo string) (string, bool) {
				gitDir := filepath.Join(repo, "actual-git")
				mustMkdirAll(t, gitDir)
				mustWrite(t, filepath.Join(repo, ".git"), "gitdir: actual-git\n")
				return gitDir, true
			},
		},
		{
			name: "unparseable gitfile",
			setup: func(t *testing.T, repo string) (string, bool) {
				mustWrite(t, filepath.Join(repo, ".git"), "not a pointer\n")
				return "", false
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			wantDir, wantOK := tt.setup(t, repo)
			gotDir, gotOK := resolveGitDir(repo)
			if gotOK != wantOK {
				t.Fatalf("ok = %v, want %v (dir %q)", gotOK, wantOK, gotDir)
			}
			if wantOK && gotDir != wantDir {
				t.Errorf("gitDir = %q, want %q", gotDir, wantDir)
			}
		})
	}
}

// TestScaffoldPrivateModeInLinkedWorktree pins the end-to-end behaviour the
// gitfile support exists for: scaffolding --private inside a linked worktree
// must write the exclude entry into the SHARED common git dir's info/exclude
// (where git actually reads it), not warn "not a git repo" or write a dead
// worktree-local file.
func TestScaffoldPrivateModeInLinkedWorktree(t *testing.T) {
	templates, repo := scaffoldFixture(t, false)
	mainGit := filepath.Join(t.TempDir(), ".git")
	wtGit := filepath.Join(mainGit, "worktrees", "wt")
	mustMkdirAll(t, filepath.Join(mainGit, "info"))
	mustMkdirAll(t, wtGit)
	mustWrite(t, filepath.Join(wtGit, "commondir"), "../..\n")
	mustWrite(t, filepath.Join(repo, ".git"), "gitdir: "+wtGit+"\n")

	out := runScaffold(t, ScaffoldOptions{
		RepoDir: repo, Private: true, TemplatesDir: templates, Date: "2026-07-04",
	})
	if strings.Contains(out, "WARN: not a git repo") {
		t.Fatalf("worktree misdetected as non-git: %q", out)
	}
	exclude, err := os.ReadFile(filepath.Join(mainGit, "info", "exclude"))
	if err != nil {
		t.Fatalf("common-dir exclude not written: %v", err)
	}
	if !strings.Contains(string(exclude), ".work.local/") {
		t.Errorf("common-dir exclude missing entry: %q", exclude)
	}
}

// TestScaffoldTrackedExcludeWithGitfile: TRACKED mode hides .work.local/ via
// the same gitfile-aware exclude machinery as private mode — the entry lands
// in the RESOLVED git dir's info/exclude, and .gitignore is never touched.
func TestScaffoldTrackedExcludeWithGitfile(t *testing.T) {
	templates, repo := scaffoldFixture(t, false)
	gitDir := filepath.Join(t.TempDir(), "gitdir")
	mustMkdirAll(t, gitDir)
	mustWrite(t, filepath.Join(repo, ".git"), "gitdir: "+gitDir+"\n")

	runScaffold(t, ScaffoldOptions{RepoDir: repo, TemplatesDir: templates, Date: "2026-07-04"})
	exclude, err := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if err != nil {
		t.Fatalf("resolved git dir's info/exclude not written: %v", err)
	}
	if !strings.Contains(string(exclude), ".work.local/") {
		t.Errorf("info/exclude missing the .work.local/ entry: %q", exclude)
	}
	if _, err := os.Lstat(filepath.Join(repo, ".gitignore")); err == nil {
		t.Error(".gitignore was created; scaffold must not touch it")
	}
	for _, rel := range []string{".work.local/scratch", ".work.local/logs"} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Errorf("%s missing: %v", rel, err)
		}
	}
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
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
