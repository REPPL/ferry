package emacs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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
// carried regular file under the repo's emacs/ tree, in lexical walk order, each
// destined for the same relative path under ~/.emacs.d/ and carried like a
// dotfile. Content is the per-machine overlay (local/emacs/<relpath>) when
// present — local wins, mirroring the dotfile, agents-asset, and terminal
// domains — else the shared repo source. Volatile, machine-generated paths (see
// excluded) are pruned and never deployed. It reads only the config repo, never
// $HOME.
//
// An absent emacs/ tree deploys nothing (the domain is enabled but nothing was
// committed). A symlinked source (the tree root or any entry inside it) is
// refused. Recoverable per-target problems (a refused/escaping path) become
// warnings and the item is skipped; only an unexpected read failure aborts.
func Plan(in PlanInput) (items []Item, warnings []string, err error) {
	root := filepath.Join(in.RepoRoot, RepoSubdir)
	safeRoot, gerr := guardPath(in.Guard, root)
	if gerr != nil {
		return nil, []string{refusal("source", RepoSubdir, gerr)}, nil
	}
	fi, serr := os.Lstat(safeRoot)
	if serr != nil {
		// An absent (or unreadable-as-absent) source deploys nothing.
		if errors.Is(serr, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, serr
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, []string{fmt.Sprintf(
			"emacs: refusing %s: symlink not allowed in the managed repo tree (copy the real dir in)", RepoSubdir)}, nil
	}
	if !fi.IsDir() {
		return nil, []string{fmt.Sprintf(
			"emacs: refusing %s: the Emacs source must be a directory tree", RepoSubdir)}, nil
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
			warnings = append(warnings, fmt.Sprintf(
				"emacs: refusing %s: symlink not allowed in the managed repo tree (copy the real file in)",
				filepath.Join(RepoSubdir, rel)))
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		item, warn, berr := buildItem(in, path, rel, info)
		if berr != nil {
			return berr
		}
		if warn != "" {
			warnings = append(warnings, warn)
			return nil
		}
		items = append(items, item)
		return nil
	})
	if walkErr != nil {
		return nil, nil, walkErr
	}
	return items, warnings, nil
}

// buildItem resolves one file's deployable content (overlay-or-shared) and its
// validated $HOME destination under ~/.emacs.d/, returning a ready Item. A
// recoverable per-file problem (a refused target path) is returned as a
// non-empty warning with a zero Item; only an unexpected read failure returns
// err.
func buildItem(in PlanInput, repoFile, rel string, info fs.FileInfo) (Item, string, error) {
	content, _, _, cerr := emacsContent(in.RepoRoot, repoFile, in.Guard)
	if cerr != nil {
		return Item{}, "", cerr
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
		Content: content,
		Exec:    info.Mode()&0o111 != 0,
	}, "", nil
}

// emacsContent resolves the bytes apply deploys for an Emacs file: the
// gitignored per-machine overlay at local/emacs/<relpath> when it exists (local
// wins), else the shared repo file. Both reads route through guard (the caller's
// symlink-refusing repo guard), so a symlink inside the managed tree is refused,
// never read through. It returns the resolved content, the overlay path (always
// computed), and whether the overlay was used.
func emacsContent(repoRoot, repoFile string, guard func(string) (string, error)) (content []byte, overlayPath string, usedOverlay bool, err error) {
	overlayPath = overlayPathFor(repoRoot, repoFile)
	if overlayPath != "" {
		safe, gerr := guardPath(guard, overlayPath)
		if gerr == nil {
			if fi, serr := os.Lstat(safe); serr == nil && fi.Mode().IsRegular() {
				data, rerr := os.ReadFile(safe)
				if rerr != nil {
					return nil, overlayPath, false, rerr
				}
				return data, overlayPath, true, nil
			}
		}
	}
	safe, gerr := guardPath(guard, repoFile)
	if gerr != nil {
		return nil, overlayPath, false, gerr
	}
	data, rerr := os.ReadFile(safe)
	if rerr != nil {
		return nil, overlayPath, false, rerr
	}
	return data, overlayPath, false, nil
}

// overlayPathFor maps an Emacs file's shared repo source
// (<repoRoot>/emacs/<relpath>) to its per-machine overlay
// (<repoRoot>/local/emacs/<relpath>) — the SAME relative shape under local/. It
// returns "" when repoFile is not under <repoRoot>/emacs (defensive; every
// planned file is), so callers treat that as "no overlay".
func overlayPathFor(repoRoot, repoFile string) string {
	emacsRoot := filepath.Join(repoRoot, RepoSubdir)
	rel, err := filepath.Rel(emacsRoot, repoFile)
	if err != nil || rel == ".." || filepath.IsAbs(rel) ||
		len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
		return ""
	}
	return filepath.Join(repoRoot, LocalSubdir, RepoSubdir, rel)
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
