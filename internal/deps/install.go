package deps

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/platform"
)

// ErrNoPackageManager is returned when no package manager is present. Callers
// REPORT this to the user; it is NEVER a trigger to install/bootstrap a manager.
var ErrNoPackageManager = errors.New("no package manager present")

// Manager binary names, resolved through PATH by the runner. brew is the same
// binary on macOS and Linuxbrew; apt installs go through apt-get (scriptable,
// no TTY assumptions), which is what we record/diff against.
const (
	brewBin      = "brew"
	aptGetBin    = "apt-get"
	aptListBin   = "apt"
	dpkgQueryBin = "dpkg-query"
)

// InstallResult is the outcome of a gated Install: the manifest that was
// selected and the set of packages ferry OBSERVED as newly installed by this
// run (installed-set after minus before). The recorded set is what
// restore --packages later uses to uninstall ONLY ferry-installed packages.
type InstallResult struct {
	// Manifest is the manifest that applied on this machine.
	Manifest Manifest
	// Installed is the sorted set of packages that appeared between the
	// before/after snapshots — i.e. packages ferry's run installed. Empty when
	// the diff was unavailable or nothing changed.
	Installed []string
}

// RecordedInstalledSet exposes the packages ferry installed this run, sorted.
// restore --packages (Wave 2) uninstalls ONLY these; the actual uninstall is
// restore's job. A nil/empty result means "nothing recorded as installed".
func (r InstallResult) RecordedInstalledSet() []string { return r.Installed }

// Install runs the GATED dependency install for THIS machine. It is invoked
// ONLY by the apply --deps path — default unattended apply never calls it.
//
// Behaviour:
//   - Selects the manifest by GOOS + the detected manager (SelectManifest).
//   - With NO manager present, returns ErrNoPackageManager and does NOTHING —
//     it never installs/bootstraps a package manager.
//   - brew: snapshots the installed set, runs `brew bundle --file=<shared>`,
//     then the .local overlay (layered last), then re-snapshots and records the
//     diff so restore --packages can later uninstall only those.
//   - apt: runs `apt-get install -y <packages>` for the apt.txt list.
//
// All manager invocations go through runner, so the eval harness and unit tests
// drive it with a stub. depsDir is the repo's deps/ directory.
func Install(depsDir string, runner CommandRunner) (InstallResult, error) {
	m, err := SelectManifest(depsDir)
	if err != nil {
		// ErrNoPackageManager flows through unchanged: the caller reports it.
		return InstallResult{}, err
	}
	return install(m, runner)
}

// install is the testable core: the manifest is already selected, so tests can
// exercise each manager + the layering without a real machine.
func install(m Manifest, runner CommandRunner) (InstallResult, error) {
	if runner == nil {
		return InstallResult{}, fmt.Errorf("deps: nil CommandRunner")
	}
	switch m.Manager {
	case platform.ManagerBrew:
		return installBrew(m, runner)
	case platform.ManagerApt:
		return installApt(m, runner)
	default:
		return InstallResult{}, ErrNoPackageManager
	}
}

// installBrew drives `brew bundle` for the shared Brewfile then the .local
// overlay, diffing the installed set before/after to record what ferry added.
//
// Recording is FAIL-CLOSED: if either the before or after snapshot is
// unavailable, no positive installed-set is computed from it. Recording a
// package ferry did not truly install would let a later restore --packages
// uninstall something the user already had, so on an unreliable snapshot we
// record nothing (Installed left empty) rather than over-record.
func installBrew(m Manifest, runner CommandRunner) (InstallResult, error) {
	res := InstallResult{Manifest: m}

	before, beforeOK := brewInstalledSet(runner)

	// Resolve+guard the Brewfiles BEFORE the install runs: a symlinked manifest is
	// refused here, so brew is never invoked on it.
	bundleFiles, err := m.bundleFiles()
	if err != nil {
		return res, err
	}

	// Gate every Brewfile through the allow-list (ValidateBrewfileDirective, via
	// ParseManifest) BEFORE any `brew bundle` runs. A cloned/wired config repo's
	// Brewfile is UNTRUSTED input and `brew bundle --file=` instance_evals it as
	// Ruby, so an unvetted directive (arbitrary Ruby, a postinstall: block, a
	// custom-tap URL) is root-of-user RCE. ALL files are validated up front so a
	// hostile .local overlay can never execute after the shared file's packages
	// were already installed: one bad directive aborts the whole install before
	// brew is invoked at all.
	for _, file := range bundleFiles {
		if _, err := ParseManifest(file, platform.ManagerBrew); err != nil {
			return res, err
		}
	}

	// Shared manifest first, then the per-machine overlay LAST (local wins / is
	// layered after shared). brew bundle is idempotent: already-present formulae
	// are left alone, so re-running is safe.
	for _, file := range bundleFiles {
		if _, err := runner.Run(brewBin, "bundle", "--file="+file); err != nil {
			return res, fmt.Errorf("deps: brew bundle --file=%s: %w", file, err)
		}
	}

	after, afterOK := brewInstalledSet(runner)
	// Only record a diff when BOTH snapshots are trustworthy. A failed before
	// snapshot would otherwise make pre-existing packages look newly installed.
	if beforeOK && afterOK {
		res.Installed = diffSets(before, after)
	}
	return res, nil
}

// bundleFiles returns the Brewfiles to bundle, shared then .local overlay. Only
// files that EXIST contribute: an absent shared Brewfile or overlay is skipped
// rather than handed to `brew bundle` (which would fail on a missing file). The
// shared file is layered before the per-machine overlay.
//
// Each repo-side Brewfile is symlink-guarded BEFORE it is os.Stat'd or handed to
// `brew bundle --file`: a symlinked manifest (e.g. deps/Brewfile.darwin ->
// ~/.ssh/config) OR a symlinked deps/ directory (deps -> ~/.ssh) is refused, so
// brew never reads through it. The guard walks from the REPO ROOT (deps/'s parent)
// so the deps component itself is Lstat'd. A refused file contributes nothing (it
// is skipped like an absent one) — the install proceeds with the safe files;
// install() surfaces the refusal via err.
func (m Manifest) bundleFiles() ([]string, error) {
	var files []string
	for _, f := range []string{m.Shared, m.Local} {
		if f == "" {
			continue
		}
		// repoRoot = <repo> (deps/'s parent): walking from here Lstats the deps
		// component, catching a symlinked deps/ directory, not just a symlinked file.
		safe, err := safeRepoManifest(filepath.Dir(filepath.Dir(f)), f)
		if err != nil {
			return nil, err
		}
		if fileExists(safe) {
			files = append(files, safe)
		}
	}
	return files, nil
}

// brewInstalledSet returns the set of currently installed brew leaves+casks and
// whether the snapshot is RELIABLE. ok is false if ANY `brew list` invocation
// failed: a partial snapshot must never be diffed, because a missing entry in
// the before set makes a pre-existing package look newly installed. The caller
// records nothing on an unreliable snapshot (fail closed); the install itself
// still proceeds — only the restore-record is suppressed.
func brewInstalledSet(runner CommandRunner) (set map[string]struct{}, ok bool) {
	set = map[string]struct{}{}
	for _, args := range [][]string{
		{brewBin, "list", "--formula"},
		{brewBin, "list", "--cask"},
	} {
		out, err := runner.Run(args...)
		if err != nil {
			return nil, false
		}
		for _, name := range strings.Fields(out) {
			set[name] = struct{}{}
		}
	}
	return set, true
}

// installApt installs the apt.txt package list via apt-get. Recording is
// FAIL-CLOSED: it records ONLY packages that went from absent→present across
// this install. A package the user already had installed must never be
// recorded, or a later restore --packages would uninstall it. We snapshot each
// requested package's installed state with dpkg-query before and after; if
// EITHER snapshot is unreliable, we record nothing rather than over-record the
// requested list.
func installApt(m Manifest, runner CommandRunner) (InstallResult, error) {
	res := InstallResult{Manifest: m}
	pkgs, err := m.Entries()
	if err != nil {
		return res, err
	}
	if len(pkgs) == 0 {
		return res, nil
	}

	before, beforeOK := aptInstalledSet(runner, pkgs)

	// `--` ends apt-get option parsing: everything after it is treated as a
	// package name, never an option. Combined with parseAptLines rejecting any
	// entry that starts with "-", this stops a repo-supplied apt.txt line such as
	// `-oDPkg::Pre-Invoke::=touch /tmp/x` from being read as an apt option and
	// executed as root under `sudo ferry apply --deps`.
	args := append([]string{aptGetBin, "install", "-y", "--"}, pkgs...)
	if _, err := runner.Run(args...); err != nil {
		return res, fmt.Errorf("deps: %s: %w", joinArgs(args), err)
	}

	after, afterOK := aptInstalledSet(runner, pkgs)
	if beforeOK && afterOK {
		res.Installed = diffSets(before, after)
	}
	return res, nil
}

// aptInstalledSet reports which of the given packages are currently installed,
// and whether the query was RELIABLE. We query each package with
// `dpkg-query -W -f=${Status} <pkg>`: a "install ok installed" status means
// present. dpkg-query exits non-zero for a not-installed package, which is the
// normal absent signal — NOT a query failure — so absence is read from the
// status text, and only an unexpected empty status with no error is treated as
// present-unknown. ok is false only when we cannot tell (kept conservative so a
// genuine tool failure suppresses recording rather than over-recording).
func aptInstalledSet(runner CommandRunner, pkgs []string) (set map[string]struct{}, ok bool) {
	set = map[string]struct{}{}
	for _, pkg := range pkgs {
		// `--` ends dpkg-query option parsing (F19, defense-parity with the install
		// rail): a package name can never be read as a dpkg-query option even before
		// ValidateAptName runs. dpkg-query is resolved through the sanitized root rail
		// (see runner.ExecRunner.Run) since this probe runs under `sudo ferry`.
		out, err := runner.Run(dpkgQueryBin, "-W", "-f=${Status}", "--", pkg)
		if err != nil {
			// Not-installed packages make dpkg-query exit non-zero; treat that as
			// "absent" only when the output confirms it (empty / no "installed").
			// Any other error text we cannot interpret means an unreliable probe.
			if strings.TrimSpace(out) == "" || !strings.Contains(out, "installed") {
				continue
			}
			return nil, false
		}
		if strings.Contains(out, "install ok installed") {
			set[pkg] = struct{}{}
		}
	}
	return set, true
}

// diffSets returns the elements in after but not before, sorted — the packages
// this run installed.
func diffSets(before, after map[string]struct{}) []string {
	var added []string
	for name := range after {
		if _, had := before[name]; !had {
			added = append(added, name)
		}
	}
	sort.Strings(added)
	return added
}
