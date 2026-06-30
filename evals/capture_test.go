package evals

// Capture-behaviour ACs. Capture is documented as interactive (approve each
// change, route shared/local). Where the non-interactive contract is not
// doc-defined, tests assert the SAFE observable (no approval => nothing written;
// secrets => never written; out-of-scope => never ingested) and never hang: the
// harness feeds empty stdin so a prompt sees EOF immediately.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCaptureInteractiveRoute covers AC-capture-interactive-route: capture must
// support THREE distinct routes for an accepted/declined change —
//
//	(1) SHARED  -> the change lands in the repo (a manifest-governed shared file),
//	(2) LOCAL   -> the change lands under <repo>/local/<domain>/, and
//	(3) REJECT  -> the change appears in NEITHER the repo nor the .local overlay.
//
// We assert the observable END-STATE of each route independently.
//
// TODO(contract): the exact accept/route/reject keystrokes are described
// ("route it shared or local", "or is rejected") but not pinned. We drive the
// natural scripted answers (e.g. y + s|l, or n) and assert the end-state; if the
// binary uses different keys this surfaces once it lands. The load-bearing gate is
// the three distinct observable outcomes, not the keys.
func TestCaptureInteractiveRoute_AC_capture_interactive_route(t *testing.T) {
	t.Parallel()

	// --- Route SHARED: accepted change reaches a SHARED repo path, NOT local/. ---
	t.Run("shared_to_repo", func(t *testing.T) {
		t.Parallel()
		s := newCaptureSandbox(t)
		marker := "SHARED_ROUTE_MARKER"
		if err := os.WriteFile(s.HomePath(".zshrc"), []byte("# zshrc\n"+marker+"\n"), 0o644); err != nil {
			t.Fatalf("local edit: %v", err)
		}
		// Accept + route shared.
		s.FerryWithInput("y\ns\ny\n", "capture")
		// The change must land in a manifest-governed SHARED repo location and
		// specifically NOT under local/ — so SHARED and LOCAL are DISTINCT
		// destinations. (An impl that routes both [s] and [l] into local/ would
		// wrongly pass a plain repoContains check.)
		landed := findFileContaining(t, s.Repo, marker)
		if landed == "" {
			t.Fatalf("AC-capture-interactive-route[shared]: accepted shared change did not land in the repo at all")
		}
		rel, _ := filepath.Rel(s.Repo, landed)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if parts[0] == "local" {
			t.Errorf("AC-capture-interactive-route[shared]: shared-routed change landed UNDER local/ (%q) — shared must go to a shared path, not the .local overlay", rel)
		}
		// And it must not ALSO have leaked into the local overlay.
		if dirTreeContains(t, s.RepoPath("local"), marker) {
			t.Errorf("AC-capture-interactive-route[shared]: shared-routed change also appears under local/ — shared and local must be distinct destinations")
		}
	})

	// --- Route LOCAL: accepted change reaches <repo>/local/<domain>/ specifically. ---
	t.Run("local_to_overlay", func(t *testing.T) {
		t.Parallel()
		s := newCaptureSandbox(t)
		marker := "LOCAL_ROUTE_MARKER"
		if err := os.WriteFile(s.HomePath(".zshrc"), []byte("# zshrc\n"+marker+"\n"), 0o644); err != nil {
			t.Fatalf("local edit: %v", err)
		}
		// Accept + route local.
		s.FerryWithInput("y\nl\ny\n", "capture")
		// The captured content must land at a path EXACTLY under local/<domain>/
		// (for a .zshrc change, the zsh domain), not merely somewhere under local/.
		landed := findFileContaining(t, s.RepoPath("local"), marker)
		if landed == "" {
			t.Fatalf("AC-capture-interactive-route[local]: accepted local change did not land under <repo>/local/")
		}
		rel, _ := filepath.Rel(s.Repo, landed)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 3 || parts[0] != "local" {
			t.Errorf("AC-capture-interactive-route[local]: local content at %q is not under local/<domain>/<file>", rel)
		} else if !containsAnyFold(parts[1], "zsh") {
			t.Errorf("AC-capture-interactive-route[local]: .zshrc change landed under local/%q, expected per-domain local/zsh/", parts[1])
		}
	})

	// --- Route REJECT: declined change appears in neither. ---
	t.Run("reject_to_neither", func(t *testing.T) {
		t.Parallel()
		s := newCaptureSandbox(t)
		marker := "REJECT_ROUTE_MARKER"
		if err := os.WriteFile(s.HomePath(".zshrc"), []byte("# zshrc\n"+marker+"\n"), 0o644); err != nil {
			t.Fatalf("local edit: %v", err)
		}
		repoZsh := s.SnapshotFile(t, s.RepoPath(".zshrc"))
		// Reject (n) — and empty stdin afterward keeps anything else unapproved.
		s.FerryWithInput("n\n", "capture")
		repoZsh.AssertUnchanged(t)
		if repoContains(t, s, marker) {
			t.Errorf("AC-capture-interactive-route[reject]: rejected change was written into the repo")
		}
		if dirTreeContains(t, s.RepoPath("local"), marker) {
			t.Errorf("AC-capture-interactive-route[reject]: rejected change was written into the .local overlay")
		}
	})
}

// newCaptureSandbox builds a git-backed sandbox with a deployed managed .zshrc,
// ready for a capture-routing test.
func newCaptureSandbox(t *testing.T) *Sandbox {
	t.Helper()
	s := newCaptureSandboxNoApply(t)
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("newCaptureSandbox: setup apply exited %d; stderr:\n%s", code, errOut)
	}
	return s
}

// newCaptureSandboxNoApply builds a git-backed sandbox with a seeded shared
// manifest but does NOT run apply — for a capture no-op (empty-stdin) scenario.
func newCaptureSandboxNoApply(t *testing.T) *Sandbox {
	t.Helper()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# baseline\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# baseline\n")
	gitCommitAll(t, s.Repo, "baseline")
	return s
}

// dirTreeContains greps a directory tree for a substring.
func dirTreeContains(t *testing.T, dir, needle string) bool {
	t.Helper()
	found := false
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr == nil && strings.Contains(string(data), needle) {
			found = true
		}
		return nil
	})
	return found
}

// TestSecretBlocked covers AC-secret-blocked: a captured change containing
// secret-like content (an OPENSSH PRIVATE KEY block) is refused entry to the repo
// and the out-of-band path is surfaced.
func TestSecretBlocked_AC_secret_blocked(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# baseline\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# baseline\n")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}

	// Stage an in-scope managed file now containing a fake private-key block.
	secret := "-----BEGIN OPENSSH PRIVATE KEY-----\nFAKEKEYMATERIALdeadbeefdeadbeef\n-----END OPENSSH PRIVATE KEY-----\n"
	if err := os.WriteFile(s.HomePath(".zshrc"), []byte("# zshrc\n"+secret), 0o644); err != nil {
		t.Fatalf("stage secret: %v", err)
	}

	// Approve everything we can ("y\n" repeated). Even if approved, the secret
	// scan must block the secret bytes from entering the repo.
	out, errOut, _ := s.FerryWithInput("y\ny\ny\ny\n", "capture")
	combined := out + errOut

	// Hard assertion: the secret bytes never land in any repo file.
	s.AssertNoSecretInRepo(t, secret)

	// The user-facing message must tie the refusal SPECIFICALLY to SECRET material —
	// require BOTH (a) it names secret/credential material AND (b) a block/refuse/
	// out-of-band action. A bare "secret" mention OR a generic "skipped"/"excluded"
	// (which a no-op could emit) is NOT sufficient. AC-secret-blocked: the change
	// "is blocked from the repo entirely and only the out-of-band path is offered."
	namesSecret := containsAnyFold(combined, "secret", "private key", "credential", "token", "key material", "sensitive")
	blockAction := containsAnyFold(combined,
		"block", "out-of-band", "out of band", "refus", "not written", "not committed",
		"will not commit", "won't commit", "cannot commit", "secret store",
		"handle separately", "handled separately", "must not enter", "kept out of the repo")
	if !(namesSecret && blockAction) {
		t.Errorf("AC-secret-blocked: capture message did not tie BLOCKING/out-of-band handling to SECRET material (need a secret-material mention AND a block/refuse/out-of-band action; a bare 'secret' or generic 'skipped' is insufficient)\n%s", combined)
	}
}

// TestScopeRespectedCapture covers AC-scope-respected-capture as a DIFFERENTIAL
// (so a capture-nothing implementation does NOT pass): an IN-scope change IS
// offered/captured (routed shared -> reaches the repo) while an OUT-OF-scope change
// (a font, fonts undeclared/false) is NEVER ingested into the repo or overlay.
func TestScopeRespectedCapture_AC_scope_respected_capture(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	// fonts disabled / undeclared; dotfiles in scope.
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# baseline\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# baseline\n")
	gitCommitAll(t, s.Repo, "baseline")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}

	// Out-of-scope font change + in-scope dotfile change, each uniquely marked.
	inScopeMarker := "IN_SCOPE_CAPTURE_MARKER"
	outScopeMarker := "OUT_OF_SCOPE_FONT_MARKER"
	if err := os.MkdirAll(s.HomePath("Library", "Fonts"), 0o755); err != nil {
		t.Fatalf("mkdir fonts: %v", err)
	}
	if err := os.WriteFile(s.HomePath("Library", "Fonts", "Experimental.ttf"), []byte(outScopeMarker), 0o644); err != nil {
		t.Fatalf("write font: %v", err)
	}
	if err := os.WriteFile(s.HomePath(".zshrc"), []byte("# zshrc\n"+inScopeMarker+"\n"), 0o644); err != nil {
		t.Fatalf("in-scope edit: %v", err)
	}

	// Accept + route shared for everything offered.
	s.FerryWithInput("y\ns\ny\ns\ny\ns\n", "capture")

	// Differential: in-scope change IS captured (reached the repo); out-of-scope
	// font is NEVER ingested into the repo or overlay.
	if !repoContains(t, s, inScopeMarker) {
		t.Errorf("AC-scope-respected-capture: in-scope change was not captured into the repo (capture-nothing impl?)")
	}
	if repoContains(t, s, outScopeMarker) {
		t.Errorf("AC-scope-respected-capture: out-of-scope font change was ingested into the repo/overlay")
	}
}

// TestSSHNotCaptured covers AC-ssh-not-captured: capture never ingests SSH key
// material into the repo, even with ~/.ssh/ populated.
func TestSSHNotCaptured_AC_ssh_not_captured(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest)
	s.SSHTripwire(t)

	// The private-key sentinel bytes seeded by SSHTripwire.
	secret := "-----BEGIN OPENSSH PRIVATE KEY-----"

	// Approve aggressively; nothing from ~/.ssh/ may enter the repo.
	s.FerryWithInput("y\ny\ny\ny\n", "capture")

	s.AssertNoSecretInRepo(t, secret)
	s.AssertSSHUntouched(t)
}

// TestCaptureHunkByHunk covers AC-capture-hunk-by-hunk: for a multi-hunk text
// change, capture lets the user accept one hunk and reject another within the same
// file, and ONLY the accepted hunk lands in the repo. Whole-file accept-all/nothing
// routing does NOT satisfy this promise.
//
// TODO(contract): the hunk accept/reject keystroke protocol (like git add -p's
// y/n) is described ("hunk by hunk for text") but not pinned to exact keys. We
// drive a git-add-p-style "y\nn\n" (accept first hunk, reject second) plus a route
// answer, and assert the OBSERVABLE outcome. If the binary uses different keys,
// this surfaces once it lands; the observable assertion (only accepted hunk in
// repo, rejected hunk nowhere) is the load-bearing gate.
func TestCaptureHunkByHunk_AC_capture_hunk_by_hunk(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest)

	// A managed text dotfile with enough separation for two distinct hunks.
	base := "# top\nL1\nL2\nL3\nL4\nL5\nL6\nL7\nL8\nL9\n# bottom\n"
	s.WriteRepoFile(t, ".zshrc", base)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), base)
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}

	// Two well-separated changes: ACCEPT_TOP near the top, REJECT_BOTTOM near the
	// bottom, with unchanged context between so they form independent hunks.
	edited := "# top\nACCEPT_TOP\nL2\nL3\nL4\nL5\nL6\nL7\nL8\nREJECT_BOTTOM\n# bottom\n"
	if err := os.WriteFile(s.HomePath(".zshrc"), []byte(edited), 0o644); err != nil {
		t.Fatalf("multi-hunk edit: %v", err)
	}

	// Accept first hunk, reject second, route shared. Extra answers are harmless.
	s.FerryWithInput("y\nn\ns\ny\nn\n", "capture")

	// Observable gate: the repo's resulting .zshrc contains the accepted hunk's
	// change and does NOT contain the rejected hunk's change; rejected content
	// appears nowhere in repo or overlay.
	repoZsh := s.RepoPath(".zshrc")
	data, err := os.ReadFile(repoZsh)
	if err != nil {
		// If capture wrote to a different shared path, fall back to a repo-wide grep.
		if repoContains(t, s, "ACCEPT_TOP") && !repoContains(t, s, "REJECT_BOTTOM") {
			return // accepted hunk landed somewhere in repo; rejected did not
		}
		t.Fatalf("AC-capture-hunk-by-hunk: could not read repo .zshrc and accept/reject not otherwise observable: %v", err)
	}
	body := string(data)
	if !contains(body, "ACCEPT_TOP") {
		t.Errorf("AC-capture-hunk-by-hunk: accepted hunk (ACCEPT_TOP) did not land in the repo")
	}
	if contains(body, "REJECT_BOTTOM") {
		t.Errorf("AC-capture-hunk-by-hunk: rejected hunk (REJECT_BOTTOM) was written to the repo (accept-all routing?)")
	}
	// Rejected content must appear nowhere in the repo / overlay.
	s.AssertNoSecretInRepo(t, "REJECT_BOTTOM")
}

// TestCaptureNoAutocommit covers AC-capture-no-autocommit: capture writes accepted
// changes into the repo WORKING TREE but does NOT commit and does NOT push. We
// FIRST prove capture actually wrote (worktree dirty — a hard failure if it did
// not, so a write-nothing impl cannot trivially "pass" no-commit), THEN assert
// HEAD is unchanged (no commit) and a real bare REMOTE's tip is unmoved (no push).
func TestCaptureNoAutocommit_AC_capture_no_autocommit(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# baseline\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# baseline\n")
	gitCommitAll(t, s.Repo, "baseline")

	// Wire a bare remote and push the baseline so a remote tip exists to tripwire.
	remote := gitAddBareRemote(t, s.Repo)
	remoteTipBefore := gitRevParse(t, remote, "HEAD")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}
	// An in-scope local change with a unique marker for capture to ingest.
	marker := "NOAUTOCOMMIT_MARKER"
	if err := os.WriteFile(s.HomePath(".zshrc"), []byte("# zshrc\n"+marker+"\n"), 0o644); err != nil {
		t.Fatalf("local edit: %v", err)
	}

	headBefore := gitRevParse(t, s.Repo, "HEAD")

	// Run capture under a git-stub recorder (placed FIRST on PATH) so we observe
	// EVERY git subcommand ferry invokes during capture. Accept + route shared so
	// capture actually writes into the working tree.
	git := newGitStub(t)
	s.FerryEnvWithInput("y\ns\ny\n", []string{git.pathEnv()}, "capture")

	// (a) capture actually WROTE: working tree is dirty / the marker is present.
	// This is a HARD failure — a write-nothing impl must not pass this AC.
	wroteToWorktree := !gitWorktreeClean(t, s.Repo) || repoContains(t, s, marker)
	if !wroteToWorktree {
		t.Fatalf("AC-capture-no-autocommit: capture wrote nothing to the repo working tree; cannot assert no-autocommit on a no-op")
	}

	// (b) REAL TRIPWIRE: ferry must have invoked NO `git push` and NO `git commit`
	// during capture (the user runs commit/push themselves). A broken capture that
	// runs `git push` with HEAD unchanged would be caught here, not by tip-compare.
	if git.invokedSubcommand("push") {
		t.Errorf("AC-capture-no-autocommit: ferry invoked `git push` during capture (must not push)\nrecorded: %v", git.lines())
	}
	if git.invokedSubcommand("commit") {
		t.Errorf("AC-capture-no-autocommit: ferry invoked `git commit` during capture (must not commit)\nrecorded: %v", git.lines())
	}

	// (c) Corroboration: HEAD unchanged (no commit) and remote tip unmoved (no push).
	if headAfter := gitRevParse(t, s.Repo, "HEAD"); headBefore != headAfter {
		t.Errorf("AC-capture-no-autocommit: HEAD changed (%s -> %s); ferry created a commit", headBefore, headAfter)
	}
	if remoteTipAfter := gitRevParse(t, remote, "HEAD"); remoteTipBefore != remoteTipAfter {
		t.Errorf("AC-capture-no-autocommit: remote tip moved (%s -> %s); ferry pushed", remoteTipBefore, remoteTipAfter)
	}
}

// --- small git/string helpers ---

// gitEnv returns a deterministic git identity env for sandbox repos.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=eval", "GIT_AUTHOR_EMAIL=eval@localhost",
		"GIT_COMMITTER_NAME=eval", "GIT_COMMITTER_EMAIL=eval@localhost")
}

func gitCommitAll(t *testing.T, repo, msg string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", msg)
}

func gitRevParse(t *testing.T, repo, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", ref)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v\n%s", ref, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitWorktreeClean(t *testing.T, repo string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "status", "--porcelain")
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out)) == ""
}

// gitAddBareRemote creates a bare repo, adds it as `origin`, and pushes the
// current branch so the remote has a tip. Returns the bare repo path (a push
// tripwire: its HEAD must stay put unless something pushes).
func gitAddBareRemote(t *testing.T, repo string) string {
	t.Helper()
	bare := t.TempDir()
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(bare, "init", "-q", "--bare")
	run(repo, "remote", "add", "origin", bare)
	// Push the current branch to establish the remote tip.
	cmd := exec.Command("git", "-C", repo, "push", "-q", "origin", "HEAD")
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git push origin HEAD: %v\n%s", err, out)
	}
	return bare
}

// gitStub is a recording wrapper for `git`: a fake `git` placed EARLIER on PATH
// that appends its full argv (one invocation per line) to a log, then FORWARDS to
// the real git so ferry's legitimate operations (clone, status, …) still work.
// Tests inspect the log to prove which subcommands ferry actually invoked (push,
// commit, clone https://…) — a real invocation tripwire, not just an end-state.
type gitStub struct {
	dir string // prepend to PATH so this `git` shadows the real one
	log string // path to the invocation log (one argv line per call)
}

// newGitStub builds the recorder. It resolves the REAL git up front so the stub can
// forward to it. Skips the test if git is unavailable.
func newGitStub(t *testing.T) gitStub {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH: git-stub recorder unavailable")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "git_invocations.log")
	// Record argv then exec the real git with the same args. Using the absolute
	// real-git path avoids recursing into this stub.
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n" +
		"exec " + shellQuote(realGit) + " \"$@\"\n"
	stub := filepath.Join(dir, "git")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("newGitStub: write stub: %v", err)
	}
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatalf("newGitStub: chmod stub: %v", err)
	}
	return gitStub{dir: dir, log: logPath}
}

// pathEnvWith returns a PATH= override that puts the stub dir FIRST, ahead of the
// rest of PATH, so ferry's `git` resolves to the recorder.
func (g gitStub) pathEnvWith(rest string) string {
	return "PATH=" + g.dir + string(os.PathListSeparator) + rest
}

// pathEnv puts the stub dir ahead of the current process PATH.
func (g gitStub) pathEnv() string {
	return g.pathEnvWith(os.Getenv("PATH"))
}

// lines returns the recorded git invocations (each the argv string of one call).
func (g gitStub) lines() []string {
	data, err := os.ReadFile(g.log)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// invokedSubcommand reports whether any recorded `git` call used the given
// subcommand as a top-level git command (e.g. "push", "commit", "clone"). It scans
// each argv for the first non-flag/non -C/-c token.
func (g gitStub) invokedSubcommand(sub string) bool {
	for _, line := range g.lines() {
		if firstGitSubcommand(line) == sub {
			return true
		}
	}
	return false
}

// invokedMatching reports whether any recorded call's argv contains ALL of the
// given substrings (e.g. "clone" + the https URL).
func (g gitStub) invokedMatching(needles ...string) bool {
	for _, line := range g.lines() {
		all := true
		for _, n := range needles {
			if !strings.Contains(line, n) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// firstGitSubcommand extracts the top-level git subcommand from an argv line,
// skipping leading global options (`-C <dir>`, `-c k=v`, `--git-dir=…`, other flags).
func firstGitSubcommand(argv string) string {
	toks := strings.Fields(argv)
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		if tok == "-C" || tok == "-c" {
			i++ // skip this flag's value
			continue
		}
		if strings.HasPrefix(tok, "-") {
			continue // other global flag
		}
		return tok // first bare token = the subcommand
	}
	return ""
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// repoContains greps the repo working tree for a substring (helper for the
// hunk-by-hunk fallback when the shared sink path is not the literal .zshrc).
func repoContains(t *testing.T, s *Sandbox, needle string) bool {
	t.Helper()
	found := false
	_ = filepath.Walk(s.Repo, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr == nil && strings.Contains(string(data), needle) {
			found = true
		}
		return nil
	})
	return found
}
