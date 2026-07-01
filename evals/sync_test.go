package evals

// Behavioral evals for `ferry sync` (v0.2.3), driving the REAL binary via the
// harness against a LOCAL BARE GIT REPO as origin — no network, no real GitHub.
// Every test asserts an OBSERVABLE outcome (bare-origin tip oids, worktree/index/
// untracked/ignored content hashes, recorded git argv, exit codes, messages) and
// never inspects ferry's source.
//
// RED-until-impl: requireBin(t) (via Sandbox.Ferry* / FerryEnv*) SKIPS every test
// when FERRY_BIN is unset, so the package compiles and `go test ./evals/...` is
// green before cmd/sync.go lands. This file needs only the std lib + os/exec git.
//
// ── COORDINATION NOTE FOR THE IMPLEMENTATION ──────────────────────────────────
// See .work/ACCEPTANCE-v0.2.3.md §Implementation coordination. In brief, for these
// evals to pass a correct cmd/sync.go must:
//   * accept a file:// / local-path origin ONLY under FERRY_ALLOW_FILE_ORIGIN=1
//     (otherwise HTTPS-only); the evals set that env var so they can run offline.
//   * run every git subprocess with GIT_TERMINAL_PROMPT=0 + GIT_SSH_COMMAND=/bin/false.
//   * refuse an unmanaged repo unless --allow-unmanaged.
//   * push a single explicit ref `git push origin HEAD:<branch>`, NEVER --force /
//     --force-with-lease (the gitStub recorder scans argv for these).
//   * gate the WHOLE push range (origin/<branch>..HEAD; first push:
//     `git rev-list HEAD --not --remotes=origin`) through internal/secret.
// If the impl chooses a different flag name or origin-allowance mechanism, update
// this file's constants + the acceptance doc in lockstep.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// allowFileOrigin is the TEST-ONLY env entry that lets ferry accept a file://
// origin offline. Production never sets it, so §AC-sync-https-only stays HTTPS-only.
const allowFileOrigin = "FERRY_ALLOW_FILE_ORIGIN=1"

// syncBranch is the branch git init creates by default in this environment; the
// seeding + assertions reference origin/<syncBranch>.
const syncBranch = "main"

// fakeSyncSecret is a NON-FUNCTIONAL private-key block — the exact shape
// internal/secret flags High (BEGIN OPENSSH PRIVATE KEY). NOT a real key.
const fakeSyncSecret = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUFAKEFAKEFAKEsyncFAKEdeadbeefcafe0102feed\n" +
	"-----END OPENSSH PRIVATE KEY-----\n"

// -----------------------------------------------------------------------------
// Bare-origin test infrastructure.
// -----------------------------------------------------------------------------

// syncRepo bundles the pieces of a seeded sync scenario: a managed clone (the
// ferry repo, a real working tree) whose `origin` is a local bare repo, plus the
// recorder that observes ferry's git argv.
type syncRepo struct {
	clone string  // the ferry-managed working clone (config.toml points here)
	bare  string  // the bare origin (a push tripwire + content sink)
	git   gitStub // records every git argv ferry runs (first on PATH)
}

// newSyncGitStub writes a `git` recorder (first on PATH) that logs argv WITH the
// inherited GIT_TERMINAL_PROMPT and GIT_SSH_COMMAND, then forwards to the real git.
// The extra env logging lets a test PROVE ferry ran git with the no-ssh / no-prompt
// posture the plan pins (GIT_TERMINAL_PROMPT=0, GIT_SSH_COMMAND=/bin/false) — argv
// alone (newGitStub) cannot show subprocess env. Skips if git is unavailable.
func newSyncGitStub(t *testing.T) gitStub {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH: sync git recorder unavailable")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "git_invocations.log")
	script := "#!/bin/sh\n" +
		"printf '%s [GTP=%s] [SSH=%s]\\n' \"$*\" \"${GIT_TERMINAL_PROMPT:-<unset>}\" \"${GIT_SSH_COMMAND:-<unset>}\" >> " + shellQuote(logPath) + "\n" +
		"exec " + shellQuote(realGit) + " \"$@\"\n"
	stub := filepath.Join(dir, "git")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("newSyncGitStub: write stub: %v", err)
	}
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatalf("newSyncGitStub: chmod stub: %v", err)
	}
	return gitStub{dir: dir, log: logPath}
}

// assertNoSSHPosture proves every recorded git call ran with the no-prompt /
// no-ssh subprocess posture: GIT_TERMINAL_PROMPT=0 AND GIT_SSH_COMMAND set to a
// no-op (/bin/false) — so even a stray ssh path can't read ~/.ssh (PLAN step 1).
// Only meaningful with newSyncGitStub, which logs the inherited env per call.
func assertNoSSHPosture(t *testing.T, sr *syncRepo) {
	t.Helper()
	for _, line := range sr.git.lines() {
		if !strings.Contains(line, "[GTP=0]") {
			t.Errorf("AC-sync-https-only: a git call did not run with GIT_TERMINAL_PROMPT=0 (no-prompt posture): %q", line)
		}
		if !strings.Contains(line, "[SSH=/bin/false]") {
			t.Errorf("AC-sync-https-only: a git call did not run with GIT_SSH_COMMAND=/bin/false (no-ssh posture): %q", line)
		}
	}
}

// assertNoSecretInBareObjects scans EVERY object in a BARE repo's object store
// (reachable + unreachable/dangling) for the needle. A bare repo has no `.git`
// subdir (it IS the git dir), so harness.AssertNoSecretInRepo's history scan
// returns early — this proves "no OBJECTS for the secret", not just "ref did not
// move". A no-op when git is absent.
func assertNoSecretInBareObjects(t *testing.T, bare, needle string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat")
	ids := map[string]bool{}
	collect := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", bare}, args...)...)
		c.Env = env
		out, _ := c.CombinedOutput()
		for _, tok := range strings.Fields(string(out)) {
			if isHexish(tok) {
				ids[tok] = true
			}
		}
	}
	collect("rev-list", "--all", "--objects", "--reflog")
	fsck := exec.Command("git", "-C", bare, "fsck", "--unreachable", "--dangling")
	fsck.Env = env
	fout, _ := fsck.CombinedOutput()
	for _, line := range strings.Split(string(fout), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && isHexish(f[2]) {
			ids[f[2]] = true
		}
	}
	for id := range ids {
		cat := exec.Command("git", "-C", bare, "cat-file", "-p", id)
		cat.Env = env
		if body, err := cat.CombinedOutput(); err == nil && strings.Contains(string(body), needle) {
			t.Errorf("secret leak: needle found in a BARE-origin git object %s (a secret reached the remote)", id)
			return
		}
	}
}

// runGit runs a real git command in dir with a deterministic identity; fatals on
// error. Used only for seeding/observation, never through the recorder.
func syncGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// syncGitOK runs a real git command and returns (output, ok) without fataling —
// for observations that may legitimately fail (e.g. rev-parse on an empty origin).
func syncGitOK(t *testing.T, dir string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

// newSyncRepo builds a bare origin + a managed working clone wired to it, seeds an
// initial commit, and points ferry's config.toml at the clone with managed=true.
// The clone's `origin` is the bare repo (a plain path; ferry treats it as file://
// under the test allowance). Returns the scenario handle. Skips if git is absent.
//
// managed controls whether config.toml records managed=true (for §AC-sync-managed-only).
func newSyncRepo(t *testing.T, s *Sandbox, managed bool) *syncRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: sync evals need git")
	}
	bare := t.TempDir()
	clone := s.Repo // the sandbox repo IS the managed clone

	syncGit(t, bare, "init", "-q", "--bare", "-b", syncBranch, ".")
	syncGit(t, clone, "init", "-q", "-b", syncBranch, ".")
	syncGit(t, clone, "config", "user.email", "eval@localhost")
	syncGit(t, clone, "config", "user.name", "eval")

	// A baseline commit so both sides share history (ferry.toml makes it a real
	// managed repo the read path recognises).
	s.WriteRepoFile(t, "ferry.toml", baseManifest)
	s.WriteRepoFile(t, "shared.txt", "line-1\nline-2\nline-3\n")
	syncGit(t, clone, "add", "-A")
	syncGit(t, clone, "commit", "-q", "-m", "baseline")
	syncGit(t, clone, "remote", "add", "origin", bare)
	syncGit(t, clone, "push", "-q", "origin", syncBranch)
	// Record the upstream so `origin/<branch>` exists (normal, not first-push, case).
	syncGit(t, clone, "fetch", "-q", "origin")

	writeSyncConfig(t, s, clone, managed)
	return &syncRepo{clone: clone, bare: bare, git: newSyncGitStub(t)}
}

// newFirstPushSyncRepo builds a managed clone whose bare origin has NO branch yet
// (the FIRST-PUSH case for §AC-sync-secret-blocked-firstpush): origin is added but
// nothing has been pushed, so origin/<branch> does not exist.
func newFirstPushSyncRepo(t *testing.T, s *Sandbox, managed bool) *syncRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: sync evals need git")
	}
	bare := t.TempDir()
	clone := s.Repo

	syncGit(t, bare, "init", "-q", "--bare", "-b", syncBranch, ".")
	syncGit(t, clone, "init", "-q", "-b", syncBranch, ".")
	syncGit(t, clone, "config", "user.email", "eval@localhost")
	syncGit(t, clone, "config", "user.name", "eval")
	s.WriteRepoFile(t, "ferry.toml", baseManifest)
	s.WriteRepoFile(t, "shared.txt", "line-1\n")
	syncGit(t, clone, "add", "-A")
	syncGit(t, clone, "commit", "-q", "-m", "baseline")
	// origin added, NOTHING pushed: origin/<branch> is absent => first-push.
	syncGit(t, clone, "remote", "add", "origin", bare)

	writeSyncConfig(t, s, clone, managed)
	return &syncRepo{clone: clone, bare: bare, git: newSyncGitStub(t)}
}

// addSecondRemoteWithHead adds a SECOND remote (`upstream`) to the clone, pushes
// the clone's current HEAD to it, and fetches it — so HEAD is now reachable from
// `upstream/<branch>` but NOT from `origin/*`. This distinguishes the correct
// first-push range `git rev-list HEAD --not --remotes=origin` (which still gates
// the commit, because it is not on the TARGET origin) from the WRONG
// `--remotes` (which would exclude it as "on some remote"). Returns nothing.
func addSecondRemoteWithHead(t *testing.T, sr *syncRepo) {
	t.Helper()
	upstreamBare := t.TempDir()
	syncGit(t, upstreamBare, "init", "-q", "--bare", "-b", syncBranch, ".")
	syncGit(t, sr.clone, "remote", "add", "upstream", upstreamBare)
	syncGit(t, sr.clone, "push", "-q", "upstream", syncBranch)
	syncGit(t, sr.clone, "fetch", "-q", "upstream")
}

// writeSyncConfig points ferry's config.toml at the clone, recording managed as
// asked. Uses the persisted TOML contract (hostname/repo/managed) machine.go pins.
func writeSyncConfig(t *testing.T, s *Sandbox, clone string, managed bool) {
	t.Helper()
	cfg := "hostname = \"evalhost\"\nrepo = \"" + clone + "\"\n"
	if managed {
		cfg += "managed = true\n"
	}
	if err := os.MkdirAll(filepath.Dir(s.ConfigTOMLPath()), 0o755); err != nil {
		t.Fatalf("writeSyncConfig mkdir: %v", err)
	}
	if err := os.WriteFile(s.ConfigTOMLPath(), []byte(cfg), 0o644); err != nil {
		t.Fatalf("writeSyncConfig: %v", err)
	}
}

// seedRemoteAhead adds a commit to the bare origin (via a throwaway clone) that the
// managed clone does not yet have — a "remote ahead" scenario. content lands in
// path P. Returns the origin tip oid after the push.
func seedRemoteAhead(t *testing.T, sr *syncRepo, path, content, msg string) string {
	t.Helper()
	tmp := t.TempDir()
	syncGit(t, tmp, "clone", "-q", sr.bare, ".")
	syncGit(t, tmp, "config", "user.email", "eval@localhost")
	syncGit(t, tmp, "config", "user.name", "eval")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(tmp, path)), 0o755); err != nil {
		t.Fatalf("seedRemoteAhead mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, path), []byte(content), 0o644); err != nil {
		t.Fatalf("seedRemoteAhead write: %v", err)
	}
	syncGit(t, tmp, "add", "-A")
	syncGit(t, tmp, "commit", "-q", "-m", msg)
	syncGit(t, tmp, "push", "-q", "origin", syncBranch)
	return originTip(t, sr)
}

// originTip returns the bare origin's branch tip oid, or "" when the branch does
// not exist yet (first-push origin). A push tripwire: compare before/after.
func originTip(t *testing.T, sr *syncRepo) string {
	t.Helper()
	oid, ok := syncGitOK(t, sr.bare, "rev-parse", "refs/heads/"+syncBranch)
	if !ok {
		return ""
	}
	return oid
}

// commitCount returns `git rev-list --count HEAD` for the clone (0 if none).
func commitCount(t *testing.T, repo string) string {
	t.Helper()
	out, ok := syncGitOK(t, repo, "rev-list", "--count", "HEAD")
	if !ok {
		return "0"
	}
	return out
}

// rebaseInProgress reports whether a rebase is half-done (a leftover the conflict
// rollback must never leave behind).
func rebaseInProgress(repo string) bool {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(repo, ".git", d)); err == nil {
			return true
		}
	}
	return false
}

// noForcePush asserts NO recorded git invocation carried --force / --force-with-lease.
// This is the load-bearing §AC-sync-no-force-push check, reused by every scenario.
func noForcePush(t *testing.T, sr *syncRepo) {
	t.Helper()
	for _, line := range sr.git.lines() {
		if firstGitSubcommand(line) != "push" {
			continue
		}
		if strings.Contains(line, "--force-with-lease") || strings.Contains(line, "--force") ||
			strings.Contains(line, " -f ") || strings.HasSuffix(line, " -f") {
			t.Errorf("AC-sync-no-force-push: a force push was recorded: %q", line)
		}
	}
}

// syncEnv is the standard env for a sync run: the git recorder first on PATH plus
// the test-only file-origin allowance.
func (sr *syncRepo) syncEnv() []string {
	return []string{sr.git.pathEnv(), allowFileOrigin}
}

// hashTree returns a map of relPath->sha256 for every file under repo, EXCLUDING
// the .git dir — capturing tracked + untracked + ignored file CONTENTS. Used to
// prove EXACT restoration after a conflict (§AC-sync-conflict-restores).
func hashTree(t *testing.T, repo string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(repo, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(repo, path)
		out[rel] = hashFile(t, path)
		return nil
	})
	return out
}

// assertTreeEqual asserts two hashTree snapshots are byte-identical (same set of
// paths, same content hashes) — the CRITICAL exact-restoration assertion.
func assertTreeEqual(t *testing.T, label string, before, after map[string]string) {
	t.Helper()
	for rel, h := range before {
		got, ok := after[rel]
		if !ok {
			t.Errorf("%s: file %s was DELETED by sync (must be restored exactly)", label, rel)
			continue
		}
		if got != h {
			t.Errorf("%s: file %s CONTENT changed by sync (must be restored byte-for-byte)", label, rel)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Errorf("%s: sync CREATED a leftover file %s (must restore to exact pre-sync state)", label, rel)
		}
	}
}

// indexDigest captures the staged (`diff --cached`) and unstaged (`diff`) diffs so
// the exact staged/unstaged split can be compared before/after a conflict.
func indexDigest(t *testing.T, repo string) (staged, unstaged string) {
	t.Helper()
	staged, _ = syncGitOK(t, repo, "diff", "--cached")
	unstaged, _ = syncGitOK(t, repo, "diff")
	return staged, unstaged
}

// -----------------------------------------------------------------------------
// AC-sync-exists — the command is registered.
// -----------------------------------------------------------------------------

func TestSyncExists_AC_sync_exists(t *testing.T) {
	t.Parallel()

	t.Run("help-resolves", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		out, errOut, code := s.Ferry("sync", "--help")
		if code != 0 {
			t.Errorf("AC-sync-exists: `ferry sync --help` exited %d (must resolve)", code)
		}
		if _, ok := containsAllFold(out+errOut, "sync"); !ok {
			t.Errorf("AC-sync-exists: `ferry sync --help` does not describe sync\n%s", out+errOut)
		}
		if !containsAnyFold(out+errOut, "--allow-unmanaged") {
			t.Errorf("AC-sync-exists: help does not advertise --allow-unmanaged\n%s", out+errOut)
		}
		if !containsAnyFold(out+errOut, "--message", "-m") {
			t.Errorf("AC-sync-exists: help does not advertise --message/-m\n%s", out+errOut)
		}
	})

	t.Run("listed-in-root-help", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		out, errOut, _ := s.Ferry("--help")
		if !containsAnyFold(out+errOut, "sync") {
			t.Errorf("AC-sync-exists: `sync` not listed in `ferry --help`\n%s", out+errOut)
		}
	})
}

// -----------------------------------------------------------------------------
// AC-sync-needs-remote — no origin => clear failure, nothing pushed.
// -----------------------------------------------------------------------------

func TestSyncNeedsRemote_AC_sync_needs_remote(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	s := NewSandbox(t)
	clone := s.Repo
	syncGit(t, clone, "init", "-q", "-b", syncBranch, ".")
	syncGit(t, clone, "config", "user.email", "eval@localhost")
	syncGit(t, clone, "config", "user.name", "eval")
	s.WriteRepoFile(t, "ferry.toml", baseManifest)
	syncGit(t, clone, "add", "-A")
	syncGit(t, clone, "commit", "-q", "-m", "baseline")
	writeSyncConfig(t, s, clone, true) // managed, but NO origin remote added

	git := newGitStub(t)
	out, errOut, code := s.FerryEnv([]string{git.pathEnv(), allowFileOrigin}, "sync")
	combined := out + errOut
	if code == 0 {
		t.Errorf("AC-sync-needs-remote: sync with no origin exited 0 (must fail)")
	}
	if !containsAnyFold(combined, "origin", "remote") {
		t.Errorf("AC-sync-needs-remote: failure did not name the missing origin/remote\n%s", combined)
	}
	if git.invokedSubcommand("push") {
		t.Errorf("AC-sync-needs-remote: a push was invoked despite no origin")
	}
}

// -----------------------------------------------------------------------------
// AC-sync-managed-only — unmanaged refuses by default; --allow-unmanaged proceeds.
// -----------------------------------------------------------------------------

func TestSyncManagedOnly_AC_sync_managed_only(t *testing.T) {
	t.Parallel()

	t.Run("unmanaged-refuses", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		sr := newSyncRepo(t, s, false) // NOT managed
		out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
		combined := out + errOut
		if code == 0 {
			t.Errorf("AC-sync-managed-only: sync on an unmanaged repo exited 0 (must refuse)")
		}
		if !containsAnyFold(combined, "manage", "allow-unmanaged", "unmanaged") {
			t.Errorf("AC-sync-managed-only: refusal did not point at --allow-unmanaged\n%s", combined)
		}
		if sr.git.invokedSubcommand("push") {
			t.Errorf("AC-sync-managed-only: pushed on an unmanaged repo without the override")
		}
		noForcePush(t, sr)
	})

	t.Run("allow-unmanaged-proceeds", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		sr := newSyncRepo(t, s, false)
		// A remote-ahead commit so a proceeding sync has real work (fetch is entered).
		seedRemoteAhead(t, sr, "shared.txt", "line-1\nline-2\nline-3\nremote\n", "remote ahead")
		_, _, code := s.FerryEnv(sr.syncEnv(), "sync", "--allow-unmanaged")
		// With the override, ferry must advance PAST the managed gate: it fetched.
		if !sr.git.invokedSubcommand("fetch") {
			t.Errorf("AC-sync-managed-only: --allow-unmanaged did not advance into the sync flow (no fetch recorded).\ngit: %v", sr.git.lines())
		}
		_ = code
		noForcePush(t, sr)
	})
}

// -----------------------------------------------------------------------------
// AC-sync-pull-then-push — remote-ahead + local-capture => both sides carry both.
// -----------------------------------------------------------------------------

func TestSyncPullThenPush_AC_sync_pull_then_push(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)

	// Remote ahead: a commit on the origin the clone lacks.
	seedRemoteAhead(t, sr, "remote-only.txt", "from-remote\n", "remote commit R")
	// Local capture: a committed local change the origin lacks.
	s.WriteRepoFile(t, "local-only.txt", "from-local\n")
	syncGit(t, sr.clone, "add", "-A")
	syncGit(t, sr.clone, "commit", "-q", "-m", "local commit L")

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("AC-sync-pull-then-push: clean divergent-but-non-conflicting sync exited %d\n%s\ngit: %v", code, combined, sr.git.lines())
	}

	// BOTH commits present on BOTH sides. Prove by file presence in each tree.
	localLog, _ := syncGitOK(t, sr.clone, "log", "--oneline")
	if !strings.Contains(localLog, "remote commit R") || !strings.Contains(localLog, "local commit L") {
		t.Errorf("AC-sync-pull-then-push: clone HEAD missing R and/or L\n%s", localLog)
	}
	// Verify the origin now contains both via a throwaway clone of the bare origin.
	verify := t.TempDir()
	syncGit(t, verify, "clone", "-q", sr.bare, ".")
	if _, err := os.Stat(filepath.Join(verify, "remote-only.txt")); err != nil {
		t.Errorf("AC-sync-pull-then-push: origin missing R's file after sync")
	}
	if _, err := os.Stat(filepath.Join(verify, "local-only.txt")); err != nil {
		t.Errorf("AC-sync-pull-then-push: origin missing L's file after sync (local work not pushed)")
	}
	// Report accuracy: mentions pulling and pushing.
	if !containsAnyFold(combined, "pull", "pulled") || !containsAnyFold(combined, "push", "pushed") {
		t.Errorf("AC-sync-pull-then-push: report did not describe pull + push\n%s", combined)
	}
	noForcePush(t, sr)
}

// -----------------------------------------------------------------------------
// AC-sync-no-local-changes — clean tree + remote ahead => ff pull, nothing pushed.
// -----------------------------------------------------------------------------

func TestSyncNoLocalChanges_AC_sync_no_local_changes(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)
	tipBefore := seedRemoteAhead(t, sr, "remote-only.txt", "from-remote\n", "remote commit R")

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("AC-sync-no-local-changes: clean fast-forward sync exited %d\n%s", code, combined)
	}
	// The clone fast-forwarded to R.
	if _, err := os.Stat(filepath.Join(sr.clone, "remote-only.txt")); err != nil {
		t.Errorf("AC-sync-no-local-changes: clone did not fast-forward to the remote commit")
	}
	// Nothing pushed: the origin tip is byte-for-byte the same as before.
	if tipAfter := originTip(t, sr); tipAfter != tipBefore {
		t.Errorf("AC-sync-no-local-changes: origin tip moved (a push happened) — was %s now %s", tipBefore, tipAfter)
	}
	if !containsAnyFold(combined, "pull", "pulled", "up to date", "up-to-date") {
		t.Errorf("AC-sync-no-local-changes: report did not describe the pull / up-to-date state\n%s", combined)
	}
	noForcePush(t, sr)
}

// -----------------------------------------------------------------------------
// AC-sync-secret-blocked-worktree + AC-sync-worktree-secret-no-commit.
// -----------------------------------------------------------------------------

func TestSyncSecretBlockedWorktree_AC_sync_secret_blocked_worktree(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)
	tipBefore := originTip(t, sr)
	headBefore := syncGit(t, sr.clone, "rev-parse", "HEAD")
	countBefore := commitCount(t, sr.clone)

	// A worktree change carrying a FAKE secret (BEGIN OPENSSH PRIVATE KEY => High).
	s.WriteRepoFile(t, "secret.conf", "token = value\n"+fakeSyncSecret)

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut

	// 1. Abort citing the secret.
	if code == 0 {
		t.Errorf("AC-sync-secret-blocked-worktree: a worktree secret exited 0 (must abort)")
	}
	if !containsAnyFold(combined, "secret", "private key", "won't", "will not", "blocked") {
		t.Errorf("AC-sync-secret-blocked-worktree: abort did not cite the secret\n%s", combined)
	}
	// 2. No new commit (AC-sync-worktree-secret-no-commit): HEAD + commit count unchanged.
	if headAfter := syncGit(t, sr.clone, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Errorf("AC-sync-worktree-secret-no-commit: HEAD moved (a commit was created) — was %s now %s", headBefore, headAfter)
	}
	if countAfter := commitCount(t, sr.clone); countAfter != countBefore {
		t.Errorf("AC-sync-worktree-secret-no-commit: commit count changed %s->%s (a commit was created then rolled back)", countBefore, countAfter)
	}
	// 3. Nothing pushed: origin tip unchanged AND the secret reached no origin OBJECT
	//    (bare repo => scan its object store directly, not the early-returning history scan).
	if tipAfter := originTip(t, sr); tipAfter != tipBefore {
		t.Errorf("AC-sync-secret-blocked-worktree: origin tip moved (a push happened) — was %s now %s", tipBefore, tipAfter)
	}
	assertNoSecretInBareObjects(t, sr.bare, "BEGIN OPENSSH PRIVATE KEY")
	// 4. The secret must never have entered the CLONE's git HISTORY or OBJECT store
	//    (a commit-then-reset leak). NOTE: the user's OWN uncommitted secret.conf
	//    legitimately stays in the working tree — a correct rollback preserves it — so
	//    we do NOT assert the working tree is clean of it; we assert nothing was
	//    committed/objectified. assertNoSecretInGitObjects scans reachable + dangling
	//    objects across the clone's .git.
	assertNoSecretInGitObjects(t, sr.clone, "BEGIN OPENSSH PRIVATE KEY")
	noForcePush(t, sr)
}

// -----------------------------------------------------------------------------
// AC-sync-secret-blocked-precommit (CRITICAL-1) — secret in a PRE-EXISTING
// unpushed commit blocks the push; nothing leaves.
// -----------------------------------------------------------------------------

func TestSyncSecretBlockedPrecommit_AC_sync_secret_blocked_precommit(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)
	tipBefore := originTip(t, sr)

	// A PRE-EXISTING local commit (unpushed) carrying a FAKE secret, then a CLEAN
	// worktree — so the worktree gate has nothing to catch and only the push-range
	// gate over origin/<branch>..HEAD can stop it.
	s.WriteRepoFile(t, "committed-secret.conf", "cfg\n"+fakeSyncSecret)
	syncGit(t, sr.clone, "add", "-A")
	syncGit(t, sr.clone, "commit", "-q", "-m", "pre-existing commit with a secret")
	if !gitWorktreeClean(t, sr.clone) {
		t.Fatalf("setup invariant: worktree must be clean before the precommit sync")
	}

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut
	if code == 0 {
		t.Errorf("AC-sync-secret-blocked-precommit (CRITICAL-1): a secret in a pre-existing unpushed commit exited 0 (push must be blocked)")
	}
	if !containsAnyFold(combined, "secret", "private key", "won't", "will not", "blocked") {
		t.Errorf("AC-sync-secret-blocked-precommit: abort did not cite the secret in the push range\n%s", combined)
	}
	// The load-bearing check: the origin never received the secret commit.
	if tipAfter := originTip(t, sr); tipAfter != tipBefore {
		t.Errorf("AC-sync-secret-blocked-precommit: origin tip moved (the secret commit was pushed) — was %s now %s", tipBefore, tipAfter)
	}
	assertNoSecretInBareObjects(t, sr.bare, "BEGIN OPENSSH PRIVATE KEY")
	noForcePush(t, sr)
}

// -----------------------------------------------------------------------------
// AC-sync-secret-blocked-firstpush (CRITICAL) — first push (no origin/<branch>)
// gates the whole visible history; nothing leaves; rev-list doesn't error.
// -----------------------------------------------------------------------------

func TestSyncSecretBlockedFirstPush_AC_sync_secret_blocked_firstpush(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newFirstPushSyncRepo(t, s, true) // origin has NO branch yet

	// Confirm the first-push precondition: origin/<branch> is absent.
	if tip := originTip(t, sr); tip != "" {
		t.Fatalf("setup invariant: expected an empty first-push origin, got tip %s", tip)
	}
	// A FAKE secret ANYWHERE in local history (here: an added commit).
	s.WriteRepoFile(t, "history-secret.conf", "cfg\n"+fakeSyncSecret)
	syncGit(t, sr.clone, "add", "-A")
	syncGit(t, sr.clone, "commit", "-q", "-m", "commit with a secret in first-push history")

	// A SECOND remote (`upstream`) now carries HEAD (incl. the secret commit), while
	// `origin` still has nothing. This proves the range must be
	// `rev-list HEAD --not --remotes=origin` (still gates the commit) and NOT
	// `--remotes` (which would WRONGLY exclude it as "already on some remote") — an
	// impl using `--remotes` would let the secret through and fail this test.
	addSecondRemoteWithHead(t, sr)

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut
	if code == 0 {
		t.Errorf("AC-sync-secret-blocked-firstpush (CRITICAL): a secret in first-push history exited 0 (must be blocked)")
	}
	// rev-list must NOT have errored on the missing upstream (that would surface as a
	// git/plumbing error, not a secret block). Require a secret-shaped abort message.
	if !containsAnyFold(combined, "secret", "private key", "won't", "will not", "blocked") {
		t.Errorf("AC-sync-secret-blocked-firstpush: abort did not cite the secret (a rev-list error on the missing upstream would look different)\n%s", combined)
	}
	// Nothing leaves: the bare origin still has NO branch and no objects for the secret.
	if tip := originTip(t, sr); tip != "" {
		t.Errorf("AC-sync-secret-blocked-firstpush: origin gained a branch tip %s (the first push happened despite the secret)", tip)
	}
	// A bare repo has no `.git`, so scan its object store directly (proving no
	// OBJECTS for the secret, not merely that the ref did not move).
	assertNoSecretInBareObjects(t, sr.bare, "BEGIN OPENSSH PRIVATE KEY")
	noForcePush(t, sr)
}

// -----------------------------------------------------------------------------
// AC-sync-conflict-restores (CRITICAL-2) — a conflict restores EXACT pre-sync
// bytes (tracked + untracked + ignored + index split); no auto-commit; no force.
// -----------------------------------------------------------------------------

func TestSyncConflictRestores_AC_sync_conflict_restores(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)

	// Remote edits shared.txt line 2; the origin advances.
	tipBefore := seedRemoteAhead(t, sr, "shared.txt", "line-1\nREMOTE-EDIT\nline-3\n", "remote edits line 2")

	// Locally: first COMMIT a conflicting edit to the SAME line (guarantees a rebase
	// conflict) AND a .gitignore — these are the committed baseline. Nothing else is
	// staged in these commits, so the index snapshot below is a REAL, non-empty
	// staged/unstaged split (not accidentally swept into a commit).
	s.WriteRepoFile(t, "shared.txt", "line-1\nLOCAL-EDIT\nline-3\n")
	s.WriteRepoFile(t, ".gitignore", "ignored-secrets/\n")
	syncGit(t, sr.clone, "add", "shared.txt", ".gitignore")
	syncGit(t, sr.clone, "commit", "-q", "-m", "local edits line 2 (conflicting) + gitignore")

	// NOW dirty the tree WITHOUT committing: a STAGED addition, plus an UNSTAGED
	// modification to a DIFFERENT tracked file so the staged and unstaged diffs are
	// each non-empty and independent (proving stash apply --index restores the split).
	s.WriteRepoFile(t, "staged.txt", "staged-content\n")
	syncGit(t, sr.clone, "add", "staged.txt") // staged, NOT committed
	s.WriteRepoFile(t, "ferry.toml", baseManifest+"\n# local unstaged tweak\n")
	// An untracked file and a gitignored file with distinct bytes (contents must survive).
	s.WriteRepoFile(t, "untracked.txt", "untracked-bytes\n")
	s.WriteRepoFile(t, filepath.Join("ignored-secrets", "keep.txt"), "ignored-bytes\n")

	// Snapshot the EXACT pre-sync state.
	headBefore := syncGit(t, sr.clone, "rev-parse", "HEAD")
	countBefore := commitCount(t, sr.clone)
	treeBefore := hashTree(t, sr.clone)
	stagedBefore, unstagedBefore := indexDigest(t, sr.clone)
	// Setup invariant: the split must be genuinely non-empty, or the restoration
	// assertion below is vacuous.
	if strings.TrimSpace(stagedBefore) == "" || strings.TrimSpace(unstagedBefore) == "" {
		t.Fatalf("AC-sync-conflict-restores: setup invariant — staged/unstaged split must be non-empty (staged=%q unstaged=%q)", stagedBefore, unstagedBefore)
	}

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut

	// 1. Non-zero with a "machine unchanged; resolve with git" message.
	if code == 0 {
		t.Errorf("AC-sync-conflict-restores (CRITICAL-2): a conflicting sync exited 0 (must abort and restore)")
	}
	if !containsAnyFold(combined, "conflict", "unchanged", "resolve", "re-run", "rerun") {
		t.Errorf("AC-sync-conflict-restores: abort did not tell the user the machine is unchanged / to resolve with git\n%s", combined)
	}
	// 2. HEAD + commit count identical (no auto-commit, no half-rebase).
	if headAfter := syncGit(t, sr.clone, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Errorf("AC-sync-conflict-restores: HEAD changed — was %s now %s (leftover auto-commit / half-rebase)", headBefore, headAfter)
	}
	if countAfter := commitCount(t, sr.clone); countAfter != countBefore {
		t.Errorf("AC-sync-conflict-restores: commit count changed %s->%s", countBefore, countAfter)
	}
	if rebaseInProgress(sr.clone) {
		t.Errorf("AC-sync-conflict-restores: a rebase is still IN PROGRESS (must be aborted and rolled back)")
	}
	// 3. EXACT restoration of tracked + untracked + ignored file CONTENTS.
	assertTreeEqual(t, "AC-sync-conflict-restores", treeBefore, hashTree(t, sr.clone))
	// 4. The staged/unstaged index split is preserved byte-for-byte.
	stagedAfter, unstagedAfter := indexDigest(t, sr.clone)
	if stagedAfter != stagedBefore {
		t.Errorf("AC-sync-conflict-restores: the STAGED diff changed (index staged split not restored)")
	}
	if unstagedAfter != unstagedBefore {
		t.Errorf("AC-sync-conflict-restores: the UNSTAGED diff changed (index unstaged split not restored)")
	}
	// 5. Nothing pushed, and NO force push anywhere.
	if tipAfter := originTip(t, sr); tipAfter != tipBefore {
		t.Errorf("AC-sync-conflict-restores: origin tip moved on a conflict — was %s now %s", tipBefore, tipAfter)
	}
	noForcePush(t, sr)
}

// -----------------------------------------------------------------------------
// AC-sync-https-only — ssh:// origin refused (SSHTripwire intact); file:// only
// under the test allowance.
// -----------------------------------------------------------------------------

func TestSyncHTTPSOnly_AC_sync_https_only(t *testing.T) {
	t.Parallel()

	// The plan enumerates EVERY non-https scheme: ssh://, git@ (scp-like), git://,
	// http://. Each must be refused with ~/.ssh untouched.
	badOrigins := map[string]string{
		"ssh":  "ssh://git@github.com/octocat/cfg.git",
		"scp":  "git@github.com:octocat/cfg.git",
		"git":  "git://github.com/octocat/cfg.git",
		"http": "http://github.com/octocat/cfg.git",
	}
	for label, badURL := range badOrigins {
		label, badURL := label, badURL
		t.Run("refuses-"+label+"-origin", func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			s.SSHTripwire(t)
			sr := newSyncRepo(t, s, true)
			syncGit(t, sr.clone, "remote", "set-url", "origin", badURL)
			out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
			combined := out + errOut
			if code == 0 {
				t.Errorf("AC-sync-https-only[%s]: a %s origin exited 0 (must refuse)", label, label)
			}
			if !containsAnyFold(combined, "https", "scheme", "origin", "ssh") {
				t.Errorf("AC-sync-https-only[%s]: refusal did not name the scheme problem\n%s", label, combined)
			}
			if sr.git.invokedSubcommand("push") {
				t.Errorf("AC-sync-https-only[%s]: pushed to a non-https origin", label)
			}
			s.AssertSSHUntouched(t)
			noForcePush(t, sr)
		})
	}

	t.Run("file-origin-refused-without-allowance", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		sr := newSyncRepo(t, s, true)
		// NO allowFileOrigin in the env: a file://-style local origin must be refused,
		// so production stays HTTPS-only.
		out, errOut, code := s.FerryEnv([]string{sr.git.pathEnv()}, "sync")
		combined := out + errOut
		if code == 0 {
			t.Errorf("AC-sync-https-only: a file:// origin was accepted WITHOUT the test allowance (production must be HTTPS-only)")
		}
		if sr.git.invokedSubcommand("push") {
			t.Errorf("AC-sync-https-only: pushed to a file:// origin without the allowance")
		}
		_ = combined
	})

	t.Run("file-origin-allowed-under-allowance", func(t *testing.T) {
		t.Parallel()
		s := NewSandbox(t)
		sr := newSyncRepo(t, s, true)
		seedRemoteAhead(t, sr, "remote-only.txt", "r\n", "remote commit")
		// WITH the allowance, the same file:// origin is accepted and sync advances.
		_, _, _ = s.FerryEnv(sr.syncEnv(), "sync")
		if !sr.git.invokedSubcommand("fetch") {
			t.Errorf("AC-sync-https-only: file:// origin under the allowance did not proceed (no fetch).\ngit: %v", sr.git.lines())
		}
		// No-ssh / no-prompt subprocess posture: EVERY git call ferry ran must carry
		// GIT_TERMINAL_PROMPT=0 AND GIT_SSH_COMMAND=/bin/false (PLAN step 1 — so even a
		// stray ssh path cannot read ~/.ssh). The sync recorder logs the inherited env.
		assertNoSSHPosture(t, sr)
		noForcePush(t, sr)
	})
}

// -----------------------------------------------------------------------------
// AC-sync-untracked-guard — a remote change that would clobber a local untracked/
// ignored file aborts cleanly; the local file is preserved.
// -----------------------------------------------------------------------------

func TestSyncUntrackedGuard_AC_sync_untracked_guard(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)

	// Remote ADDS a file at path P.
	tipBefore := seedRemoteAhead(t, sr, "new-from-remote.txt", "REMOTE-BYTES\n", "remote adds P")
	// Locally, P already exists as an UNTRACKED file with DIFFERENT bytes — a checkout
	// would clobber it. Snapshot its exact bytes.
	localP := s.WriteRepoFile(t, "new-from-remote.txt", "LOCAL-UNTRACKED-BYTES\n")
	before := s.SnapshotFile(t, localP)

	out, errOut, code := s.FerryEnv(sr.syncEnv(), "sync")
	combined := out + errOut
	if code == 0 {
		t.Errorf("AC-sync-untracked-guard: sync that would clobber a local untracked file exited 0 (must abort)")
	}
	if !containsAnyFold(combined, "untracked", "overwrite", "clobber", "conflict", "local") {
		t.Errorf("AC-sync-untracked-guard: abort did not explain the untracked-overwrite guard\n%s", combined)
	}
	// The local untracked file is byte-for-byte preserved.
	before.AssertUnchanged(t)
	// Nothing pushed.
	if tipAfter := originTip(t, sr); tipAfter != tipBefore {
		t.Errorf("AC-sync-untracked-guard: origin tip moved despite the abort — was %s now %s", tipBefore, tipAfter)
	}
	noForcePush(t, sr)
}

// -----------------------------------------------------------------------------
// AC-sync-no-apply — sync deploys nothing to live/system locations.
// -----------------------------------------------------------------------------

func TestSyncNoApply_AC_sync_no_apply(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SSHTripwire(t)
	sr := newSyncRepo(t, s, true)

	// A managed dotfile whose LIVE target in HOME differs from the repo copy: if sync
	// ran apply, this file would be rewritten. It must NOT be.
	s.WriteRepoFile(t, ".zshrc", "export EDITOR=nvim\n") // repo copy
	syncGit(t, sr.clone, "add", "-A")
	syncGit(t, sr.clone, "commit", "-q", "-m", "add dotfile source")
	liveTarget := s.WriteHomeFile(t, ".zshrc", "export EDITOR=DIFFERENT\n", 0o644)
	before := s.SnapshotFile(t, liveTarget)

	// A remote-ahead commit so sync has legitimate pull work.
	seedRemoteAhead(t, sr, "remote-only.txt", "r\n", "remote commit")

	s.FerryEnv(sr.syncEnv(), "sync")

	// The live dotfile is byte-, mode-, mtime-UNCHANGED: sync never deployed.
	before.AssertUnchanged(t)
	s.AssertSSHUntouched(t)
	noForcePush(t, sr)
}

// -----------------------------------------------------------------------------
// AC-sync-no-force-push — across scenarios, sync NEVER force-pushes; the push is a
// single explicit ref. (Each scenario above also calls noForcePush; this test adds
// a focused happy-path check that the push argv is the explicit-ref form.)
// -----------------------------------------------------------------------------

func TestSyncNoForcePush_AC_sync_no_force_push(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	sr := newSyncRepo(t, s, true)
	// A local commit to push (no remote-ahead => a clean fast-forward push).
	s.WriteRepoFile(t, "local-only.txt", "from-local\n")
	syncGit(t, sr.clone, "add", "-A")
	syncGit(t, sr.clone, "commit", "-q", "-m", "local commit to push")

	if _, errOut, code := s.FerryEnv(sr.syncEnv(), "sync"); code != 0 {
		t.Fatalf("AC-sync-no-force-push: clean push sync exited %d\n%s", code, errOut)
	}
	// A push happened, and it was NEVER a force push.
	if !sr.git.invokedSubcommand("push") {
		t.Errorf("AC-sync-no-force-push: no push recorded on a clean local commit")
	}
	noForcePush(t, sr)
	// The plan's promise is a SINGLE explicit ref: `git push origin HEAD:<branch>`.
	// Enforce BOTH halves: (1) EXACTLY ONE push was recorded (not a fan-out), and
	// (2) EVERY push line is the explicit `origin HEAD:<branch>` form — NOT a bare
	// `git push` relying on configured refspecs (which could push unintended branches).
	var pushLines []string
	for _, line := range sr.git.lines() {
		if firstGitSubcommand(line) == "push" {
			pushLines = append(pushLines, line)
		}
	}
	if len(pushLines) != 1 {
		t.Errorf("AC-sync-no-force-push: expected EXACTLY one push (single explicit ref); got %d.\npushes: %v", len(pushLines), pushLines)
	}
	for _, line := range pushLines {
		pos := pushPositionals(line)
		// EXACTLY two positionals: the remote `origin` and a SINGLE `HEAD:<branch>`
		// refspec. A second refspec (e.g. `other:branch2`) or a bare `git push` (no
		// positionals) fails — closing the "extra refspec slips through substring match".
		okForm := len(pos) == 2 && pos[0] == "origin" &&
			(pos[1] == "HEAD:"+syncBranch || pos[1] == "HEAD:refs/heads/"+syncBranch)
		if !okForm {
			t.Errorf("AC-sync-no-force-push: push was not EXACTLY `origin HEAD:%s` (positionals=%v): %q", syncBranch, pos, line)
		}
		// A ref-fanout flag would push MORE than the single explicit ref even with clean
		// positionals — forbid --tags/--all/--mirror/--follow-tags/--prune/--force*.
		for _, bad := range []string{"--tags", "--all", "--mirror", "--follow-tags", "--prune", "--force"} {
			if strings.Contains(line, bad) {
				t.Errorf("AC-sync-no-force-push: push carried a ref-fanout/force flag %q (pushes more than the single explicit ref): %q", bad, line)
			}
		}
	}
}

// pushPositionals returns the non-flag arguments AFTER the `push` subcommand in a
// recorded git argv line, stripping the recorder's trailing `[GTP=…] [SSH=…]`
// annotations and any `-C <dir>` / flags. For a well-formed sync push this is
// exactly `["origin", "HEAD:<branch>"]`.
func pushPositionals(line string) []string {
	// Drop the recorder's bracketed env annotations.
	if i := strings.Index(line, " ["); i >= 0 {
		line = line[:i]
	}
	toks := strings.Fields(line)
	var pos []string
	seenPush := false
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		if !seenPush {
			if tok == "-C" || tok == "-c" {
				i++
				continue
			}
			if tok == "push" {
				seenPush = true
			}
			continue
		}
		if strings.HasPrefix(tok, "-") {
			continue // a push flag (e.g. -q) — not a positional refspec
		}
		pos = append(pos, tok)
	}
	return pos
}
