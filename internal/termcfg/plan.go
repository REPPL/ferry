package termcfg

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/dotfile"
)

// KeyPrefix namespaces the terminal domain's records inside the shared
// last-applied store, so the de-scope pass can tell them apart from dotfiles
// and agents targets.
const KeyPrefix = "terminals/"

// Item is one (content, target) pair the terminal domain deploys: the planner's
// one-to-many expansion produces these (one per file in a directory terminal,
// or one for a single-file terminal), and the write path stays strictly 1:1.
type Item struct {
	// Key is the stable last-applied store key ("terminals/<name>" for a
	// single-file terminal, "terminals/<name>/<relpath>" for a file inside a
	// directory terminal). The "terminals/" prefix namespaces the domain in the
	// shared last-applied store.
	Key string
	// Label is the human-facing name reports print (e.g. "terminals:alacritty").
	Label string
	// Target carries the validated $HOME destination (built via
	// dotfile.NestedTarget, so ~/.ssh and $HOME-escapes are impossible).
	Target dotfile.Target
	// Content is the exact bytes to materialise (the per-machine overlay when one
	// exists, else the shared repo source), with any {{ferry.secret ...}}
	// placeholders already rendered by the caller.
	Content []byte
	// Exec preserves the repo source's executable bit on a first-ever write.
	Exec bool
	// SecretRouted marks a target whose Content was rendered from the secret store
	// (a {{ferry.secret ...}} placeholder was substituted). Such a file carries
	// plaintext credentials, so ApplyItem materialises it 0600 (never
	// group-/world-readable) and the apply command records only its hash — never the
	// bytes — in the last-applied snapshot, exactly like a secret-routed dotfile.
	SecretRouted bool
}

// PlanInput carries the planner's inputs. Guard validates a repo-side path
// before it is read (the caller passes its symlink-refusing repo guard) and
// returns the safe path; nil means no extra validation (tests).
type PlanInput struct {
	RepoRoot string
	Home     string
	Config   config.TerminalsConfig
	Guard    func(candidate string) (string, error)
}

// Plan expands the terminal domain into its 1:1 (content, target) items: one
// per resolved terminal's config, carried like a dotfile. A directory terminal
// (alacritty/, kitty/) fans out to one item per file in lexical walk order; a
// single-file terminal (wezterm.lua) is one item. Content is the per-machine
// overlay (local/terminals/<source>/<relpath>) when present — local wins,
// mirroring the dotfile and agents-asset domains — else the shared repo source.
// It reads only the config repo, never $HOME.
//
// Recoverable per-target problems (an absent source, a refused/escaping path, a
// symlink inside the tree) become warnings and the item is skipped; only a
// config error (a bad terminal declaration) or an unexpected read failure
// aborts.
func Plan(in PlanInput) (items []Item, warnings []string, err error) {
	terminals, err := Resolve(in.Config)
	if err != nil {
		return nil, nil, err
	}

	for _, term := range terminals {
		termItems, termWarnings, terr := planTerminal(in, term)
		if terr != nil {
			return nil, nil, terr
		}
		items = append(items, termItems...)
		warnings = append(warnings, termWarnings...)
	}
	return items, warnings, nil
}

// planTerminal expands one resolved terminal into its items. A source that does
// not exist deploys nothing (a built-in you do not use); a symlinked source is
// refused. A directory source is walked file-by-file; a single regular file
// becomes one item destined for the terminal's target directly.
func planTerminal(in PlanInput, term Terminal) (items []Item, warnings []string, err error) {
	root := filepath.Join(in.RepoRoot, RepoSubdir, term.Source)
	safeRoot, gerr := guardPath(in.Guard, root)
	if gerr != nil {
		return nil, []string{refusal("source", filepath.Join(RepoSubdir, term.Source), gerr)}, nil
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
			"terminals: refusing %s: symlink not allowed in the managed repo tree (copy the real file/dir in)",
			filepath.Join(RepoSubdir, term.Source))}, nil
	}

	if !fi.IsDir() {
		// Single-file terminal (e.g. wezterm.lua -> ~/.wezterm.lua).
		if !fi.Mode().IsRegular() {
			return nil, []string{fmt.Sprintf(
				"terminals: refusing %s: not a regular file or directory",
				filepath.Join(RepoSubdir, term.Source))}, nil
		}
		item, warn, ierr := buildItem(in, term, safeRoot, term.Name, term.Target, fi)
		if ierr != nil {
			return nil, nil, ierr
		}
		if warn != "" {
			return nil, []string{warn}, nil
		}
		return []Item{item}, nil, nil
	}

	// Directory terminal: one item per regular file, in lexical walk order.
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
				"terminals: refusing %s: symlink not allowed in the managed repo tree (copy the real file in)",
				filepath.Join(RepoSubdir, term.Source, rel)))
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
		key := term.Name + "/" + filepath.ToSlash(rel)
		item, warn, berr := buildItem(in, term, path, key, filepath.Join(term.Target, rel), info)
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
// validated $HOME destination, returning a ready Item. A recoverable per-file
// problem (a refused target path) is returned as a non-empty warning with a
// zero Item; only an unexpected read failure returns err.
func buildItem(in PlanInput, term Terminal, repoFile, key, rel string, info fs.FileInfo) (Item, string, error) {
	content, _, _, cerr := terminalContent(in.RepoRoot, repoFile, in.Guard)
	if cerr != nil {
		return Item{}, "", cerr
	}
	t, terr := dotfile.NestedTarget(in.Home, rel, KeyPrefix+key)
	if terr != nil {
		return Item{}, refusal("target", "terminals:"+key, terr), nil
	}
	return Item{
		Key:     KeyPrefix + key,
		Label:   "terminals:" + key,
		Target:  t,
		Content: content,
		Exec:    info.Mode()&0o111 != 0,
	}, "", nil
}

// terminalContent resolves the bytes apply deploys for a terminal file: the
// gitignored per-machine overlay at local/terminals/<source-relpath> when it
// exists (local wins), else the shared repo file. Both reads route through
// guard (the caller's symlink-refusing repo guard), so a symlink inside the
// managed tree is refused, never read through. It returns the resolved content,
// the overlay path (always computed), and whether the overlay was used.
func terminalContent(repoRoot, repoFile string, guard func(string) (string, error)) (content []byte, overlayPath string, usedOverlay bool, err error) {
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

// overlayPathFor maps a terminal file's shared repo source
// (<repoRoot>/terminals/<relpath>) to its per-machine overlay
// (<repoRoot>/local/terminals/<relpath>) — the SAME relative shape under
// local/. It returns "" when repoFile is not under <repoRoot>/terminals
// (defensive; every planned file is), so callers treat that as "no overlay".
func overlayPathFor(repoRoot, repoFile string) string {
	termsRoot := filepath.Join(repoRoot, RepoSubdir)
	rel, err := filepath.Rel(termsRoot, repoFile)
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

// refusal renders a clear, user-facing warning for a skipped terminal target,
// mirroring the dotfile and agents domains' refusal wording.
func refusal(what, name string, err error) string {
	switch {
	case errors.Is(err, dotfile.ErrForbiddenSSHPath):
		return fmt.Sprintf("terminals: refusing %s %s: ferry never manages paths under ~/.ssh", what, name)
	case errors.Is(err, dotfile.ErrPathEscapesHome):
		return fmt.Sprintf("terminals: refusing %s %s: invalid managed path (escapes $HOME)", what, name)
	default:
		return fmt.Sprintf("terminals: refusing %s %s: %v", what, name, err)
	}
}
