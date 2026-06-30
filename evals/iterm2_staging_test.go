package evals

// Spot-checks for the iTerm2 secret-render-or-skip-on-apply fix: apply renders
// the repo iterm2 plist into a ferry-owned RENDERED STAGING FOLDER under StateDir
// and points PrefsCustomFolder THERE, so a present secret is substituted (iTerm2
// never loads the raw `{{ferry.secret}}` plist); a MISSING secret OR a refused /
// symlinked leaf plist SKIPS the iterm2 domain (PrefsCustomFolder not pointed at
// it, live config intact). Darwin-only (the terminal domain is macOS-native).

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// seedSecret writes a single secret value into the out-of-repo store so a
// `{{ferry.secret "domain.key"}}` placeholder resolves at apply time.
func seedSecret(t *testing.T, s *Sandbox, domain, key, value string) {
	t.Helper()
	dir := filepath.Join(s.Home, ".config", "ferry", "secrets-local")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("seedSecret mkdir: %v", err)
	}
	f := filepath.Join(dir, domain+".toml")
	if err := os.WriteFile(f, []byte(key+" = \""+value+"\"\n"), 0o600); err != nil {
		t.Fatalf("seedSecret write: %v", err)
	}
}

// stagedITerm2Plist is the path apply stages the rendered iterm2 plist at.
func stagedITerm2Plist(s *Sandbox) string {
	return filepath.Join(s.Home, ".local", "state", "ferry", "rendered", "iterm2", "com.googlecode.iterm2.plist")
}

func writeITerm2Manifest(t *testing.T, s *Sandbox) {
	t.Helper()
	s.WriteRepoFile(t, "ferry.toml", "[manage]\ndotfiles = [\".zshrc\"]\niterm2 = true\n")
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")
	if err := os.WriteFile(s.ConfigTOMLPath(), []byte("repo = \""+s.Repo+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// (a) present secret => rendered plist staged (placeholder substituted) and
// PrefsCustomFolder points at the staging folder, never the raw repo folder.
func TestSpot_ITerm2SecretPresentRendersIntoStaging(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("iterm2 staging: macOS-only")
	}
	s := NewSandbox(t)
	writeITerm2Manifest(t, s)
	seedSecret(t, s, "x", "y", "S3CRET-VALUE")
	s.WriteRepoFile(t, filepath.Join("iterm2", "com.googlecode.iterm2.plist"),
		"token={{ferry.secret \"x.y\"}}\n")

	defStubDir, defLog := makeDefaultsStub(t)
	pathOverride := "PATH=" + defStubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	out, errOut, code := s.FerryEnv([]string{pathOverride}, "apply")
	if code != 0 {
		t.Fatalf("apply exited %d\n%s", code, out+errOut)
	}

	staged := stagedITerm2Plist(s)
	data, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("rendered plist not staged at %s: %v", staged, err)
	}
	if !strings.Contains(string(data), "S3CRET-VALUE") {
		t.Errorf("staged plist did not substitute the secret\n%s", data)
	}
	if strings.Contains(string(data), "ferry.secret") {
		t.Errorf("staged plist still carries the raw placeholder (unrendered)\n%s", data)
	}
	// PrefsCustomFolder must point at the staging folder, NOT the raw repo folder.
	logData, _ := os.ReadFile(defLog)
	log := string(logData)
	stageFolder := filepath.Dir(staged)
	if !strings.Contains(log, "PrefsCustomFolder") || !strings.Contains(log, stageFolder) {
		t.Errorf("PrefsCustomFolder not pointed at the staging folder %s\ndefaults log:\n%s", stageFolder, log)
	}
	if strings.Contains(log, s.RepoPath("iterm2")+" ") || strings.Contains(log, s.RepoPath("iterm2")+"\n") {
		t.Errorf("PrefsCustomFolder was pointed at the RAW repo folder (leaks the placeholder plist)\n%s", log)
	}
	// The rendered secret must never reach the repo working tree / history.
	s.AssertNoSecretInRepo(t, "S3CRET-VALUE")
}

// (b) missing secret => iterm2 domain SKIPPED: nothing staged, PrefsCustomFolder
// not pointed at it, live config intact.
func TestSpot_ITerm2SecretMissingSkipsDomain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("iterm2 staging: macOS-only")
	}
	s := NewSandbox(t)
	writeITerm2Manifest(t, s)
	// No secret seeded => x.y is MISSING.
	s.WriteRepoFile(t, filepath.Join("iterm2", "com.googlecode.iterm2.plist"),
		"token={{ferry.secret \"x.y\"}}\n")

	defStubDir, defLog := makeDefaultsStub(t)
	pathOverride := "PATH=" + defStubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	out, errOut, code := s.FerryEnv([]string{pathOverride}, "apply")
	if code != 0 {
		t.Fatalf("apply exited %d\n%s", code, out+errOut)
	}
	if !containsAnyFold(out+errOut, "iterm2") || !containsAnyFold(out+errOut, "skip", "missing") {
		t.Errorf("expected an iterm2 skip notice for the missing secret\n%s", out+errOut)
	}
	if _, err := os.Stat(stagedITerm2Plist(s)); err == nil {
		t.Errorf("a plist was staged despite the missing secret (domain should be skipped)")
	}
	logData, _ := os.ReadFile(defLog)
	if strings.Contains(string(logData), "PrefsCustomFolder") {
		t.Errorf("PrefsCustomFolder was set despite the missing secret\n%s", logData)
	}
}

// (c) symlinked/escaping leaf plist => iterm2 domain SKIPPED (refusal honored for
// the very file iTerm2 would load, not swallowed). PrefsCustomFolder not set.
func TestSpot_ITerm2LeafPlistSymlinkSkipsDomain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("iterm2 staging: macOS-only")
	}
	s := NewSandbox(t)
	s.SSHTripwire(t)
	writeITerm2Manifest(t, s)
	// A regular repo iterm2/ folder, but the LEAF plist is a symlink into ~/.ssh.
	if err := os.MkdirAll(s.RepoPath("iterm2"), 0o755); err != nil {
		t.Fatal(err)
	}
	leaf := s.RepoPath("iterm2", "com.googlecode.iterm2.plist")
	if err := os.Symlink(filepath.Join(s.Home, ".ssh", "config"), leaf); err != nil {
		t.Fatal(err)
	}

	defStubDir, defLog := makeDefaultsStub(t)
	pathOverride := "PATH=" + defStubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	out, errOut, _ := s.FerryEnv([]string{pathOverride}, "apply")
	if !containsAnyFold(out+errOut, "iterm2") || !containsAnyFold(out+errOut, "refus", "skip") {
		t.Errorf("expected a refusal/skip notice for the symlinked iterm2 leaf plist\n%s", out+errOut)
	}
	if _, err := os.Stat(stagedITerm2Plist(s)); err == nil {
		t.Errorf("a plist was staged from a refused leaf (refusal swallowed)")
	}
	logData, _ := os.ReadFile(defLog)
	if strings.Contains(string(logData), "PrefsCustomFolder") {
		t.Errorf("PrefsCustomFolder was set for a refused leaf plist\n%s", logData)
	}
	s.AssertSSHUntouched(t)
}

// (d) a normal plist with no secrets => applies; the rendered staged copy equals
// the original and iTerm2 is pointed at the staging folder.
func TestSpot_ITerm2NoSecretsStagesUnchangedCopy(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("iterm2 staging: macOS-only")
	}
	s := NewSandbox(t)
	writeITerm2Manifest(t, s)
	const body = "plain=1\nno-secrets-here\n"
	s.WriteRepoFile(t, filepath.Join("iterm2", "com.googlecode.iterm2.plist"), body)

	defStubDir, defLog := makeDefaultsStub(t)
	pathOverride := "PATH=" + defStubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	out, errOut, code := s.FerryEnv([]string{pathOverride}, "apply")
	if code != 0 {
		t.Fatalf("apply exited %d\n%s", code, out+errOut)
	}
	staged := stagedITerm2Plist(s)
	data, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("rendered plist not staged at %s: %v", staged, err)
	}
	if string(data) != body {
		t.Errorf("staged copy != original\n got: %q\nwant: %q", data, body)
	}
	logData, _ := os.ReadFile(defLog)
	if !strings.Contains(string(logData), "PrefsCustomFolder") || !strings.Contains(string(logData), filepath.Dir(staged)) {
		t.Errorf("PrefsCustomFolder not pointed at the staging folder\n%s", logData)
	}
}
