package agents

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ScaffoldOptions parameterises Scaffold. RepoDir is the USER's project repo
// (any directory; it need not be under any particular workspace). Name
// defaults to RepoDir's base name; Date (YYYY-MM-DD) defaults to today and
// exists so tests are deterministic. TemplatesDir is the config repo's
// agents/templates directory; Guard validates each template read (the caller
// passes its symlink-refusing repo guard; nil = no extra validation).
type ScaffoldOptions struct {
	RepoDir      string
	Name         string
	Private      bool
	TemplatesDir string
	Date         string
	Guard        func(candidate string) (string, error)
}

// Scaffold sets a project repo up for the multi-tool agent pipeline. It is
// idempotent and NEVER overwrites an existing file.
//
// Runtime artefacts (scratch output, logs) live in `.work.local/` in BOTH
// modes and are never committed: every scaffold creates
// .work.local/{scratch,logs} and hides the whole directory via the git
// info/exclude mechanism (local to the checkout — no tracked file is ever
// touched for it; there is no .gitignore editing).
//
// Default (tracked) mode — your own repo — additionally creates the
// COMMITTED project memory:
//   - AGENTS.md router from the template, with {{PROJECT}} and {{DATE}}
//     substituted (plain string replacement — no metacharacter hazards);
//   - CLAUDE.md and GEMINI.md as RELATIVE symlinks to AGENTS.md INSIDE the
//     project repo (these are the project's own tracked bridges — ferry never
//     deploys them to $HOME, so the copy-not-symlink invariant is untouched);
//   - a committed .work/ holding ONLY NEXT.md and DECISIONS.md;
//   - the pre-commit template, only when the repo has none.
//
// --private mode — a repo you don't own, zero tracked trace:
//   - .work.local/ only (NEXT.md, DECISIONS.md, ISSUES.md alongside the
//     scratch/logs dirs). No AGENTS.md, no symlinks, no tracked file touched.
func Scaffold(opts ScaffoldOptions, out io.Writer) error {
	repo, err := filepath.Abs(opts.RepoDir)
	if err != nil {
		return err
	}
	if fi, serr := os.Stat(repo); serr != nil || !fi.IsDir() {
		return fmt.Errorf("agents scaffold: %s is not an existing directory", opts.RepoDir)
	}

	name := opts.Name
	if name == "" {
		name = filepath.Base(repo)
	}
	date := opts.Date
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	// put stamps one template to dest with {{PROJECT}}/{{DATE}} substituted,
	// skipping (never overwriting) an existing dest of any kind.
	put := func(templateName, dest string) error {
		if _, lerr := os.Lstat(dest); lerr == nil {
			fmt.Fprintf(out, "exists:   %s (skipped)\n", dest)
			return nil
		}
		content, rerr := readTemplate(opts, templateName)
		if rerr != nil {
			return rerr
		}
		rendered := strings.ReplaceAll(string(content), "{{PROJECT}}", name)
		rendered = strings.ReplaceAll(rendered, "{{DATE}}", date)
		if werr := os.WriteFile(dest, []byte(rendered), 0o644); werr != nil {
			return werr
		}
		fmt.Fprintf(out, "created:  %s\n", dest)
		return nil
	}

	// Runtime artefacts live in .work.local/ in BOTH modes: create the dirs
	// and hide the directory from git before anything mode-specific happens.
	if err := ensureWorkLocalDirs(repo); err != nil {
		return err
	}
	excludeWorkLocal(repo, out)

	if opts.Private {
		return scaffoldPrivate(repo, name, put, out)
	}
	return scaffoldTracked(opts, repo, name, put, out)
}

// ensureWorkLocalDirs creates the runtime-artefact layout every scaffold
// guarantees: .work.local/scratch and .work.local/logs.
func ensureWorkLocalDirs(repo string) error {
	for _, d := range []string{
		filepath.Join(repo, ".work.local", "scratch"),
		filepath.Join(repo, ".work.local", "logs"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// excludeWorkLocal hides .work.local/ from git without touching any tracked
// file: the entry goes into the repo's REAL git dir's info/exclude —
// resolveGitDir follows a gitfile (linked worktree, submodule) and the
// worktree commondir, since git reads info/exclude from the common dir. A
// non-git directory only warns (there is nothing to exclude from).
func excludeWorkLocal(repo string, out io.Writer) {
	gitDir, ok := resolveGitDir(repo)
	if !ok {
		fmt.Fprintln(out, "WARN: not a git repo — .work.local/ cannot be excluded locally")
		return
	}
	exclude := filepath.Join(gitDir, "info", "exclude")
	if err := appendLineOnce(exclude, ".work.local/"); err != nil {
		fmt.Fprintf(out, "WARN: could not write %s (%v) — exclude .work.local/ yourself\n", exclude, err)
		return
	}
	fmt.Fprintln(out, "excluded: .work.local/ via git info/exclude (local-only, not committed)")
}

// scaffoldPrivate creates the zero-tracked-trace layout: .work.local/ holds
// the three logs alongside the runtime-artefact dirs the caller already
// created; nothing tracked is created or modified.
func scaffoldPrivate(repo, name string, put func(templateName, dest string) error, out io.Writer) error {
	w := filepath.Join(repo, ".work.local")
	if err := put("NEXT.md", filepath.Join(w, "NEXT.md")); err != nil {
		return err
	}
	if err := put("DECISIONS.md", filepath.Join(w, "DECISIONS.md")); err != nil {
		return err
	}
	if err := put("ISSUES.md", filepath.Join(w, "ISSUES.md")); err != nil {
		return err
	}
	fmt.Fprintf(out, "done: %s (private mode — no tracked files were created or modified)\n", name)
	return nil
}

// scaffoldTracked creates the committed layout on top of the shared
// .work.local/ runtime dirs: a .work/ holding ONLY the committed memory
// (NEXT.md, DECISIONS.md), the AGENTS.md router, the docs/ hierarchy with
// its map (docs/README.md), the in-repo CLAUDE.md/GEMINI.md symlinks, and
// the pre-commit config when absent. It never touches .gitignore — runtime
// artefacts are excluded via .work.local/ + info/exclude, and .work/ is
// meant to be committed whole.
func scaffoldTracked(opts ScaffoldOptions, repo, name string, put func(templateName, dest string) error, out io.Writer) error {
	if err := os.MkdirAll(filepath.Join(repo, ".work"), 0o755); err != nil {
		return err
	}
	if err := put("NEXT.md", filepath.Join(repo, ".work", "NEXT.md")); err != nil {
		return err
	}
	if err := put("DECISIONS.md", filepath.Join(repo, ".work", "DECISIONS.md")); err != nil {
		return err
	}
	if err := put("AGENTS.md", filepath.Join(repo, "AGENTS.md")); err != nil {
		return err
	}

	// Docs hierarchy: the map (docs/README.md, stamped from the
	// docs-README.md template, never overwriting an existing one) plus the
	// directories that hold dated records. The Diátaxis content directories
	// (tutorials/how-to/reference/explanation) are created on first use, not
	// up front.
	for _, d := range []string{"decisions", "research", "plans"} {
		if err := os.MkdirAll(filepath.Join(repo, "docs", d), 0o755); err != nil {
			return err
		}
	}
	if err := put("docs-README.md", filepath.Join(repo, "docs", "README.md")); err != nil {
		return err
	}

	// Bridges: Claude Code + companion both read CLAUDE.md; Gemini reads
	// GEMINI.md. (Codex CLI and OpenCode read AGENTS.md natively.) These are
	// RELATIVE symlinks INSIDE the user's project repo — tracked project
	// content, not a $HOME deploy. NOTHING pre-existing is ever replaced: a
	// real file is skipped, and a symlink pointing anywhere OTHER than
	// AGENTS.md is the user's own wiring — reported and left alone, exactly
	// like a real file. Only an absent path is linked.
	for _, f := range []string{"CLAUDE.md", "GEMINI.md"} {
		link := filepath.Join(repo, f)
		if fi, lerr := os.Lstat(link); lerr == nil {
			if fi.Mode()&fs.ModeSymlink == 0 {
				fmt.Fprintf(out, "exists:   %s is a real file (skipped — merge into AGENTS.md first)\n", f)
				continue
			}
			target, rerr := os.Readlink(link)
			if rerr == nil && target == "AGENTS.md" {
				fmt.Fprintf(out, "linked:   %s -> AGENTS.md (already)\n", f)
				continue
			}
			fmt.Fprintf(out, "exists:   %s is a symlink to %s (skipped — repoint it to AGENTS.md yourself)\n", f, target)
			continue
		}
		if serr := os.Symlink("AGENTS.md", link); serr != nil {
			return serr
		}
		fmt.Fprintf(out, "linked:   %s -> AGENTS.md\n", f)
	}

	// Offer the pre-commit config (verbatim copy, no substitution) when the
	// repo has none.
	preCommit := filepath.Join(repo, ".pre-commit-config.yaml")
	if _, lerr := os.Lstat(preCommit); errors.Is(lerr, fs.ErrNotExist) {
		content, rerr := readTemplate(opts, "pre-commit-config.yaml")
		if rerr != nil {
			return rerr
		}
		if werr := os.WriteFile(preCommit, content, 0o644); werr != nil {
			return werr
		}
		fmt.Fprintln(out, "created:  .pre-commit-config.yaml (activate with: pre-commit install)")
	}

	fmt.Fprintf(out, "done: %s — now fill in the placeholder sections of AGENTS.md\n", name)
	return nil
}

// readTemplate reads one scaffold template from the config repo's templates
// directory, through the caller's guard. A missing template is a clear,
// actionable error (the config repo's agents/templates/ must carry it).
func readTemplate(opts ScaffoldOptions, name string) ([]byte, error) {
	path := filepath.Join(opts.TemplatesDir, name)
	safe, err := guardPath(opts.Guard, path)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(safe)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("agents scaffold: template %s is missing from the config repo — add it under %s (or import an existing set with `ferry agents adopt <dir>`)", name, opts.TemplatesDir)
		}
		return nil, err
	}
	return content, nil
}

// resolveGitDir locates a repo's git directory for info/exclude purposes,
// covering all three layouts git produces:
//
//   - a .git DIRECTORY (the standard checkout): used as-is;
//   - a .git FILE (a linked worktree or submodule): its "gitdir: <path>"
//     pointer is followed, with a relative path anchored at the repo;
//   - a resolved git dir containing a "commondir" file (a linked worktree):
//     the shared COMMON dir is resolved too (relative paths anchored at the
//     git dir), because git reads info/exclude from $GIT_COMMON_DIR — a
//     worktree-local exclude would silently not apply.
//
// ok is false when the repo has no .git at all (or an unparseable gitfile).
func resolveGitDir(repo string) (gitDir string, ok bool) {
	gitPath := filepath.Join(repo, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return "", false
	}
	gitDir = gitPath
	if !fi.IsDir() {
		data, rerr := os.ReadFile(gitPath)
		if rerr != nil {
			return "", false
		}
		line, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
		const prefix = "gitdir:"
		if !strings.HasPrefix(line, prefix) {
			return "", false
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if gitDir == "" {
			return "", false
		}
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(repo, gitDir)
		}
		gitDir = filepath.Clean(gitDir)
	}
	// GIT_COMMON_DIR semantics: a linked worktree's git dir names the shared
	// git dir in a "commondir" file; info/exclude lives THERE.
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		if common := strings.TrimSpace(string(data)); common != "" {
			if !filepath.IsAbs(common) {
				common = filepath.Join(gitDir, common)
			}
			gitDir = filepath.Clean(common)
		}
	}
	return gitDir, true
}

// appendLineOnce appends line (plus newline) to path unless the file already
// contains it as an exact line, creating the file (and its parent) if absent.
func appendLineOnce(path, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(existing), "\n") {
		if l == line {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}
