package evals

// iTerm2 Dynamic Profiles — a repo-authoritative FileDomain (v0.7.0 Phase B). The
// config repo's iterm2/DynamicProfiles/*.json deploy to
// ~/Library/Application Support/iTerm2/DynamicProfiles/ as regular-file COPIES
// (never symlinks); a profile's Guid is byte-preserved; a malformed JSON is refused
// (never deployed, so it can't disable all dynamic profiles); the domain is gated by
// [manage] iterm2-profiles; the per-machine local/iterm2-profiles/ overlay wins per
// file; and status reports clean after apply and drift after a live edit. Runs on
// every platform (JSON validity is checked in pure Go; the deploy is a file copy).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const iterm2ProfilesManifest = `[manage]
dotfiles = [".zshrc"]
iterm2-profiles = true
brew = false
iterm2 = false
fonts = false
`

// dynamicProfilesDir is the deploy destination under $HOME.
func dynamicProfilesDir(s *Sandbox, rel ...string) string {
	return s.HomePath(append([]string{"Library", "Application Support", "iTerm2", "DynamicProfiles"}, rel...)...)
}

const workProfileJSON = `{"Profiles":[{"Name":"Work","Guid":"WORK-GUID-FROZEN-1","Rewritable":false}]}`

// TestITerm2ProfilesDeploysValidRefusesMalformed proves a valid profile deploys as a
// regular-file copy with its GUID byte-preserved, while a malformed JSON is refused
// and never deployed.
func TestITerm2ProfilesDeploysValidRefusesMalformed(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, iterm2ProfilesManifest)

	s.WriteRepoFile(t, filepath.Join("iterm2", "DynamicProfiles", "Work.json"), workProfileJSON)
	s.WriteRepoFile(t, filepath.Join("iterm2", "DynamicProfiles", "Bad.json"), "{ this is not json")
	gitCommitAll(t, s.Repo, "dynamic profiles")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	target := dynamicProfilesDir(s, "Work.json")
	assertFileContains(t, target, "WORK-GUID-FROZEN-1")
	// Byte-identical: ferry never rewrites the JSON (frozen GUID contract).
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read deployed profile: %v", err)
	}
	if string(data) != workProfileJSON {
		t.Errorf("deployed profile bytes changed:\n got: %s\nwant: %s", data, workProfileJSON)
	}
	// Deployed as a REGULAR FILE copy — ferry never symlinks under $HOME.
	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("lstat target: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		t.Errorf("%s is not a regular-file copy (mode %v)", target, fi.Mode())
	}
	// The malformed profile was refused and never deployed.
	if _, err := os.Lstat(dynamicProfilesDir(s, "Bad.json")); err == nil {
		t.Errorf("a malformed profile was deployed (must be refused)")
	}
}

// TestITerm2ProfilesScopeGated proves the domain is OFF by default: with no
// iterm2-profiles key in scope, a repo tree is never deployed.
func TestITerm2ProfilesScopeGated(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest) // no iterm2-profiles key
	s.WriteRepoFile(t, filepath.Join("iterm2", "DynamicProfiles", "Work.json"), workProfileJSON)
	gitCommitAll(t, s.Repo, "profiles not in scope")

	tw := s.SnapshotFile(t, dynamicProfilesDir(s, "Work.json")) // absent
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	tw.AssertUnchanged(t) // still absent — domain out of scope
}

// TestITerm2ProfilesLocalOverlayWins proves the per-machine overlay
// (local/iterm2-profiles/<rel>) overrides the shared repo copy for that file.
func TestITerm2ProfilesLocalOverlayWins(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, iterm2ProfilesManifest)

	s.WriteRepoFile(t, filepath.Join("iterm2", "DynamicProfiles", "Work.json"), workProfileJSON)
	// A machine-local child profile referencing the shared parent's frozen GUID.
	local := `{"Profiles":[{"Name":"Work","Guid":"WORK-GUID-FROZEN-1","Rewritable":false,"Normal Font":"Menlo 16"}]}`
	s.WriteRepoFile(t, filepath.Join("local", "iterm2-profiles", "Work.json"), local)
	gitCommitAll(t, s.Repo, "profiles with local overlay")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	assertFileContains(t, dynamicProfilesDir(s, "Work.json"), "Menlo 16")
}

// TestITerm2ProfilesStatusCleanThenDrift proves status is clean right after apply
// (no spurious de-scope warning) and drifts after a live edit — the domain is
// repo-authoritative (apply would skip a live edit).
func TestITerm2ProfilesStatusCleanThenDrift(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, iterm2ProfilesManifest)
	s.WriteRepoFile(t, filepath.Join("iterm2", "DynamicProfiles", "Work.json"), workProfileJSON)
	gitCommitAll(t, s.Repo, "profiles")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	stdout, errOut, code := s.Ferry("status")
	if code != 0 {
		t.Fatalf("status exited %d; stderr:\n%s", code, errOut)
	}
	if !strings.Contains(stdout, "no drift detected") {
		t.Errorf("status not clean right after apply; stdout:\n%s", stdout)
	}
	if strings.Contains(stdout, "no longer managed") || strings.Contains(stdout, "no longer part of the iterm2-profiles plan") {
		t.Errorf("clean status falsely warned the managed profile is de-scoped; stdout:\n%s", stdout)
	}

	// A live edit must surface as drift (repo-authoritative: apply would skip it).
	if err := os.WriteFile(dynamicProfilesDir(s, "Work.json"), []byte(`{"Profiles":[{"Name":"Edited"}]}`), 0o644); err != nil {
		t.Fatalf("live edit: %v", err)
	}
	stdout, errOut, code = s.Ferry("status")
	if code != 0 {
		t.Fatalf("status after edit exited %d; stderr:\n%s", code, errOut)
	}
	if strings.Contains(stdout, "no drift detected") || !strings.Contains(stdout, "drift") {
		t.Errorf("status did not report drift after a live edit; stdout:\n%s", stdout)
	}
}
