package evals

// iTerm2 GLOBAL preference domain — the v0.7.0 D4 import-blob model (the
// custom-prefs-folder mechanism is retired). apply imports the committed,
// allowlist-filtered plist via `defaults import`, REFUSING when iTerm2 is running
// and flushing cfprefsd afterwards; capture reduces the live export to the
// allowlisted global keys (dropping NoSync*/geometry) before committing it.
// Darwin-only (the terminal domain is macOS-native).

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// seedSecret writes a single secret value into the out-of-repo store so a
// `{{ferry.secret "domain.key"}}` placeholder resolves at apply time. (Shared with
// other evals — this domain's own secret handling rides the same pipeline.)
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

// makeITerm2Stubs writes fake `defaults`, `pgrep` and `killall` recorders on a PATH
// dir. `running` controls whether `pgrep -x iTerm2` reports iTerm2 alive (exit 0)
// or not (exit 1). `defaults export …` prints exportPlist so Backup/capture see a
// live domain; `defaults import …` captures its stdin to importStdin. Returns the
// dir plus the paths of the recorder log and the captured import stdin.
func makeITerm2Stubs(t *testing.T, running bool, exportPlist string) (dir, logPath, importStdin string) {
	t.Helper()
	dir = t.TempDir()
	logPath = filepath.Join(dir, "invocations.log")
	importStdin = filepath.Join(dir, "import_stdin")
	exportFile := filepath.Join(dir, "export.plist")
	if err := os.WriteFile(exportFile, []byte(exportPlist), 0o644); err != nil {
		t.Fatal(err)
	}

	defaults := "#!/bin/sh\n" +
		"echo \"defaults $*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1\" = export ]; then cat " + shellQuote(exportFile) + "; fi\n" +
		"if [ \"$1\" = import ]; then cat > " + shellQuote(importStdin) + "; fi\n" +
		"exit 0\n"
	pgrepExit := "1"
	if running {
		pgrepExit = "0"
	}
	pgrep := "#!/bin/sh\necho \"pgrep $*\" >> " + shellQuote(logPath) + "\nexit " + pgrepExit + "\n"
	killall := "#!/bin/sh\necho \"killall $*\" >> " + shellQuote(logPath) + "\nexit 0\n"

	for name, body := range map[string]string{"defaults": defaults, "pgrep": pgrep, "killall": killall} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatalf("stub %s: %v", name, err)
		}
	}
	return dir, logPath, importStdin
}

func iTerm2PathEnv(stubDir string) string {
	return "PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")
}

// writeGlobalITerm2Manifest seeds a repo with the iTerm2 global domain in scope and
// a committed com.googlecode.iterm2.plist (the allowlist-filtered global prefs).
func writeGlobalITerm2Manifest(t *testing.T, s *Sandbox, plist string) {
	t.Helper()
	s.WriteRepoFile(t, "ferry.toml", "[manage]\ndotfiles = [\".zshrc\"]\niterm2 = true\n")
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")
	if plist != "" {
		s.WriteRepoFile(t, filepath.Join("iterm2", "com.googlecode.iterm2.plist"), plist)
	}
	if err := os.WriteFile(s.ConfigTOMLPath(), []byte("repo = \""+s.Repo+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

const globalITerm2Plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PromptOnQuit</key>
	<false/>
</dict>
</plist>
`

// (a) apply with iTerm2 NOT running imports the committed plist via `defaults
// import` and flushes cfprefsd (killall cfprefsd).
func TestSpot_ITerm2GlobalApplyImportsWhenNotRunning(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("iterm2 global domain: macOS-only")
	}
	s := NewSandbox(t)
	writeGlobalITerm2Manifest(t, s, globalITerm2Plist)

	stubDir, logPath, importStdin := makeITerm2Stubs(t, false, globalITerm2Plist)
	out, errOut, code := s.FerryEnv([]string{iTerm2PathEnv(stubDir)}, "apply")
	if code != 0 {
		t.Fatalf("apply exited %d\n%s", code, out+errOut)
	}
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "defaults import com.googlecode.iterm2 -") {
		t.Errorf("did not `defaults import` the global plist\nlog:\n%s", log)
	}
	if !strings.Contains(string(log), "killall cfprefsd") {
		t.Errorf("did not flush cfprefsd after import\nlog:\n%s", log)
	}
	imported, _ := os.ReadFile(importStdin)
	if !strings.Contains(string(imported), "PromptOnQuit") {
		t.Errorf("imported blob was not the committed plist\n%s", imported)
	}
}

// (b) apply REFUSES when iTerm2 is running: no import, cfprefsd not flushed, live
// config left intact, and the user is told to quit iTerm2.
func TestSpot_ITerm2GlobalApplyRefusesWhenRunning(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("iterm2 global domain: macOS-only")
	}
	s := NewSandbox(t)
	writeGlobalITerm2Manifest(t, s, globalITerm2Plist)

	stubDir, logPath, _ := makeITerm2Stubs(t, true /* running */, globalITerm2Plist)
	out, errOut, code := s.FerryEnv([]string{iTerm2PathEnv(stubDir)}, "apply")
	if code != 0 {
		t.Fatalf("apply exited %d\n%s", code, out+errOut)
	}
	if !containsAnyFold(out+errOut, "quit iterm2") {
		t.Errorf("expected a 'quit iTerm2' skip notice while running\n%s", out+errOut)
	}
	log, _ := os.ReadFile(logPath)
	if strings.Contains(string(log), "defaults import com.googlecode.iterm2") {
		t.Errorf("imported into a RUNNING iTerm2 (would be silently lost)\nlog:\n%s", log)
	}
	if strings.Contains(string(log), "killall cfprefsd") {
		t.Errorf("flushed cfprefsd despite skipping the import\nlog:\n%s", log)
	}
}

// (c) capture reduces the live export to the ALLOWLISTED global keys: an
// allowlisted key survives into the committed plist; volatile NoSync*/geometry keys
// are dropped.
func TestSpot_ITerm2GlobalCaptureFiltersAllowlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("iterm2 global domain: macOS-only")
	}
	s := NewSandbox(t)
	// No committed plist yet, so the live export is pure drift to capture.
	writeGlobalITerm2Manifest(t, s, "")

	liveExport := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PromptOnQuit</key>
	<true/>
	<key>NoSyncSuppressAnnoyingBellOffer</key>
	<true/>
	<key>NSWindow Frame iTerm Window 0</key>
	<string>0 0 800 600</string>
	<key>PrefsCustomFolder</key>
	<string>/tmp/whatever</string>
</dict>
</plist>
`
	stubDir, _, _ := makeITerm2Stubs(t, false, liveExport)
	// Answer: capture the whole domain [y], route [s]hared.
	out, errOut, code := s.FerryEnvWithInput("y\ns\n", []string{iTerm2PathEnv(stubDir)}, "capture")
	if code != 0 {
		t.Fatalf("capture exited %d\n%s", code, out+errOut)
	}
	committed := s.RepoPath("iterm2", "com.googlecode.iterm2.plist")
	data, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("committed global plist not written at %s: %v\n%s", committed, err, out+errOut)
	}
	body := string(data)
	if !strings.Contains(body, "PromptOnQuit") {
		t.Errorf("allowlisted key PromptOnQuit was dropped\n%s", body)
	}
	for _, volatile := range []string{"NoSyncSuppressAnnoyingBellOffer", "NSWindow Frame", "PrefsCustomFolder"} {
		if strings.Contains(body, volatile) {
			t.Errorf("volatile key %q survived capture (must be dropped by the allowlist)\n%s", volatile, body)
		}
	}
}
