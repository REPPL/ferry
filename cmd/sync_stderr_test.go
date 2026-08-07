package cmd

// A git stderr warning (an unreadable config, a broken ref, a safe.directory
// advisory) must never leak into the machine-parsed stdout of ferry's hardened
// git helper: with combined streams, the warning text fuses with the first
// NUL-delimited field, so the first untracked/ignored entry silently vanishes
// from the out-of-band backup — blinding the overwrite guard and rollback.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// noisyGit puts a `git` shim first on PATH that writes a warning line to stderr
// and execs the real git — simulating any warning-emitting git configuration
// while keeping exit status 0.
func noisyGit(t *testing.T) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'warning: sync-stderr-test noise' >&2\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// MAJOR: parsed git stdout stays free of stderr noise.
func TestGitSyncStdoutFreeOfStderrNoise(t *testing.T) {
	repo := r3Repo(t)
	noisyGit(t)
	out, err := gitSync(repo, "status", "--porcelain", "--ignored", "-z")
	if err != nil {
		t.Fatalf("status --porcelain: %v", err)
	}
	if strings.Contains(out, "warning:") {
		t.Fatalf("git stderr leaked into parsed stdout: %q", out)
	}
}

// MAJOR: the first untracked entry survives a warning-emitting git — the
// out-of-band backup enumeration must not lose it to stream contamination.
func TestBackupOutOfBandSurvivesGitStderrNoise(t *testing.T) {
	repo := r3Repo(t)
	// aaa.txt sorts first in status output, so stderr noise would fuse with it.
	if err := os.WriteFile(filepath.Join(repo, "aaa.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	noisyGit(t)
	snap, err := takeSnapshot(repo)
	if err != nil {
		t.Fatalf("takeSnapshot: %v", err)
	}
	if !snap.untracked["aaa.txt"] {
		t.Fatalf("first untracked entry lost to stderr contamination: untracked=%v", snap.untracked)
	}
}

// MAJOR: a malformed porcelain field (anything that is not "XY path") aborts
// the out-of-band enumeration instead of being skipped — an enumeration ferry
// cannot parse cannot back the overwrite guard or a byte-for-byte rollback.
func TestParseStatusZFailsClosedOnMalformedField(t *testing.T) {
	if _, _, err := parseStatusZ("warning: noise?? aaa.txt\x00!! bbb.txt\x00"); err == nil {
		t.Fatal("malformed status field was silently skipped, want fail-closed error")
	}
	u, ig, err := parseStatusZ("?? aaa.txt\x00!! bbb.txt\x00R  new.txt\x00old.txt\x00 M base.txt\x00")
	if err != nil {
		t.Fatalf("valid porcelain output rejected: %v", err)
	}
	// A WORKTREE-side rename ("mv old new && git add -N new") carries the code
	// in the Y column; its origin field must be consumed the same way — and ONLY
	// that field, so the entry that follows survives (an over-consuming skip
	// would silently swallow it and still parse clean).
	ru, _, rerr := parseStatusZ(" R new.txt\x00old.txt\x00?? aaa.txt\x00")
	if rerr != nil {
		t.Fatalf("Y-column rename rejected: %v", rerr)
	}
	if len(ru) != 1 || ru[0].rel != "aaa.txt" {
		t.Fatalf("Y-column rename over-consumed the next entry: untracked=%v, want aaa.txt", ru)
	}
	if len(u) != 1 || u[0].rel != "aaa.txt" || len(ig) != 1 || ig[0].rel != "bbb.txt" {
		t.Fatalf("parse = %v / %v, want aaa.txt untracked and bbb.txt ignored", u, ig)
	}
}
