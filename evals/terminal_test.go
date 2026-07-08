package evals

// Terminal-config AC. macOS-only: both documented terminal apps (iTerm2 AND Apple
// Terminal) must be handled as macOS-native preference domains, not naive file
// copies. Mechanism-agnostic (CFPreferences or `defaults` both valid). The deep
// per-app plist key/value + live store is deferred to the Layer-2 eval-vs-AC pass.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// terminalApp pairs a documented terminal domain with its native preference
// domain id and the human names that may appear in prose plans.
type terminalApp struct {
	domain  string   // manifest key
	prefID  string   // macOS preference domain id
	aliases []string // names a prose plan might use for this app/domain
}

var terminalApps = []terminalApp{
	{"iterm2", "com.googlecode.iterm2", []string{"iterm2", "iterm 2", "iterm"}},
	{"terminal", "com.apple.Terminal", []string{"apple terminal", "terminal.app", "macos terminal"}},
}

// prefChangeVerbs are words signalling a planned PREFERENCE change for a terminal
// domain (so a prose plan "would configure iTerm2 preferences" is caught even
// without the prefID token).
var prefChangeVerbs = []string{
	"preference", "prefs", "configure", "set ", "write", "apply", "defaults",
	"plist", "cfpref", "would set", "would configure", "would apply", "update",
}

// names returns the lowercased identifiers that denote THIS terminal app/domain in
// diff/prose output: the manifest domain key, the native prefID, and human aliases.
func (a terminalApp) names() []string {
	out := []string{strings.ToLower(a.domain), strings.ToLower(a.prefID)}
	for _, al := range a.aliases {
		out = append(out, strings.ToLower(al))
	}
	return out
}

// diffPlansTerminalChange reports whether the (lowercased) diff output plans a
// terminal-PREFERENCE change for this app — by referencing the app/domain NAME or
// prefID on a line that also carries a preference-change verb, OR by referencing
// the prefID directly. Catches a prose plan that omits the prefID.
func diffPlansTerminalChange(diffLower string, app terminalApp) bool {
	if strings.Contains(diffLower, strings.ToLower(app.prefID)) {
		return true
	}
	for _, line := range strings.Split(diffLower, "\n") {
		namesApp := false
		for _, n := range app.names() {
			if strings.Contains(line, n) {
				namesApp = true
				break
			}
		}
		if !namesApp {
			continue
		}
		for _, v := range prefChangeVerbs {
			if strings.Contains(line, v) {
				return true
			}
		}
	}
	return false
}

// nativePrefMarkers are words that, when associated with a terminal domain in
// diff output, signal NATIVE-PREFERENCE handling distinct from a generic
// file/dotfile copy. The bare domain NAME is intentionally excluded — a plain
// dotfile-copy impl could also print the name; only a preference/native qualifier
// (or the `defaults` recorder firing) proves native handling.
var nativePrefMarkers = []string{
	"preference", "defaults write", "defaults", "cfpref", "plist domain",
	"native preference", "preference domain", "macos preference", "prefs",
}

// fileCopyMarkers are words signalling a GENERIC FILE/DOTFILE COPY plan for a
// domain — the WRONG handling for a terminal app. If the diff marks THIS terminal
// domain with one of these, it was planned as a dumb copy, not a preference domain.
var fileCopyMarkers = []string{
	"copy file", "copying", "file copy", "copy ", "cp ", "symlink", "dotfile copy",
	"copied to", "copy to",
}

// TestTerminalConfig covers AC-terminal-config (GATING on macOS): for BOTH iTerm2
// AND Apple Terminal, an in-scope terminal domain is handled as a macOS-native
// preference domain (named as such in diff output, OR a stubbed `defaults`
// recorder fires), NOT as a plain dotfile copy. An impl that handles no terminal
// domain — or only one of the two — must not fully pass. Out-of-scope => no
// terminal change (tripwire).
//
// TODO(contract): the docs define no terminal-settings schema, so the concrete
// fixture (which keys/values) and live plist verification are DEFERRED to the
// Layer-2 eval-vs-AC pass. The differential gate (native-pref-domain vs file-copy;
// in-scope vs out-of-scope) is what is asserted here.
func TestTerminalConfig_AC_terminal_config(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("AC-terminal-config: macOS-only (terminal-app preferences are a macOS-native mechanism)")
	}

	for _, app := range terminalApps {
		app := app
		t.Run(app.domain, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)

			// In-scope: this terminal domain enabled.
			s.WriteRepoFile(t, "ferry.toml", "[manage]\ndotfiles = [\".zshrc\"]\n"+app.domain+" = true\n")
			s.WriteRepoFile(t, ".zshrc", "# managed\n")
			s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")
			if err := os.WriteFile(s.ConfigTOMLPath(), []byte("repo = \""+s.Repo+"\"\n"), 0o644); err != nil {
				t.Fatalf("write config.toml: %v", err)
			}

			// Stub `defaults` recorder: if the impl shells out to apply prefs, fires.
			defStubDir, defLog := makeDefaultsStub(t)
			pathOverride := "PATH=" + defStubDir + string(os.PathListSeparator) + os.Getenv("PATH")

			// Drive diff (preview) for the diff-marker signal, then apply so a
			// `defaults`-shelling impl exercises the recorder.
			diffOut, diffErr, _ := s.FerryEnv([]string{pathOverride}, "diff")
			diffCombined := strings.ToLower(diffOut + diffErr)
			s.FerryEnv([]string{pathOverride}, "apply")

			prefID := strings.ToLower(app.prefID)

			// DECISIVE GATE (clarified round-8): the REQUIRED, always-available signal
			// is the `ferry diff` PLAN. `ferry diff` is a documented command, so a
			// correct native-preference impl surfaces THIS domain in its plan as a
			// preference/native domain REGARDLESS of whether it ultimately uses
			// `defaults` or CFPreferences directly; a naive file-copy impl surfaces it
			// as a file copy. We gate on the diff plan — NOT on the mechanism — so a
			// valid direct-CFPreferences impl is not false-failed. There is NO skip
			// path for the in-scope case: absence of the preference-domain marker (or
			// the domain shown as a file copy) is a FAIL.
			//
			// "Marked as a preference/native domain" = the diff references THIS app's
			// preference DOMAIN ID (com.googlecode.iterm2 / com.apple.Terminal) or a
			// native preference qualifier co-located with this domain's name/prefID.
			refsDomainID := strings.Contains(diffCombined, prefID)
			markedNativeInDiff := refsDomainID || lineHasMarkerNearDomain(diffCombined, app, nativePrefMarkers)

			// The domain must NOT be planned as a generic dotfile/file-copy entry.
			shownAsFileCopy := lineHasMarkerNearDomain(diffCombined, app, fileCopyMarkers)

			if !markedNativeInDiff {
				t.Errorf("AC-terminal-config[%s]: `ferry diff` plan does NOT mark this terminal domain as a preference/native domain "+
					"(no preference domain id %q or native-preference marker for it) — dotfiles-only, only-other-app, or a file copy? This is the decisive gate (no skip).\ndiff:\n%s",
					app.domain, app.prefID, diffOut+diffErr)
			}
			if shownAsFileCopy {
				t.Errorf("AC-terminal-config[%s]: `ferry diff` plans this terminal domain as a generic FILE-COPY entry (not a native preference domain)\ndiff:\n%s",
					app.domain, diffOut+diffErr)
			}

			// CORROBORATING extras (NOT substitutes, NOT gating): a `defaults`
			// recorder firing for the domain or a valid plist at the prefs path are
			// nice-to-have evidence; a bare plist alone is explicitly NOT a pass and
			// the gate above already decided on the diff plan.
			if logReferences(defLog, app.prefID) || logReferences(defLog, app.domain) {
				t.Logf("AC-terminal-config[%s] (corroborating): `defaults` recorder fired for the domain.", app.domain)
			}
			if isValidPlistFile(s.HomePath("Library", "Preferences", app.prefID+".plist")) {
				t.Logf("AC-terminal-config[%s] (corroborating): a valid plist exists in the prefs store.", app.domain)
			}
			// NAIVE-COPY failure (mechanism-agnostic): fire ONLY on evidence of a
			// GENERIC DOTFILE-style copy — the terminal settings materialised to a
			// NON-preference location (e.g. a ~/.iterm2 dotfile or under ~/.config),
			// or the domain routed through the plain dotfiles path. A plist in
			// ~/Library/Preferences/ (CFPreferences OR `defaults`) is NOT a naive copy.
			for _, badPath := range []string{
				s.HomePath("." + app.domain),               // ~/.iterm2 / ~/.terminal
				s.HomePath("." + app.domain + "rc"),        // ~/.iterm2rc
				s.HomePath(".config", app.domain),          // ~/.config/iterm2
				s.HomePath("." + app.prefID),               // ~/.com.googlecode.iterm2
				s.HomePath("Library", app.prefID+".plist"), // wrong (non-prefs) Library location
			} {
				if info, err := os.Lstat(badPath); err == nil && (info.Mode().IsRegular() || info.IsDir()) {
					t.Errorf("AC-terminal-config[%s]: terminal settings materialised to a NON-preference location %s (generic dotfile-style copy, not a native preference domain)",
						app.domain, badPath)
				}
			}
		})
	}
}

// TestTerminalConfigOutOfScope covers the out-of-scope half of AC-terminal-config
// (overlaps AC-effective-scope-overlay): with a terminal domain DISABLED, NO
// terminal-preference change is planned/referenced FOR THAT DOMAIN — caught for a
// `defaults`-shelling impl (recorder silent for the domain), a direct-CFPreferences
// impl (no plist written for the domain), AND in the diff (no reference to the
// disabled domain's prefID). Run per-app so an "only iTerm2 / only Terminal"
// regression is caught.
func TestTerminalConfigOutOfScope_AC_terminal_config(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("AC-terminal-config: macOS-only")
	}
	for _, app := range terminalApps {
		app := app
		t.Run(app.domain, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			// BOTH terminal domains disabled; this app is the one under test.
			s.WriteRepoFile(t, "ferry.toml", "[manage]\ndotfiles = [\".zshrc\"]\niterm2 = false\nterminal = false\n")
			s.WriteRepoFile(t, ".zshrc", "# managed\n")
			s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")
			if err := os.WriteFile(s.ConfigTOMLPath(), []byte("repo = \""+s.Repo+"\"\n"), 0o644); err != nil {
				t.Fatalf("write config.toml: %v", err)
			}

			defStubDir, defLog := makeDefaultsStub(t)
			pathOverride := "PATH=" + defStubDir + string(os.PathListSeparator) + os.Getenv("PATH")

			// Tripwire the per-domain plist target (catches a direct-CFPreferences/copy impl).
			plistTarget := s.HomePath("Library", "Preferences", app.prefID+".plist")
			plistTW := s.SnapshotFile(t, plistTarget) // absent

			diffOut, diffErr, _ := s.FerryEnv([]string{pathOverride}, "diff")
			diffCombined := strings.ToLower(diffOut + diffErr)
			if _, errOut, code := s.FerryEnv([]string{pathOverride}, "apply"); code != 0 {
				t.Fatalf("AC-terminal-config(out-of-scope)[%s]: apply exited %d; stderr:\n%s", app.domain, code, errOut)
			}

			// (a) `defaults` recorder must not have fired for THIS domain.
			if logReferences(defLog, app.prefID) || logReferences(defLog, app.domain) {
				t.Errorf("AC-terminal-config(out-of-scope)[%s]: `defaults` was invoked for a DISABLED terminal domain", app.domain)
			}
			// (b) no plist written for THIS domain (direct-CFPreferences impl).
			plistTW.AssertUnchanged(t)
			// (c) diff must plan NO terminal-preference change for THIS domain — in
			// ANY form: not only the prefID token, but also a prose plan that names
			// the app/domain (iterm2 / "Apple Terminal" / com.apple.Terminal) together
			// with a preference-change verb ("would configure iterm2 preferences").
			if diffPlansTerminalChange(diffCombined, app) {
				t.Errorf("AC-terminal-config(out-of-scope)[%s]: diff plans a terminal-preference change for the DISABLED domain (named + change verb, or prefID)\n%s",
					app.domain, diffOut+diffErr)
			}
		})
	}
}

// lineHasMarkerNearDomain reports whether any line of out contains BOTH a native
// preference marker AND this app's domain name or preference id — so the marker is
// tied to THIS terminal domain, not a stray preference word elsewhere in the diff.
func lineHasMarkerNearDomain(out string, app terminalApp, markers []string) bool {
	dom := strings.ToLower(app.domain)
	pid := strings.ToLower(app.prefID)
	for _, line := range strings.Split(out, "\n") {
		l := strings.ToLower(line)
		if !strings.Contains(l, dom) && !strings.Contains(l, pid) {
			continue
		}
		for _, m := range markers {
			if strings.Contains(l, m) {
				return true
			}
		}
	}
	return false
}

// isValidPlistFile reports whether path is a regular file whose contents look like
// a real macOS plist — a binary plist (magic "bplist00") or an XML plist (XML
// declaration / <plist> / <dict>). A raw copy of an arbitrary non-plist source
// (e.g. a junk file) would NOT match, separating a native preference write from a
// dumb file copy that merely targets the plist path.
func isValidPlistFile(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	head := strings.ToLower(string(buf[:n]))
	if strings.HasPrefix(string(buf[:n]), "bplist00") {
		return true // binary plist magic
	}
	return strings.Contains(head, "<?xml") || strings.Contains(head, "<plist") || strings.Contains(head, "<dict")
}

// logReferences reports whether the recorder log mentions the given token (e.g. a
// preference domain id passed as an argument to `defaults`).
func logReferences(logPath, token string) bool {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), strings.ToLower(token))
}

// makeDefaultsStub writes a fake macOS `defaults` recorder that logs each
// invocation. Returns (dir, logPath).
func makeDefaultsStub(t *testing.T) (dir, logPath string) {
	t.Helper()
	dir = t.TempDir()
	logPath = filepath.Join(dir, "defaults_invocations.log")
	script := "#!/bin/sh\necho \"defaults $*\" >> " + shellQuote(logPath) + "\nexit 0\n"
	stub := filepath.Join(dir, "defaults")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("makeDefaultsStub: %v", err)
	}
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatalf("makeDefaultsStub chmod: %v", err)
	}
	return dir, logPath
}
