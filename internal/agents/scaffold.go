package agents

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
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

	// Attribution marks a repo that REQUIRES AI disclosure (e.g. a research
	// project), overriding the workspace no-attribution default: tracked mode
	// additionally installs the prepare-commit-msg hook (a kernel-style
	// `Assisted-by:` trailer on agent-authored commits), points
	// core.hooksPath at it, and appends the AI-attribution section to
	// AGENTS.md. Mutually exclusive with Private — a repo you do not own is
	// not yours to set policy in.
	Attribution bool
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
//   - a committed .work/ holding the standing-facts memory DECISIONS.md and
//     CONTEXT.md (NEXT.md, the volatile handoff note, lives in .work.local/);
//   - the pre-commit template, only when the repo has none.
//
// --private mode — a repo you don't own, zero tracked trace:
//   - .work.local/ only (NEXT.md, DECISIONS.md, CONTEXT.md, ISSUES.md
//     alongside the scratch/logs dirs). No AGENTS.md, no symlinks, no tracked
//     file touched.
func Scaffold(opts ScaffoldOptions, out io.Writer) error {
	if opts.Private && opts.Attribution {
		return errors.New("agents scaffold: --attribution and --private are mutually exclusive (a repo you do not own is not yours to set attribution policy in)")
	}
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

// scaffoldMode records which scaffold(s) a layout entry belongs to.
type scaffoldMode int

const (
	modeTracked scaffoldMode = iota // default (tracked) scaffold only
	modePrivate                     // --private scaffold only
	modeBoth                        // both scaffolds, at mode-specific dests
)

// scaffoldFile is one templated put() target in the scaffold layout. dest
// paths are repo-relative. mode says which scaffold(s) create the file; the
// matching dest gives where. For modeBoth the two dests differ (`.work/` vs
// `.work.local/`); for a single-mode file only that mode's dest is set.
type scaffoldFile struct {
	template    string
	mode        scaffoldMode
	trackedDest string
	privateDest string
}

// destFor returns f's repo-relative dest for the requested scaffold, or "" when
// f is not created in that scaffold.
func (f scaffoldFile) destFor(private bool) string {
	switch f.mode {
	case modeTracked:
		if private {
			return ""
		}
		return f.trackedDest
	case modePrivate:
		if private {
			return f.privateDest
		}
		return ""
	default: // modeBoth
		if private {
			return f.privateDest
		}
		return f.trackedDest
	}
}

// scaffoldLayout is the single source of truth for the templated put() files,
// in the EXACT order the scaffolds stamp them. Tracked mode walks the entries
// with a trackedDest (DECISIONS + CONTEXT in the committed .work/, AGENTS,
// docs/README, and NEXT in the private .work.local/); private mode walks the
// entries with a privateDest (NEXT, DECISIONS, CONTEXT, ISSUES, all in
// .work.local/). The mode-specific steps (the CLAUDE/GEMINI symlinks, the
// pre-commit copy, the --attribution hook, and excludeWorkLocal) are
// imperative and live outside this table.
var scaffoldLayout = []scaffoldFile{
	{template: "NEXT.md", mode: modeBoth, trackedDest: ".work.local/NEXT.md", privateDest: ".work.local/NEXT.md"},
	{template: "DECISIONS.md", mode: modeBoth, trackedDest: ".work/DECISIONS.md", privateDest: ".work.local/DECISIONS.md"},
	{template: "CONTEXT.md", mode: modeBoth, trackedDest: ".work/CONTEXT.md", privateDest: ".work.local/CONTEXT.md"},
	{template: "ISSUES.md", mode: modePrivate, privateDest: ".work.local/ISSUES.md"},
	{template: "AGENTS.md", mode: modeTracked, trackedDest: "AGENTS.md"},
	{template: "docs-README.md", mode: modeTracked, trackedDest: "docs/README.md"},
}

// scaffoldDevDocsDirs are the .abcd/development/ subdirectories tracked mode
// creates up front (they hold the durable developer record: dated plans and
// research, sequential ADRs). docs/ is reserved for user-facing Diátaxis
// content, whose dirs (tutorials/how-to/reference/explanation) are created on
// first use, not here.
var scaffoldDevDocsDirs = []string{"plans", "research", "decisions"}

// putLayout stamps every scaffoldLayout entry that applies to the requested
// scaffold, in table order, creating each dest's parent dir first (put itself
// does not). It preserves the exact put() order and the never-overwrite/skip
// semantics.
func putLayout(repo string, private bool, put func(templateName, dest string) error) error {
	for _, f := range scaffoldLayout {
		dest := f.destFor(private)
		if dest == "" {
			continue
		}
		full := filepath.Join(repo, dest)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := put(f.template, full); err != nil {
			return err
		}
	}
	return nil
}

// scaffoldPrivate creates the zero-tracked-trace layout: .work.local/ holds
// the memory files (NEXT.md, DECISIONS.md, CONTEXT.md, ISSUES.md) alongside
// the runtime-artefact dirs the caller already created; nothing tracked is
// created or modified.
func scaffoldPrivate(repo, name string, put func(templateName, dest string) error, out io.Writer) error {
	if err := putLayout(repo, true, put); err != nil {
		return err
	}
	fmt.Fprintf(out, "done: %s (private mode — no tracked files were created or modified)\n", name)
	return nil
}

// scaffoldTracked creates the committed layout on top of the shared
// .work.local/ runtime dirs: a .work/ holding the committed standing-facts
// memory (DECISIONS.md, CONTEXT.md), the AGENTS.md router, the user-facing
// docs/ map (docs/README.md), the .abcd/development/ developer-record dirs,
// the in-repo CLAUDE.md/GEMINI.md symlinks, and the pre-commit config when
// absent. The volatile handoff note
// NEXT.md lands in .work.local/ (uncommitted) in both modes. It never touches
// .gitignore — runtime artefacts are excluded via .work.local/ + info/exclude,
// and .work/ is meant to be committed whole.
func scaffoldTracked(opts ScaffoldOptions, repo, name string, put func(templateName, dest string) error, out io.Writer) error {
	// Developer-record directories that hold dated plans/research and
	// sequential ADRs, under the .abcd/development/ namespace (the user-facing
	// map docs/README.md is a scaffoldLayout entry). docs/ stays Diátaxis-only;
	// its content directories (tutorials/how-to/reference/explanation) are
	// created on first use, not up front.
	for _, d := range scaffoldDevDocsDirs {
		if err := os.MkdirAll(filepath.Join(repo, ".abcd", "development", d), 0o755); err != nil {
			return err
		}
	}

	// The put files, in table order: the volatile .work.local/NEXT.md, the
	// committed .work/ memory (DECISIONS.md, CONTEXT.md), the AGENTS.md
	// router, and the docs map (docs/README.md, never overwriting an
	// existing one).
	if err := putLayout(repo, false, put); err != nil {
		return err
	}

	// Bridges: Claude Code reads CLAUDE.md; Gemini reads GEMINI.md. (Codex
	// CLI and OpenCode read AGENTS.md natively.) These are
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

	// --attribution: this repo REQUIRES AI disclosure (e.g. a research
	// project), overriding the workspace no-attribution default: a
	// kernel-style `Assisted-by:` trailer, added only to agent-authored
	// commits, for every harness.
	if opts.Attribution {
		if err := scaffoldAttribution(opts, repo, put, out); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "done: %s — now fill in the placeholder sections of AGENTS.md\n", name)
	return nil
}

// attributionSection is the exact AGENTS.md section --attribution appends
// (guarded by the "## AI attribution" heading, so a re-run appends nothing).
const attributionSection = `
## AI attribution

This repo REQUIRES AI disclosure, overriding the workspace no-attribution
default: agent-authored commits carry an ` + "`Assisted-by:`" + ` trailer (enforced by
` + "`.githooks/prepare-commit-msg`" + `; activate per clone with
` + "`git config core.hooksPath .githooks`" + `). Never use ` + "`Co-Authored-By`" + ` for AI —
the human is always the author; the tool is disclosed.
`

// scaffoldAttribution installs the AI-disclosure policy in a tracked repo:
// the prepare-commit-msg hook (stamped from the template, never overwriting,
// made executable), core.hooksPath pointed at .githooks/ when the target is a
// git repo (a PER-CLONE setting — the output says so), and the AI-attribution
// section appended to AGENTS.md when its heading is absent.
func scaffoldAttribution(opts ScaffoldOptions, repo string, put func(templateName, dest string) error, out io.Writer) error {
	hooksDir := filepath.Join(repo, ".githooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}
	hook := filepath.Join(hooksDir, "prepare-commit-msg")
	if err := put("prepare-commit-msg", hook); err != nil {
		return err
	}
	// git only runs an executable hook. Add the executable bits whether the
	// hook was just stamped or already there (preserving the rest of the
	// mode) — exactly what the reference's unconditional chmod +x does.
	if fi, err := os.Lstat(hook); err == nil && fi.Mode().IsRegular() {
		if err := os.Chmod(hook, fi.Mode().Perm()|0o111); err != nil {
			return err
		}
	}

	// Point git at the tracked hooks dir. core.hooksPath is per-clone config,
	// so every fresh clone must re-run it — the message says so. A non-git
	// directory simply skips (nothing to configure); a failing git call warns
	// with the manual command rather than aborting the scaffold.
	if _, ok := resolveGitDir(repo); ok {
		if err := exec.Command("git", "-C", repo, "config", "core.hooksPath", ".githooks").Run(); err != nil {
			fmt.Fprintf(out, "WARN: could not set core.hooksPath (%v) — run `git config core.hooksPath .githooks` yourself\n", err)
		} else {
			fmt.Fprintln(out, "enabled:  core.hooksPath .githooks (re-run this config per clone)")
		}
	}

	// Append the policy section to AGENTS.md unless its heading is already
	// there (idempotent; the router was stamped earlier in tracked mode).
	agentsMD := filepath.Join(repo, "AGENTS.md")
	existing, err := os.ReadFile(agentsMD)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if !hasAttributionSection(existing) {
		f, oerr := os.OpenFile(agentsMD, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if oerr != nil {
			return oerr
		}
		if _, werr := f.WriteString(attributionSection); werr != nil {
			f.Close()
			return werr
		}
		if cerr := f.Close(); cerr != nil {
			return cerr
		}
		fmt.Fprintln(out, "updated:  AGENTS.md (AI attribution section)")
	}
	return nil
}

// hasAttributionSection reports whether content already carries the
// "## AI attribution" heading at the start of a line (the reference's
// grep -q '^## AI attribution' guard).
func hasAttributionSection(content []byte) bool {
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, "## AI attribution") {
			return true
		}
	}
	return false
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
