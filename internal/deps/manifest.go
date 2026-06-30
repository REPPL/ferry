package deps

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/REPPL/ferry/internal/platform"
)

// Manifest describes the dependency manifest that applies on THIS machine: the
// detected package manager, the shared manifest file, and its per-machine
// gitignored overlay (if present). It is selected by runtime.GOOS plus the
// detected manager — never assumed to be brew.
type Manifest struct {
	// Manager is the detected package manager this manifest targets.
	Manager platform.PackageManager
	// GOOS is the runtime.GOOS the manifest was selected for ("darwin"/"linux").
	GOOS string
	// Shared is the absolute path to the committed manifest file
	// (deps/Brewfile.<goos> or deps/apt.txt). It is the manifest's identity even
	// when the file does not yet exist on disk.
	Shared string
	// Local is the absolute path to the per-machine gitignored overlay
	// (deps/Brewfile.<goos>.local). Empty for apt (no overlay convention).
	Local string
}

// SelectManifest returns the manifest that applies on THIS machine: it chooses
// the file by the host runtime.GOOS and the DETECTED package manager.
//
//   - brew  -> deps/Brewfile.<goos> (+ deps/Brewfile.<goos>.local overlay)
//   - apt   -> deps/apt.txt (no overlay)
//   - none  -> error: no package manager present (the caller REPORTS this; it is
//     never a reason to bootstrap a manager)
//
// depsDir is the repo's deps/ directory. The returned paths are absolute under
// depsDir; their on-disk existence is the caller's concern (Install handles a
// missing manifest gracefully).
func SelectManifest(depsDir string) (Manifest, error) {
	return selectManifest(depsDir, runtime.GOOS, platform.DetectPackageManager())
}

// selectManifest is the testable core: GOOS and the detected manager are
// explicit so tests cover every (GOOS, manager) pair without a real machine.
func selectManifest(depsDir, goos string, mgr platform.PackageManager) (Manifest, error) {
	if depsDir == "" {
		return Manifest{}, fmt.Errorf("deps: empty deps directory")
	}
	switch mgr {
	case platform.ManagerBrew:
		base := "Brewfile." + goos
		return Manifest{
			Manager: mgr,
			GOOS:    goos,
			Shared:  filepath.Join(depsDir, base),
			Local:   filepath.Join(depsDir, base+".local"),
		}, nil
	case platform.ManagerApt:
		return Manifest{
			Manager: mgr,
			GOOS:    goos,
			Shared:  filepath.Join(depsDir, "apt.txt"),
			Local:   "", // apt has no per-machine overlay convention
		}, nil
	default:
		return Manifest{}, ErrNoPackageManager
	}
}

// Entries reads the manifest and returns its parsed entries, layering the
// per-machine .local overlay AFTER the shared file (local entries appended last,
// so apply --deps installs them last). A missing shared OR local file is not an
// error — it contributes no entries. The slice preserves source order.
func (m Manifest) Entries() ([]string, error) {
	entries, err := parseManifestFile(m.Shared, m.Manager)
	if err != nil {
		return nil, err
	}
	if m.Local != "" {
		local, err := parseManifestFile(m.Local, m.Manager)
		if err != nil {
			return nil, err
		}
		entries = append(entries, local...)
	}
	return entries, nil
}

// ParseManifest parses a single manifest file (no overlay layering) into its
// entries, interpreting it per the given manager's format. A non-existent file
// yields an empty slice and no error — an absent manifest is a valid empty one.
func ParseManifest(path string, mgr platform.PackageManager) ([]string, error) {
	return parseManifestFile(path, mgr)
}

func parseManifestFile(path string, mgr platform.PackageManager) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("deps: read manifest %s: %w", path, err)
	}
	switch mgr {
	case platform.ManagerApt:
		return parseAptLines(string(data)), nil
	default:
		// brew (and any future bundle-style manager): keep each meaningful
		// Brewfile line verbatim (brew/cask/tap/mas/font ...) so the entry set is
		// the real manifest content the installed-set diff is checked against.
		return parseBrewfileLines(string(data)), nil
	}
}

// parseBrewfileLines returns the non-blank, non-comment lines of a Brewfile,
// trimmed of surrounding whitespace. We do NOT decode the Ruby DSL — the entry
// is the directive line as written (e.g. `brew "zoxide"`, `cask "iterm2"`),
// which is exactly what we diff the installed set against and what brew bundle
// itself consumes.
func parseBrewfileLines(s string) []string {
	var out []string
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// parseAptLines returns the package names in an apt.txt: one package per line,
// blank lines and # comments ignored, inline trailing comments stripped.
func parseAptLines(s string) []string {
	var out []string
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
