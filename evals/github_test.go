package evals

// Route-2 (managed GitHub) behavioral evals for `ferry init --github`. Each test
// drives the REAL binary via the harness and asserts an OBSERVABLE outcome
// (recorded gh/git invocations, files present/absent, exit codes, stdout wording,
// tripwires) — never ferry internals. They are RED-until-impl: requireBin(t) (via
// Sandbox.Ferry / FerryEnv) SKIPS every test when FERRY_BIN is unset, so the
// package stays green before cmd/init.go gains --github + internal/ghcli land.
//
// NEVER hits real GitHub. The `gh` CLI is replaced by a PATH-shim MOCK the test
// writes into a temp bin dir placed FIRST on PATH: it RECORDS every invocation's
// argv to a log the test reads, and returns CANNED output per scenario (driven by
// an env var the test sets). The `git push` is observed either by the existing
// gitStub recorder (proving a push was / was not invoked) or a local bare repo as
// origin (proving content actually moved).
//
// ── COORDINATION NOTE FOR THE IMPLEMENTATION ──────────────────────────────────
// The mock's canned-scenario dispatcher below assumes ferry emits these exact gh
// subcommands (see .work/ACCEPTANCE-v0.2.2.md §Implementation coordination):
//   gh api user                                              -> {"login": "<owner>"}
//   gh auth status                                           -> exit 0 (or 1 unauthed)
//   gh repo view <owner>/<name>                              -> exit 0 exists / non-zero not
//   gh repo view <owner>/<name> --json nameWithOwner,isPrivate,url -> canned JSON
//   gh repo create <owner>/<name> --private                 -> success / already-exists error
// If the impl chooses different argv, update ghMock's dispatch + the acceptance doc
// in lockstep. Concretely, the mock keys off the SUBCOMMAND words and emits a fixed
// JSON shape; an impl that instead uses `gh api user --jq .login`, `gh repo view
// --jq …`, `--json owner` (nested object) rather than `nameWithOwner`, or a different
// verb order would need matching mock branches — otherwise a CORRECT impl false-fails.
// This is the single coordination point between these evals and the impl.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ghOwner is the canned login the mock `gh api user` returns. Tests build the
// intended <owner>/<name> against it. NOT a real account.
const ghOwner = "octocat"

// A fake, NON-FUNCTIONAL secret for the ~/.zshrc secret-gate test — the realistic
// shape internal/secret keys on, never a real key.
const fakeGitHubSecret = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUFAKEFAKEFAKEFAKEdeadbeefcafe0102feed\n" +
	"-----END OPENSSH PRIVATE KEY-----\n"

// -----------------------------------------------------------------------------
// Mock gh — a PATH shim that records argv and returns canned output per scenario.
// -----------------------------------------------------------------------------

// ghMock is a recording, canned-output replacement for the `gh` CLI. It writes a
// shell script named `gh` into its own temp dir (placed FIRST on PATH) that:
//   - appends its full argv (one line per call) to a log the test reads, and
//   - branches on the argv to emit canned stdout + the right exit code, driven by
//     scenario knobs passed as env vars (FERRY_GHMOCK_*), so ONE mock binary
//     serves every scenario. It never contacts the network.
type ghMock struct {
	dir string // prepend to PATH so this `gh` shadows any real gh
	log string // path to the invocation log (one argv line per call)
}

// ghScenario is the set of canned-behaviour knobs the mock reads from the env at
// call time. Zero values are the "clean happy path" defaults where sensible.
type ghScenario struct {
	// authStatusExit: exit code for `gh auth status` (0 = authed).
	authOK bool
	// login: the value `gh api user` reports (the resolved owner).
	login string
	// repoViewExists: when true, `gh repo view <owner>/<name>` exits 0 (repo EXISTS);
	// when false it exits non-zero with a "not found" message (repo AVAILABLE).
	repoViewExists bool
	// repoViewNetErr: when true, `gh repo view <owner>/<name>` exits with a NETWORK
	// error (not a clean "not found") — ferry must surface it, not treat as exists.
	repoViewNetErr bool
	// createExit: exit code for `gh repo create` (0 = created; non-zero = failure,
	// e.g. an already-exists TOCTOU).
	createFails bool
	// The post-create `gh repo view --json …` canned fields.
	jsonIsPrivate     bool
	jsonNameWithOwner string // e.g. "octocat/myrepo"; empty => omit (missing-field case)
	jsonURL           string // the clone url; e.g. https://github.com/octocat/myrepo.git
	// jsonMalformed: when true, the `repo view --json` form emits invalid JSON so the
	// parse-error abort path (step 7) is exercised.
	jsonMalformed bool
}

// env renders the scenario as FERRY_GHMOCK_* env entries the shim reads.
func (sc ghScenario) env() []string {
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	login := sc.login
	if login == "" {
		login = ghOwner
	}
	return []string{
		"FERRY_GHMOCK_AUTH_OK=" + b(sc.authOK),
		"FERRY_GHMOCK_LOGIN=" + login,
		"FERRY_GHMOCK_VIEW_EXISTS=" + b(sc.repoViewExists),
		"FERRY_GHMOCK_VIEW_NETERR=" + b(sc.repoViewNetErr),
		"FERRY_GHMOCK_CREATE_FAILS=" + b(sc.createFails),
		"FERRY_GHMOCK_JSON_PRIVATE=" + b(sc.jsonIsPrivate),
		"FERRY_GHMOCK_JSON_NWO=" + sc.jsonNameWithOwner,
		"FERRY_GHMOCK_JSON_URL=" + sc.jsonURL,
		"FERRY_GHMOCK_JSON_MALFORMED=" + b(sc.jsonMalformed),
	}
}

// newGHMock writes the mock `gh` script and returns the handle. The script is a
// POSIX shell dispatcher: it logs argv, then branches on the subcommand. All canned
// output is FAKE; it never reaches the network.
func newGHMock(t *testing.T) ghMock {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gh_invocations.log")

	// The dispatcher. `$*` is the full argv string (used for logging + matching).
	// We key off substrings because ferry's exact flag ordering is impl-defined; the
	// SUBCOMMAND words (api user / auth status / repo view / repo create / --json /
	// --private) are the contract. Emit canned JSON for the two view forms.
	script := `#!/bin/sh
argv="$*"
# Record the argv AND the GIT_TERMINAL_PROMPT the subprocess inherited, so a test can
# prove ferry ran gh noninteractively (GIT_TERMINAL_PROMPT=0, PLAN step 1). A missing
# var logs as "GTP=<unset>".
printf '%s [GTP=%s]\n' "$argv" "${GIT_TERMINAL_PROMPT:-<unset>}" >> ` + shellQuote(logPath) + `

case "$argv" in
  *"auth status"*)
    if [ "$FERRY_GHMOCK_AUTH_OK" = "1" ]; then
      # Emit a token-SHAPED sentinel exactly as a real gh auth status --show-token
      # would. ferry must NEVER copy this into any file it owns (no-token invariant).
      echo "Logged in to github.com as $FERRY_GHMOCK_LOGIN (mock)"
      echo "  - Token: ghp_FERRYMOCKTOKENSENTINELdeadbeef0123456789"; exit 0
    fi
    echo "gh: not logged in. Run: gh auth login" 1>&2; exit 1
    ;;
  *"api user"*)
    printf '{"login":"%s"}\n' "$FERRY_GHMOCK_LOGIN"; exit 0
    ;;
  *"repo create"*)
    if [ "$FERRY_GHMOCK_CREATE_FAILS" = "1" ]; then
      echo "GraphQL: Name already exists on this account (createRepository)" 1>&2; exit 1
    fi
    echo "Created repository $FERRY_GHMOCK_LOGIN/mock on GitHub"; exit 0
    ;;
  *"repo view"*"--json"*)
    # Post-create verification. Emit a JSON object; omit a field only when its knob
    # is empty (the missing-field case). isPrivate is a real JSON bool. When the
    # malformed knob is set, emit invalid JSON so the parse-error abort path runs.
    if [ "$FERRY_GHMOCK_JSON_MALFORMED" = "1" ]; then
      printf '{not valid json <<<\n'; exit 0
    fi
    priv=false; [ "$FERRY_GHMOCK_JSON_PRIVATE" = "1" ] && priv=true
    printf '{'
    printf '"isPrivate":%s' "$priv"
    [ -n "$FERRY_GHMOCK_JSON_NWO" ] && printf ',"nameWithOwner":"%s"' "$FERRY_GHMOCK_JSON_NWO"
    [ -n "$FERRY_GHMOCK_JSON_URL" ] && printf ',"url":"%s"' "$FERRY_GHMOCK_JSON_URL"
    printf '}\n'
    exit 0
    ;;
  *"repo view"*)
    # Existence check (step 4).
    if [ "$FERRY_GHMOCK_VIEW_NETERR" = "1" ]; then
      echo "error connecting to api.github.com (mock network error)" 1>&2; exit 1
    fi
    if [ "$FERRY_GHMOCK_VIEW_EXISTS" = "1" ]; then
      echo "name:  the repo exists (mock)"; exit 0
    fi
    echo "GraphQL: Could not resolve to a Repository (mock not-found)" 1>&2; exit 1
    ;;
  *)
    # Any UNEXPECTED gh subcommand: record the marker to the LOG (so a log scan catches
    # it even if ferry ignores the failure) AND fail loudly on stderr + a non-zero exit.
    printf 'GHMOCK_UNEXPECTED_CALL %s\n' "$argv" >> ` + shellQuote(logPath) + `
    echo "GHMOCK_UNEXPECTED_CALL: $argv" 1>&2
    exit 3
    ;;
esac
`
	stub := filepath.Join(dir, "gh")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("newGHMock: write stub: %v", err)
	}
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatalf("newGHMock: chmod stub: %v", err)
	}
	return ghMock{dir: dir, log: logPath}
}

// lines returns the recorded gh invocations (each the argv string of one call).
func (g ghMock) lines() []string {
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

// invokedMatching reports whether any recorded gh call's argv contains ALL needles.
func (g ghMock) invokedMatching(needles ...string) bool {
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

// anyLineContains reports whether ANY recorded gh call's argv contains needle.
func (g ghMock) anyLineContains(needle string) bool {
	for _, line := range g.lines() {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

// countMatching returns how many recorded gh calls contain ALL needles.
func (g ghMock) countMatching(needles ...string) int {
	n := 0
	for _, line := range g.lines() {
		all := true
		for _, needle := range needles {
			if !strings.Contains(line, needle) {
				all = false
				break
			}
		}
		if all {
			n++
		}
	}
	return n
}

// allCallsNoninteractive reports whether EVERY recorded gh call inherited
// GIT_TERMINAL_PROMPT=0 (the noninteractive-credential invariant, PLAN step 1). The
// mock logs "[GTP=<val>]" per call; any call that is not GTP=0 fails this. Returns
// (ok, offendingLine).
func (g ghMock) allCallsNoninteractive() (bool, string) {
	for _, line := range g.lines() {
		if strings.Contains(line, "GHMOCK_UNEXPECTED_CALL") {
			continue // separate assertion covers stray calls
		}
		if !strings.Contains(line, "[GTP=0]") {
			return false, line
		}
	}
	return true, ""
}

// pushWasNoninteractive reports whether every recorded `git push` line ran with
// GIT_TERMINAL_PROMPT=0. Only meaningful for the GTP-logging stubs
// (newPushRecordingGitStub / newPushFailingGitStub). Returns (ok, offendingLine);
// ok=true with no push lines means "no push to check" (caller asserts push separately).
func (g gitStub) pushWasNoninteractive() (bool, string) {
	for _, line := range g.lines() {
		if firstGitSubcommand(line) == "push" && !strings.Contains(line, "[GTP=0]") {
			return false, line
		}
	}
	return true, ""
}

// -----------------------------------------------------------------------------
// git push observation.
// -----------------------------------------------------------------------------

// newPushFailingGitStub writes a `git` shim (first on PATH) that LOGS argv like the
// normal recorder, but forces any `push` subcommand to FAIL (exit 1) while
// forwarding every OTHER git subcommand to the real git — so init/commit/remote all
// work and only the first push fails. Used by AC-github-partial-failure.
func newPushFailingGitStub(t *testing.T) gitStub {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH: push-failing git stub unavailable")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "git_invocations.log")
	// Log argv WITH the inherited GIT_TERMINAL_PROMPT so a push line records whether it
	// was noninteractive (PLAN step 1: every git subprocess runs GIT_TERMINAL_PROMPT=0).
	script := "#!/bin/sh\n" +
		"printf '%s [GTP=%s]\\n' \"$*\" \"${GIT_TERMINAL_PROMPT:-<unset>}\" >> " + shellQuote(logPath) + "\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in\n" +
		"    push) echo 'mock: push failed (network)' 1>&2; exit 1 ;;\n" +
		"    -*) : ;;\n" +
		"    *) break ;;\n" +
		"  esac\n" +
		"done\n" +
		"exec " + shellQuote(realGit) + " \"$@\"\n"
	stub := filepath.Join(dir, "git")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("newPushFailingGitStub: write stub: %v", err)
	}
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatalf("newPushFailingGitStub: chmod stub: %v", err)
	}
	return gitStub{dir: dir, log: logPath}
}

// newPushRecordingGitStub writes a `git` shim (first on PATH) that LOGS argv and
// INTERCEPTS `push`, returning SUCCESS (exit 0) WITHOUT a real network push, while
// forwarding every OTHER subcommand (init/add/commit/remote/…) to the real git.
// This is the HAPPY-PATH recorder: ferry marks managed only after a SUCCESSFUL first
// push, and the mock's canned https clone url does not resolve offline — so a real
// push would always fail and FALSE-FAIL a correct impl. Intercepting push lets the
// happy path both (a) record the real `git push` invocation (proving ferry pushed)
// AND (b) succeed, so exit-0 / managed=true / adopt-zshrc can be asserted offline.
// The `push` invocation is still in the log, so invokedSubcommand("push") holds.
func newPushRecordingGitStub(t *testing.T) gitStub {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH: push-recording git stub unavailable")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "git_invocations.log")
	// Log argv WITH the inherited GIT_TERMINAL_PROMPT (noninteractive invariant).
	script := "#!/bin/sh\n" +
		"printf '%s [GTP=%s]\\n' \"$*\" \"${GIT_TERMINAL_PROMPT:-<unset>}\" >> " + shellQuote(logPath) + "\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in\n" +
		"    push) echo 'mock: push recorded (offline success)'; exit 0 ;;\n" +
		"    -*) : ;;\n" +
		"    *) break ;;\n" +
		"  esac\n" +
		"done\n" +
		"exec " + shellQuote(realGit) + " \"$@\"\n"
	stub := filepath.Join(dir, "git")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("newPushRecordingGitStub: write stub: %v", err)
	}
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatalf("newPushRecordingGitStub: chmod stub: %v", err)
	}
	return gitStub{dir: dir, log: logPath}
}

// combinedPathEnv builds a single PATH= entry putting BOTH the gh mock dir and the
// git stub dir ahead of the real PATH, so ferry resolves the mock gh and the
// recording git. Order: gh mock first, git stub next, then the inherited PATH.
func combinedPathEnv(gh ghMock, git gitStub) string {
	return "PATH=" + gh.dir + string(os.PathListSeparator) +
		git.dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

// ghOnlyPathEnv puts ONLY the gh mock dir ahead of the real PATH (real git is used
// directly). For flows where a bare-repo origin observes the push, not a git stub.
func ghOnlyPathEnv(gh ghMock) string {
	return "PATH=" + gh.dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

// seedGoodZshrc writes a substantial, SECRET-FREE ~/.zshrc so initFresh's adoption
// path has real content to adopt (AC-github-adopts-zshrc) and the secret gate has
// nothing to block on the happy path.
func seedGoodZshrc(t *testing.T, s *Sandbox) string {
	t.Helper()
	body := "# my shell\nexport EDITOR=vim\nalias ll='ls -la'\nautoload -Uz compinit && compinit\n"
	return s.WriteHomeFile(t, ".zshrc", body, 0o644)
}

// noConfig asserts ferry wrote NO machine config.toml (used by abort-path ACs that
// must create nothing).
func assertNoConfig(t *testing.T, s *Sandbox) {
	t.Helper()
	if _, err := os.Stat(s.ConfigTOMLPath()); err == nil {
		t.Errorf("expected NO config.toml after an aborted managed init, but one exists at %s", s.ConfigTOMLPath())
	}
}

// configManaged reports whether config.toml records managed=true (tolerant of TOML
// spelling: `managed = true`). Absent key / absent file => false.
func configManaged(t *testing.T, s *Sandbox) bool {
	t.Helper()
	data, err := os.ReadFile(s.ConfigTOMLPath())
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		l := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(l, "#") {
			continue
		}
		if strings.HasPrefix(l, "managed") && strings.Contains(l, "=") {
			val := strings.TrimSpace(l[strings.IndexByte(l, '=')+1:])
			return val == "true"
		}
	}
	return false
}

// managedRepoPath returns the repo path ferry recorded in config.toml, or s.Repo as
// a fallback. The managed route reuses initFresh, which lands the repo at ferry's
// neutral default (~/.config/ferry/repo) — so origin/push land there, not s.Repo.
func managedRepoPath(s *Sandbox) string {
	data, err := os.ReadFile(s.ConfigTOMLPath())
	if err == nil {
		if p := extractRepoPath(string(data)); p != "" {
			return p
		}
	}
	return s.HomePath(".config", "ferry", "repo")
}

// originURLIn returns origin's URL for an explicit repo dir.
func originURLIn(repo string) string {
	cmd := exec.Command("git", "-C", repo, "remote", "get-url", "origin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// -----------------------------------------------------------------------------
// AC-github-exists — command surface + arg validation.
// -----------------------------------------------------------------------------

func TestGitHubExists_AC_github_exists(t *testing.T) {
	t.Parallel()

	t.Run("flag-resolves", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		out, errOut, code := s.Ferry("init", "--help")
		if code != 0 {
			t.Errorf("AC-github-exists: `ferry init --help` exited %d (must resolve)", code)
		}
		if !containsAnyFold(out+errOut, "--github", "github") {
			t.Errorf("AC-github-exists: `ferry init --help` does not advertise --github\n%s", out+errOut)
		}
	})

	t.Run("rejects-github-plus-fresh", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		gh := newGHMock(t)
		_, errOut, code := s.FerryEnv([]string{ghOnlyPathEnv(gh)}, "init", "--github", "myrepo", "--fresh")
		if code == 0 {
			t.Errorf("AC-github-exists: `init --github --fresh` accepted (must reject; mutually exclusive)")
		}
		_ = errOut
	})

	t.Run("rejects-github-plus-source", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		gh := newGHMock(t)
		_, _, code := s.FerryEnv([]string{ghOnlyPathEnv(gh)}, "init", "--github", "myrepo", "https://example.com/x/y.git")
		if code == 0 {
			t.Errorf("AC-github-exists: `init --github <name> <clone-source>` accepted (must reject)")
		}
	})
}

// -----------------------------------------------------------------------------
// AC-github-needs-gh — no gh / unauthed => actionable failure, nothing created.
// -----------------------------------------------------------------------------

func TestGitHubNeedsGH_AC_github_needs_gh(t *testing.T) {
	t.Parallel()

	t.Run("no-gh-on-path", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		// A PATH with NO gh: a dir containing ONLY a real git (so git-preflight passes
		// and the failure is specifically about gh), nothing else.
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Skip("git not on PATH")
		}
		binDir := t.TempDir()
		if err := os.Symlink(realGit, filepath.Join(binDir, "git")); err != nil {
			t.Skipf("cannot stage git-only PATH: %v", err)
		}
		out, errOut, code := s.FerryEnv([]string{"PATH=" + binDir}, "init", "--github", "myrepo", "--yes")
		combined := out + errOut
		if code == 0 {
			t.Errorf("AC-github-needs-gh: `init --github` with no gh on PATH exited 0 (must fail)")
		}
		if !containsAnyFold(combined, "gh") {
			t.Errorf("AC-github-needs-gh: failure did not name gh\n%s", combined)
		}
		assertNoConfig(t, s)
	})

	t.Run("unauthed", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		gh := newGHMock(t)
		sc := ghScenario{authOK: false} // gh present but `auth status` fails
		out, errOut, code := s.FerryEnv(append(sc.env(), ghOnlyPathEnv(gh)), "init", "--github", "myrepo", "--yes")
		combined := out + errOut
		if code == 0 {
			t.Errorf("AC-github-needs-gh: `init --github` while unauthed exited 0 (must fail)")
		}
		if !containsAnyFold(combined, "gh auth login", "auth", "log in", "login") {
			t.Errorf("AC-github-needs-gh: unauthed failure did not point at gh auth login\n%s", combined)
		}
		if gh.anyLineContains("repo create") {
			t.Errorf("AC-github-needs-gh: ferry created a repo while unauthed (recorded a repo create)")
		}
		assertNoConfig(t, s)
	})
}

// -----------------------------------------------------------------------------
// AC-github-name-grammar — bad names rejected BEFORE any gh call.
// -----------------------------------------------------------------------------

func TestGitHubNameGrammar_AC_github_name_grammar(t *testing.T) {
	t.Parallel()

	bad := map[string]string{
		"slash-orgrepo": "acme/myrepo",
		"url":           "https://github.com/x/y",
		"dotgit-suffix": "myrepo.git",
		"whitespace":    "bad name",
		"reserved":      "ferry",
		"bad-char":      "my$repo",                             // outside [A-Za-z0-9._-]
		"over-length":   strings.Repeat("a", 120) + "-toolong", // GitHub caps repo names at 100
	}
	for label, name := range bad {
		label, name := label, name
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			gh := newGHMock(t)
			sc := ghScenario{authOK: true}
			out, errOut, code := s.FerryEnv(append(sc.env(), ghOnlyPathEnv(gh)), "init", "--github", name, "--yes")
			if code == 0 {
				t.Errorf("AC-github-name-grammar[%s]: name %q accepted (must reject)", label, name)
			}
			_ = out
			_ = errOut
			// The load-bearing assertion: rejection happened BEFORE any gh subcommand —
			// the mock recorded ZERO invocations. A name check that ran api/view/create
			// first would leave a line in the log.
			if lines := gh.lines(); len(lines) != 0 {
				t.Errorf("AC-github-name-grammar[%s]: bad name %q reached gh (must be rejected before any gh call). Recorded: %v", label, name, lines)
			}
			assertNoConfig(t, s)
		})
	}
}

// -----------------------------------------------------------------------------
// AC-github-confirms-account — non-interactive without --yes refuses; --yes proceeds.
// -----------------------------------------------------------------------------

func TestGitHubConfirmsAccount_AC_github_confirms_account(t *testing.T) {
	t.Parallel()

	t.Run("refuses-without-yes", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		seedGoodZshrc(t, s)
		gh := newGHMock(t)
		git := newGitStub(t) // a recorder so we can prove NO push happened
		sc := ghScenario{authOK: true, login: ghOwner, repoViewExists: false}
		// Empty stdin (non-interactive) and NO --yes: must refuse to create/push.
		out, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "myrepo")
		combined := out + errOut
		if code == 0 {
			t.Errorf("AC-github-confirms-account: non-interactive without --yes proceeded (must refuse)")
		}
		// It must print the FULL resolved target so the user confirms host + owner +
		// name (PLAN step 2: "<host>/<owner>/<name>"). Require the host AND the owner/name
		// so a prompt that omits the account or the host cannot pass. Accept either the
		// combined "github.com/octocat/myrepo" form or host + "octocat/myrepo" appearing.
		hasHost := containsAnyFold(combined, "github.com")
		hasOwnerRepo := containsAnyFold(combined, ghOwner+"/myrepo")
		if !(hasHost && hasOwnerRepo) {
			t.Errorf("AC-github-confirms-account: refusal did not print the full resolved <host>/<owner>/<name> (host=%v ownerRepo=%v)\n%s", hasHost, hasOwnerRepo, combined)
		}
		// And it must NOT have created or pushed.
		if gh.anyLineContains("repo create") {
			t.Errorf("AC-github-confirms-account: created a repo without confirmation (recorded repo create)")
		}
		if git.invokedSubcommand("push") {
			t.Errorf("AC-github-confirms-account: pushed without confirmation")
		}
	})

	t.Run("proceeds-with-yes", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		seedGoodZshrc(t, s)
		gh := newGHMock(t)
		git := newPushRecordingGitStub(t)
		sc := ghScenario{
			authOK: true, login: ghOwner, repoViewExists: false,
			jsonIsPrivate: true, jsonNameWithOwner: ghOwner + "/myrepo",
			jsonURL: "https://github.com/" + ghOwner + "/myrepo.git",
		}
		_, _, _ = s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "myrepo", "--yes")
		// With --yes, ferry advanced into the create flow (a repo create was recorded).
		if !gh.anyLineContains("repo create") {
			t.Errorf("AC-github-confirms-account: `--yes` did not proceed into the create flow.\nrecorded gh: %v", gh.lines())
		}
	})
}

// -----------------------------------------------------------------------------
// AC-github-refuses-existing — mock says exists => abort, no create/push, no derive.
// -----------------------------------------------------------------------------

func TestGitHubRefusesExisting_AC_github_refuses_existing(t *testing.T) {
	t.Parallel()

	t.Run("exists-aborts", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		seedGoodZshrc(t, s)
		gh := newGHMock(t)
		git := newGitStub(t)
		sc := ghScenario{authOK: true, login: ghOwner, repoViewExists: true}
		out, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "taken", "--yes")
		combined := out + errOut
		if code == 0 {
			t.Errorf("AC-github-refuses-existing: target exists but init exited 0 (must abort)")
		}
		if !containsAnyFold(combined, "different name", "already exists", "exists", "another name") {
			t.Errorf("AC-github-refuses-existing: abort did not tell the user to pass a different name\n%s", combined)
		}
		if gh.anyLineContains("repo create") {
			t.Errorf("AC-github-refuses-existing: created a repo despite the target existing")
		}
		// No auto-derive (absolute): ferry must view EXACTLY the confirmed name once and
		// probe NO other repo name. Requiring "exactly one repo view, and it names
		// octocat/taken" catches ANY derived probe (taken-2, taken-copy, …), not just a
		// single hard-coded suffix.
		views := gh.countMatching("repo view")
		if views != 1 {
			t.Errorf("AC-github-refuses-existing: expected EXACTLY one `repo view` (the confirmed name); got %d — a derived-name probe or a loop. gh: %v", views, gh.lines())
		}
		if !gh.invokedMatching("repo view", ghOwner+"/taken") {
			t.Errorf("AC-github-refuses-existing: the single repo view did not target the exact confirmed name %s/taken. gh: %v", ghOwner, gh.lines())
		}
		// And no push happened.
		if git.invokedSubcommand("push") {
			t.Errorf("AC-github-refuses-existing: a git push was invoked despite the abort")
		}
	})

	t.Run("network-error-not-exists", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		seedGoodZshrc(t, s)
		gh := newGHMock(t)
		git := newGitStub(t)
		sc := ghScenario{authOK: true, login: ghOwner, repoViewNetErr: true}
		out, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "myrepo", "--yes")
		combined := out + errOut
		if code == 0 {
			t.Errorf("AC-github-refuses-existing[neterr]: a view network error exited 0 (must abort)")
		}
		// The abort surfaces the API/network error — NOT "already exists".
		if containsAnyFold(combined, "already exists") {
			t.Errorf("AC-github-refuses-existing[neterr]: a network error was reported as 'already exists'\n%s", combined)
		}
		if gh.anyLineContains("repo create") {
			t.Errorf("AC-github-refuses-existing[neterr]: created a repo despite a view error")
		}
		// Not looped: a single `repo view` (no retry storm) — at most one existence view.
		if n := gh.countMatching("repo view", "myrepo"); n > 1 {
			t.Errorf("AC-github-refuses-existing[neterr]: ferry retried the failing view %d times (must not loop)", n)
		}
		if git.invokedSubcommand("push") {
			t.Errorf("AC-github-refuses-existing[neterr]: a push happened despite the view error")
		}
	})

	// Create-race / TOCTOU (PLAN step 6): the step-4 view says AVAILABLE, but the
	// `repo create` itself FAILS with a name-exists error (the name was taken between
	// check and create). ferry must ABORT — NEVER fall through to push, never mark
	// managed. A create failure never proceeds.
	t.Run("create-race-toctou", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		seedGoodZshrc(t, s)
		gh := newGHMock(t)
		git := newGitStub(t)
		sc := ghScenario{
			authOK: true, login: ghOwner, repoViewExists: false, // view: available
			createFails: true, // but create races and fails (name now exists)
		}
		out, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "myrepo", "--yes")
		combined := out + errOut
		if code == 0 {
			t.Errorf("AC-github-refuses-existing[toctou]: `repo create` failed but init exited 0 (a create failure must never proceed)")
		}
		// The create was attempted (recorded) and failed — ferry must NOT push after it.
		if !gh.anyLineContains("repo create") {
			t.Errorf("AC-github-refuses-existing[toctou]: setup invariant — expected a repo create attempt")
		}
		if git.invokedSubcommand("push") {
			t.Errorf("AC-github-refuses-existing[toctou]: pushed after a failed repo create (must abort)")
		}
		if configManaged(t, s) {
			t.Errorf("AC-github-refuses-existing[toctou]: managed=true set despite a failed create")
		}
		// Require the message to name the CREATE step AND that it failed/the name is
		// taken — a bare generic "failed" is too weak to explain a failed create.
		namesCreate := containsAnyFold(combined, "create", "creating", "repo")
		namesFailure := containsAnyFold(combined, "exist", "taken", "already")
		if !(namesCreate && namesFailure) {
			t.Errorf("AC-github-refuses-existing[toctou]: abort did not explain the FAILED CREATE (names-create=%v names-failure=%v)\n%s", namesCreate, namesFailure, combined)
		}
	})
}

// -----------------------------------------------------------------------------
// AC-github-secret-blocked (CRITICAL) — ~/.zshrc secret => abort, no commit, no push.
// -----------------------------------------------------------------------------

func TestGitHubSecretBlocked_AC_github_secret_blocked(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	// A ~/.zshrc carrying a FAKE private key — the same shape internal/secret blocks.
	s.WriteHomeFile(t, ".zshrc", "# my shell\nexport EDITOR=vim\n"+fakeGitHubSecret, 0o644)

	gh := newGHMock(t)
	git := newGitStub(t)
	// The mock would happily let create/view/push succeed — proving ferry's OWN gate
	// (not a gh failure) is what stops the secret.
	sc := ghScenario{
		authOK: true, login: ghOwner, repoViewExists: false,
		jsonIsPrivate: true, jsonNameWithOwner: ghOwner + "/secretcfg",
		jsonURL: "https://github.com/" + ghOwner + "/secretcfg.git",
	}
	out, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "secretcfg", "--yes")
	combined := out + errOut

	// 1. ABORT with a secret message.
	if code == 0 {
		t.Errorf("AC-github-secret-blocked (CRITICAL): a ~/.zshrc with a secret exited 0 (must abort before any push)")
	}
	if !containsAnyFold(combined, "secret", "private key", "looks like", "won't push", "will not push") {
		t.Errorf("AC-github-secret-blocked (CRITICAL): abort did not cite the secret in ~/.zshrc\n%s", combined)
	}

	// 2. NO push recorded AND NO commit recorded — the secret must never enter a commit,
	// let alone a remote. The git recorder logs every argv, so assert neither `push`
	// nor `commit` was invoked with the secret-bearing tree staged. (A commit-then-reset
	// leak is caught by the object-store scan in step 3.)
	if git.invokedSubcommand("push") {
		t.Errorf("AC-github-secret-blocked (CRITICAL): a git push was invoked with a secret-bearing config (must NEVER push)")
	}
	if git.invokedSubcommand("commit") {
		t.Errorf("AC-github-secret-blocked (CRITICAL): a git commit was invoked with a secret-bearing tree (the secret must never enter a commit).\ngit: %v", git.lines())
	}

	// 3. The secret bytes appear NOWHERE in the repo: working tree, reachable history,
	// AND the object store / reflog (a commit-then-reset leaves the blob dangling but
	// recoverable). managedRepoPath resolves the repo ferry would have used.
	repo := managedRepoPath(s)
	scan := &Sandbox{t: s.t, Home: s.Home, Repo: repo, BinDir: s.BinDir}
	scan.AssertNoSecretInRepo(t, fakeGitHubSecret)                   // working tree + git log -p --all + rev-list
	scan.AssertNoSecretInRepo(t, "BEGIN OPENSSH PRIVATE KEY")        // and the header alone
	assertNoSecretInGitObjects(t, repo, "BEGIN OPENSSH PRIVATE KEY") // dangling/unreachable objects too

	// 4. No ferry-owned file under HOME carries the secret bytes.
	assertNoSecretUnderHome(t, s, "BEGIN OPENSSH PRIVATE KEY")
}

// assertNoSecretInGitObjects scans EVERY object in the repo's object store — including
// UNREACHABLE / dangling blobs a commit-then-reset would leave behind (which git log /
// rev-list --all would miss) — for the needle. This closes the "committed then reset,
// object still recoverable" leak path. A no-op when repo has no .git or git is absent.
func assertNoSecretInGitObjects(t *testing.T, repo, needle string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		return // no git repo: nothing to scan (working-tree scan already ran)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	// `git rev-list --all --objects --reflog` + fsck's dangling/unreachable set together
	// enumerate reachable AND recoverable objects. cat-file --batch dumps each blob's
	// content; grep the lot for the needle.
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat")
	collect := func(args ...string) []string {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		c.Env = env
		out, _ := c.CombinedOutput()
		return strings.Fields(string(out))
	}
	// SHA-1/256 object ids from every ref, the reflog, and the dangling set.
	ids := map[string]bool{}
	for _, tok := range collect("rev-list", "--all", "--objects", "--reflog") {
		if len(tok) >= 7 && isHexish(tok) {
			ids[tok] = true
		}
	}
	// `git fsck --unreachable --dangling --no-reflogs` lists loose recoverable objects.
	fsck := exec.Command("git", "-C", repo, "fsck", "--unreachable", "--dangling", "--no-reflogs")
	fsck.Env = env
	fout, _ := fsck.CombinedOutput()
	for _, line := range strings.Split(string(fout), "\n") {
		f := strings.Fields(line) // e.g. "unreachable blob <sha>"
		if len(f) >= 3 && isHexish(f[2]) {
			ids[f[2]] = true
		}
	}
	for id := range ids {
		cat := exec.Command("git", "-C", repo, "cat-file", "-p", id)
		cat.Env = env
		if body, err := cat.CombinedOutput(); err == nil && strings.Contains(string(body), needle) {
			t.Errorf("AC-github-secret-blocked (CRITICAL): secret found in a git OBJECT %s (a commit-then-reset leak — recoverable from the object store)", id)
			return
		}
	}
}

// isHexish reports whether s looks like a git object id (all hex, len>=7).
func isHexish(s string) bool {
	if len(s) < 7 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// assertNoSecretUnderHome walks the ferry-owned areas of HOME (config + state) and
// the recorded repo, asserting the needle is absent. ~/.zshrc itself legitimately
// holds the seeded secret (it is the user's own file, untouched), so it is skipped.
func assertNoSecretUnderHome(t *testing.T, s *Sandbox, needle string) {
	t.Helper()
	roots := []string{
		s.HomePath(".config", "ferry"),
		s.HomePath(".local", "state", "ferry"),
		managedRepoPath(s),
	}
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr == nil && strings.Contains(string(data), needle) {
				rel, _ := filepath.Rel(s.Home, path)
				t.Errorf("AC-github-secret-blocked: secret bytes found in ferry-owned file ~/%s", rel)
			}
			return nil
		})
	}
}

// -----------------------------------------------------------------------------
// AC-github-creates-private — clean case records repo create --private, https origin, real push.
// -----------------------------------------------------------------------------

func TestGitHubCreatesPrivate_AC_github_creates_private(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	seedGoodZshrc(t, s)
	gh := newGHMock(t)
	git := newPushRecordingGitStub(t)
	url := "https://github.com/" + ghOwner + "/myrepo.git"
	sc := ghScenario{
		authOK: true, login: ghOwner, repoViewExists: false,
		jsonIsPrivate: true, jsonNameWithOwner: ghOwner + "/myrepo", jsonURL: url,
	}
	out, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "myrepo", "--yes")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("AC-github-creates-private: clean managed init exited %d\n%s\ngh: %v\ngit: %v", code, combined, gh.lines(), git.lines())
	}

	// 1. Recorded `gh repo create <owner>/myrepo --private` (the exact intended repo,
	//    not just any create), and NEVER --public in ANY gh call.
	if !gh.invokedMatching("repo create", ghOwner+"/myrepo", "--private") {
		t.Errorf("AC-github-creates-private: no `gh repo create %s/myrepo … --private` recorded.\ngh: %v", ghOwner, gh.lines())
	}
	if gh.anyLineContains("--public") {
		t.Errorf("AC-github-creates-private: a gh call passed --public (must NEVER).\ngh: %v", gh.lines())
	}
	// The post-create verification (repo view --json) must have run against the same
	// repo — so the private-check is real, not skipped.
	if !gh.invokedMatching("repo view", ghOwner+"/myrepo", "--json") {
		t.Errorf("AC-github-creates-private: no post-create `repo view %s/myrepo --json` recorded (verification skipped?).\ngh: %v", ghOwner, gh.lines())
	}
	// No stray/unexpected gh subcommand was invoked (the mock fails loudly on one).
	if gh.anyLineContains("GHMOCK_UNEXPECTED_CALL") {
		t.Errorf("AC-github-creates-private: an unexpected gh call was made.\ngh: %v", gh.lines())
	}
	// Noninteractive-credential invariant: EVERY gh call ran with GIT_TERMINAL_PROMPT=0
	// (PLAN step 1) — a credential prompt can never hang or capture a typed secret.
	if ok, bad := gh.allCallsNoninteractive(); !ok {
		t.Errorf("AC-github-creates-private: a gh call did not run with GIT_TERMINAL_PROMPT=0 (noninteractive invariant): %q", bad)
	}

	// 2. Origin is https://…
	repo := managedRepoPath(s)
	if o := originURLIn(repo); !strings.HasPrefix(o, "https://") {
		t.Errorf("AC-github-creates-private: origin is %q; want an https:// URL", o)
	}

	// 3. A real git push was invoked (the recorder saw a push subcommand), AND it ran
	//    noninteractively (GIT_TERMINAL_PROMPT=0 — the push must never prompt for creds).
	if !git.invokedSubcommand("push") {
		t.Errorf("AC-github-creates-private: no real `git push` was invoked.\ngit: %v", git.lines())
	}
	if ok, bad := git.pushWasNoninteractive(); !ok {
		t.Errorf("AC-github-creates-private: `git push` did not run with GIT_TERMINAL_PROMPT=0 (must never prompt for credentials): %q", bad)
	}
}

// -----------------------------------------------------------------------------
// AC-github-verifies-private — not-private or wrong nameWithOwner => abort before push.
// -----------------------------------------------------------------------------

func TestGitHubVerifiesPrivate_AC_github_verifies_private(t *testing.T) {
	t.Parallel()

	// Every case: create SUCCEEDS, then the post-create `repo view --json` reports a
	// BAD verification (not private / wrong owner / missing url / missing owner /
	// malformed JSON). ferry must abort BEFORE push in all of them. Each case also
	// asserts ferry actually RAN the create + the verify view (so a generic early
	// abort mentioning "private" cannot masquerade as a real verification failure).
	base := func() ghScenario {
		return ghScenario{authOK: true, login: ghOwner, repoViewExists: false}
	}
	cases := []struct {
		name string
		mut  func(sc *ghScenario)
		want []string // the abort must cite one of these
	}{
		{"not-private", func(sc *ghScenario) {
			sc.jsonIsPrivate = false
			sc.jsonNameWithOwner = ghOwner + "/myrepo"
			sc.jsonURL = "https://github.com/" + ghOwner + "/myrepo.git"
		}, []string{"private", "not private", "verify"}},
		{"wrong-owner", func(sc *ghScenario) {
			sc.jsonIsPrivate = true
			sc.jsonNameWithOwner = "someoneelse/other"
			sc.jsonURL = "https://github.com/someoneelse/other.git"
		}, []string{"mismatch", "unexpected", "owner", "identity", "someoneelse"}},
		{"missing-url", func(sc *ghScenario) {
			sc.jsonIsPrivate = true
			sc.jsonNameWithOwner = ghOwner + "/myrepo"
			sc.jsonURL = "" // url field omitted -> abort (url must be present)
		}, []string{"url", "missing", "verify", "field"}},
		{"missing-owner", func(sc *ghScenario) {
			sc.jsonIsPrivate = true
			sc.jsonNameWithOwner = "" // nameWithOwner omitted -> abort
			sc.jsonURL = "https://github.com/" + ghOwner + "/myrepo.git"
		}, []string{"owner", "missing", "verify", "field", "identity"}},
		{"malformed-json", func(sc *ghScenario) {
			sc.jsonMalformed = true // invalid JSON -> parse error abort
		}, []string{"parse", "json", "verify", "invalid", "malformed"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			seedGoodZshrc(t, s)
			gh := newGHMock(t)
			git := newGitStub(t)
			sc := base()
			tc.mut(&sc)
			out, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "myrepo", "--yes")
			combined := out + errOut
			if code == 0 {
				t.Errorf("AC-github-verifies-private[%s]: init exited 0 on a bad verification (must abort before push)\n%s", tc.name, combined)
			}
			if !containsAnyFold(combined, tc.want...) {
				t.Errorf("AC-github-verifies-private[%s]: abort did not cite the verification failure %v\n%s", tc.name, tc.want, combined)
			}
			// The create + the verify view must actually have run (a real verification
			// failure, not a generic early abort).
			if !gh.invokedMatching("repo create", ghOwner+"/myrepo") {
				t.Errorf("AC-github-verifies-private[%s]: no repo create was recorded — abort was not a post-create verification.\ngh: %v", tc.name, gh.lines())
			}
			if !gh.invokedMatching("repo view", "--json") {
				t.Errorf("AC-github-verifies-private[%s]: no `repo view --json` verify call recorded.\ngh: %v", tc.name, gh.lines())
			}
			// The load-bearing safety assertion: NO push on a failed verification, and
			// managed must NOT be set (config is finalised only after a verified push).
			if git.invokedSubcommand("push") {
				t.Errorf("AC-github-verifies-private[%s]: pushed despite a failed verification", tc.name)
			}
			if configManaged(t, s) {
				t.Errorf("AC-github-verifies-private[%s]: managed=true set despite a failed verification", tc.name)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// AC-github-https-remote — origin is https; a non-https clone url is rejected.
// -----------------------------------------------------------------------------

func TestGitHubHTTPSRemote_AC_github_https_remote(t *testing.T) {
	t.Parallel()

	// The plan enumerates EVERY non-https scheme (ssh://, git@, git://, http://).
	// ferry NEVER trusts gh's reported clone url as origin: it CONSTRUCTS the
	// canonical https origin from the verified owner/name, so a hostile/non-https
	// view.URL is NEUTRALISED (ignored), not merely rejected. The observable
	// invariant is the same or stronger: the origin ferry sets is ALWAYS a plain
	// https://github.com/<owner>/<name> — never the bad scheme, never ssh, never a
	// credentialed url — and SSH material is never touched.
	badURLs := map[string]string{
		"ssh":  "ssh://git@github.com/" + ghOwner + "/myrepo.git",
		"scp":  "git@github.com:" + ghOwner + "/myrepo.git",
		"git":  "git://github.com/" + ghOwner + "/myrepo.git",
		"http": "http://github.com/" + ghOwner + "/myrepo.git",
	}
	for label, badURL := range badURLs {
		label, badURL := label, badURL
		t.Run("neutralises-"+label, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			s.SSHTripwire(t) // init must touch no SSH material
			seedGoodZshrc(t, s)
			gh := newGHMock(t)
			git := newPushRecordingGitStub(t)
			sc := ghScenario{
				authOK: true, login: ghOwner, repoViewExists: false,
				jsonIsPrivate: true, jsonNameWithOwner: ghOwner + "/myrepo",
				jsonURL: badURL, // hostile view.URL — must be IGNORED, never used as origin
			}
			_, _, _ = s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "myrepo", "--yes")
			// The load-bearing safety invariant: whatever origin ferry set, it is the
			// canonical https origin — NEVER the bad scheme from view.URL.
			o := originURLIn(managedRepoPath(s))
			if o != "" && !strings.HasPrefix(o, "https://github.com/") {
				t.Errorf("AC-github-https-remote[%s]: ferry set a non-canonical origin %q (view.URL must never be used)", label, o)
			}
			if strings.HasPrefix(o, "ssh://") || strings.HasPrefix(o, "git://") ||
				strings.HasPrefix(o, "http://") || strings.HasPrefix(o, "git@") {
				t.Errorf("AC-github-https-remote[%s]: ferry set the hostile view.URL scheme as origin: %q", label, o)
			}
			// origin must not embed a credential either (no userinfo@).
			if strings.Contains(o, "@") {
				t.Errorf("AC-github-https-remote[%s]: origin embeds userinfo/credential: %q", label, o)
			}
			s.AssertSSHUntouched(t)
		})
	}

	// CRITICAL regression: a hostile view.URL that embeds a token in userinfo
	// (https://ghp_TOKEN@github.com/...) or points at a DIFFERENT owner must NEVER be
	// used as origin — ferry constructs the canonical origin from the VERIFIED
	// owner/name, so the origin carries no token and always names the verified repo.
	hostile := map[string]string{
		"userinfo-token": "https://ghp_FERRYMOCKTOKENSENTINELdeadbeef@github.com/" + ghOwner + "/myrepo.git",
		"userinfo-basic": "https://user:secretpw@github.com/" + ghOwner + "/myrepo.git",
		"wrong-owner":    "https://github.com/someoneelse/myrepo.git",
		"has-query":      "https://github.com/" + ghOwner + "/myrepo.git?access_token=ghp_x",
	}
	for label, badURL := range hostile {
		label, badURL := label, badURL
		t.Run("canonical-origin-"+label, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			s.SSHTripwire(t)
			seedGoodZshrc(t, s)
			gh := newGHMock(t)
			git := newPushRecordingGitStub(t)
			sc := ghScenario{
				authOK: true, login: ghOwner, repoViewExists: false,
				jsonIsPrivate: true, jsonNameWithOwner: ghOwner + "/myrepo",
				jsonURL: badURL,
			}
			_, _, _ = s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "myrepo", "--yes")
			o := originURLIn(managedRepoPath(s))
			// If an origin was set at all, it is EXACTLY the canonical verified repo url —
			// no token, no userinfo, no query, no foreign owner.
			if o != "" {
				wantPrefix := "https://github.com/" + ghOwner + "/myrepo"
				if !strings.HasPrefix(o, wantPrefix) {
					t.Errorf("AC-github-https-remote[%s]: origin %q is not the canonical verified %s…", label, o, wantPrefix)
				}
				if strings.Contains(o, "@") || strings.Contains(o, "ghp_") ||
					strings.Contains(o, "someoneelse") || strings.Contains(o, "?") {
					t.Errorf("AC-github-https-remote[%s]: origin carries a token/userinfo/foreign-owner/query: %q", label, o)
				}
			}
			s.AssertSSHUntouched(t)
		})
	}

	// Happy path: a genuine https url IS accepted and set as origin.
	t.Run("accepts-https", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		s.SSHTripwire(t)
		seedGoodZshrc(t, s)
		gh := newGHMock(t)
		git := newPushRecordingGitStub(t)
		sc := ghScenario{
			authOK: true, login: ghOwner, repoViewExists: false,
			jsonIsPrivate: true, jsonNameWithOwner: ghOwner + "/myrepo",
			jsonURL: "https://github.com/" + ghOwner + "/myrepo.git",
		}
		if _, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "myrepo", "--yes"); code != 0 {
			t.Fatalf("AC-github-https-remote[accepts-https]: happy init exited %d\n%s", code, errOut)
		}
		if o := originURLIn(managedRepoPath(s)); !strings.HasPrefix(o, "https://") {
			t.Errorf("AC-github-https-remote[accepts-https]: origin is %q; want https://", o)
		}
		s.AssertSSHUntouched(t)
	})
}

// -----------------------------------------------------------------------------
// AC-github-no-token — no token in config/state/repo after the flow.
// -----------------------------------------------------------------------------

func TestGitHubNoToken_AC_github_no_token(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	seedGoodZshrc(t, s)
	gh := newGHMock(t)
	git := newPushRecordingGitStub(t)
	sc := ghScenario{
		authOK: true, login: ghOwner, repoViewExists: false,
		jsonIsPrivate: true, jsonNameWithOwner: ghOwner + "/myrepo",
		jsonURL: "https://github.com/" + ghOwner + "/myrepo.git",
	}
	// The scan is only meaningful if the flow actually RAN to completion (an early
	// abort that writes nothing would trivially "pass"). Require exit 0 first.
	if _, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "myrepo", "--yes"); code != 0 {
		t.Fatalf("AC-github-no-token: happy managed init exited %d (the no-token scan needs a completed flow)\n%s", code, errOut)
	}

	// The mock's `gh auth status` emitted a token-shaped SENTINEL (ghp_FERRYMOCKTOKEN…).
	// ferry sees it but must persist NOTHING: scan ferry-owned areas for that exact
	// sentinel AND for generic credential markers. Any hit = ferry stored a credential.
	const tokenSentinel = "ghp_FERRYMOCKTOKENSENTINEL"
	markers := []string{tokenSentinel, "ghp_", "gho_", "ghs_", "github_pat_", "x-access-token", "oauth_token", "Authorization:", "Bearer "}
	roots := []string{
		s.HomePath(".config", "ferry"),
		s.HomePath(".local", "state", "ferry"),
		managedRepoPath(s),
	}
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				if info != nil && info.IsDir() && info.Name() == ".git" {
					return nil // still scan git internals: a token in a remote URL would live there
				}
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			for _, m := range markers {
				if strings.Contains(string(data), m) {
					rel, _ := filepath.Rel(s.Home, path)
					t.Errorf("AC-github-no-token: credential marker %q found in ferry-owned file ~/%s", m, rel)
				}
			}
			return nil
		})
	}
	// The origin URL must be a plain https://… with NO embedded credential (user:pass@).
	if o := originURLIn(managedRepoPath(s)); strings.Contains(o, "@") && strings.HasPrefix(o, "https://") {
		t.Errorf("AC-github-no-token: origin URL embeds a credential: %q", o)
	}
	// ferry must not have passed a token on the gh/git argv either (the recorders log argv).
	for _, line := range append(gh.lines(), git.lines()...) {
		for _, m := range []string{"ghp_", "github_pat_", "--token", "-H Authorization"} {
			if strings.Contains(line, m) {
				t.Errorf("AC-github-no-token: a recorded gh/git call carried a token marker %q: %s", m, line)
			}
		}
	}
}

// -----------------------------------------------------------------------------
// AC-github-adopts-zshrc — a secret-free ~/.zshrc is adopted (no wipe).
// -----------------------------------------------------------------------------

func TestGitHubAdoptsZshrc_AC_github_adopts_zshrc(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	zshrc := seedGoodZshrc(t, s)
	snap := s.SnapshotFile(t, zshrc) // the live ~/.zshrc must not be wiped

	gh := newGHMock(t)
	git := newPushRecordingGitStub(t)
	sc := ghScenario{
		authOK: true, login: ghOwner, repoViewExists: false,
		jsonIsPrivate: true, jsonNameWithOwner: ghOwner + "/myrepo",
		jsonURL: "https://github.com/" + ghOwner + "/myrepo.git",
	}
	if _, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "myrepo", "--yes"); code != 0 {
		t.Fatalf("AC-github-adopts-zshrc: clean managed init exited %d\n%s", code, errOut)
	}

	// The live ~/.zshrc is byte-unchanged by init (adopted, not wiped).
	snap.AssertUnchanged(t)

	// The repo adopted the content: a .zshrc source somewhere in the repo carries the
	// live file's marker.
	repo := managedRepoPath(s)
	if !repoTreeContains(t, repo, "alias ll='ls -la'") {
		t.Errorf("AC-github-adopts-zshrc: the repo did not adopt the existing ~/.zshrc content")
	}

	// The AC's real promise: the first `apply` is a NO-OP (adoption means repo == live,
	// so apply neither wipes nor rewrites ~/.zshrc). Re-snapshot AFTER init (git set the
	// managed remote; the mtime is what matters) and assert apply leaves ~/.zshrc
	// byte-, mode- and mtime-identical. gh is not needed for apply, but keep it on PATH
	// so nothing unexpected shells out.
	post := s.SnapshotFile(t, zshrc)
	if _, _, code := s.FerryEnv([]string{ghOnlyPathEnv(gh)}, "apply"); code != 0 {
		// A non-zero apply is itself a failure of "first apply is a no-op".
		t.Errorf("AC-github-adopts-zshrc: `ferry apply` after adoption exited %d (adoption should make apply a clean no-op)", code)
	}
	post.AssertUnchanged(t) // no wipe, no rewrite: the adopted ~/.zshrc is untouched
}

// -----------------------------------------------------------------------------
// AC-github-existing-local-repo — a config.toml already present => refuse.
// -----------------------------------------------------------------------------

func TestGitHubExistingLocalRepo_AC_github_existing_local_repo(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	seedGoodZshrc(t, s)
	// Pre-seed a machine config recording an existing local repo dir.
	existing := s.HomePath(".config", "ferry", "repo")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing repo: %v", err)
	}
	if err := os.WriteFile(s.ConfigTOMLPath(), []byte("hostname = \"h\"\nrepo = \""+existing+"\"\n"), 0o644); err != nil {
		t.Fatalf("seed config.toml: %v", err)
	}

	gh := newGHMock(t)
	git := newGitStub(t)
	sc := ghScenario{authOK: true, login: ghOwner, repoViewExists: false}
	out, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "newrepo", "--yes")
	combined := out + errOut
	if code == 0 {
		t.Errorf("AC-github-existing-local-repo: init --github with an existing config repo exited 0 (must refuse)")
	}
	if !containsAnyFold(combined, "already exists", "config repo", "remove", "rename", "existing") {
		t.Errorf("AC-github-existing-local-repo: refusal did not cite the existing config repo\n%s", combined)
	}
	if gh.anyLineContains("repo create") {
		t.Errorf("AC-github-existing-local-repo: created a GitHub repo despite the existing local repo")
	}
	if git.invokedSubcommand("push") {
		t.Errorf("AC-github-existing-local-repo: pushed despite refusing")
	}
}

// -----------------------------------------------------------------------------
// AC-github-config-managed — success => managed=true; old config still loads.
// -----------------------------------------------------------------------------

func TestGitHubConfigManaged_AC_github_config_managed(t *testing.T) {
	t.Parallel()

	t.Run("success-sets-managed", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		seedGoodZshrc(t, s)
		gh := newGHMock(t)
		git := newPushRecordingGitStub(t)
		sc := ghScenario{
			authOK: true, login: ghOwner, repoViewExists: false,
			jsonIsPrivate: true, jsonNameWithOwner: ghOwner + "/myrepo",
			jsonURL: "https://github.com/" + ghOwner + "/myrepo.git",
		}
		if _, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "myrepo", "--yes"); code != 0 {
			t.Fatalf("AC-github-config-managed: managed init exited %d\n%s", code, errOut)
		}
		if !configManaged(t, s) {
			data, _ := os.ReadFile(s.ConfigTOMLPath())
			t.Errorf("AC-github-config-managed: config.toml does not record managed=true after a successful managed init\n%s", data)
		}
	})

	t.Run("route1-stays-unmanaged", func(t *testing.T) {
		t.Parallel()
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not on PATH")
		}
		s := NewSandbox(t)
		// A plain route-1 fresh init (no --github) must NOT mark managed.
		if _, errOut, code := s.Ferry("init"); code != 0 {
			t.Fatalf("route-1 init exited %d\n%s", code, errOut)
		}
		if configManaged(t, s) {
			t.Errorf("AC-github-config-managed: a route-1 `ferry init` repo was marked managed=true (must stay unmanaged)")
		}
	})

	t.Run("old-config-still-loads", func(t *testing.T) {
		t.Parallel()
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not on PATH")
		}
		s := NewSandbox(t)
		// An OLD config carrying only hostname+repo (no managed key) must still LOAD.
		repo := s.HomePath(".config", "ferry", "repo")
		s.InitGitRepoAt(t, repo)
		if err := os.WriteFile(s.ConfigTOMLPath(), []byte("hostname = \"oldhost\"\nrepo = \""+repo+"\"\n"), 0o644); err != nil {
			t.Fatalf("seed old config: %v", err)
		}
		// A read-path command must not fail to PARSE the config (managed treated as
		// false). Match only parse/decode-specific phrasing — NOT the bare word "toml",
		// which also appears in the legitimate "no ferry manifest (ferry.toml)" message
		// status emits for an empty repo (that is correct behaviour, not a load failure).
		_, errOut, code := s.Ferry("status")
		if code != 0 && containsAnyFold(errOut, "parse config", "decode config", "unknown field", "unmarshal", "invalid toml", "toml:") {
			t.Errorf("AC-github-config-managed: an old (no-managed) config.toml failed to load: exit %d\n%s", code, errOut)
		}
	})
}

// InitGitRepoAt initialises a git working tree at an explicit dir (a variant of
// InitGitRepo for a path other than s.Repo). Skips if git is unavailable.
func (s *Sandbox) InitGitRepoAt(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("InitGitRepoAt mkdir: %v", err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "eval@localhost"}, {"config", "user.name", "eval"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// -----------------------------------------------------------------------------
// AC-github-partial-failure — create ok but push fails => reports repo, not managed,
// a re-run does not create a second repo.
// -----------------------------------------------------------------------------

func TestGitHubPartialFailure_AC_github_partial_failure(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	seedGoodZshrc(t, s)
	gh := newGHMock(t)
	git := newPushFailingGitStub(t) // create/verify succeed; the FIRST push fails
	url := "https://github.com/" + ghOwner + "/myrepo.git"
	sc := ghScenario{
		authOK: true, login: ghOwner, repoViewExists: false,
		jsonIsPrivate: true, jsonNameWithOwner: ghOwner + "/myrepo", jsonURL: url,
	}
	out, errOut, code := s.FerryEnv(append(sc.env(), "PATH="+gh.dir+string(os.PathListSeparator)+git.dir+string(os.PathListSeparator)+os.Getenv("PATH")), "init", "--github", "myrepo", "--yes")
	combined := out + errOut

	// 1. Non-zero, and the report names the created repo + a recovery hint.
	if code == 0 {
		t.Errorf("AC-github-partial-failure: push failed but init exited 0 (must report the partial failure)")
	}
	if !containsAnyFold(combined, "myrepo", url, "github.com/"+ghOwner) {
		t.Errorf("AC-github-partial-failure: report did not name the created repo\n%s", combined)
	}
	if !containsAnyFold(combined, "recover", "delete", "manual", "re-run", "rerun", "already created", "created") {
		t.Errorf("AC-github-partial-failure: report did not give a recovery path\n%s", combined)
	}
	// "How far it got" (PLAN step 10): the report must name the failing STAGE — the repo
	// was created but the PUSH failed — not just a generic error, so the user knows the
	// repo exists but is empty.
	if !containsAnyFold(combined, "push") {
		t.Errorf("AC-github-partial-failure: report did not name the failing stage (push) — 'how far it got' is unclear\n%s", combined)
	}
	// The create really happened (this is why a re-run must not create a second).
	if !gh.anyLineContains("repo create") {
		t.Errorf("AC-github-partial-failure: setup invariant — expected a repo create to have been recorded before the push failure")
	}
	// The failure was AT push, not an early abort: ferry must have reached and INVOKED
	// `git push` (the push-failing stub recorded it) — proving "create ok, later step
	// (push) failed", the actual partial-failure shape.
	if !git.invokedSubcommand("push") {
		t.Errorf("AC-github-partial-failure: no `git push` was invoked — the failure was not at push (an early abort would not exercise partial-failure recovery).\ngit: %v", git.lines())
	}

	// 2. managed is NOT set (push never succeeded).
	if configManaged(t, s) {
		t.Errorf("AC-github-partial-failure: managed=true was set despite the failed push")
	}

	// 3. RE-RUN: now the repo EXISTS (create left it behind). ferry's check-and-avoid
	// must refuse — NO second repo create.
	gh2 := newGHMock(t)
	git2 := newGitStub(t)
	sc2 := ghScenario{authOK: true, login: ghOwner, repoViewExists: true}
	_, _, code2 := s.FerryEnv(append(sc2.env(), combinedPathEnv(gh2, git2)), "init", "--github", "myrepo", "--yes")
	if code2 == 0 {
		t.Errorf("AC-github-partial-failure: re-run after a partial failure exited 0 (must refuse the now-existing repo)")
	}
	if gh2.anyLineContains("repo create") {
		t.Errorf("AC-github-partial-failure: re-run created a SECOND repo (must not fan out).\ngh: %v", gh2.lines())
	}
	if git2.invokedSubcommand("push") {
		t.Errorf("AC-github-partial-failure: re-run pushed despite the repo already existing")
	}

	// 4. No token left behind (cross-reference AC-github-no-token).
	assertNoSecretUnderHome(t, s, "ghp_")
}
