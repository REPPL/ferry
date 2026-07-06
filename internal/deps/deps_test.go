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

// fakeRunner is a CommandRunner stub: it records every invocation and serves
// canned output keyed by a substring of the joined args (so a test can make
// `brew list --formula` return a fixed set). It NEVER runs a real binary.
type fakeRunner struct {
	calls   [][]string
	replies map[string]string // substring of joined args -> output
	failOn  string            // if non-empty and joined args contain it, return an error
}

func newFakeRunner() *fakeRunner { return &fakeRunner{replies: map[string]string{}} }

func (f *fakeRunner) Run(args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	if f.failOn != "" && strings.Contains(joined, f.failOn) {
		return "", errors.New("fake runner: forced failure on " + f.failOn)
	}
	for sub, out := range f.replies {
		if strings.Contains(joined, sub) {
			return out, nil
		}
	}
	return "", nil
}

// invoked reports whether any recorded call's joined args contain sub.
func (f *fakeRunner) invoked(sub string) bool {
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), sub) {
			return true
		}
	}
	return false
}

// --- manifest selection -----------------------------------------------------

func TestSelectManifest_PerGOOSAndManager(t *testing.T) {
	depsDir := "/repo/deps"
	cases := []struct {
		goos       string
		mgr        platform.PackageManager
		wantShared string
		wantLocal  string
		wantErr    error
	}{
		{"darwin", platform.ManagerBrew, "/repo/deps/Brewfile.darwin", "/repo/deps/Brewfile.darwin.local", nil},
		{"linux", platform.ManagerBrew, "/repo/deps/Brewfile.linux", "/repo/deps/Brewfile.linux.local", nil},
		{"linux", platform.ManagerApt, "/repo/deps/apt.txt", "", nil},
		{"darwin", platform.ManagerNone, "", "", ErrNoPackageManager},
	}
	for _, tc := range cases {
		m, err := selectManifest(depsDir, tc.goos, tc.mgr)
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("selectManifest(%s,%s): err=%v want %v", tc.goos, tc.mgr, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("selectManifest(%s,%s): unexpected err %v", tc.goos, tc.mgr, err)
		}
		if m.Shared != tc.wantShared {
			t.Errorf("selectManifest(%s,%s): Shared=%q want %q", tc.goos, tc.mgr, m.Shared, tc.wantShared)
		}
		if m.Local != tc.wantLocal {
			t.Errorf("selectManifest(%s,%s): Local=%q want %q", tc.goos, tc.mgr, m.Local, tc.wantLocal)
		}
	}
}

func TestSelectManifest_EmptyDepsDir(t *testing.T) {
	if _, err := selectManifest("", "darwin", platform.ManagerBrew); err == nil {
		t.Error("selectManifest with empty depsDir: want error, got nil")
	}
}

// --- manifest parsing + .local layering -------------------------------------

func TestEntries_LayersLocalAfterShared(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "Brewfile.darwin")
	local := filepath.Join(dir, "Brewfile.darwin.local")
	writeFile(t, shared, "# shared\ntap \"homebrew/bundle\"\nbrew \"zoxide\"\nbrew \"direnv\"\n")
	writeFile(t, local, "\n# machine-specific\nbrew \"htop\"\n")

	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: shared, Local: local}
	got, err := m.Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	want := []string{`tap "homebrew/bundle"`, `brew "zoxide"`, `brew "direnv"`, `brew "htop"`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Entries (shared then local): got %v want %v", got, want)
	}
}

func TestEntries_MissingFilesAreEmptyNotError(t *testing.T) {
	dir := t.TempDir()
	m := Manifest{
		Manager: platform.ManagerBrew,
		GOOS:    "darwin",
		Shared:  filepath.Join(dir, "Brewfile.darwin"),       // absent
		Local:   filepath.Join(dir, "Brewfile.darwin.local"), // absent
	}
	got, err := m.Entries()
	if err != nil {
		t.Fatalf("Entries on absent files: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Entries on absent files: got %v want empty", got)
	}
}

func TestParseManifest_Apt(t *testing.T) {
	dir := t.TempDir()
	apt := filepath.Join(dir, "apt.txt")
	writeFile(t, apt, "# debian deps\nzsh\nzoxide # nice cd\n\ndirenv\n")
	got, err := ParseManifest(apt, platform.ManagerApt)
	if err != nil {
		t.Fatalf("ParseManifest apt: %v", err)
	}
	want := []string{"zsh", "zoxide", "direnv"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseManifest apt: got %v want %v", got, want)
	}
}

// --- gated install: invokes the manager with the selected Brewfile + .local ---

func TestInstallBrew_InvokesBundleSharedThenLocal(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "Brewfile.darwin")
	local := filepath.Join(dir, "Brewfile.darwin.local")
	writeFile(t, shared, "brew \"zoxide\"\n")
	writeFile(t, local, "brew \"htop\"\n")
	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: shared, Local: local}

	r := newFakeRunner()
	// before: only zoxide; after: zoxide + htop -> htop is the recorded install.
	r.replies["list --formula"] = "zoxide"

	if _, err := install(m, r); err != nil {
		t.Fatalf("install brew: %v", err)
	}

	// brew bundle invoked for the SHARED file then the .local overlay, in order.
	var bundles []string
	for _, c := range r.calls {
		j := strings.Join(c, " ")
		if strings.Contains(j, "bundle") && strings.Contains(j, "--file=") {
			bundles = append(bundles, j)
		}
	}
	if len(bundles) != 2 {
		t.Fatalf("brew bundle invoked %d times want 2: %v", len(bundles), bundles)
	}
	if !strings.Contains(bundles[0], "--file="+shared) {
		t.Errorf("first bundle not the shared Brewfile: %q", bundles[0])
	}
	if !strings.Contains(bundles[1], "--file="+local) {
		t.Errorf("second bundle not the .local overlay (layered last): %q", bundles[1])
	}
}

func TestInstallBrew_OverlaySkippedWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "Brewfile.darwin")
	writeFile(t, shared, "brew \"zoxide\"\n")
	// .local path set but file does not exist -> must be skipped, not errored.
	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: shared,
		Local: filepath.Join(dir, "Brewfile.darwin.local")}

	r := newFakeRunner()
	if _, err := install(m, r); err != nil {
		t.Fatalf("install brew (no overlay): %v", err)
	}
	if r.invoked(".local") {
		t.Error("bundled a non-existent .local overlay")
	}
	if !r.invoked("--file=" + shared) {
		t.Error("did not bundle the shared Brewfile")
	}
}

// --- records the installed set (before/after diff) --------------------------

func TestInstallBrew_RecordsInstalledSet(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "Brewfile.darwin")
	writeFile(t, shared, "brew \"htop\"\nbrew \"direnv\"\n")
	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: shared}

	// The fake serves a GROWING installed set: first `list` call (before) returns
	// the base set; we simulate the bundle adding htop+direnv by switching the
	// reply after the bundle runs.
	r := &growingRunner{before: "zoxide", after: "zoxide direnv htop"}

	res, err := install(m, r)
	if err != nil {
		t.Fatalf("install brew: %v", err)
	}
	got := res.RecordedInstalledSet()
	want := []string{"direnv", "htop"} // sorted, only the newly-added
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RecordedInstalledSet: got %v want %v", got, want)
	}
}

// growingRunner returns `before` for brew list until a bundle runs, then `after`.
type growingRunner struct {
	before, after string
	bundled       bool
}

func (g *growingRunner) Run(args ...string) (string, error) {
	j := strings.Join(args, " ")
	if strings.Contains(j, "bundle") && !strings.Contains(j, "dump") {
		g.bundled = true
		return "", nil
	}
	if strings.Contains(j, "list --formula") {
		if g.bundled {
			return g.after, nil
		}
		return g.before, nil
	}
	return "", nil
}

// TestInstallBrew_FailedBeforeSnapshot_RecordsNothing is the fail-closed
// guarantee: if the BEFORE `brew list` snapshot fails, ferry must NOT record
// pre-existing packages as newly installed (which would let restore --packages
// later uninstall packages the user already had). It records nothing instead.
func TestInstallBrew_FailedBeforeSnapshot_RecordsNothing(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "Brewfile.darwin")
	writeFile(t, shared, "brew \"htop\"\n")
	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: shared}

	// before snapshot FAILS; after snapshot would return a full set. A naive diff
	// would record every package in `after` as newly installed.
	r := &snapshotRunner{afterList: "zoxide direnv htop", failBefore: true}

	res, err := install(m, r)
	if err != nil {
		t.Fatalf("install brew: %v", err)
	}
	if got := res.RecordedInstalledSet(); len(got) != 0 {
		t.Errorf("failed before-snapshot must record nothing, got %v", got)
	}
}

// snapshotRunner serves a different `brew list` reply before vs after the
// bundle, and can force the before snapshot to fail.
type snapshotRunner struct {
	afterList  string
	failBefore bool
	bundled    bool
}

func (s *snapshotRunner) Run(args ...string) (string, error) {
	j := strings.Join(args, " ")
	if strings.Contains(j, "bundle") && !strings.Contains(j, "dump") {
		s.bundled = true
		return "", nil
	}
	if strings.Contains(j, "list --formula") || strings.Contains(j, "list --cask") {
		if !s.bundled {
			if s.failBefore {
				return "", errors.New("fake: before snapshot failed")
			}
			return "", nil
		}
		if strings.Contains(j, "--formula") {
			return s.afterList, nil
		}
		return "", nil
	}
	return "", nil
}

// TestInstallApt_AlreadyInstalledNotRecorded checks the apt fail-closed record:
// a package present BEFORE the install is not recorded as ferry-installed; only
// a genuinely new (absent→present) package is.
func TestInstallApt_AlreadyInstalledNotRecorded(t *testing.T) {
	dir := t.TempDir()
	apt := filepath.Join(dir, "apt.txt")
	writeFile(t, apt, "zsh\nzoxide\n")
	m := Manifest{Manager: platform.ManagerApt, GOOS: "linux", Shared: apt}

	// zsh already installed before; zoxide absent before, present after.
	r := &aptStateRunner{
		before: map[string]string{"zsh": "install ok installed"},
		after:  map[string]string{"zsh": "install ok installed", "zoxide": "install ok installed"},
	}

	res, err := install(m, r)
	if err != nil {
		t.Fatalf("install apt: %v", err)
	}
	want := []string{"zoxide"} // zsh pre-existed, must NOT be recorded
	if got := res.RecordedInstalledSet(); !reflect.DeepEqual(got, want) {
		t.Errorf("apt recorded set: got %v want %v", got, want)
	}
}

// aptStateRunner answers dpkg-query from a before/after status map and flips
// after apt-get install runs. A package absent from the map is "not installed":
// dpkg-query exits non-zero with empty output, which the prober reads as absent.
type aptStateRunner struct {
	before, after map[string]string
	installed     bool
	installArgs   []string // argv of the apt-get install invocation, once seen
}

func (a *aptStateRunner) Run(args ...string) (string, error) {
	j := strings.Join(args, " ")
	if strings.Contains(j, "apt-get install") {
		a.installed = true
		a.installArgs = append([]string(nil), args...)
		return "", nil
	}
	if strings.Contains(j, "dpkg-query") {
		pkg := args[len(args)-1]
		m := a.before
		if a.installed {
			m = a.after
		}
		if status, ok := m[pkg]; ok {
			return status, nil
		}
		// Not installed: dpkg-query exits non-zero with empty status.
		return "", errors.New("dpkg-query: no packages found matching " + pkg)
	}
	return "", nil
}

// TestInstallBrew_AbsentSharedBrewfileNotBundled checks fix #3: a shared
// Brewfile that does not exist must NOT be handed to `brew bundle` (which would
// fail on a missing file); the present .local overlay is still processed.
func TestInstallBrew_AbsentSharedBrewfileNotBundled(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "Brewfile.darwin") // absent
	local := filepath.Join(dir, "Brewfile.darwin.local")
	writeFile(t, local, "brew \"htop\"\n")
	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: shared, Local: local}

	r := newFakeRunner()
	if _, err := install(m, r); err != nil {
		t.Fatalf("install brew (absent shared): %v", err)
	}
	// Collect the exact bundled file args (exact match: shared is a prefix of
	// local, so a substring check would conflate them).
	var bundled []string
	for _, c := range r.calls {
		for _, a := range c {
			if strings.HasPrefix(a, "--file=") {
				bundled = append(bundled, strings.TrimPrefix(a, "--file="))
			}
		}
	}
	for _, f := range bundled {
		if f == shared {
			t.Error("bundled a non-existent shared Brewfile")
		}
	}
	if !reflect.DeepEqual(bundled, []string{local}) {
		t.Errorf("expected only the present .local overlay bundled, got %v", bundled)
	}
}

// --- no manager present: reports, does NOT bootstrap ------------------------

func TestInstall_NoManager_ReportsNoBootstrap(t *testing.T) {
	m := Manifest{Manager: platform.ManagerNone}
	r := newFakeRunner()
	_, err := install(m, r)
	if !errors.Is(err, ErrNoPackageManager) {
		t.Fatalf("install with no manager: err=%v want ErrNoPackageManager", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("install with no manager invoked the runner %d times (must NOT bootstrap a PM): %v", len(r.calls), r.calls)
	}
}

func TestSelectManifest_NoManagerErrorMessageMentionsPM(t *testing.T) {
	_, err := selectManifest("/repo/deps", "darwin", platform.ManagerNone)
	if err == nil || !strings.Contains(err.Error(), "package manager") {
		t.Errorf("no-manager error should mention 'package manager', got %v", err)
	}
}

// --- apt install ------------------------------------------------------------

func TestInstallApt_InstallsListAndRecords(t *testing.T) {
	dir := t.TempDir()
	apt := filepath.Join(dir, "apt.txt")
	writeFile(t, apt, "zsh\nzoxide\ndirenv\n")
	m := Manifest{Manager: platform.ManagerApt, GOOS: "linux", Shared: apt}

	// All three absent before, all present after -> all three recorded.
	r := &aptStateRunner{
		before: map[string]string{},
		after: map[string]string{
			"zsh": "install ok installed", "zoxide": "install ok installed", "direnv": "install ok installed",
		},
	}
	res, err := install(m, r)
	if err != nil {
		t.Fatalf("install apt: %v", err)
	}
	if !r.installed {
		t.Errorf("apt install did not invoke apt-get install -y")
	}
	want := []string{"direnv", "zoxide", "zsh"}
	if !reflect.DeepEqual(res.RecordedInstalledSet(), want) {
		t.Errorf("apt RecordedInstalledSet: got %v want %v", res.RecordedInstalledSet(), want)
	}
}

// TestParseAptLines_RefusesArgumentInjection checks the trust-boundary fix: a
// repo-supplied apt.txt line that starts with "-" is an apt-get OPTION, not a
// package. parseAptLines must refuse the whole manifest with an error naming the
// offending line — otherwise `-oDPkg::Pre-Invoke::=touch /tmp/x` runs as root
// under `sudo ferry apply --deps`.
func TestParseAptLines_RefusesArgumentInjection(t *testing.T) {
	dir := t.TempDir()
	apt := filepath.Join(dir, "apt.txt")
	const evil = "-oDPkg::Pre-Invoke::=touch /tmp/x"
	writeFile(t, apt, "zsh\n"+evil+"\n")

	_, err := ParseManifest(apt, platform.ManagerApt)
	if err == nil {
		t.Fatalf("ParseManifest accepted an apt option-injection line; want refusal")
	}
	if !strings.Contains(err.Error(), evil) {
		t.Errorf("refusal error does not name the offending line: %v", err)
	}
}

// TestParseAptLines_RefusesDisallowedChars checks that entries carrying
// characters outside the apt name charset (spaces, "=", "/", ...) are refused,
// so a name cannot smuggle shell/option payloads even without a leading dash.
func TestParseAptLines_RefusesDisallowedChars(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"zsh htop", "pkg=1.0", "a/b", "$(touch x)"} {
		apt := filepath.Join(dir, "apt.txt")
		writeFile(t, apt, bad+"\n")
		if _, err := ParseManifest(apt, platform.ManagerApt); err == nil {
			t.Errorf("ParseManifest accepted disallowed entry %q; want refusal", bad)
		}
	}
}

// TestParseAptLines_RefusesRemoveModifier checks that a trailing "-" is refused:
// apt-get treats a trailing "-" on a positional package specifier as its REMOVE
// modifier, parsed during package resolution (not option parsing), so the `--`
// separator gives no protection. A repo-supplied line such as `ufw-` would run
// `apt-get install -y -- ufw-` and REMOVE ufw as root under `sudo ferry apply
// --deps` — the exact trust boundary this fix closes. A leading "~" or "." is
// likewise refused because apt would read it as a pattern/regex selector.
func TestParseAptLines_RefusesRemoveModifier(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"ufw-", "apparmor-", "passwd-", "~ndaemon", ".foo"} {
		apt := filepath.Join(dir, "apt.txt")
		writeFile(t, apt, bad+"\n")
		if _, err := ParseManifest(apt, platform.ManagerApt); err == nil {
			t.Errorf("ParseManifest accepted apt specifier-modifier entry %q; want refusal", bad)
		}
	}
}

// TestValidateAptRemoveName_RefusesInstallModifier checks the uninstall rail
// additionally refuses a trailing "+": apt-get treats it as the INSTALL modifier
// during package resolution (not option parsing), so `apt-get remove -y --
// openssh-server+` would INSTALL openssh-server as root. Every rule the install
// rail enforces (leading "-", trailing "-", bad charset) must also hold here.
func TestValidateAptRemoveName_RefusesInstallModifier(t *testing.T) {
	for _, bad := range []string{"openssh-server+", "evilpkg+", "g++", "ufw-", "-oX", "~ndaemon"} {
		if err := ValidateAptRemoveName(bad); err == nil {
			t.Errorf("ValidateAptRemoveName accepted %q; want refusal", bad)
		}
	}
}

// TestValidateAptRemoveName_AllowsPlainNames checks the legitimate uninstall path
// is preserved: plain names (including those with an interior "+") still pass.
func TestValidateAptRemoveName_AllowsPlainNames(t *testing.T) {
	for _, ok := range []string{"ripgrep", "python3.11", "libfoo-dev", "foo:amd64", "libstdc++6"} {
		if err := ValidateAptRemoveName(ok); err != nil {
			t.Errorf("ValidateAptRemoveName refused legitimate name %q: %v", ok, err)
		}
	}
}

// TestValidateAptName_AllowsTrailingInstallModifierOnInstallRail pins the
// remove-only nature of the "+" reject: `g++` (name ends in "+") MUST stay
// installable, so ValidateAptName — the install-rail validator — keeps it.
func TestValidateAptName_AllowsTrailingInstallModifierOnInstallRail(t *testing.T) {
	if err := ValidateAptName("g++"); err != nil {
		t.Errorf("ValidateAptName refused g++ on the install rail: %v", err)
	}
}

// TestParseAptLines_AllowsValidNames checks the legitimate path still parses:
// real package names, versioned/epoch forms, and inline comments survive.
func TestParseAptLines_AllowsValidNames(t *testing.T) {
	dir := t.TempDir()
	apt := filepath.Join(dir, "apt.txt")
	writeFile(t, apt, "zsh\ng++\nlibfoo-dev\npython3.11\nfoo:amd64 # arch\n")
	got, err := ParseManifest(apt, platform.ManagerApt)
	if err != nil {
		t.Fatalf("ParseManifest refused valid names: %v", err)
	}
	want := []string{"zsh", "g++", "libfoo-dev", "python3.11", "foo:amd64"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseManifest: got %v want %v", got, want)
	}
}

// TestInstallApt_ArgvHasEndOfOptionsSeparator checks the apt-get install argv
// carries `--` immediately before the package list, so any package name is
// treated as a package even if the name validation were ever bypassed.
func TestInstallApt_ArgvHasEndOfOptionsSeparator(t *testing.T) {
	dir := t.TempDir()
	apt := filepath.Join(dir, "apt.txt")
	writeFile(t, apt, "zsh\nzoxide\n")
	m := Manifest{Manager: platform.ManagerApt, GOOS: "linux", Shared: apt}

	r := &aptStateRunner{before: map[string]string{}, after: map[string]string{}}
	if _, err := install(m, r); err != nil {
		t.Fatalf("install apt: %v", err)
	}
	if !r.installed {
		t.Fatalf("apt-get install was not invoked")
	}
	// argv: apt-get install -y -- zsh zoxide
	want := []string{"apt-get", "install", "-y", "--", "zsh", "zoxide"}
	if !reflect.DeepEqual(r.installArgs, want) {
		t.Fatalf("apt-get install argv: got %v want %v", r.installArgs, want)
	}
	// `--` must come immediately BEFORE the first package.
	sep, first := -1, -1
	for i, a := range r.installArgs {
		if a == "--" && sep == -1 {
			sep = i
		}
		if a == "zsh" {
			first = i
		}
	}
	if sep == -1 || sep != first-1 {
		t.Errorf("`--` not immediately before the package list: sep=%d first=%d argv=%v", sep, first, r.installArgs)
	}
}

// --- capture re-dump targets ONLY the detected manager's file ---------------

func TestReDump_Brew_TargetsOnlyDetectedFile(t *testing.T) {
	dir := t.TempDir()
	depsDir := filepath.Join(dir, "deps")
	// darwin/brew manifest; the dump must target Brewfile.darwin and NOTHING else.
	m := Manifest{
		Manager: platform.ManagerBrew,
		GOOS:    "darwin",
		Shared:  filepath.Join(depsDir, "Brewfile.darwin"),
		Local:   filepath.Join(depsDir, "Brewfile.darwin.local"),
	}
	r := newFakeRunner()
	got, err := reDump(m, r)
	if err != nil {
		t.Fatalf("reDump brew: %v", err)
	}
	if got != m.Shared {
		t.Errorf("reDump returned %q want %q", got, m.Shared)
	}
	if !r.invoked("bundle dump") || !r.invoked("--file="+m.Shared) {
		t.Errorf("reDump did not `brew bundle dump --file=Brewfile.darwin`: %v", r.calls)
	}
	// It must NEVER touch another OS's file or the .local overlay.
	for _, forbidden := range []string{"Brewfile.linux", "apt.txt", "Brewfile.darwin.local"} {
		if r.invoked(forbidden) {
			t.Errorf("reDump touched %q — must target ONLY the detected manager's shared file", forbidden)
		}
	}
}

// TestReDump_Brew_RefusesSymlinkTarget: a SYMLINK at deps/Brewfile.<goos> must be
// refused — brew is NEVER invoked and the symlink's target is left byte-identical
// (the guard never writes through it, mimicking a repo Brewfile.darwin -> ~/.ssh/config).
func TestReDump_Brew_RefusesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	depsDir := filepath.Join(dir, "deps")
	if err := os.MkdirAll(depsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Sentinel stands in for ~/.ssh/config: a regular file OUTSIDE the deps tree
	// that the symlink points at. It must stay byte-identical.
	sentinel := filepath.Join(dir, "secret-config")
	const sentinelBody = "Host *\n  IdentityFile ~/.ssh/id_ed25519\n"
	if err := os.WriteFile(sentinel, []byte(sentinelBody), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(depsDir, "Brewfile.darwin")
	if err := os.Symlink(sentinel, target); err != nil {
		t.Fatal(err)
	}

	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: target}
	r := newFakeRunner()
	if _, err := reDump(m, r); err == nil {
		t.Fatal("reDump with symlink target: want refusal error, got nil")
	}
	if r.invoked("bundle dump") || r.invoked("dump") {
		t.Errorf("reDump invoked brew on a symlink target — must refuse before brew: %v", r.calls)
	}
	// The symlink target (sentinel) must be untouched.
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sentinelBody {
		t.Errorf("symlink target was modified: got %q want %q", got, sentinelBody)
	}
}

// TestReDump_Brew_AbsentTargetDumps: an absent (to-be-created) regular target
// re-dumps normally — the fake runner records the `brew bundle dump` call.
func TestReDump_Brew_AbsentTargetDumps(t *testing.T) {
	dir := t.TempDir()
	depsDir := filepath.Join(dir, "deps") // not yet created
	target := filepath.Join(depsDir, "Brewfile.darwin")
	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: target}
	r := newFakeRunner()
	got, err := reDump(m, r)
	if err != nil {
		t.Fatalf("reDump absent target: %v", err)
	}
	if got != target {
		t.Errorf("reDump returned %q want %q", got, target)
	}
	if !r.invoked("bundle dump") || !r.invoked("--file="+target) {
		t.Errorf("reDump did not invoke `brew bundle dump --file=%s`: %v", target, r.calls)
	}
}

func TestReDump_NoManager_Reports(t *testing.T) {
	m := Manifest{Manager: platform.ManagerNone}
	r := newFakeRunner()
	if _, err := reDump(m, r); !errors.Is(err, ErrNoPackageManager) {
		t.Errorf("reDump no manager: err=%v want ErrNoPackageManager", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("reDump no manager invoked runner %d times (must not bootstrap): %v", len(r.calls), r.calls)
	}
}

func TestReDump_AptUnsupported(t *testing.T) {
	m := Manifest{Manager: platform.ManagerApt, GOOS: "linux", Shared: "/repo/deps/apt.txt"}
	r := newFakeRunner()
	_, err := reDump(m, r)
	if err == nil {
		t.Fatal("reDump apt: want unsupported error, got nil")
	}
	if r.invoked("dump") {
		t.Error("reDump apt invoked a dump (apt has no clean dump)")
	}
}

func TestInstall_NilRunner(t *testing.T) {
	m := Manifest{Manager: platform.ManagerBrew, Shared: "/x/Brewfile.darwin"}
	if _, err := install(m, nil); err == nil {
		t.Error("install with nil runner: want error")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
