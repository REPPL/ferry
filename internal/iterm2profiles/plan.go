package iterm2profiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/dotfile"
)

// RepoSubdir is the config-repo subdirectory holding the Dynamic Profiles source
// (a tree of *.json files). It sits beside the global-plist file the iTerm2
// ResourceDomain reads (iterm2/com.googlecode.iterm2.plist).
var RepoSubdir = filepath.Join("iterm2", "DynamicProfiles")

// LocalName is the per-machine overlay directory under the repo's gitignored
// local/ root: local/iterm2-profiles/<rel> overrides (or, when present only there,
// adds) a Dynamic Profile for this machine — the natural home for a child profile
// referencing a shared parent's GUID.
const LocalName = "iterm2-profiles"

// LocalSubdir is the gitignored per-machine overlay root inside the repo.
const LocalSubdir = "local"

// TargetHome is the home-relative destination the profiles deploy to. It is inside
// $HOME, so dotfile.NestedTarget's containment guard passes it.
var TargetHome = filepath.Join("Library", "Application Support", "iTerm2", "DynamicProfiles")

// KeyPrefix namespaces the domain's records in the shared last-applied store, so
// the de-scope pass can tell them apart from dotfiles/agents/terminals/emacs/
// keybindings targets.
const KeyPrefix = "iterm2-profiles/"

// Item is one (content, target) pair the domain deploys — one per carried *.json
// file. It mirrors emacs.Item/termcfg.Item so the command layer's per-target
// planning is identical.
type Item struct {
	// Key is the stable last-applied store key ("iterm2-profiles/<relpath>").
	Key string
	// Label is the human-facing name reports print ("iterm2-profiles:<relpath>").
	Label string
	// Target is the validated $HOME destination (built via dotfile.NestedTarget, so
	// ~/.ssh and $HOME-escapes are impossible).
	Target dotfile.Target
	// Content is the exact bytes to materialise (the per-machine overlay when one
	// exists, else the shared repo source). GUIDs are byte-preserved: ferry never
	// mutates the JSON.
	Content []byte
}

// PlanInput carries the planner's inputs. Guard validates a repo-side path before
// it is read (the caller passes its symlink-refusing repo guard) and returns the
// safe path; nil means no extra validation (tests). Linter validates JSON format on
// macOS; nil skips the plutil check (tests / non-macOS).
type PlanInput struct {
	RepoRoot string
	Home     string
	Guard    func(candidate string) (string, error)
	Linter   Linter
}

// Plan expands the Dynamic Profiles domain into its 1:1 (content, target) items:
// one per carried *.json file, destined for the same relative path under
// ~/Library/Application Support/iTerm2/DynamicProfiles/ and carried like a dotfile.
// Content is the per-machine overlay (local/iterm2-profiles/<rel>) when present —
// local wins — else the shared repo source; a file present ONLY under the local
// overlay deploys as a machine-only profile. Each file is validated (JSON validity
// on every platform, plus `plutil -lint` on macOS) before it is carried; a
// malformed file becomes a warning and is skipped, never deployed (one bad file
// disables ALL of iTerm2's dynamic profiles). It reads only the config repo, never
// $HOME.
func Plan(in PlanInput) (items []Item, warnings []string, err error) {
	sources, warns, err := resolveSources(in)
	warnings = append(warnings, warns...)
	if err != nil {
		return nil, warnings, err
	}

	rels := make([]string, 0, len(sources))
	for rel := range sources {
		rels = append(rels, rel)
	}
	sort.Strings(rels) // deterministic plan order

	for _, rel := range rels {
		src := sources[rel]
		safe, gerr := guardPath(in.Guard, src)
		if gerr != nil {
			warnings = append(warnings, refusal("source", filepath.ToSlash(rel), gerr))
			continue
		}
		data, rerr := os.ReadFile(safe)
		if rerr != nil {
			return nil, warnings, rerr
		}
		if w := validateJSON(data, in.Linter, safe, rel); w != "" {
			warnings = append(warnings, w)
			continue
		}
		key := KeyPrefix + filepath.ToSlash(rel)
		t, terr := dotfile.NestedTarget(in.Home, filepath.Join(TargetHome, rel), key)
		if terr != nil {
			warnings = append(warnings, refusal("target", filepath.ToSlash(rel), terr))
			continue
		}
		items = append(items, Item{
			Key:     key,
			Label:   "iterm2-profiles:" + filepath.ToSlash(rel),
			Target:  t,
			Content: data,
		})
	}
	return items, warnings, nil
}

// resolveSources returns the union of *.json profile relpaths to deploy, each
// mapped to its winning source path: the shared iterm2/DynamicProfiles/<rel> unless
// a local/iterm2-profiles/<rel> overlay exists (local wins), plus any file present
// ONLY under the local overlay (a machine-only profile). Symlinks in either tree
// are refused with a warning and skipped.
func resolveSources(in PlanInput) (map[string]string, []string, error) {
	var warnings []string
	sources := map[string]string{}

	sharedRoot := filepath.Join(in.RepoRoot, RepoSubdir)
	localRoot := filepath.Join(in.RepoRoot, LocalSubdir, LocalName)

	// Shared tree: the authoritative set of profiles.
	sw, err := walkJSON(in.Guard, sharedRoot, RepoSubdir)
	if err != nil {
		return nil, sw.warnings, err
	}
	warnings = append(warnings, sw.warnings...)
	for rel := range sw.files {
		sources[rel] = filepath.Join(sharedRoot, rel)
	}

	// Local overlay: overrides a shared rel, or adds a machine-only profile.
	lw, err := walkJSON(in.Guard, localRoot, filepath.Join(LocalSubdir, LocalName))
	if err != nil {
		return nil, warnings, err
	}
	warnings = append(warnings, lw.warnings...)
	for rel := range lw.files {
		sources[rel] = filepath.Join(localRoot, rel)
	}
	return sources, warnings, nil
}

type walkResult struct {
	files    map[string]bool
	warnings []string
}

// walkJSON enumerates the *.json regular files under root (relative paths, slash
// form), refusing symlinks. An absent root is normal (nothing to deploy). label is
// the repo-relative directory name used in warnings.
func walkJSON(guard func(string) (string, error), root, label string) (walkResult, error) {
	res := walkResult{files: map[string]bool{}}
	safeRoot, gerr := guardPath(guard, root)
	if gerr != nil {
		res.warnings = append(res.warnings, refusal("source", label, gerr))
		return res, nil
	}
	fi, serr := os.Lstat(safeRoot)
	if serr != nil {
		if errors.Is(serr, fs.ErrNotExist) {
			return res, nil // absent tree: nothing to deploy
		}
		return res, serr
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		res.warnings = append(res.warnings, fmt.Sprintf(
			"iterm2-profiles: refusing %s: symlink not allowed in the managed repo tree (copy the real dir in)", label))
		return res, nil
	}
	if !fi.IsDir() {
		return res, nil
	}
	walkErr := filepath.WalkDir(safeRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(safeRoot, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			res.warnings = append(res.warnings, fmt.Sprintf(
				"iterm2-profiles: refusing %s: symlink not allowed in the managed repo tree (copy the real file in)",
				filepath.ToSlash(filepath.Join(label, rel))))
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(rel), ".json") {
			return nil // only *.json files are Dynamic Profiles
		}
		res.files[rel] = true
		return nil
	})
	if walkErr != nil {
		return res, walkErr
	}
	return res, nil
}

// validateJSON enforces the domain's format hygiene, returning a user-facing
// warning when the file is not valid (empty means it passed). The pure-Go
// encoding/json validity check runs on EVERY platform (so a malformed profile is
// refused off macOS too); `plutil -lint` runs only on macOS (a nil or ErrNotDarwin
// lint result passes).
func validateJSON(data []byte, linter Linter, path, rel string) string {
	if !json.Valid(data) {
		return fmt.Sprintf(
			"iterm2-profiles: refusing %s: not valid JSON — one malformed Dynamic Profile disables ALL of iTerm2's dynamic profiles; fix it, then re-run `ferry apply`",
			filepath.ToSlash(rel))
	}
	if linter != nil {
		if err := linter.Lint(path); err != nil && !errors.Is(err, ErrNotDarwin) {
			return fmt.Sprintf(
				"iterm2-profiles: refusing %s: property-list lint failed (%v) — fix the JSON, then re-run `ferry apply`",
				filepath.ToSlash(rel), err)
		}
	}
	return ""
}

// guardPath routes a repo-side candidate through the caller's guard (when
// provided), so every repo read in this package honours the same symlink-refusing
// policy as the rest of ferry.
func guardPath(guard func(string) (string, error), candidate string) (string, error) {
	if guard == nil {
		return candidate, nil
	}
	return guard(candidate)
}

// refusal renders a clear, user-facing warning for a skipped target, mirroring the
// emacs, termcfg, and keybindings domains' refusal wording.
func refusal(what, name string, err error) string {
	switch {
	case errors.Is(err, dotfile.ErrForbiddenSSHPath):
		return fmt.Sprintf("iterm2-profiles: refusing %s %s: ferry never manages paths under ~/.ssh", what, name)
	case errors.Is(err, dotfile.ErrPathEscapesHome):
		return fmt.Sprintf("iterm2-profiles: refusing %s %s: invalid managed path (escapes $HOME)", what, name)
	default:
		return fmt.Sprintf("iterm2-profiles: refusing %s %s: %v", what, name, err)
	}
}
