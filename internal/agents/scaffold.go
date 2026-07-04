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
// Default (tracked) mode — your own repo:
//   - AGENTS.md router from the template, with {{PROJECT}} and {{DATE}}
//     substituted (plain string replacement — no metacharacter hazards);
//   - CLAUDE.md and GEMINI.md as RELATIVE symlinks to AGENTS.md INSIDE the
//     project repo (these are the project's own tracked bridges — ferry never
//     deploys them to $HOME, so the copy-not-symlink invariant is untouched);
//   - a committed .work/ skeleton (NEXT.md, DECISIONS.md) with gitignored
//     scratch/ and logs/;
//   - .gitignore entries for the scratch parts;
//   - the pre-commit template, only when the repo has none.
//
// --private mode — a repo you don't own, zero tracked trace:
//   - .work.local/ only (NEXT.md, DECISIONS.md, ISSUES.md + scratch/logs),
//     hidden via .git/info/exclude (local to the checkout, never committed).
//     No AGENTS.md, no symlinks, no .gitignore edits.
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

	if opts.Private {
		return scaffoldPrivate(repo, name, put, out)
	}
	return scaffoldTracked(opts, repo, name, put, out)
}

// scaffoldPrivate creates the zero-tracked-trace layout: .work.local/ with the
// three logs, excluded via .git/info/exclude — which is local to the checkout
// and never committed or pushed.
func scaffoldPrivate(repo, name string, put func(templateName, dest string) error, out io.Writer) error {
	w := filepath.Join(repo, ".work.local")
	for _, d := range []string{filepath.Join(w, "scratch"), filepath.Join(w, "logs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := put("NEXT.md", filepath.Join(w, "NEXT.md")); err != nil {
		return err
	}
	if err := put("DECISIONS.md", filepath.Join(w, "DECISIONS.md")); err != nil {
		return err
	}
	if err := put("ISSUES.md", filepath.Join(w, "ISSUES.md")); err != nil {
		return err
	}

	// Hide .work.local/ from git without touching any tracked file.
	if fi, err := os.Stat(filepath.Join(repo, ".git")); err == nil && fi.IsDir() {
		exclude := filepath.Join(repo, ".git", "info", "exclude")
		if err := appendLineOnce(exclude, ".work.local/"); err != nil {
			return err
		}
		fmt.Fprintln(out, "excluded: .work.local/ via .git/info/exclude (local-only, not committed)")
	} else {
		fmt.Fprintln(out, "WARN: not a git repo — .work.local/ cannot be excluded locally")
	}
	fmt.Fprintf(out, "done: %s (private mode — no tracked files were created or modified)\n", name)
	return nil
}

// scaffoldTracked creates the tracked layout: committed .work/ skeleton,
// AGENTS.md router, in-repo CLAUDE.md/GEMINI.md symlinks, the pre-commit
// config when absent, and the .gitignore entries.
func scaffoldTracked(opts ScaffoldOptions, repo, name string, put func(templateName, dest string) error, out io.Writer) error {
	for _, d := range []string{filepath.Join(repo, ".work", "scratch"), filepath.Join(repo, ".work", "logs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
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

	// Bridges: Claude Code + companion both read CLAUDE.md; Gemini reads
	// GEMINI.md. (Codex CLI and OpenCode read AGENTS.md natively.) These are
	// RELATIVE symlinks INSIDE the user's project repo — tracked project
	// content, not a $HOME deploy. A real file in the way is never replaced.
	for _, f := range []string{"CLAUDE.md", "GEMINI.md"} {
		link := filepath.Join(repo, f)
		if fi, lerr := os.Lstat(link); lerr == nil {
			if fi.Mode()&fs.ModeSymlink == 0 {
				fmt.Fprintf(out, "exists:   %s is a real file (skipped — merge into AGENTS.md first)\n", f)
				continue
			}
			if rerr := os.Remove(link); rerr != nil {
				return rerr
			}
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

	// Gitignore the scratch parts of .work/ (NEXT.md / DECISIONS.md ARE committed).
	if fi, err := os.Stat(filepath.Join(repo, ".git")); err == nil && fi.IsDir() {
		gi := filepath.Join(repo, ".gitignore")
		updated, aerr := appendGitignoreBlock(gi)
		if aerr != nil {
			return aerr
		}
		if updated {
			fmt.Fprintln(out, "updated:  .gitignore")
		}
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

// gitignoreBlock is the exact block the shell scaffold appended; the sentinel
// line ".work/scratch/" gates idempotence.
const gitignoreBlock = "\n# agent working state (NEXT.md and DECISIONS.md ARE committed)\n.work/scratch/\n.work/logs/\n"

// appendGitignoreBlock appends the .work/ scratch entries to the repo's
// .gitignore unless the sentinel line is already present, creating the file if
// absent. It reports whether it wrote anything.
func appendGitignoreBlock(path string) (bool, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	for _, l := range strings.Split(string(existing), "\n") {
		if strings.HasPrefix(l, ".work/scratch/") {
			return false, nil
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.WriteString(gitignoreBlock); err != nil {
		return false, err
	}
	return true, nil
}
