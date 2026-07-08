package deps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// npmBin is the npm executable name, resolved through PATH by the runner (npm is
// NOT a root rail — it runs as the invoking user, never under sudo — so it stays
// $PATH-resolved like brew, which also lets the eval harness shadow it with a stub).
const npmBin = "npm"

// npmGlobalsFileName is the committed, cross-machine list of globally-installed
// npm package NAMES (never versions/tree — a version-pinned list churns on every
// minor bump). It lives beside the Brewfile/apt manifests in the repo's deps/ dir.
const npmGlobalsFileName = "npm-globals.txt"

// NpmGlobalsFile returns the absolute path of the committed npm globals list
// under depsDir. Its on-disk existence is the caller's concern (an absent list is
// a valid empty one).
func NpmGlobalsFile(depsDir string) string {
	return filepath.Join(depsDir, npmGlobalsFileName)
}

// npmGlobalsQuery is the read-only listing that names the globally-installed
// packages. `--depth=0` keeps it to the top level (no dependency tree) and
// `--json` gives a stable, parseable shape; NAMES are taken from the top-level
// dependencies object and versions are discarded.
var npmGlobalsQuery = []string{"ls", "-g", "--json", "--depth=0"}

// npmLsOutput is the subset of `npm ls -g --json --depth=0` we read: the
// top-level dependencies object, keyed by package name. Versions are ignored on
// purpose (names-only carry).
type npmLsOutput struct {
	Dependencies map[string]json.RawMessage `json:"dependencies"`
}

// DumpNpmGlobals returns the sorted, de-duplicated NAMES of the globally-installed
// npm packages on THIS machine, excluding npm itself (npm is the manager, never a
// carried package). It runs the read-only `npm ls -g --json --depth=0` through the
// runner and reads names only — never versions. `npm ls` can exit non-zero when
// the global tree has peer-dependency warnings while still emitting valid JSON, so
// a parseable body is used even when the runner reports an error; only an
// unparseable body is a hard failure.
func DumpNpmGlobals(runner CommandRunner) ([]string, error) {
	if runner == nil {
		return nil, fmt.Errorf("deps: nil CommandRunner")
	}
	out, runErr := runner.Run(append([]string{npmBin}, npmGlobalsQuery...)...)
	var parsed npmLsOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("deps: npm ls -g: %w", runErr)
		}
		return nil, fmt.Errorf("deps: parse npm ls -g output: %w", err)
	}
	var names []string
	for name := range parsed.Dependencies {
		if name == "" || name == npmBin {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// ReDumpNpmGlobals re-dumps THIS machine's global npm package NAMES to
// deps/npm-globals.txt (deterministic, sorted, one name per line). It is the
// npm analogue of ReDumpManifest for brew: capture calls it. The target is
// symlink-guarded BEFORE the write (ferry only writes regular files under deps/,
// so a symlinked npm-globals.txt or a symlinked deps/ directory is refused, never
// written through). Returns the absolute path written.
func ReDumpNpmGlobals(depsDir string, runner CommandRunner) (string, error) {
	if depsDir == "" {
		return "", fmt.Errorf("deps: empty deps directory")
	}
	names, err := DumpNpmGlobals(runner)
	if err != nil {
		return "", err
	}
	target := NpmGlobalsFile(depsDir)
	if err := refuseSymlinkTarget(target); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("deps: create deps dir for %s: %w", target, err)
	}
	if err := os.WriteFile(target, []byte(renderNpmGlobals(names)), 0o644); err != nil {
		return "", fmt.Errorf("deps: write %s: %w", target, err)
	}
	return target, nil
}

// renderNpmGlobals renders names as a deterministic file body: one name per line,
// sorted (the caller sorts), with a trailing newline. An empty list yields an
// empty file, which reads back as an empty set.
func renderNpmGlobals(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, "\n") + "\n"
}

// ReadNpmGlobals reads and validates the committed npm globals list. A missing
// file is a valid empty set (not an error). Every surviving entry is validated as
// a plain npm package NAME (ValidateNpmName): the list comes from a cloned config
// repo and its entries reach `npm i -g` as arguments, so a spec such as a git URL,
// a leading "-" flag, or a local path is REFUSED before install — fail closed,
// mirroring the apt/Brewfile gates.
func ReadNpmGlobals(depsDir string) ([]string, error) {
	if depsDir == "" {
		return nil, fmt.Errorf("deps: empty deps directory")
	}
	target := NpmGlobalsFile(depsDir)
	// Symlink-guard the repo-side list BEFORE os.ReadFile, mirroring the Brewfile/
	// apt.txt guard: a symlinked npm-globals.txt (or symlinked deps/) is refused,
	// never read through.
	safe, err := safeRepoManifest(filepath.Dir(filepath.Dir(target)), target)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(safe)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("deps: read %s: %w", safe, err)
	}
	return parseNpmGlobals(string(data))
}

// parseNpmGlobals returns the validated, de-duplicated, sorted package names in an
// npm-globals.txt: one name per line, blank lines and # comments ignored. A name
// that fails ValidateNpmName refuses the WHOLE list (fail closed).
func parseNpmGlobals(s string) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := ValidateNpmName(line); err != nil {
			return nil, err
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	sort.Strings(out)
	return out, nil
}

// InstallNpmGlobals reconciles this machine's global npm packages to the committed
// list: `npm i -g -- <names…>`. It is INSTALL/RECONCILE-ONLY — it never uninstalls
// (mirroring the Homebrew cleanup-out decision), so packages present locally but
// not in the list are left alone. An empty list is a clean no-op. `--` ends npm's
// option parsing so a name can never be read as a flag (defence in depth on top of
// ValidateNpmName). Returns the reconciled names. All npm invocation goes through
// runner, so unit tests drive it with a stub — no real npm.
func InstallNpmGlobals(depsDir string, runner CommandRunner) ([]string, error) {
	if runner == nil {
		return nil, fmt.Errorf("deps: nil CommandRunner")
	}
	names, err := ReadNpmGlobals(depsDir)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}
	args := append([]string{npmBin, "i", "-g", "--"}, names...)
	if _, err := runner.Run(args...); err != nil {
		return nil, fmt.Errorf("deps: %s: %w", joinArgs(args), err)
	}
	return names, nil
}

// NpmGlobalsDrift reports how THIS machine's globally-installed npm packages
// differ from the committed list, WITHOUT installing anything. Added = installed
// live but not in the list (capture would record); Removed = in the list but not
// installed (apply --deps would install). Names-only, so a version bump never
// shows as drift. Presence of npm is the caller's gate (platform.HasNpm); this
// runs only the read-only listing through the runner.
func NpmGlobalsDrift(depsDir string, runner CommandRunner) (Drift, error) {
	listed, err := ReadNpmGlobals(depsDir)
	if err != nil {
		return Drift{}, err
	}
	live, err := DumpNpmGlobals(runner)
	if err != nil {
		return Drift{}, err
	}
	repoSet := toSet(listed)
	liveSet := toSet(live)
	return diffDrift(repoSet, liveSet), nil
}

// toSet builds a presence set from a name slice.
func toSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set
}

// ValidateNpmName refuses an npm-globals.txt entry that is not a plain package
// name. It is a trust boundary: npm-globals.txt comes from a cloned config repo
// and every entry reaches `npm i -g` as an argument, where `npm install` accepts
// far more than package names — git URLs, tarball URLs, `<folder>` local paths,
// and `@scope/name@version` specs — any of which can fetch and run arbitrary
// install-time code (npm lifecycle scripts). Only a bare registry package name is
// allowed:
//
//   - optionally scoped: a single leading "@scope/" segment;
//   - the (scope and) package part is lowercase-name-shaped: URL-safe characters
//     only (letters, digits, and - . _ ~), no version/tag ("@" mid-name), no path
//     or URL punctuation (":" "/" beyond the one scope separator), no leading "-"
//     (an npm flag) and no "..".
//
// This admits `typescript`, `@angular/cli`, `npm-check-updates`, while refusing
// `git+https://…`, `../evil`, `-g`, and `left-pad@1.0.0`.
func ValidateNpmName(name string) error {
	if name == "" {
		return fmt.Errorf("deps: refusing empty npm globals entry")
	}
	// npm's own arg parser (npm-package-arg) classifies ANY spec ending in
	// `.tgz`/`.tar`/`.tar.gz` (case-insensitive) as a local FILE spec — not a
	// registry package — WITHOUT needing a path separator or URL punctuation. The
	// charset check below permits "." and so would let `foo.tgz` through; `npm i -g
	// foo.tgz` then resolves it as a tarball relative to CWD and runs its lifecycle
	// scripts. Refuse the filename shape up front (mirrors npm's isFilename), so a
	// cloned repo's list can never smuggle a tarball spec past the "registry names
	// only" guarantee.
	if npmFilenameRe.MatchString(name) {
		return fmt.Errorf("deps: refusing npm globals entry %q: names ending in .tgz/.tar/.tar.gz are local tarball specs npm would run install-time code from, not registry package names", name)
	}
	scope, pkg := name, ""
	if strings.HasPrefix(name, "@") {
		slash := strings.IndexByte(name, '/')
		if slash < 0 {
			return fmt.Errorf("deps: refusing npm globals entry %q: a scoped name must be `@scope/name`", name)
		}
		scope = name[1:slash] // the part after "@", before "/"
		pkg = name[slash+1:]
		if pkg == "" || strings.Contains(pkg, "/") {
			return fmt.Errorf("deps: refusing npm globals entry %q: a scoped name must be exactly `@scope/name`", name)
		}
		if !isNpmSegmentShaped(scope) {
			return fmt.Errorf("deps: refusing npm globals entry %q: the scope %q is not a plain name", name, scope)
		}
		if !isNpmSegmentShaped(pkg) {
			return fmt.Errorf("deps: refusing npm globals entry %q: the package part %q is not a plain name", name, pkg)
		}
		return nil
	}
	if !isNpmSegmentShaped(name) {
		return fmt.Errorf("deps: refusing npm globals entry %q: not a plain npm package name (a URL, version tag, path, or flag is not allowed)", name)
	}
	return nil
}

// npmFilenameRe matches npm's local-tarball filename heuristic (npm-package-arg's
// isFilename): a trailing `.tgz`, `.tar`, or `.tar.gz`, case-insensitive. A name
// matching it is a FILE spec to `npm install`, never a registry package.
var npmFilenameRe = regexp.MustCompile(`(?i)\.(tgz|tar\.gz|tar)$`)

// isNpmSegmentShaped reports whether seg is a single npm name segment: it starts
// with an ASCII letter or digit and otherwise contains only npm's URL-safe name
// characters (letters, digits, and "-", ".", "_", "~"), with no "..". A leading
// "-" (an npm flag), "@" (a version/tag), ":" or "/" (a URL/path), or any other
// punctuation is refused.
func isNpmSegmentShaped(seg string) bool {
	if seg == "" {
		return false
	}
	if first := seg[0]; !(first >= 'a' && first <= 'z' || first >= 'A' && first <= 'Z' || first >= '0' && first <= '9') {
		return false
	}
	if strings.Contains(seg, "..") {
		return false
	}
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '.' || r == '_' || r == '~':
		default:
			return false
		}
	}
	return true
}
