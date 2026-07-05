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
// sources: general.md, coding.md, templates/, and the asset-mapping source
// directories (skills/, agents/, hooks/, and any user-declared ones).
const RepoSubdir = "agents"

// TargetSpec is one enumerated destination the agents domain manages: a
// stable store key, a display label, the home-relative destination, and where
// its content comes from (a rendered instruction Source, or a repo asset
// file). It carries NO content and NO resolved $HOME path — it is the SINGLE
// enumeration every consumer shares: Plan attaches content and builds
// validated targets, and FindBridges derives its candidate paths from it, so
// the consumers can never disagree about what the domain manages.
type TargetSpec struct {
	Key      string
	Label    string
	Rel      string // home-relative destination
	Source   Source // instruction targets: which rendered source; "" for assets
	RepoFile string // asset targets: absolute (guard-validated) repo source; "" otherwise
	Exec     bool   // asset targets: the repo source carries an executable bit
}

// enumerateSpecs resolves cfg into the domain's full, ordered destination
// list: one spec per resolved harness, the optional devtree workspace file,
// then one per asset file under each resolved asset mapping's source tree in
// lexical walk order. Repo-side asset probing is routed through guard (nil =
// no extra validation); a symlink inside an asset tree is refused with a
// warning. Only a config error (bad harness/asset declaration) or an
// unexpected filesystem failure aborts.
func enumerateSpecs(repoRoot string, cfg config.AgentsConfig, guard func(string) (string, error)) (specs []TargetSpec, warnings []string, err error) {
	specs, err = instructionSpecs(cfg)
	if err != nil {
		return nil, nil, err
	}
	assets, warnings, err := assetSpecs(repoRoot, cfg, guard)
	if err != nil {
		return nil, nil, err
	}
	specs = append(specs, assets...)
	if err := validateSpecCollisions(specs); err != nil {
		return nil, nil, err
	}
	return specs, warnings, nil
}

// validateSpecCollisions refuses a plan in which two enumerated targets share
// a store key or a destination path. Both are silent-corruption hazards, not
// warnings: a duplicated key (e.g. a user harness literally named "devtree"
// next to a configured devtree) makes two targets fight over one last-applied
// record, and a duplicated destination (e.g. devtree = ".claude" colliding
// with the claude harness's ~/.claude/CLAUDE.md) makes the second write
// clobber the first every apply. The error names both colliding parties.
func validateSpecCollisions(specs []TargetSpec) error {
	byKey := map[string]string{}
	byRel := map[string]string{}
	for _, s := range specs {
		if prev, ok := byKey[s.Key]; ok {
			return fmt.Errorf("agents: %s and %s collide on the store key %q — rename one of them", prev, s.Label, s.Key)
		}
		byKey[s.Key] = s.Label
		rel := filepath.Clean(s.Rel)
		if prev, ok := byRel[rel]; ok {
			return fmt.Errorf("agents: %s and %s resolve to the same destination ~/%s — change one target (or the devtree)", prev, s.Label, rel)
		}
		byRel[rel] = s.Label
	}

	// File-vs-directory prefix collision: one target is a file at a path that is
	// an ANCESTOR directory of another target (e.g. ".githooks" vs
	// ".githooks/pre-commit"). Both cannot exist — the write that needs the
	// directory hits ENOTDIR against the file mid-apply — so refuse at plan
	// time. Every target here is a regular-file destination; a shared ancestor
	// that is NOT itself a target (two files under ".claude/skills/") is fine.
	for _, s := range specs {
		rel := filepath.Clean(s.Rel)
		for anc := filepath.Dir(rel); anc != "." && anc != string(filepath.Separator); anc = filepath.Dir(anc) {
			if prev, ok := byRel[anc]; ok {
				return fmt.Errorf("agents: %s (a file at ~/%s) and %s (~/%s) collide — one needs a directory where the other places a file; change one target (or the devtree)", prev, anc, s.Label, rel)
			}
		}
	}
	return nil
}

// instructionSpecs enumerates the rendered-instruction destinations: one spec
// per resolved harness plus the optional devtree workspace file. It needs no
// repo access, so consumers that only care about destinations (FindBridges)
// can share it without walking the asset trees.
func instructionSpecs(cfg config.AgentsConfig) ([]TargetSpec, error) {
	harnesses, err := Resolve(cfg)
	if err != nil {
		return nil, err
	}
	var specs []TargetSpec
	for _, h := range harnesses {
		specs = append(specs, TargetSpec{
			Key:    "agents/" + h.Name,
			Label:  "agents:" + h.Name,
			Rel:    h.Target,
			Source: h.Source,
		})
	}

	// Optional devtree workspace layer: coding.md at the devtree ROOT (the
	// ancestor walk-up reads <dir>/CLAUDE.md, not <dir>/.claude/CLAUDE.md).
	if cfg.Devtree != "" {
		specs = append(specs, TargetSpec{
			Key:    "agents/devtree",
			Label:  "agents:devtree",
			Rel:    filepath.Join(cfg.Devtree, "CLAUDE.md"),
			Source: SourceCoding,
		})
	}
	return specs, nil
}

// assetSpecs enumerates the asset-file destinations by walking each resolved
// asset mapping's source tree (agents/<source>/ in the config repo) in
// lexical order, one spec per regular file, destined for <target>/<relpath>
// under $HOME. Symlinks anywhere in a tree are refused with a warning
// (managed content is copied, never symlinked); executable bits are recorded
// per file so hook scripts and dispatchers deploy runnable.
//
// Keys are "agents/<mapping-name>/<relpath>": for the built-in mappings the
// name equals the source directory, so records written before a mapping was
// data keep matching.
func assetSpecs(repoRoot string, cfg config.AgentsConfig, guard func(string) (string, error)) (specs []TargetSpec, warnings []string, err error) {
	mappings, err := ResolveAssets(cfg)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range mappings {
		root := filepath.Join(repoRoot, RepoSubdir, m.Source)
		safeRoot, gerr := guardPath(guard, root)
		if gerr != nil {
			warnings = append(warnings, refusal("asset tree", filepath.Join(RepoSubdir, m.Source), gerr))
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
					filepath.Join(RepoSubdir, m.Source, rel)))
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			safe, gerr := guardPath(guard, path)
			if gerr != nil {
				warnings = append(warnings, refusal("asset", filepath.Join(RepoSubdir, m.Source, rel), gerr))
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil {
				return ierr
			}
			specs = append(specs, TargetSpec{
				Key:      "agents/" + m.Name + "/" + filepath.ToSlash(rel),
				Label:    "agents:" + m.Name + "/" + filepath.ToSlash(rel),
				Rel:      filepath.Join(m.Target, rel),
				RepoFile: safe,
				Exec:     info.Mode()&0o111 != 0,
			})
			return nil
		})
		if walkErr != nil {
			return nil, nil, walkErr
		}
	}
	return specs, warnings, nil
}

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

// Plan expands the agents domain into its 1:1 (content, target) items: the
// shared enumeration (enumerateSpecs) with content attached — instruction
// targets receive their rendered source, asset targets their repo file's
// bytes. It reads only the config repo — never $HOME — and is deterministic:
// registry order, then devtree, then assets in lexical walk order.
//
// Recoverable per-target problems (a missing source file, a refused or
// escaping target path, a symlink inside an asset tree) become warnings and
// the item is skipped; only a config error (bad harness declaration) or an
// unexpected read failure aborts.
func Plan(in PlanInput) (items []Item, warnings []string, err error) {
	specs, warnings, err := enumerateSpecs(in.RepoRoot, in.Config, in.Guard)
	if err != nil {
		return nil, nil, err
	}

	sources, sourceWarnings, err := loadSources(in)
	if err != nil {
		return nil, nil, err
	}
	warnings = append(warnings, sourceWarnings...)

	for _, spec := range specs {
		var content []byte
		if spec.RepoFile != "" {
			// An asset target: the spec's RepoFile is the guard-validated path
			// the enumeration walked.
			content, err = os.ReadFile(spec.RepoFile)
			if err != nil {
				return nil, nil, err
			}
		} else {
			// An instruction target: the rendered source, when its inputs exist
			// (a missing input already produced one warning).
			var ok bool
			content, ok = sources[spec.Source]
			if !ok {
				continue
			}
		}
		t, terr := dotfile.NestedTarget(in.Home, spec.Rel, spec.Key)
		if terr != nil {
			warnings = append(warnings, refusal("target", spec.Label, terr))
			continue
		}
		items = append(items, Item{
			Key:     spec.Key,
			Label:   spec.Label,
			Target:  t,
			Content: content,
			Exec:    spec.Exec,
		})
	}

	return items, warnings, nil
}

// loadSources reads general.md and coding.md from the config repo and derives
// the combined content. Each rendered source appears in the returned map only
// when its inputs exist; a missing input produces ONE warning here so the
// per-spec loop can skip silently.
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
