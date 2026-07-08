package emacs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/REPPL/ferry/internal/dotfile"
)

// RepoSubdir is the config-repo subdirectory that holds the Emacs source tree.
const RepoSubdir = "emacs"

// LocalSubdir is the gitignored per-machine overlay root inside the repo. A
// file's per-machine override lives at local/emacs/<relpath>, mirroring the
// dotfile, agents-asset, and terminal .local layers — the natural home for a
// Customize-written inits/custom.el or a hand-authored init.local.el.
const LocalSubdir = "local"

// TargetHome is the home-relative destination the emacs/ tree deploys to. It is
// inside $HOME, so dotfile.NestedTarget's containment guard passes it. Note
// ~/.emacs.d shadows the XDG ~/.config/emacs — Emacs reads ~/.emacs.d when it
// exists and never consults ~/.config/emacs.
const TargetHome = ".emacs.d"

// KeyPrefix namespaces the domain's records in the shared last-applied store, so
// the de-scope pass can tell them apart from dotfiles/agents/terminals/
// keybindings targets.
const KeyPrefix = "emacs/"

// Item is one (content, target) pair the Emacs domain deploys: the planner's
// one-to-many expansion produces these (one per carried regular file in the
// tree), and the write path stays strictly 1:1. It mirrors termcfg.Item's shape
// so the command layer's per-target planning is identical.
type Item struct {
	// Key is the stable last-applied store key ("emacs/<relpath>"). The "emacs/"
	// prefix namespaces the domain in the shared last-applied store.
	Key string
	// Label is the human-facing name reports print (e.g. "emacs:init.el").
	Label string
	// Target carries the validated $HOME destination (built via
	// dotfile.NestedTarget, so ~/.ssh and $HOME-escapes are impossible).
	Target dotfile.Target
	// Content is the exact bytes to materialise (the per-machine overlay when one
	// exists, else the shared repo source), with any {{ferry.secret ...}}
	// placeholders already rendered by the caller.
	Content []byte
	// Exec preserves the repo source's executable bit on a first-ever write (an
	// Emacs config tree may ship a helper script).
	Exec bool
}

// PlanInput carries the planner's inputs. Guard validates a repo-side path
// before it is read (the caller passes its symlink-refusing repo guard) and
// returns the safe path; nil means no extra validation (tests).
type PlanInput struct {
	RepoRoot string
	Home     string
	Guard    func(candidate string) (string, error)
}

// Plan expands the Emacs domain into its 1:1 (content, target) items: one per
// carried regular file in the UNION of the shared emacs/ tree and the
// per-machine local/emacs/ overlay tree, in lexical order, each destined for the
// same relative path under ~/.emacs.d/ and carried like a dotfile. For a file
// present in both trees the overlay wins (local content, mirroring the dotfile,
// agents-asset, and terminal domains); a file present ONLY under local/emacs/
// deploys as a machine-only file (mirroring iTerm2's Dynamic Profiles overlay).
// Volatile, machine-generated paths (see excluded) are pruned from BOTH trees
// and never deployed. It reads only the config repo, never $HOME.
//
// An absent emacs/ tree with an absent overlay deploys nothing (the domain is
// enabled but nothing was committed). A symlinked source (either tree root or any
// entry inside either tree) is refused. Recoverable per-target problems (a
// refused/escaping path) become warnings and the item is skipped; only an
// unexpected read failure aborts.
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
		item, warn, berr := buildItem(in, sources[rel], rel)
		if berr != nil {
			return nil, warnings, berr
		}
		if warn != "" {
			warnings = append(warnings, warn)
			continue
		}
		items = append(items, item)
	}
	return items, warnings, nil
}

// resolveSources returns the union of carried relpaths across the shared emacs/
// tree and the per-machine local/emacs/ overlay tree, each mapped to its winning
// source path: the shared emacs/<rel> unless a local/emacs/<rel> overlay exists
// (local wins), plus any file present ONLY under the overlay (a machine-only
// file). Volatile paths are pruned and symlinks are refused with a warning in
// BOTH trees.
func resolveSources(in PlanInput) (map[string]string, []string, error) {
	var warnings []string
	sources := map[string]string{}

	sharedRoot := filepath.Join(in.RepoRoot, RepoSubdir)
	localRoot := filepath.Join(in.RepoRoot, LocalSubdir, RepoSubdir)

	// Shared tree: the authoritative carry set.
	sw, err := walkTree(in.Guard, sharedRoot, RepoSubdir)
	warnings = append(warnings, sw.warnings...)
	if err != nil {
		return nil, warnings, err
	}
	for rel := range sw.files {
		sources[rel] = filepath.Join(sharedRoot, rel)
	}

	// Local overlay: overrides a shared rel, or adds a machine-only file.
	lw, err := walkTree(in.Guard, localRoot, filepath.Join(LocalSubdir, RepoSubdir))
	warnings = append(warnings, lw.warnings...)
	if err != nil {
		return nil, warnings, err
	}
	for rel := range lw.files {
		sources[rel] = filepath.Join(localRoot, rel)
	}
	return sources, warnings, nil
}

type walkResult struct {
	files    map[string]bool
	warnings []string
}

// walkTree enumerates the carried regular files under root (relative paths),
// pruning volatile paths (excluded) and refusing symlinks. An absent root is
// normal (nothing to deploy). label is the repo-relative directory name used in
// warnings.
func walkTree(guard func(string) (string, error), root, label string) (walkResult, error) {
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
			"emacs: refusing %s: symlink not allowed in the managed repo tree (copy the real dir in)", label))
		return res, nil
	}
	if !fi.IsDir() {
		res.warnings = append(res.warnings, fmt.Sprintf(
			"emacs: refusing %s: the Emacs source must be a directory tree", label))
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
		// Prune volatile, machine-generated paths: a whole excluded subtree is
		// skipped, a single excluded file is dropped — never deployed even if the
		// source tree contains it.
		if excluded(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			res.warnings = append(res.warnings, fmt.Sprintf(
				"emacs: refusing %s: symlink not allowed in the managed repo tree (copy the real file in)",
				filepath.ToSlash(filepath.Join(label, rel))))
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		res.files[rel] = true
		return nil
	})
	if walkErr != nil {
		return res, walkErr
	}
	return res, nil
}

// buildItem reads one file's deployable content from its winning source path
// (shared or overlay) and resolves its validated $HOME destination under
// ~/.emacs.d/, returning a ready Item. A recoverable per-file problem (a refused
// source or target path) is returned as a non-empty warning with a zero Item;
// only an unexpected read failure returns err. The executable bit is taken from
// the winning source so a local-only or overlay file's mode is honoured.
func buildItem(in PlanInput, repoFile, rel string) (Item, string, error) {
	safe, gerr := guardPath(in.Guard, repoFile)
	if gerr != nil {
		return Item{}, refusal("source", "emacs:"+filepath.ToSlash(rel), gerr), nil
	}
	info, serr := os.Lstat(safe)
	if serr != nil {
		return Item{}, "", serr
	}
	data, rerr := os.ReadFile(safe)
	if rerr != nil {
		return Item{}, "", rerr
	}
	key := filepath.ToSlash(rel)
	t, terr := dotfile.NestedTarget(in.Home, filepath.Join(TargetHome, rel), KeyPrefix+key)
	if terr != nil {
		return Item{}, refusal("target", "emacs:"+key, terr), nil
	}
	return Item{
		Key:     KeyPrefix + key,
		Label:   "emacs:" + key,
		Target:  t,
		Content: data,
		Exec:    info.Mode()&0o111 != 0,
	}, "", nil
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

// refusal renders a clear, user-facing warning for a skipped Emacs target,
// mirroring the dotfile, termcfg, and keybindings domains' refusal wording.
func refusal(what, name string, err error) string {
	switch {
	case errors.Is(err, dotfile.ErrForbiddenSSHPath):
		return fmt.Sprintf("emacs: refusing %s %s: ferry never manages paths under ~/.ssh", what, name)
	case errors.Is(err, dotfile.ErrPathEscapesHome):
		return fmt.Sprintf("emacs: refusing %s %s: invalid managed path (escapes $HOME)", what, name)
	default:
		return fmt.Sprintf("emacs: refusing %s %s: %v", what, name, err)
	}
}
