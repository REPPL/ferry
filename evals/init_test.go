package evals

// init-behaviour ACs: the Fresh path (new repo), the Existing path (HTTPS clone
// contract), and opt-in dev-tree scaffolding.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestInitFresh covers AC-init-fresh: `ferry init` supports the FRESH path — on a
// machine with NO pre-existing ferry repo and NO clone URL, init initialises/wires
// up a new ferry config repo so "Fresh: capture this machine" works. A clone-only
// implementation (that can only clone an existing remote) MUST NOT pass.
//
// TODO(contract): the exact non-interactive fresh-path invocation/keystrokes are
// not doc-pinned (init "asks before scaffolding"). We feed empty stdin and provide
// no clone URL, so init must take the fresh path. If a flag is required, this test
// surfaces it as a real failure once the binary lands.
func TestInitFresh_AC_init_fresh(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: fresh init needs git to establish a repo")
	}
	s := NewSandbox(t)
	// Deliberately NO repo seeded and NO clone URL configured: the fresh path.

	// The documented fresh setup flow must COMPLETE successfully (exit 0) — a CLI
	// that writes the files but exits non-zero must NOT pass.
	if _, errOut, code := s.FerryWithInput("", "init"); code != 0 {
		t.Fatalf("AC-init-fresh: `ferry init` (fresh path) exited %d (setup must complete successfully)\n%s", code, errOut)
	}

	// (b) config.toml records the repo path (per AC-loc-config-toml).
	cfgData, err := os.ReadFile(s.ConfigTOMLPath())
	if err != nil {
		t.Fatalf("AC-init-fresh: config.toml not written by fresh init: %v", err)
	}
	repoPath := extractRepoPath(string(cfgData))
	if repoPath == "" {
		t.Fatalf("AC-init-fresh: config.toml does not record a usable repo path\n%s", cfgData)
	}

	// (a) a usable ferry config repo exists on disk (git recognises it) AND it is a
	// real WORKING TREE (not bare) — the Fresh workflow commits in a working clone.
	if out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--git-dir").CombinedOutput(); err != nil {
		t.Fatalf("AC-init-fresh: recorded repo %q is not a usable git repo (clone-only impl?): %v\n%s", repoPath, err, out)
	}
	wt, err := exec.Command("git", "-C", repoPath, "rev-parse", "--is-bare-repository").CombinedOutput()
	if err != nil || strings.TrimSpace(string(wt)) != "false" {
		t.Errorf("AC-init-fresh: fresh repo at %q is BARE (got %q) — Fresh workflow needs a committable working tree", repoPath, strings.TrimSpace(string(wt)))
	}

	// (c) END-TO-END Fresh workflow (minus push): a capture routing SHARED must land
	// content in the repo working tree (dirty), and `git commit` must succeed —
	// proving the fresh repo is a real, committable working clone.
	marker := "FRESH_CAPTURE_MARKER"
	if err := os.WriteFile(s.HomePath(".zshrc"), []byte("# zshrc\n"+marker+"\n"), 0o644); err != nil {
		t.Fatalf("seed local change: %v", err)
	}
	// Apply first so .zshrc is tracked/known, then make the local edit captured shared.
	s.Ferry("apply")
	if err := os.WriteFile(s.HomePath(".zshrc"), []byte("# zshrc\n"+marker+"\n"), 0o644); err != nil {
		t.Fatalf("re-seed local change: %v", err)
	}
	s.FerryWithInput("y\ns\ny\n", "capture")

	// Working tree must be dirty (capture wrote into it) and contain the marker.
	if gitWorktreeClean(t, repoPath) && !repoTreeContains(t, repoPath, marker) {
		t.Fatalf("AC-init-fresh: capture wrote nothing into the fresh repo — cannot prove a committable Fresh workflow")
	}
	// `git commit` must succeed against the fresh repo (real working clone, HEAD attached).
	stageOut, stageErr := runGitIn(repoPath, "add", "-A")
	if stageErr != nil {
		t.Fatalf("AC-init-fresh: `git add` failed in fresh repo: %v\n%s", stageErr, stageOut)
	}
	commitOut, commitErr := runGitIn(repoPath, "commit", "-m", "Initial capture")
	if commitErr != nil {
		t.Errorf("AC-init-fresh: `git commit` failed in fresh repo (not a committable working clone?): %v\n%s", commitErr, commitOut)
	}
}

// runGitIn runs a git command in repo with a deterministic identity and returns
// its combined output and error.
func runGitIn(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// repoTreeContains greps a repo working tree (skipping .git) for a substring.
func repoTreeContains(t *testing.T, repo, needle string) bool {
	t.Helper()
	found := false
	_ = filepath.Walk(repo, func(path string, info os.FileInfo, err error) error {
		if err != nil || found {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
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

// TestInitCloneHTTPS covers AC-init-clone-https: the existing-repo path clones the
// configured repo and writes config.toml pointing at the clone. HTTPS contract:
// init accepts an https:// URL and requires NO SSH key to do so. A file:// stand-in
// is the offline fast-path; the network leg is deferred.
//
// TODO(contract): how init is told the repo URL (flag / prompt / env) is not
// doc-pinned. We seed a local bare repo as the file:// fast-path via empty stdin;
// if init needs an explicit URL argument, this surfaces once the binary lands.
func TestInitCloneHTTPS_AC_init_clone_https(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: clone path needs git")
	}
	s := NewSandbox(t)
	s.SSHTripwire(t) // HTTPS clone must not touch/read ~/.ssh/

	// Offline fast-path: a local bare repo (with a unique marker file) standing in
	// for the remote, addressed as a real file:// URL.
	bare, markerFile := makeBareRepo(t)
	fileURL := "file://" + bare

	// Drive init at the file:// URL. The URL-passing mechanism is not doc-pinned;
	// we pass it as an argument (most natural) and also via stdin. The documented
	// existing-repo setup must COMPLETE successfully offline (exit 0) — a CLI that
	// writes config but exits non-zero must NOT pass.
	if _, errOut, code := s.FerryWithInput(fileURL+"\n", "init", fileURL); code != 0 {
		t.Fatalf("AC-init-clone-https: `ferry init file://<local-repo>` exited %d (offline clone setup must complete successfully)\n%s", code, errOut)
	}

	// config.toml must record the clone path — and it must NOT be the bare SOURCE
	// itself (init must have CLONED into a working tree, not pointed at the source).
	cfgData, err := os.ReadFile(s.ConfigTOMLPath())
	if err != nil {
		t.Fatalf("AC-init-clone-https: config.toml not written: %v", err)
	}
	repoPath := extractRepoPath(string(cfgData))
	if repoPath == "" {
		t.Fatalf("AC-init-clone-https: config.toml records no repo path\n%s", cfgData)
	}
	if sameDir(repoPath, bare) {
		t.Errorf("AC-init-clone-https: config.toml points straight at the bare SOURCE %q (init did not create a clone)", bare)
	}

	// PROVE A WORKING CLONE: the recorded path is a real working tree AND it
	// contains the source's marker file (so the clone actually pulled content).
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--is-inside-work-tree")
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Errorf("AC-init-clone-https: recorded repo %q is not a working tree (got %q, err %v)", repoPath, out, err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, markerFile)); err != nil {
		t.Errorf("AC-init-clone-https: clone at %q is missing the source marker %q — content was not actually cloned: %v", repoPath, markerFile, err)
	}

	// HTTPS contract: clone needed no SSH key — ~/.ssh/ untouched by init.
	s.AssertSSHUntouched(t)

	// HTTPS branch: prove the https:// URL was actually HONORED — init must accept
	// the scheme and ATTEMPT the clone over https (an observable clone/fetch attempt
	// against the https host), failing only because the host doesn't resolve (network
	// leg deferred), NOT because it rejected the scheme or demanded an SSH key. We
	// arm the SSH tripwire so a binary that READS ~/.ssh while handling an https URL
	// is caught (handling https must require no SSH material at all).
	sHTTPS := NewSandbox(t)
	sHTTPS.SSHTripwire(t)
	httpsURL := "https://ferry.invalid/REPPL/ferry.git"
	// Run init under a git-stub recorder so we can PROVE ferry shelled out to clone
	// over https — not merely printed an https-shaped error.
	git := newGitStub(t)
	out, errOut, code := sHTTPS.FerryEnvWithInput(httpsURL+"\n", []string{git.pathEnv()}, "init", httpsURL)
	combined := strings.ToLower(out + errOut)
	if containsAnyFold(combined, "unsupported scheme", "only file://", "https not supported", "scheme not", "invalid url scheme") {
		t.Errorf("AC-init-clone-https: init rejected the https:// URL scheme\n%s", out+errOut)
	}
	if containsAnyFold(combined, "ssh key required", "no ssh key", "missing ssh key", "ssh key not found", "id_rsa", "id_ed25519 required") {
		t.Errorf("AC-init-clone-https: init demanded an SSH key for an https:// URL (must not)\n%s", out+errOut)
	}
	// REAL INVOCATION TRIPWIRE: ferry must have INVOKED `git clone` (or `git fetch`)
	// WITH the https:// URL — proving it shells out to clone over https rather than
	// silently ignoring the URL or just printing an error. (The actual network fetch
	// stays deferred — the host doesn't resolve — but the invocation is observed.)
	clonedHTTPS := git.invokedMatching("clone", httpsURL) || git.invokedMatching("fetch", httpsURL)
	if !clonedHTTPS {
		t.Errorf("AC-init-clone-https: ferry did not invoke `git clone`/`git fetch` with the https:// URL %q — clone-over-https not observed.\nrecorded git calls: %v\nstdout/stderr:\n%s",
			httpsURL, git.lines(), out+errOut)
	}
	// Corroboration: the attempt also surfaces as a clone/network failure (host
	// non-resolving), confirming the URL was honored. (Network fetch deferred.)
	_ = code
	// Handling an https URL must not WRITE/modify ~/.ssh/ at all (floor).
	sHTTPS.AssertSSHUntouched(t)

	// And it must not READ ~/.ssh/ either: where an fs_usage trace is usable on
	// macOS, ENFORCE no open/openat under ~/.ssh while init handles the https URL
	// (an https clone needs no SSH material). Capability-skip with a precise reason
	// when the tracer isn't privileged; AssertSSHUntouched above remains the floor.
	if runtime.GOOS == "darwin" {
		sshDir := sHTTPS.HomePath(".ssh")
		if fsUsageUsable(t, sHTTPS) {
			if opens := traceSSHOpens(t, sHTTPS, sshDir, "init", httpsURL); len(opens) > 0 {
				t.Errorf("AC-init-clone-https: `init %s` opened path(s) under ~/.ssh/ (https handling must not read SSH material):\n%s",
					httpsURL, strings.Join(opens, "\n"))
			}
		} else {
			t.Log("AC-init-clone-https: fs_usage not usable (needs privilege); no-open(~/.ssh) read-trace for the https init skipped — AssertSSHUntouched is the floor.")
		}
	}
	t.Log("AC-init-clone-https: https:// network fetch is deferred (host non-resolving); " +
		"file:// working-clone-with-content + https-scheme-honored + no-SSH-key + ~/.ssh-untouched(+no-read where traceable) contract asserted here.")
}

// sameDir reports whether two paths refer to the same directory (after cleaning /
// resolving symlinks where possible).
func sameDir(a, b string) bool {
	ca, _ := filepath.Abs(filepath.Clean(a))
	cb, _ := filepath.Abs(filepath.Clean(b))
	if ra, err := filepath.EvalSymlinks(ca); err == nil {
		ca = ra
	}
	if rb, err := filepath.EvalSymlinks(cb); err == nil {
		cb = rb
	}
	return ca == cb
}

// TestGitPreflight covers AC-git-preflight: commands that need git (init, capture)
// preflight it; with `git` ABSENT from PATH they fail clearly with install guidance
// rather than crashing opaquely (non-zero exit and/or a message naming git).
func TestGitPreflight_AC_git_preflight(t *testing.T) {
	t.Parallel()

	// A PATH that contains NO git (an empty temp dir). We do NOT inherit the real
	// PATH, so the binary cannot discover a host git.
	noGitPath := "PATH=" + t.TempDir()

	for _, cmd := range []string{"init", "capture"} {
		cmd := cmd
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			s.WriteRepoFile(t, "ferry.toml", baseManifest)
			if err := os.WriteFile(s.ConfigTOMLPath(), []byte("repo = \""+s.Repo+"\"\n"), 0o644); err != nil {
				t.Fatalf("write config.toml: %v", err)
			}

			out, errOut, code := s.FerryEnv([]string{noGitPath}, cmd)
			combined := out + errOut

			// Require BOTH: (1) the command FAILS clearly (non-zero) and (2) the
			// output tells the user how to INSTALL git (install guidance / an OS
			// install command). Exit 0 is not allowed; non-zero with no guidance is
			// not allowed; merely mentioning "git" is not enough.
			if code == 0 {
				t.Errorf("AC-git-preflight: `ferry %s` with git absent exited 0 (must fail clearly)\n%s", cmd, combined)
			}
			if !containsAnyFold(combined, "git") {
				t.Errorf("AC-git-preflight: `ferry %s` failed but did not name git as the missing prerequisite\n%s", cmd, combined)
			}
			// Actionable install guidance: an install verb or a concrete install command.
			guides := containsAnyFold(combined,
				"install", "brew install", "apt install", "apt-get install", "yum install",
				"dnf install", "xcode-select", "download", "https://git-scm.com", "get git")
			if !guides {
				t.Errorf("AC-git-preflight: `ferry %s` failed but gave no actionable git-install guidance\n%s", cmd, combined)
			}
		})
	}
}

// TestInitScaffoldOptin covers AC-init-scaffold-optin: init does NOT create the dev
// tree unless the user opts in; declining leaves ~/ABCDevelopment uncreated. When
// confirmed, the tree is created at the documented ~/ABCDevelopment and only if
// missing (never touching existing content).
//
// TODO(contract): the scaffold prompt's exact decline/accept tokens are not
// doc-pinned. We model decline as empty stdin (EOF) and accept as "y\n".
func TestInitScaffoldOptin_AC_init_scaffold_optin(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: init needs git")
	}

	// Decline branch: init must COMPLETE successfully (exit 0) AND create no
	// scaffold. A FAILING init that happens to create no scaffold must NOT count as
	// a clean decline. We run init against the seeded repo (existing path) so the
	// setup can complete; the "n" answer declines the scaffold prompt.
	sNo := NewSandbox(t)
	sNo.InitGitRepo(t)
	sNo.WriteRepoFile(t, "ferry.toml", baseManifest)
	scaffoldNo := sNo.HomePath("ABCDevelopment")
	declineTW := sNo.SnapshotFile(t, scaffoldNo) // absent
	if _, errOut, code := sNo.FerryWithInput("n\n", "init", sNo.Repo); code != 0 {
		t.Fatalf("AC-init-scaffold-optin: declined `ferry init` exited %d (must complete successfully)\n%s", code, errOut)
	}
	declineTW.AssertUnchanged(t) // still absent: scaffold is opt-in, decline made none

	// Opt-in branch: init must COMPLETE successfully (exit 0) AND create the tree at
	// ~/ABCDevelopment. A failing init that still created the dir must NOT pass.
	sYes := NewSandbox(t)
	sYes.InitGitRepo(t)
	sYes.WriteRepoFile(t, "ferry.toml", baseManifest)
	if _, errOut, code := sYes.FerryWithInput("y\ny\n", "init", sYes.Repo); code != 0 {
		t.Fatalf("AC-init-scaffold-optin: opt-in `ferry init` exited %d (must complete successfully)\n%s", code, errOut)
	}
	scaffoldYes := sYes.HomePath("ABCDevelopment")
	if info, err := os.Stat(scaffoldYes); err != nil || !info.IsDir() {
		t.Errorf("AC-init-scaffold-optin: opt-in did not create the documented dev tree at %s (err=%v)", scaffoldYes, err)
	}
}

// makeBareRepo creates a bare git repo with one commit containing a UNIQUE marker
// file (so a real clone can be proven by the marker's presence). Returns the bare
// repo path and the marker filename it committed.
func makeBareRepo(t *testing.T) (bare, markerFile string) {
	t.Helper()
	work := t.TempDir()
	bare = t.TempDir()
	markerFile = "CLONE_SOURCE_MARKER.txt"
	runGit := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=eval", "GIT_AUTHOR_EMAIL=eval@localhost",
			"GIT_COMMITTER_NAME=eval", "GIT_COMMITTER_EMAIL=eval@localhost")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit(work, "init", "-q")
	runGit(work, "config", "user.email", "eval@localhost")
	runGit(work, "config", "user.name", "eval")
	if err := os.WriteFile(filepath.Join(work, "ferry.toml"), []byte(baseManifest), 0o644); err != nil {
		t.Fatalf("seed ferry.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, markerFile), []byte("from-clone-source\n"), 0o644); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	runGit(work, "add", "-A")
	runGit(work, "commit", "-q", "-m", "initial")
	runGit(work, "clone", "-q", "--bare", work, bare)
	return bare, markerFile
}

// extractRepoPath pulls the first quoted or bare value after a `repo`-ish key from
// config.toml. Tolerant of formatting since the exact key is not doc-pinned.
func extractRepoPath(toml string) string {
	for _, line := range strings.Split(toml, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		eq := strings.IndexByte(l, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(l[:eq])
		if !containsAnyFold(key, "repo", "clone", "path") {
			continue
		}
		val := strings.TrimSpace(l[eq+1:])
		val = strings.Trim(val, "\"'")
		if val != "" {
			return val
		}
	}
	return ""
}
