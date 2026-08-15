package evals

// Behavioural eval for the per-machine deps overlay promise: deps/Brewfile.<goos>.local
// "belongs to one machine only" — it must never be committed or published. The repos
// ferry CREATES must therefore ignore it out of the box, or `ferry sync`'s `git add -A`
// commits a machine's private overlay, every other machine clones and installs it via
// `apply --deps`, and `bundle export` (tracked-set driven) ships it.
//
// Drives the REAL binary (skips when FERRY_BIN is unset) against a LOCAL BARE GIT
// REPO as origin — no network, no real GitHub.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFreshInitIgnoresPerMachineDepsOverlay: a fresh `ferry init` repo must leave
// a written deps/Brewfile.<goos>.local UNTRACKED across a real `ferry sync` — the
// sync commit must not contain it and it must not reach the bare origin.
func TestFreshInitIgnoresPerMachineDepsOverlay(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: init/sync evals need git")
	}
	s := NewSandbox(t)

	// Fresh init at an explicit destination: ferry writes the repo AND its .gitignore.
	if _, errOut, code := s.FerryWithInput("", "init", "--fresh", s.Repo); code != 0 {
		t.Fatalf("`ferry init --fresh` exited %d\n%s", code, errOut)
	}

	// The per-machine overlay this machine would use (deps/Brewfile.<goos>.local).
	overlayRel := "deps/Brewfile." + runtime.GOOS + ".local"
	const overlayMarker = "PER_MACHINE_ONLY_OVERLAY_MARKER"

	// (1) Cheap, direct check: git itself must consider the path ignored in the
	// repo ferry just created.
	if err := os.MkdirAll(filepath.Join(s.Repo, "deps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Repo, overlayRel),
		[]byte("# "+overlayMarker+"\nbrew \"ripgrep\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runGitIn(s.Repo, "check-ignore", "-q", "--", overlayRel); err != nil {
		ign, _ := os.ReadFile(filepath.Join(s.Repo, ".gitignore"))
		t.Errorf("fresh init repo does NOT ignore %s (git check-ignore: %v %s)\n.gitignore:\n%s",
			overlayRel, err, strings.TrimSpace(out), ign)
	}

	// (2) End-to-end: a real `ferry sync` must not commit or publish it.
	bare := t.TempDir()
	syncGit(t, bare, "init", "-q", "--bare", "-b", syncBranch, ".")
	syncGit(t, s.Repo, "config", "user.email", "eval@localhost")
	syncGit(t, s.Repo, "config", "user.name", "eval")
	syncGit(t, s.Repo, "add", "-A")
	syncGit(t, s.Repo, "commit", "-q", "--allow-empty", "-m", "baseline")
	syncGit(t, s.Repo, "remote", "add", "origin", bare)
	syncGit(t, s.Repo, "push", "-q", "origin", syncBranch)
	syncGit(t, s.Repo, "fetch", "-q", "origin")

	// A tracked change so sync has real work to commit and push alongside the overlay.
	s.WriteRepoFile(t, "shared.txt", "line-1\n")

	if _, errOut, code := s.FerryEnvWithInput("y\n", []string{allowFileOrigin},
		"sync", "--allow-unmanaged"); code != 0 {
		t.Fatalf("`ferry sync` exited %d\n%s", code, errOut)
	}

	// The overlay must still be untracked in the working clone...
	// (`ls-files -- <path>` lists the path only when it is in the index.)
	if tracked, ok := syncGitOK(t, s.Repo, "ls-files", "--", overlayRel); ok && strings.TrimSpace(tracked) != "" {
		t.Errorf("`ferry sync` TRACKED the per-machine overlay %s (it must stay untracked): %s", overlayRel, tracked)
	}
	// ...and must not appear anywhere in the pushed history on the origin.
	if out, ok := syncGitOK(t, bare, "log", "--all", "--name-only", "--pretty=format:"); ok {
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == overlayRel {
				t.Errorf("the per-machine overlay %s was PUBLISHED to the origin (it belongs to one machine only)", overlayRel)
			}
		}
	}
	// Belt and braces: no object in the origin's store carries the overlay's marker.
	assertMarkerNotInBareObjects(t, bare, overlayMarker)

	// The overlay's content on this machine survives untouched.
	body, err := os.ReadFile(filepath.Join(s.Repo, overlayRel))
	if err != nil || !strings.Contains(string(body), overlayMarker) {
		t.Errorf("the per-machine overlay was lost or rewritten locally: %v %q", err, body)
	}
}

// assertMarkerNotInBareObjects scans EVERY object in a BARE repo's store
// (reachable + unreachable/dangling) for the needle — proving "no OBJECTS carry
// this content", not merely "no ref points at it". A no-op when git is absent.
func assertMarkerNotInBareObjects(t *testing.T, bare, needle string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	env := gitIsolatedEnv("GIT_PAGER=cat")
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
		if f := strings.Fields(line); len(f) >= 3 && isHexish(f[2]) {
			ids[f[2]] = true
		}
	}
	for id := range ids {
		cat := exec.Command("git", "-C", bare, "cat-file", "-p", id)
		cat.Env = env
		if body, err := cat.CombinedOutput(); err == nil && strings.Contains(string(body), needle) {
			t.Errorf("the per-machine overlay's content reached the origin: needle found in bare object %s", id)
			return
		}
	}
}
