// Package evals is a behavioral eval harness that drives the REAL ferry binary
// inside a sandboxed, throwaway $HOME. Every eval asserts an observable outcome
// (files present/absent, mode bits, exit codes, stdout/stderr content, and
// absence-of-write tripwires) and never inspects ferry's source.
//
// The binary under test is located via the FERRY_BIN environment variable. When
// FERRY_BIN is unset (the default before the implementing wave lands), requireBin
// skips the test with a clear message, so the package always compiles and
// `go test ./evals/...` runs clean (all skipped, no failures).
package evals

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ferryTimeout bounds every binary invocation so an unexpected interactive
// prompt cannot hang the suite. Stdin is always wired to an empty reader, so a
// well-behaved binary reading a prompt sees EOF immediately rather than blocking.
const ferryTimeout = 30 * time.Second

// requireBin skips the calling test when FERRY_BIN is unset or does not point at
// an executable file. This is the single gate that keeps the package green before
// the binary exists: no FERRY_BIN => every real assertion is skipped, not failed.
func requireBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("FERRY_BIN")
	if bin == "" {
		t.Skip("FERRY_BIN unset: ferry binary not built yet (evals are RED until the implementing wave lands). " +
			"Build ferry, then run: FERRY_BIN=/path/to/ferry go test ./evals/...")
	}
	info, err := os.Stat(bin)
	if err != nil {
		t.Skipf("FERRY_BIN=%q is not accessible: %v", bin, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		t.Skipf("FERRY_BIN=%q is not an executable file", bin)
	}
	return bin
}

// Sandbox is an isolated throwaway environment for one test. Home is a t.TempDir()
// acting as $HOME; Repo is a fake ferry config repo (a git working tree) inside a
// separate temp dir. Nothing here mutates the real $HOME.
type Sandbox struct {
	t    *testing.T
	Home string // throwaway $HOME
	Repo string // fake ferry repo clone (its own dir, not under Home by default)
	// BinDir is the install dir analogue (~/.local/bin within the sandbox HOME).
	BinDir string

	// sshSnap holds the ~/.ssh tripwire snapshot for THIS sandbox. Stored per
	// sandbox (not in a shared map) so parallel tests never race on it.
	sshSnap *sshSnapshot
}

// NewSandbox builds a fresh sandbox: a throwaway HOME with the standard XDG
// subdirectories and a ~/.local/bin install dir, plus an empty fake repo dir.
// It never touches the real $HOME. Tests pass HOME=<sandbox.Home> to the binary
// via Sandbox.Ferry.
func NewSandbox(t *testing.T) *Sandbox {
	t.Helper()
	home := t.TempDir()
	repo := t.TempDir()

	// Minimal HOME structure ferry is documented to use (XDG-style locations).
	for _, d := range []string{
		filepath.Join(home, ".config", "ferry"),
		filepath.Join(home, ".local", "state", "ferry"),
		filepath.Join(home, ".local", "bin"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("NewSandbox: mkdir %s: %v", d, err)
		}
	}

	return &Sandbox{
		t:      t,
		Home:   home,
		Repo:   repo,
		BinDir: filepath.Join(home, ".local", "bin"),
	}
}

// env builds the controlled environment for a binary invocation: a clean-ish
// PATH plus the sandbox HOME and XDG dirs pointed inside it. Extra "KEY=VALUE"
// entries (and PATH overrides) can be appended by callers.
func (s *Sandbox) env(extra ...string) []string {
	env := []string{
		"HOME=" + s.Home,
		"XDG_CONFIG_HOME=" + filepath.Join(s.Home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(s.Home, ".local", "state"),
		"XDG_DATA_HOME=" + filepath.Join(s.Home, ".local", "share"),
		// Keep a real PATH so the binary can find git etc., but tests may override.
		"PATH=" + os.Getenv("PATH"),
		// Force non-interactive / no pager behaviour where tools honour it.
		"GIT_TERMINAL_PROMPT=0",
		"NO_COLOR=1",
		"TERM=dumb",
	}
	return append(env, extra...)
}

// Ferry runs the ferry binary with the sandbox HOME and a controlled environment,
// feeding empty stdin, and returns captured stdout, stderr and the exit code. A
// context timeout guarantees the call cannot hang on an interactive prompt.
//
// The test is skipped (not failed) when FERRY_BIN is unset/invalid.
func (s *Sandbox) Ferry(args ...string) (stdout, stderr string, exitCode int) {
	return s.FerryWithInput("", args...)
}

// FerryWithInput is like Ferry but pipes the given string to the binary's stdin.
// Use this to script interactive flows (e.g. capture hunk approval) once the
// non-interactive contract is doc-defined.
func (s *Sandbox) FerryWithInput(stdin string, args ...string) (stdoutStr, stderrStr string, exitCode int) {
	s.t.Helper()
	bin := requireBin(s.t)

	ctx, cancel := context.WithTimeout(context.Background(), ferryTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = s.env()
	cmd.Dir = s.Home
	cmd.Stdin = strings.NewReader(stdin)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		s.t.Fatalf("ferry %v timed out after %s (interactive hang?)\nstdout:\n%s\nstderr:\n%s",
			args, ferryTimeout, outBuf.String(), errBuf.String())
	}

	exitCode = 0
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			exitCode = ee.ExitCode()
		} else {
			// Couldn't even start the process (e.g. binary vanished mid-run).
			s.t.Fatalf("ferry %v failed to run: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// FerryEnv runs ferry with additional environment entries appended (and any PATH=
// override they contain takes effect because exec uses the last matching key).
func (s *Sandbox) FerryEnv(extraEnv []string, args ...string) (stdoutStr, stderrStr string, exitCode int) {
	s.t.Helper()
	return s.FerryEnvWithInput("", extraEnv, args...)
}

// FerryEnvWithInput runs ferry with extra env entries AND scripted stdin — for
// interactive flows that also need a PATH/env override (e.g. capture under a
// git-stub recorder).
func (s *Sandbox) FerryEnvWithInput(stdin string, extraEnv []string, args ...string) (stdoutStr, stderrStr string, exitCode int) {
	s.t.Helper()
	bin := requireBin(s.t)

	ctx, cancel := context.WithTimeout(context.Background(), ferryTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = s.env(extraEnv...)
	cmd.Dir = s.Home
	cmd.Stdin = strings.NewReader(stdin)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		s.t.Fatalf("ferry %v timed out after %s", args, ferryTimeout)
	}
	exitCode = 0
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			exitCode = ee.ExitCode()
		} else {
			s.t.Fatalf("ferry %v failed to run: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// asExitError is a tiny wrapper around errors.As kept local to avoid importing
// errors in callers; returns true and fills *target when err is an *exec.ExitError.
func asExitError(err error, target **exec.ExitError) bool {
	for e := err; e != nil; {
		if ee, ok := e.(*exec.ExitError); ok {
			*target = ee
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := e.(unwrapper)
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return false
}

// -----------------------------------------------------------------------------
// SSH tripwire
// -----------------------------------------------------------------------------

// sshSnapshot records the content hash, mode, and mtime of every file under ~/.ssh,
// PLUS the mode+mtime of the ~/.ssh directory itself and every subdirectory, so
// AssertSSHUntouched can prove nothing was read-modified, created, or deleted —
// including a same-bytes/same-mode file rewrite (mtime catches it) AND a directory
// mode "fix" like 0777->0700 (which AC-ssh-untouched / AC-doctor-ssh-readonly forbid).
type sshSnapshot struct {
	hashes    map[string]string      // file rel path -> sha256
	modes     map[string]os.FileMode // file rel path -> mode
	mtimes    map[string]time.Time   // file rel path -> modtime
	dirModes  map[string]os.FileMode // dir rel path (root keyed ".") -> mode
	dirMtimes map[string]time.Time   // dir rel path (root keyed ".") -> modtime
}

// SSHTripwire seeds <home>/.ssh/ with realistic sentinel files (private key,
// public key, config, known_hosts, authorized_keys) and records a snapshot of
// their contents and modes. Pair with AssertSSHUntouched after running commands.
//
// Covers: AC-ssh-untouched, AC-doctor-ssh-readonly, AC-ssh-not-captured.
func (s *Sandbox) SSHTripwire(t *testing.T) {
	t.Helper()
	sshDir := filepath.Join(s.Home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("SSHTripwire: mkdir .ssh: %v", err)
	}

	// A fake private key block with the canonical header the secret scanner and
	// any naive sync would key on. This is NOT a real key.
	privKey := "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZWQy\n" +
		"NTUxOQAAACDFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE\n" +
		"-----END OPENSSH PRIVATE KEY-----\n"

	files := map[string]struct {
		content string
		mode    os.FileMode
	}{
		"id_ed25519":      {privKey, 0o600},
		"id_ed25519.pub":  {"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5FAKE you@sentinel\n", 0o644},
		"config":          {"Host sentinel\n  HostName 10.0.0.1\n  User root\n", 0o600},
		"known_hosts":     {"sentinel.example.com ssh-ed25519 AAAAC3NzaFAKE\n", 0o644},
		"authorized_keys": {"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5FAKE other@host\n", 0o600},
	}

	snap := sshSnapshot{
		hashes: map[string]string{}, modes: map[string]os.FileMode{}, mtimes: map[string]time.Time{},
		dirModes: map[string]os.FileMode{}, dirMtimes: map[string]time.Time{},
	}
	for name, f := range files {
		p := filepath.Join(sshDir, name)
		if err := os.WriteFile(p, []byte(f.content), f.mode); err != nil {
			t.Fatalf("SSHTripwire: write %s: %v", name, err)
		}
		// WriteFile is subject to umask; force the intended mode.
		if err := os.Chmod(p, f.mode); err != nil {
			t.Fatalf("SSHTripwire: chmod %s: %v", name, err)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("SSHTripwire: stat %s: %v", name, err)
		}
		snap.hashes[name] = hashFile(t, p)
		snap.modes[name] = f.mode
		snap.mtimes[name] = info.ModTime()
	}
	if err := os.Chmod(sshDir, 0o700); err != nil {
		t.Fatalf("SSHTripwire: chmod .ssh: %v", err)
	}
	// Snapshot the ~/.ssh directory's own mode + mtime (keyed ".") — a directory
	// mode "fix" must be caught by the tripwire too.
	if info, err := os.Stat(sshDir); err == nil {
		snap.dirModes["."] = info.Mode()
		snap.dirMtimes["."] = info.ModTime()
	} else {
		t.Fatalf("SSHTripwire: stat .ssh dir: %v", err)
	}
	s.sshSnap = &snap
}

// ReSnapshotSSH re-records the CURRENT content+mode of every file under ~/.ssh/
// as the expected state. Use it after deliberately mutating perms (e.g. setting
// wrong modes for AC-doctor-ssh-readonly) so AssertSSHUntouched asserts that the
// (now wrong) state is preserved by ferry, not the original canonical state.
func (s *Sandbox) ReSnapshotSSH(t *testing.T) {
	t.Helper()
	sshDir := filepath.Join(s.Home, ".ssh")
	snap := sshSnapshot{
		hashes: map[string]string{}, modes: map[string]os.FileMode{}, mtimes: map[string]time.Time{},
		dirModes: map[string]os.FileMode{}, dirMtimes: map[string]time.Time{},
	}
	err := filepath.Walk(sshDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(sshDir, path)
		if info.IsDir() {
			// Record the ~/.ssh dir (rel == ".") and every subdir.
			snap.dirModes[rel] = info.Mode()
			snap.dirMtimes[rel] = info.ModTime()
			return nil
		}
		snap.hashes[rel] = hashFile(t, path)
		snap.modes[rel] = info.Mode()
		snap.mtimes[rel] = info.ModTime()
		return nil
	})
	if err != nil {
		t.Fatalf("ReSnapshotSSH: walk: %v", err)
	}
	s.sshSnap = &snap
}

// AssertSSHUntouched proves that nothing under <home>/.ssh/ was created, deleted,
// or modified (content or mode) since SSHTripwire ran.
//
// Covers: AC-ssh-untouched (and the read-only halves of AC-doctor-ssh-readonly,
// AC-ssh-not-captured).
func (s *Sandbox) AssertSSHUntouched(t *testing.T) {
	t.Helper()
	if s.sshSnap == nil {
		t.Fatalf("AssertSSHUntouched called without a prior SSHTripwire")
	}
	snap := *s.sshSnap
	sshDir := filepath.Join(s.Home, ".ssh")

	seen := map[string]bool{}
	seenDir := map[string]bool{}
	err := filepath.Walk(sshDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(sshDir, path)
		if info.IsDir() {
			// The ~/.ssh dir itself (rel ".") and any subdir: its OWN mode+mtime
			// must be unchanged — a directory mode "fix" (e.g. 0777->0700) is a
			// change ferry must never make.
			seenDir[rel] = true
			wantMode, known := snap.dirModes[rel]
			if !known {
				t.Errorf("ssh tripwire: unexpected new directory under ~/.ssh/: %s", rel)
				return nil
			}
			if got := info.Mode().Perm(); got != wantMode.Perm() {
				label := "~/.ssh/" + rel
				if rel == "." {
					label = "~/.ssh (directory itself)"
				}
				t.Errorf("ssh tripwire: ferry CHANGED mode of %s: was %o now %o",
					label, wantMode.Perm(), got)
			}
			if got := info.ModTime(); !got.Equal(snap.dirMtimes[rel]) {
				label := "~/.ssh/" + rel
				if rel == "." {
					label = "~/.ssh (directory itself)"
				}
				t.Errorf("ssh tripwire: ferry changed mtime of %s (was %s now %s)",
					label, snap.dirMtimes[rel], got)
			}
			return nil
		}
		seen[rel] = true
		wantHash, known := snap.hashes[rel]
		if !known {
			t.Errorf("ssh tripwire: ferry CREATED a new file under ~/.ssh/: %s", rel)
			return nil
		}
		if got := hashFile(t, path); got != wantHash {
			t.Errorf("ssh tripwire: ferry MODIFIED content of ~/.ssh/%s", rel)
		}
		if got := info.Mode().Perm(); got != snap.modes[rel].Perm() {
			t.Errorf("ssh tripwire: ferry CHANGED mode of ~/.ssh/%s: was %o now %o",
				rel, snap.modes[rel].Perm(), got)
		}
		// mtime catches a same-bytes/same-mode rewrite (a touch ferry must not do).
		if got := info.ModTime(); !got.Equal(snap.mtimes[rel]) {
			t.Errorf("ssh tripwire: ferry REWROTE ~/.ssh/%s (mtime changed: was %s now %s)",
				rel, snap.mtimes[rel], got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("AssertSSHUntouched: walk: %v", err)
	}
	for rel := range snap.hashes {
		if !seen[rel] {
			t.Errorf("ssh tripwire: ferry DELETED ~/.ssh/%s", rel)
		}
	}
	for rel := range snap.dirModes {
		if !seenDir[rel] {
			t.Errorf("ssh tripwire: ferry DELETED directory ~/.ssh/%s", rel)
		}
	}
}

// AssertNoSecretInRepo proves the seeded secret bytes never landed anywhere in the
// repo — neither in the working tree NOR in git history. A secret that was
// captured -> committed -> later removed from the working tree would still be in
// history (and recoverable), so history scanning is essential, not optional.
//
// Covers: AC-ssh-not-captured, AC-secret-blocked.
func (s *Sandbox) AssertNoSecretInRepo(t *testing.T, secret string) {
	t.Helper()
	needle := []byte(secret)

	// (1) Working tree scan (skip the .git object store; history is scanned below).
	err := filepath.Walk(s.Repo, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil // unreadable -> can't contain plaintext secret in a way we'd assert
		}
		if bytes.Contains(data, needle) {
			rel, _ := filepath.Rel(s.Repo, path)
			t.Errorf("secret leak: seeded secret bytes found in repo working-tree file %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("AssertNoSecretInRepo: walk: %v", err)
	}

	// (2) Git HISTORY scan — only if the repo is a git repo (these tests init one).
	if _, statErr := os.Stat(filepath.Join(s.Repo, ".git")); statErr != nil {
		return // not a git repo: working-tree scan is the only possible check
	}
	if _, lookErr := exec.LookPath("git"); lookErr != nil {
		return
	}
	// `git log -p --all` emits every blob diff across every ref + dangling commits
	// reachable from reflogs; grepping its full text catches a secret that was
	// committed and later removed from the working tree.
	logCmd := exec.Command("git", "-C", s.Repo, "log", "-p", "--all", "--full-history")
	logCmd.Env = append(os.Environ(), "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0")
	if out, e := logCmd.CombinedOutput(); e == nil {
		if bytes.Contains(out, needle) {
			t.Errorf("secret leak: seeded secret bytes found in git HISTORY (git log -p --all) — committed then possibly removed")
		}
	}
	// Belt-and-braces: grep every committed blob across all reachable commits.
	gitEnv := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	revList := exec.Command("git", "-C", s.Repo, "rev-list", "--all")
	revList.Env = gitEnv
	if revs, e := revList.CombinedOutput(); e == nil {
		args := []string{"-C", s.Repo, "grep", "-F", "-I", "-e", secret}
		for _, rev := range strings.Fields(string(revs)) {
			args = append(args, rev)
		}
		if len(args) > 6 { // have at least one rev to search
			g := exec.Command("git", args...)
			g.Env = gitEnv
			out, _ := g.CombinedOutput()
			// `git grep <revs>` exits 0 with matching lines, 1 with none. A match means leak.
			if bytes.Contains(out, needle) {
				t.Errorf("secret leak: seeded secret found in a committed blob (git grep across all revs)")
			}
		}
	}
}

// -----------------------------------------------------------------------------
// Filesystem helpers / tripwires
// -----------------------------------------------------------------------------

// hashFile returns the hex sha256 of a file's contents.
func hashFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("hashFile %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// fileTripwire snapshots a single path's existence, content hash, mode, mtime and
// size. mtime+size catch a same-bytes/same-mode rewrite (e.g. a non-idempotent
// re-apply that re-copies identical content) that a content+mode check would miss.
type fileTripwire struct {
	path    string
	exists  bool
	hash    string
	mode    os.FileMode
	symlink bool
	mtime   time.Time
	size    int64
}

// SnapshotFile records the current state of path (which may be absent) for later
// assertion that ferry did or did not change it.
func (s *Sandbox) SnapshotFile(t *testing.T, path string) fileTripwire {
	t.Helper()
	tw := fileTripwire{path: path}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return tw // exists=false
		}
		t.Fatalf("SnapshotFile %s: %v", path, err)
	}
	tw.exists = true
	tw.mode = info.Mode()
	tw.symlink = info.Mode()&os.ModeSymlink != 0
	tw.mtime = info.ModTime()
	tw.size = info.Size()
	if !tw.symlink {
		tw.hash = hashFile(t, path)
	}
	return tw
}

// AssertUnchanged proves the path is byte-, mode-, mtime- and size-identical to
// the snapshot (including "still absent" if it was absent). The mtime check is
// what makes a non-idempotent same-bytes rewrite fail. This is the core write-tripwire.
func (tw fileTripwire) AssertUnchanged(t *testing.T) {
	t.Helper()
	info, err := os.Lstat(tw.path)
	if !tw.exists {
		if err == nil {
			t.Errorf("tripwire: %s was CREATED but should have been left absent", tw.path)
		}
		return
	}
	if err != nil {
		t.Errorf("tripwire: %s was DELETED/became unreadable: %v", tw.path, err)
		return
	}
	if info.Mode()&os.ModeSymlink != 0 != tw.symlink {
		t.Errorf("tripwire: %s symlink-ness changed", tw.path)
	}
	if !tw.symlink {
		if got := hashFile(t, tw.path); got != tw.hash {
			t.Errorf("tripwire: %s CONTENT changed", tw.path)
		}
	}
	if got := info.Mode().Perm(); got != tw.mode.Perm() {
		t.Errorf("tripwire: %s MODE changed: was %o now %o", tw.path, tw.mode.Perm(), got)
	}
	if got := info.Size(); got != tw.size {
		t.Errorf("tripwire: %s SIZE changed: was %d now %d", tw.path, tw.size, got)
	}
	// mtime catches a rewrite that re-stamps the file with identical bytes/mode.
	if got := info.ModTime(); !got.Equal(tw.mtime) {
		t.Errorf("tripwire: %s was REWRITTEN (mtime changed: was %s now %s)", tw.path, tw.mtime, got)
	}
}

// AssertNoWritesOutsideHomeAndInstallDir is a best-effort tripwire for
// AC-no-admin-system-locations. It snapshots the mtimes of common system roots AND
// the specific promised-forbidden targets the docs call out (/usr/local/bin/ferry,
// /etc), plus the listing of /usr/local/bin, before a command and verifies nothing
// new appears.
//
// HONEST LIMIT: this is NOT a full filesystem write tracer. It cannot prove the
// negative for *every* path on the system; it samples the system roots the docs
// explicitly promise ferry never touches (/usr, /etc, /opt, /Library, /usr/local)
// AND the named forbidden targets. A truly exhaustive check needs an OS-level
// sandbox (seatbelt / landlock / a syscall monitor), which is not portable across
// CI hosts. Within the sandbox model, the strong guarantee comes from running the
// binary with HOME pointed at a temp dir and asserting all expected artifacts live
// under that HOME (and BinDir) — combined with these tripwires as a second line.
func (s *Sandbox) AssertNoWritesOutsideHomeAndInstallDir(t *testing.T, run func()) {
	t.Helper()
	// Top-level system roots (mtime tripwire).
	roots := []string{"/etc", "/usr", "/usr/local", "/opt", "/Library"}
	beforeRoot := map[string]time.Time{}
	for _, r := range roots {
		if info, err := os.Stat(r); err == nil {
			beforeRoot[r] = info.ModTime()
		}
	}

	// Specific promised-forbidden targets: a ferry binary must NEVER be placed in a
	// system bindir, and /etc must not gain a ferry file. Record their existence so
	// a NEWLY created file is caught even if the parent dir mtime is coarse.
	forbiddenTargets := []string{
		"/usr/local/bin/ferry",
		"/usr/bin/ferry",
		"/opt/homebrew/bin/ferry",
		"/etc/ferry",
		"/etc/ferry.toml",
	}
	existedBefore := map[string]bool{}
	for _, p := range forbiddenTargets {
		_, err := os.Lstat(p)
		existedBefore[p] = err == nil
	}

	// Snapshot the listing of system bindirs so a NEW entry (e.g. a ferry binary) is
	// detected regardless of dir-mtime granularity.
	bindirs := []string{"/usr/local/bin", "/usr/bin", "/opt/homebrew/bin"}
	beforeListing := map[string]map[string]bool{}
	for _, d := range bindirs {
		beforeListing[d] = dirEntrySet(d)
	}

	run()

	for r, was := range beforeRoot {
		info, err := os.Stat(r)
		if err != nil {
			t.Errorf("system-root tripwire: %s became unreadable after ferry ran", r)
			continue
		}
		if !info.ModTime().Equal(was) {
			t.Errorf("system-root tripwire: %s mtime changed (possible write outside HOME)", r)
		}
	}
	// A forbidden target that did NOT exist before but exists now = ferry wrote it.
	for _, p := range forbiddenTargets {
		if existedBefore[p] {
			continue
		}
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("system-location tripwire: ferry created %s (must install only to ~/.local/bin, never a system location)", p)
		}
	}
	// A NEW entry appeared in a system bindir = ferry placed something there.
	for _, d := range bindirs {
		after := dirEntrySet(d)
		for name := range after {
			if !beforeListing[d][name] {
				t.Errorf("system-location tripwire: new entry %q appeared in %s after ferry ran (must not write to system bindirs)", name, d)
			}
		}
	}
}

// dirEntrySet returns the set of immediate entry names in dir (empty if unreadable).
func dirEntrySet(dir string) map[string]bool {
	set := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return set
	}
	for _, e := range entries {
		set[e.Name()] = true
	}
	return set
}

// AssertAllArtifactsUnderHome walks HOME and BinDir is implied within it; it is a
// positive companion to the negative tripwire: after a run, every file ferry
// created should be reachable under Home. (Repo is a separate, legitimately
// written location for capture flows; callers pass allowExtra to whitelist it.)
//
// This helper documents the sandbox guarantee rather than enforcing a global FS
// scan; see AssertNoWritesOutsideHomeAndInstallDir for the honest-limit note.
func (s *Sandbox) UnderHome(p string) bool {
	rel, err := filepath.Rel(s.Home, p)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..")
}

// -----------------------------------------------------------------------------
// Repo / scope seeding helpers
// -----------------------------------------------------------------------------

// InitGitRepo turns s.Repo into a git working tree so gitignore / tracking
// assertions (AC-loc-ferry-local-toml-gitignored, AC-loc-local-overlay-dir) work.
// Skips the test if git is unavailable.
func (s *Sandbox) InitGitRepo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: skipping git-tracking assertions")
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = s.Repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=eval", "GIT_AUTHOR_EMAIL=eval@localhost",
			"GIT_COMMITTER_NAME=eval", "GIT_COMMITTER_EMAIL=eval@localhost")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "eval@localhost")
	run("config", "user.name", "eval")
}

// WriteRepoFile writes a file (creating parent dirs) inside the fake repo.
func (s *Sandbox) WriteRepoFile(t *testing.T, rel, content string) string {
	t.Helper()
	p := filepath.Join(s.Repo, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("WriteRepoFile mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteRepoFile %s: %v", rel, err)
	}
	return p
}

// WriteHomeFile writes a file (creating parent dirs) inside the sandbox HOME.
func (s *Sandbox) WriteHomeFile(t *testing.T, rel, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(s.Home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("WriteHomeFile mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatalf("WriteHomeFile %s: %v", rel, err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatalf("WriteHomeFile chmod %s: %v", rel, err)
	}
	return p
}

// HomePath joins a relative path onto the sandbox HOME.
func (s *Sandbox) HomePath(parts ...string) string {
	return filepath.Join(append([]string{s.Home}, parts...)...)
}

// RepoPath joins a relative path onto the fake repo.
func (s *Sandbox) RepoPath(parts ...string) string {
	return filepath.Join(append([]string{s.Repo}, parts...)...)
}

// ConfigTOMLPath is the documented location of ferry's machine config
// (AC-loc-config-toml): the docs name ~/.config/ferry/ only (no XDG_CONFIG_HOME
// behaviour is promised), which the sandbox pins to <home>/.config/ferry/config.toml.
func (s *Sandbox) ConfigTOMLPath() string {
	return filepath.Join(s.Home, ".config", "ferry", "config.toml")
}

// StateDir is the conventional state location an implementation MAY use
// (~/.local/state/ferry). NOTE: AC-apply-tracks-last-written (renamed from the old
// AC-loc-state-dir) asserts NO state path or $HOME-locality — the docs name no
// state dir. This helper is retained only for sandbox setup convenience and is not
// the subject of any assertion.
func (s *Sandbox) StateDir() string {
	return filepath.Join(s.Home, ".local", "state", "ferry")
}

// SeedSharedManifest writes a minimal ferry.toml into the repo and points
// config.toml at the repo clone, so commands that read scope have something to
// read. Returns nothing; paths are derived from the sandbox.
//
// TODO(contract): the exact key used in config.toml to record the repo path is
// not spelled out in the four docs. We write the most likely form ("repo =").
// If init writes config.toml itself, prefer running `ferry init` over this seed.
func (s *Sandbox) SeedSharedManifest(t *testing.T, manifest string) {
	t.Helper()
	s.WriteRepoFile(t, "ferry.toml", manifest)
	cfg := "repo = \"" + s.Repo + "\"\n"
	if err := os.MkdirAll(filepath.Dir(s.ConfigTOMLPath()), 0o755); err != nil {
		t.Fatalf("SeedSharedManifest mkdir config: %v", err)
	}
	if err := os.WriteFile(s.ConfigTOMLPath(), []byte(cfg), 0o644); err != nil {
		t.Fatalf("SeedSharedManifest write config: %v", err)
	}
}

// containsAllFold reports whether haystack contains every needle (case-insensitive).
// Used to assert help text mentions documented behaviour without over-fitting to
// exact wording.
func containsAllFold(haystack string, needles ...string) (string, bool) {
	h := strings.ToLower(haystack)
	for _, n := range needles {
		if !strings.Contains(h, strings.ToLower(n)) {
			return n, false
		}
	}
	return "", true
}

// containsAnyFold reports whether haystack contains at least one needle (fold).
func containsAnyFold(haystack string, needles ...string) bool {
	h := strings.ToLower(haystack)
	for _, n := range needles {
		if strings.Contains(h, strings.ToLower(n)) {
			return true
		}
	}
	return false
}
