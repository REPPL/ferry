package agents

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/dotfile"
)

// RepoSubdir is the config-repo subdirectory that holds the agents domain's
// sources: general.md, coding.md, templates/, skills/, agents/, hooks/.
const RepoSubdir = "agents"

// assetTrees are the repo-side asset directories the domain deploys
// recursively into the matching ~/.claude/<tree>/ destination. Each tree is
// optional; an absent one deploys nothing.
var assetTrees = []string{"skills", "agents", "hooks"}

// assetHomeRoot is the home-relative directory the asset trees deploy under.
const assetHomeRoot = ".claude"

// Item is one (content, target) pair the agents domain deploys: the planner's
// one-to-many expansion produces these, and the write path stays strictly 1:1.
type Item struct {
	// Key is the stable last-applied store key ("agents/<harness>",
	// "agents/devtree", or "agents/<tree>/<relpath>"). The "agents/" prefix
	// namespaces the domain inside the shared last-applied store.
	Key string
	// Label is the human-facing name reports print (e.g. "agents:claude").
	Label string
	// Target carries the validated $HOME destination (built via
	// dotfile.NestedTarget, so ~/.ssh and $HOME-escapes are impossible).
	// Target.Name == Key; Target.Repo is empty — Content is authoritative.
	Target dotfile.Target
	// Content is the exact bytes to materialise (already derived for
	// combined-source targets).
	Content []byte
	// Exec preserves the repo source's executable bit on a first-ever write
	// (hooks are scripts; instruction files are not).
	Exec bool
}

// PlanInput carries the planner's inputs. Guard validates a repo-side path
// before it is read (the caller passes its symlink-refusing repo guard) and
// returns the safe path; nil means no extra validation (tests).
type PlanInput struct {
	RepoRoot string
	Home     string
	Config   config.AgentsConfig
	Guard    func(candidate string) (string, error)
}

// Plan expands the agents domain into its 1:1 (content, target) items:
// one per in-scope harness (source rendered per the registry), the optional
// devtree workspace file, and one per asset file under agents/{skills,agents,
// hooks}. It reads only the config repo — never $HOME — and is deterministic:
// registry order, then devtree, then assets in lexical walk order.
//
// Recoverable per-target problems (a missing source file, a refused or
// escaping target path, a symlink inside an asset tree) become warnings and
// the item is skipped; only a config error (bad harness declaration) or an
// unexpected read failure aborts.
func Plan(in PlanInput) (items []Item, warnings []string, err error) {
	harnesses, err := Resolve(in.Config)
	if err != nil {
		return nil, nil, err
	}

	sources, sourceWarnings, err := loadSources(in)
	if err != nil {
		return nil, nil, err
	}
	warnings = append(warnings, sourceWarnings...)

	for _, h := range harnesses {
		content, ok := sources[h.Source]
		if !ok {
			continue // the missing source file already produced one warning
		}
		t, terr := dotfile.NestedTarget(in.Home, h.Target, "agents/"+h.Name)
		if terr != nil {
			warnings = append(warnings, refusal("harness "+h.Name, h.Target, terr))
			continue
		}
		items = append(items, Item{
			Key:     t.Name,
			Label:   "agents:" + h.Name,
			Target:  t,
			Content: content,
		})
	}

	// Optional devtree workspace layer: coding.md at the devtree ROOT (the
	// ancestor walk-up reads <dir>/CLAUDE.md, not <dir>/.claude/CLAUDE.md).
	if in.Config.Devtree != "" {
		if content, ok := sources[SourceCoding]; ok {
			rel := filepath.Join(in.Config.Devtree, "CLAUDE.md")
			t, terr := dotfile.NestedTarget(in.Home, rel, "agents/devtree")
			if terr != nil {
				warnings = append(warnings, refusal("devtree", rel, terr))
			} else {
				items = append(items, Item{
					Key:     t.Name,
					Label:   "agents:devtree",
					Target:  t,
					Content: content,
				})
			}
		}
	}

	assetItems, assetWarnings, err := planAssets(in)
	if err != nil {
		return nil, nil, err
	}
	items = append(items, assetItems...)
	warnings = append(warnings, assetWarnings...)

	return items, warnings, nil
}

// loadSources reads general.md and coding.md from the config repo and derives
// the combined content. Each rendered source appears in the returned map only
// when its inputs exist; a missing input produces ONE warning here so the
// per-harness loop can skip silently.
func loadSources(in PlanInput) (map[Source][]byte, []string, error) {
	var warnings []string
	read := func(name string) ([]byte, bool, error) {
		path := filepath.Join(in.RepoRoot, RepoSubdir, name)
		safe, gerr := guardPath(in.Guard, path)
		if gerr != nil {
			warnings = append(warnings, refusal("source", filepath.Join(RepoSubdir, name), gerr))
			return nil, false, nil
		}
		data, rerr := os.ReadFile(safe)
		if rerr != nil {
			if errors.Is(rerr, fs.ErrNotExist) {
				warnings = append(warnings, fmt.Sprintf(
					"agents: %s is missing from the config repo — targets that need it are skipped (create it, or run `ferry agents adopt <dir>` to import an existing set)",
					filepath.Join(RepoSubdir, name)))
				return nil, false, nil
			}
			return nil, false, rerr
		}
		return data, true, nil
	}

	general, haveGeneral, err := read("general.md")
	if err != nil {
		return nil, nil, err
	}
	coding, haveCoding, err := read("coding.md")
	if err != nil {
		return nil, nil, err
	}

	sources := map[Source][]byte{}
	if haveGeneral {
		sources[SourceGeneral] = general
	}
	if haveCoding {
		sources[SourceCoding] = coding
	}
	if haveGeneral && haveCoding {
		sources[SourceCombined] = RenderCombined(general, coding)
	}
	return sources, warnings, nil
}

// planAssets expands the asset trees (agents/{skills,agents,hooks} →
// ~/.claude/{skills,agents,hooks}) into per-file items, recursively, in
// lexical walk order. Symlinks anywhere in a tree are refused with a warning
// (managed content is copied, never symlinked); executable bits are recorded
// so hooks deploy runnable.
func planAssets(in PlanInput) (items []Item, warnings []string, err error) {
	for _, tree := range assetTrees {
		root := filepath.Join(in.RepoRoot, RepoSubdir, tree)
		safeRoot, gerr := guardPath(in.Guard, root)
		if gerr != nil {
			warnings = append(warnings, refusal("asset tree", filepath.Join(RepoSubdir, tree), gerr))
			continue
		}
		if fi, serr := os.Lstat(safeRoot); serr != nil || !fi.IsDir() {
			continue // an absent (or non-directory) tree deploys nothing
		}

		walkErr := filepath.WalkDir(safeRoot, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			rel, rerr := filepath.Rel(safeRoot, path)
			if rerr != nil {
				return rerr
			}
			if d.Type()&fs.ModeSymlink != 0 {
				warnings = append(warnings, fmt.Sprintf(
					"agents: refusing %s: symlink not allowed in the managed repo tree (copy the real file in)",
					filepath.Join(RepoSubdir, tree, rel)))
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			safe, gerr := guardPath(in.Guard, path)
			if gerr != nil {
				warnings = append(warnings, refusal("asset", filepath.Join(RepoSubdir, tree, rel), gerr))
				return nil
			}
			content, cerr := os.ReadFile(safe)
			if cerr != nil {
				return cerr
			}
			info, ierr := d.Info()
			if ierr != nil {
				return ierr
			}

			key := "agents/" + tree + "/" + filepath.ToSlash(rel)
			t, terr := dotfile.NestedTarget(in.Home, filepath.Join(assetHomeRoot, tree, rel), key)
			if terr != nil {
				warnings = append(warnings, refusal("asset", filepath.Join(RepoSubdir, tree, rel), terr))
				return nil
			}
			items = append(items, Item{
				Key:     key,
				Label:   "agents:" + tree + "/" + filepath.ToSlash(rel),
				Target:  t,
				Content: content,
				Exec:    info.Mode()&0o111 != 0,
			})
			return nil
		})
		if walkErr != nil {
			return nil, nil, walkErr
		}
	}
	return items, warnings, nil
}

// TargetPaths resolves the absolute $HOME destinations the agents domain
// manages for cfg, WITHOUT reading any source content — the scoped-restore
// path uses it to map the "agents" domain name onto the engine's per-file
// baselines. Asset destinations are derived from the files currently in the
// repo trees; a harness/devtree target that fails validation is skipped.
func TargetPaths(repoRoot, home string, cfg config.AgentsConfig) ([]string, error) {
	harnesses, err := Resolve(cfg)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, h := range harnesses {
		if t, terr := dotfile.NestedTarget(home, h.Target, "agents/"+h.Name); terr == nil {
			paths = append(paths, t.Home)
		}
	}
	if cfg.Devtree != "" {
		if t, terr := dotfile.NestedTarget(home, filepath.Join(cfg.Devtree, "CLAUDE.md"), "agents/devtree"); terr == nil {
			paths = append(paths, t.Home)
		}
	}
	for _, tree := range assetTrees {
		root := filepath.Join(repoRoot, RepoSubdir, tree)
		if fi, serr := os.Lstat(root); serr != nil || !fi.IsDir() {
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.Type()&fs.ModeSymlink != 0 {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			key := "agents/" + tree + "/" + filepath.ToSlash(rel)
			if t, terr := dotfile.NestedTarget(home, filepath.Join(assetHomeRoot, tree, rel), key); terr == nil {
				paths = append(paths, t.Home)
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	return paths, nil
}

// guardPath routes a repo-side candidate through the caller's guard (when
// provided), so every repo read in this package honours the same
// symlink-refusing policy as the rest of ferry.
func guardPath(guard func(string) (string, error), candidate string) (string, error) {
	if guard == nil {
		return candidate, nil
	}
	return guard(candidate)
}

// refusal renders a clear, user-facing warning for a skipped agents target,
// mirroring the dotfile domain's refusal wording.
func refusal(what, name string, err error) string {
	switch {
	case errors.Is(err, dotfile.ErrForbiddenSSHPath):
		return fmt.Sprintf("agents: refusing %s %s: ferry never manages paths under ~/.ssh", what, name)
	case errors.Is(err, dotfile.ErrPathEscapesHome):
		return fmt.Sprintf("agents: refusing %s %s: invalid managed path (escapes $HOME)", what, name)
	default:
		return fmt.Sprintf("agents: refusing %s %s: %v", what, name, err)
	}
}
