package deps

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/platform"
)

// --- Homebrew status drift (read-only) --------------------------------------

func TestBrewDrift_ReportsAddedAndRemoved(t *testing.T) {
	depsDir := t.TempDir()
	// Repo (desired) Brewfile: a + b.
	writeFile(t, filepath.Join(depsDir, "Brewfile.darwin"), `brew "a"
cask "b"
`)
	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin",
		Shared: filepath.Join(depsDir, "Brewfile.darwin"),
		Local:  filepath.Join(depsDir, "Brewfile.darwin.local")}

	// Live (installed) set: a + c. `a` is common, `cask b` is only in the repo
	// (would be installed), `brew c` is only live (would be captured).
	r := newFakeRunner()
	r.replies["bundle dump"] = "brew \"a\"\nbrew \"c\"\n"

	drift, ok, err := brewDrift(m, r)
	if err != nil {
		t.Fatalf("brewDrift: %v", err)
	}
	if !ok {
		t.Fatalf("brewDrift: want supported=true for brew")
	}
	if drift.Empty() {
		t.Fatalf("brewDrift: want drift, got empty")
	}
	if want := []string{"brew c"}; !reflect.DeepEqual(drift.Added, want) {
		t.Errorf("Added = %v, want %v", drift.Added, want)
	}
	if want := []string{"cask b"}; !reflect.DeepEqual(drift.Removed, want) {
		t.Errorf("Removed = %v, want %v", drift.Removed, want)
	}
	// The live read MUST be the read-only stdout dump, never an install.
	if !r.invoked("bundle dump --file=-") {
		t.Errorf("brewDrift did not run the read-only `brew bundle dump --file=-`: %v", r.calls)
	}
	for _, c := range r.calls {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "bundle --file") || strings.Contains(joined, "bundle install") {
			t.Errorf("brewDrift ran an INSTALL (%q) — status must be read-only", joined)
		}
	}
}

func TestBrewDrift_CleanWhenMatch(t *testing.T) {
	depsDir := t.TempDir()
	writeFile(t, filepath.Join(depsDir, "Brewfile.darwin"), "brew \"a\"\ncask \"b\"\n")
	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin",
		Shared: filepath.Join(depsDir, "Brewfile.darwin"),
		Local:  filepath.Join(depsDir, "Brewfile.darwin.local")}

	r := newFakeRunner()
	// Same identities, DIFFERENT options — must still read as clean (identity-grained).
	r.replies["bundle dump"] = "cask \"b\"\nbrew \"a\", link: false\n"

	drift, ok, err := brewDrift(m, r)
	if err != nil || !ok {
		t.Fatalf("brewDrift: ok=%v err=%v", ok, err)
	}
	if !drift.Empty() {
		t.Errorf("brewDrift: want clean, got Added=%v Removed=%v", drift.Added, drift.Removed)
	}
}

func TestBrewDrift_LenientLiveParseIgnoresNoise(t *testing.T) {
	depsDir := t.TempDir()
	writeFile(t, filepath.Join(depsDir, "Brewfile.darwin"), "brew \"a\"\n")
	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin",
		Shared: filepath.Join(depsDir, "Brewfile.darwin"),
		Local:  filepath.Join(depsDir, "Brewfile.darwin.local")}

	r := newFakeRunner()
	// Homebrew's first-run auto-update banner interleaved with the dump. The live
	// parse must drop non-directive lines, not choke on them.
	r.replies["bundle dump"] = strings.Join([]string{
		"==> Auto-updating Homebrew...",
		"Warning: No remote 'origin'",
		"# a comment",
		"brew \"a\"",
		"Error: update-report should not be called directly!",
	}, "\n")

	drift, ok, err := brewDrift(m, r)
	if err != nil || !ok {
		t.Fatalf("brewDrift: ok=%v err=%v", ok, err)
	}
	if !drift.Empty() {
		t.Errorf("brewDrift: banner noise leaked into drift: Added=%v Removed=%v", drift.Added, drift.Removed)
	}
}

func TestBrewDrift_AptUnsupported(t *testing.T) {
	m := Manifest{Manager: platform.ManagerApt, GOOS: "linux", Shared: "/repo/deps/apt.txt"}
	_, ok, err := brewDrift(m, newFakeRunner())
	if err != nil {
		t.Fatalf("brewDrift(apt): %v", err)
	}
	if ok {
		t.Errorf("brewDrift(apt): want supported=false (apt has no clean dump)")
	}
}

// --- npm globals capture ----------------------------------------------------

func TestDumpNpmGlobals_NamesOnlySortedExcludesNpm(t *testing.T) {
	r := newFakeRunner()
	r.replies["ls -g"] = `{
	  "name": "lib",
	  "dependencies": {
	    "typescript": {"version": "5.4.0"},
	    "npm": {"version": "11.0.0"},
	    "@angular/cli": {"version": "17.0.0"},
	    "pyright": {"version": "1.1.0"}
	  }
	}`

	names, err := DumpNpmGlobals(r)
	if err != nil {
		t.Fatalf("DumpNpmGlobals: %v", err)
	}
	want := []string{"@angular/cli", "pyright", "typescript"} // sorted, npm excluded, no versions
	if !reflect.DeepEqual(names, want) {
		t.Errorf("DumpNpmGlobals = %v, want %v", names, want)
	}
}

func TestDumpNpmGlobals_ToleratesNonZeroExitWithJSON(t *testing.T) {
	// `npm ls` exits non-zero on peer-dep warnings while still emitting valid JSON.
	r := &jsonErrRunner{body: `{"dependencies":{"typescript":{"version":"5.4.0"}}}`}
	names, err := DumpNpmGlobals(r)
	if err != nil {
		t.Fatalf("DumpNpmGlobals with non-zero exit + valid JSON: %v", err)
	}
	if want := []string{"typescript"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestReDumpNpmGlobals_WritesSortedList(t *testing.T) {
	depsDir := t.TempDir()
	r := newFakeRunner()
	r.replies["ls -g"] = `{"dependencies":{"zed":{"version":"1"},"apple":{"version":"2"},"npm":{"version":"3"}}}`

	path, err := ReDumpNpmGlobals(depsDir, r)
	if err != nil {
		t.Fatalf("ReDumpNpmGlobals: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written list: %v", err)
	}
	if want := "apple\nzed\n"; string(got) != want {
		t.Errorf("npm-globals.txt = %q, want %q", got, want)
	}
}

func TestReDumpNpmGlobals_RefusesSymlinkTarget(t *testing.T) {
	depsDir := t.TempDir()
	// Point the target at a symlink: the write-through guard must refuse.
	target := filepath.Join(depsDir, "npm-globals.txt")
	if err := os.Symlink(filepath.Join(depsDir, "elsewhere"), target); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	r := newFakeRunner()
	r.replies["ls -g"] = `{"dependencies":{"a":{"version":"1"}}}`
	if _, err := ReDumpNpmGlobals(depsDir, r); err == nil {
		t.Errorf("ReDumpNpmGlobals wrote through a symlink target — must refuse")
	}
}

// --- npm globals install ----------------------------------------------------

func TestInstallNpmGlobals_InvokesGlobalInstallWithNames(t *testing.T) {
	depsDir := t.TempDir()
	writeFile(t, filepath.Join(depsDir, "npm-globals.txt"), "typescript\n@angular/cli\n")
	r := newFakeRunner()

	names, err := InstallNpmGlobals(depsDir, r)
	if err != nil {
		t.Fatalf("InstallNpmGlobals: %v", err)
	}
	// Reconciled the sorted, validated set.
	if want := []string{"@angular/cli", "typescript"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	// Exactly one `npm i -g -- <names>` invocation with the end-of-options guard.
	if len(r.calls) != 1 {
		t.Fatalf("want 1 npm call, got %d: %v", len(r.calls), r.calls)
	}
	got := r.calls[0]
	want := []string{"npm", "i", "-g", "--", "@angular/cli", "typescript"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("npm install argv = %v, want %v", got, want)
	}
}

func TestInstallNpmGlobals_EmptyListNoInvocation(t *testing.T) {
	depsDir := t.TempDir()
	// No npm-globals.txt at all: a clean no-op that never touches npm.
	r := newFakeRunner()
	names, err := InstallNpmGlobals(depsDir, r)
	if err != nil {
		t.Fatalf("InstallNpmGlobals empty: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("names = %v, want empty", names)
	}
	if len(r.calls) != 0 {
		t.Errorf("empty list invoked npm %d times (must be a no-op): %v", len(r.calls), r.calls)
	}
}

func TestInstallNpmGlobals_RefusesInjectionSpec(t *testing.T) {
	depsDir := t.TempDir()
	// A git-URL spec would fetch+run arbitrary install-time code: refuse before npm.
	writeFile(t, filepath.Join(depsDir, "npm-globals.txt"), "typescript\ngit+https://evil.example/x.git\n")
	r := newFakeRunner()
	if _, err := InstallNpmGlobals(depsDir, r); err == nil {
		t.Errorf("InstallNpmGlobals accepted a git-URL spec — must refuse")
	}
	if len(r.calls) != 0 {
		t.Errorf("refused list still invoked npm: %v", r.calls)
	}
}

// --- npm globals drift ------------------------------------------------------

func TestNpmGlobalsDrift_AddedAndRemoved(t *testing.T) {
	depsDir := t.TempDir()
	writeFile(t, filepath.Join(depsDir, "npm-globals.txt"), "typescript\npyright\n")
	r := newFakeRunner()
	// Live: typescript + eslint. pyright is listed-not-installed (to install);
	// eslint is installed-not-listed (to capture).
	r.replies["ls -g"] = `{"dependencies":{"typescript":{"version":"5"},"eslint":{"version":"9"},"npm":{"version":"11"}}}`

	drift, err := NpmGlobalsDrift(depsDir, r)
	if err != nil {
		t.Fatalf("NpmGlobalsDrift: %v", err)
	}
	if want := []string{"eslint"}; !reflect.DeepEqual(drift.Added, want) {
		t.Errorf("Added = %v, want %v", drift.Added, want)
	}
	if want := []string{"pyright"}; !reflect.DeepEqual(drift.Removed, want) {
		t.Errorf("Removed = %v, want %v", drift.Removed, want)
	}
}

// --- validation -------------------------------------------------------------

func TestValidateNpmName_AcceptsPlainAndScoped(t *testing.T) {
	for _, ok := range []string{"typescript", "npm-check-updates", "@angular/cli", "pyright", "vite", "@a/b.c_d~e"} {
		if err := ValidateNpmName(ok); err != nil {
			t.Errorf("ValidateNpmName(%q) rejected a valid name: %v", ok, err)
		}
	}
}

func TestValidateNpmName_RefusesSpecsAndFlags(t *testing.T) {
	for _, bad := range []string{
		"git+https://evil/x.git", // URL spec
		"../evil",                // local path
		"-g",                     // a flag
		"left-pad@1.0.0",         // version tag
		"@scope",                 // no package part
		"@scope/",                // empty package part
		"@scope/a/b",             // extra segment
		"a b",                    // whitespace
		"..",                     // traversal
		"https://x/y.tgz",        // tarball URL
		"foo.tgz",                // local tarball spec (npm classifies as type=file)
		"bar.tar",                // local tarball spec
		"baz.tar.gz",             // local tarball spec
		"1.tgz",                  // digit-led tarball spec
		"foo.TGZ",                // case-insensitive suffix
		"typescript.tar.gz",      // plausible-looking tarball spec
	} {
		if err := ValidateNpmName(bad); err == nil {
			t.Errorf("ValidateNpmName(%q) accepted an unsafe entry — must refuse", bad)
		}
	}
}

// --- regression: the Brewfile gate still refuses an npm directive ------------

func TestValidateBrewfileDirective_StillRefusesNpm(t *testing.T) {
	// npm globals get their OWN file/manager; an `npm` line must NEVER be accepted
	// into a Brewfile (it is not in the allow-list and `brew bundle` would run it).
	if err := ValidateBrewfileDirective(`npm "typescript"`); err == nil {
		t.Errorf("ValidateBrewfileDirective accepted an `npm` directive — the allow-list must refuse it")
	}
}

// --- helpers ----------------------------------------------------------------

// jsonErrRunner returns a fixed body together with a non-nil error, modelling
// `npm ls` exiting non-zero (peer-dep warnings) while still emitting valid JSON.
type jsonErrRunner struct{ body string }

func (j *jsonErrRunner) Run(_ ...string) (string, error) {
	return j.body, errors.New("npm ls: exit status 1")
}
