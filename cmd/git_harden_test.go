package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests drive the ONE untrusted-transport git helper (runHardenedGit /
// gitSync) against REAL git repos crafted with the hostile .git/config a cloned
// or wired config repo could carry. They prove: an ext:: insteadOf rewrite runs
// NO command (fail closed), a malicious core.fsmonitor runs NO command on a
// worktree scan, a legitimate file:// clone still succeeds (file allowed for
// user actions), and a leading-dash source is refused.

func rawGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = gitIsolatedEnv("GIT_PAGER=cat")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newCommittedRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	rawGit(t, repo, "init", "-q", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rawGit(t, repo, "add", "-A")
	rawGit(t, repo, "commit", "-qm", "base")
	return repo
}

// TestGitSync_InsteadOfExtNeutralized: a wired repo whose .git/config rewrites its
// origin to an `ext::` command via insteadOf must fail closed on fetch — no marker
// file appears. (R3 ext:: leg.)
//
// The control (`-c protocol.ext.allow=never`) only bites when a user's global
// gitconfig has RAISED the ext policy to `always`, so the test installs exactly
// that hostile user-global (via GIT_CONFIG_GLOBAL) and proves a fail-before: raw
// git DOES fire the ext:: command, gitSync does NOT — mirroring the fsmonitor
// baseline. Without the hostile global, default git refuses ext:: on its own and
// the test would pass with or without the hardening.
func TestGitSync_InsteadOfExtNeutralized(t *testing.T) {
	repo := newCommittedRepo(t)
	markerDir := t.TempDir()

	// Hostile USER-GLOBAL: protocol.ext.allow=always makes git willing to run an
	// ext:: transport for a config-rewritten URL. Point GIT_CONFIG_GLOBAL at it (and
	// neuter the system config) so BOTH raw git and gitSync inherit it — the only
	// difference is gitSync's command-line `-c protocol.ext.allow=never` override.
	globalCfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalCfg, []byte("[protocol \"ext\"]\n\tallow = always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	writeExtConfig := func(marker string) {
		// insteadOf matches the FULL origin URL so nothing trails the ext command:
		// a prefix-only match would append the URL remainder ("repo.git") to the
		// command string and corrupt it. The rewritten URL is exactly
		// `ext::sh -c touch>MARKER`, which git splits to `sh -c "touch>MARKER"`.
		cfg := "[core]\n\trepositoryformatversion = 0\n" +
			"[remote \"origin\"]\n\turl = https://example.invalid/repo.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n" +
			"[url \"ext::sh -c touch>" + marker + "\"]\n\tinsteadOf = https://example.invalid/repo.git\n"
		if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Baseline: raw (unhardened) git fetch WITH the hostile global FIRES the ext:: cmd.
	rawMarker := filepath.Join(markerDir, "raw.txt")
	writeExtConfig(rawMarker)
	rawFetch := exec.Command("git", "-C", repo, "-c", "fetch.recurseSubmodules=no", "fetch", "--no-recurse-submodules", "--no-tags", "origin")
	rawFetch.Env = gitIsolatedEnv("GIT_PAGER=cat")
	_, _ = rawFetch.CombinedOutput()
	if _, err := os.Stat(rawMarker); err != nil {
		t.Skipf("ext:: did not fire under raw git on this platform (%v) — fixture inert here", err)
	}

	// Hardened: gitSync must NOT fire the ext:: cmd (`protocol.ext.allow=never` wins).
	hardMarker := filepath.Join(markerDir, "hard.txt")
	writeExtConfig(hardMarker)
	if _, err := gitSync(repo, "-c", "fetch.recurseSubmodules=no", "fetch", "--no-recurse-submodules", "--no-tags", "origin"); err == nil {
		t.Errorf("hostile ext:: fetch should have failed closed")
	}
	if _, err := os.Stat(hardMarker); err == nil {
		t.Fatalf("ext:: command executed (marker %s created) — insteadOf NOT neutralized", hardMarker)
	}
}

// TestGitSync_MaliciousFsmonitorNeutralized: a repo whose core.fsmonitor is a
// command must NOT run it on a worktree scan through the hardened helper. Without
// the hardening (raw git), the same fixture DOES run it — proving the fixture is
// genuinely hostile.
func TestGitSync_MaliciousFsmonitorNeutralized(t *testing.T) {
	repo := newCommittedRepo(t)
	markerDir := t.TempDir()
	rawMarker := filepath.Join(markerDir, "raw.txt")
	// core.fsmonitor is a shell command that touches a marker then exits non-zero.
	appendConfig(t, repo, "[core]\n\tfsmonitor = \"touch "+rawMarker+"; false\"\n")

	// Baseline: raw (unhardened) git status FIRES the fsmonitor — the fixture is real.
	rawStatus := exec.Command("git", "-C", repo, "status", "--porcelain")
	rawStatus.Env = gitIsolatedEnv("GIT_PAGER=cat")
	_, _ = rawStatus.CombinedOutput()
	if _, err := os.Stat(rawMarker); err != nil {
		t.Skipf("fsmonitor did not fire under raw git on this platform (%v) — fixture inert here", err)
	}

	// Hardened: gitSync status must NOT fire the fsmonitor.
	hardMarker := filepath.Join(markerDir, "hard.txt")
	appendConfig(t, repo, "[core]\n\tfsmonitor = \"touch "+hardMarker+"; false\"\n")
	_, _ = gitSync(repo, "status", "--porcelain")
	if _, err := os.Stat(hardMarker); err == nil {
		t.Fatalf("malicious core.fsmonitor executed through the hardened helper (marker %s) — core.fsmonitor NOT disabled", hardMarker)
	}
}

// TestRunHardenedGit_LegitFileCloneSucceeds: a legitimate command-line file://
// clone still works (protocol.file.allow=user), proving the file-allow vs ext-deny
// distinction. (R3 legit-clone leg.)
func TestRunHardenedGit_LegitFileCloneSucceeds(t *testing.T) {
	src := newCommittedRepo(t)
	dest := filepath.Join(t.TempDir(), "clone")
	if out, err := runHardenedGit("clone", "--", "file://"+src, dest); err != nil {
		t.Fatalf("legitimate file:// clone should succeed under the hardened helper: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dest, "f.txt")); err != nil {
		t.Errorf("cloned working tree missing expected file: %v", err)
	}
}

// TestRejectLeadingDashSource refuses a clone source that begins with '-' (it
// could be read as a git option). (F7.)
func TestRejectLeadingDashSource(t *testing.T) {
	for _, bad := range []string{"-oProxyCommand=touch /tmp/x", "--upload-pack=touch /tmp/x", "-"} {
		if err := checkCloneSource(bad); err == nil {
			t.Errorf("checkCloneSource(%q) should refuse a leading-dash source", bad)
		}
	}
	// A normal https source is still accepted.
	if err := checkCloneSource("https://github.com/owner/repo.git"); err != nil {
		t.Errorf("checkCloneSource rejected a normal https source: %v", err)
	}
}

// TestSyncEffectivePushURL_RejectsPushInsteadOf: F8 — a hostile pushInsteadOf that
// rewrites ONLY the push URL to a non-HTTPS (ext::) target is caught by rechecking
// the effective push URL scheme, even though the fetch URL looks https.
func TestSyncEffectivePushURL_RejectsPushInsteadOf(t *testing.T) {
	repo := newCommittedRepo(t)
	cfg := "[core]\n\trepositoryformatversion = 0\n" +
		"[remote \"origin\"]\n\turl = https://example.invalid/repo.git\n" +
		"[url \"ext::sh -c evil\"]\n\tpushInsteadOf = https://example.invalid/\n"
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	pushURL, err := syncEffectivePushURL(repo)
	if err != nil {
		t.Fatalf("resolving effective push URL: %v", err)
	}
	if !strings.HasPrefix(pushURL, "ext::") {
		t.Fatalf("expected effective push URL to reflect pushInsteadOf (ext::…), got %q", pushURL)
	}
	if err := checkOriginScheme(pushURL); err == nil {
		t.Errorf("checkOriginScheme must reject the ext:: effective push URL %q", pushURL)
	}
}

func appendConfig(t *testing.T, repo, section string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(section); err != nil {
		t.Fatal(err)
	}
}
