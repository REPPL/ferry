package deps

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/platform"
)

// Drift is a READ-ONLY comparison between a repo dependency manifest (the
// desired state) and the packages actually installed on THIS machine (the live
// state). It is names/identity-grained, never version-grained: two entries for
// the same package with different options/versions are the SAME identity, so a
// benign option or version bump never shows as drift.
//
//   - Added   — installed live but absent from the repo manifest. `ferry capture`
//     would record these.
//   - Removed — declared in the repo manifest but not installed live.
//     `ferry apply --deps` would install these.
type Drift struct {
	Added   []string
	Removed []string
}

// Empty reports whether the machine matches the repo manifest (no drift).
func (d Drift) Empty() bool { return len(d.Added) == 0 && len(d.Removed) == 0 }

// BrewDrift reports how THIS machine's installed Homebrew set differs from the
// repo Brewfile (shared + per-machine overlay), WITHOUT installing anything and
// WITHOUT rewriting the repo Brewfile. The live set is read through a read-only
// `brew bundle dump --file=-` (stdout, creates no file); the repo set is parsed
// through the SAME allow-list gate `apply --deps` uses.
//
// The returned bool is whether Homebrew drift is SUPPORTED on this machine: it
// is false (with a nil error) when no package manager is present or the detected
// manager is not brew (apt has no clean installed-set dump — `ferry status`
// reports it as n/a rather than guessing). depsDir is the repo's deps/ directory.
func BrewDrift(depsDir string, runner CommandRunner) (Drift, bool, error) {
	m, err := SelectManifest(depsDir)
	if err != nil {
		if errors.Is(err, ErrNoPackageManager) {
			return Drift{}, false, nil
		}
		return Drift{}, false, err
	}
	return brewDrift(m, runner)
}

// brewDrift is the testable core: the manifest is pre-selected so a unit test
// drives it with a fake runner and a fixed live-dump reply — no real brew.
func brewDrift(m Manifest, runner CommandRunner) (Drift, bool, error) {
	if m.Manager != platform.ManagerBrew {
		// apt/none: no clean installed-set dump, so drift is unsupported (not empty).
		return Drift{}, false, nil
	}
	if runner == nil {
		return Drift{}, false, fmt.Errorf("deps: nil CommandRunner")
	}

	// Repo (desired) set: parsed + allow-list gated exactly as the install path.
	repoEntries, err := m.Entries()
	if err != nil {
		return Drift{}, true, err
	}
	repoKeys := brewKeySet(repoEntries)

	// Live set: a read-only dump to stdout. `--file=-` pipes the Brewfile to
	// stdout and creates NO file, so status never writes the repo Brewfile (that
	// is capture's job). The dump is brew's OWN output — never handed back to
	// `brew bundle` — so it is parsed LENIENTLY (any non-directive line, e.g.
	// Homebrew's auto-update banner mixed into combined output, is ignored)
	// rather than through the fail-closed install allow-list.
	out, err := runner.Run(brewBin, "bundle", "dump", "--file=-")
	if err != nil {
		return Drift{}, true, fmt.Errorf("deps: brew bundle dump --file=-: %w", err)
	}
	liveKeys := brewKeySetLenient(out)

	return diffDrift(repoKeys, liveKeys), true, nil
}

// brewKeySet builds the identity set of already-parsed, allow-list-validated
// Brewfile directive lines. Each key is the package IDENTITY (directive keyword
// + name), so option/version differences on the same package are one key.
func brewKeySet(entries []string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, e := range entries {
		if key, ok := brewEntryKey(e); ok {
			set[key] = struct{}{}
		}
	}
	return set
}

// brewKeySetLenient builds the identity set from raw `brew bundle dump` output,
// keeping ONLY lines whose first token is an allowed Brewfile directive and
// silently dropping everything else (blank lines, comments, and any banner/warning
// text brew may interleave). Safe because this text is never executed.
func brewKeySetLenient(dump string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, raw := range strings.Split(dump, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, ok := brewEntryKey(line); ok {
			set[key] = struct{}{}
		}
	}
	return set
}

// brewEntryKey reduces one Brewfile directive line to its package IDENTITY key
// ("<keyword> <name>", e.g. `brew ripgrep`, `cask iterm2`, `mas Xcode`). It
// returns ok=false for a line whose first token is not an allowed directive, so
// unknown/noise lines are ignored by the drift comparison. Options (link:,
// id:, restart_service:) are deliberately NOT part of the key — comparing on
// identity keeps a benign option change from reading as drift.
func brewEntryKey(line string) (string, bool) {
	kw, rest := splitFirstField(line)
	if !allowedBrewDirectives[kw] {
		return "", false
	}
	name, _, ok := firstQuotedArg(rest)
	if !ok {
		return "", false
	}
	return kw + " " + name, true
}

// diffDrift computes the two-way difference between a repo (desired) key set and
// a live (installed) key set, sorted for a stable report.
func diffDrift(repo, live map[string]struct{}) Drift {
	var d Drift
	for k := range live {
		if _, ok := repo[k]; !ok {
			d.Added = append(d.Added, k)
		}
	}
	for k := range repo {
		if _, ok := live[k]; !ok {
			d.Removed = append(d.Removed, k)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	return d
}
