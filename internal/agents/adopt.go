package agents

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/dotfile"
)

// sstTopFiles / sstTrees are the source-of-truth pieces adopt imports from an
// existing instruction directory into the config repo's agents/ area. A
// generated combined.md and the old bin/ scripts are deliberately NOT
// imported: combined is derived by ferry, and the scripts are what ferry
// replaces.
// sstTopFiles are the source-of-truth top-level files adopt imports. The
// subtrees to import are DERIVED from the resolved asset registry (see
// importTrees), not hard-coded, so a custom mapping's tree is imported and
// its bridge is not removed with nothing to replace it.
var sstTopFiles = []string{"general.md", "coding.md"}

// alwaysImportedTrees are the non-asset subtrees adopt always imports: the
// scaffold templates ferry carries but never deploys to $HOME. The asset
// subtrees are appended from the resolved registry.
var alwaysImportedTrees = []string{"templates"}

// importTrees returns the source subtrees ImportSST copies from the adopted
// directory: the always-imported scaffold templates plus every resolved asset
// mapping's SOURCE directory. Deriving from the registry — instead of a
// hard-coded list — keeps import in lockstep with FindBridges (which is
// registry-driven), so adopting a machine that bridged a custom mapping
// imports that mapping's live tree rather than dropping it.
func importTrees(cfg config.AgentsConfig) ([]string, error) {
	mappings, err := ResolveAssets(cfg)
	if err != nil {
		return nil, err
	}
	trees := append([]string(nil), alwaysImportedTrees...)
	seen := map[string]bool{}
	for _, t := range trees {
		seen[t] = true
	}
	for _, m := range mappings {
		src := filepath.Clean(m.Source)
		if seen[src] {
			continue
		}
		seen[src] = true
		trees = append(trees, src)
	}
	return trees, nil
}

// ImportSST copies an existing instruction directory's source files into the
// config repo's agents/ area, NON-DESTRUCTIVELY on both sides: srcDir is only
// ever read, and an existing repo file with DIFFERENT content is skipped with
// a message (never overwritten) so a re-run cannot clobber repo edits. An
// identical file is a quiet no-op, so ImportSST is idempotent. Executable
// bits are preserved (hooks stay runnable); symlinks inside srcDir are
// skipped with a note (managed content is copied, never symlinked).
//
// EVERY destination is routed through guard BEFORE it is read or written —
// the caller passes its symlink-refusing repo guard (safeRepoPath), so a
// symlink already sitting inside the config repo's agents/ tree (e.g.
// agents/hooks -> ~/.ssh) is refused with a loud skip and never written
// THROUGH. nil disables the extra validation (tests only).
func ImportSST(srcDir, destDir string, cfg config.AgentsConfig, guard func(string) (string, error), out io.Writer) error {
	for _, name := range sstTopFiles {
		if err := importFile(filepath.Join(srcDir, name), filepath.Join(destDir, name), guard, out); err != nil {
			return err
		}
	}
	trees, err := importTrees(cfg)
	if err != nil {
		return err
	}
	for _, tree := range trees {
		root := filepath.Join(srcDir, tree)
		fi, err := os.Lstat(root)
		if err != nil || !fi.IsDir() {
			continue // an absent tree imports nothing
		}
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			if d.Type()&fs.ModeSymlink != 0 {
				fmt.Fprintf(out, "skip:     %s (symlink — copy the real file into the repo yourself)\n", filepath.Join(tree, rel))
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			return importFile(path, filepath.Join(destDir, tree, rel), guard, out)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// importFile copies one regular file src -> dest (mode preserved), creating
// parent directories. An identical existing dest is a quiet no-op; a
// differing one is skipped with a message. dest is validated by guard FIRST:
// a refused destination (a symlinked component in the repo tree) is skipped
// loudly and never read or written through.
func importFile(src, dest string, guard func(string) (string, error), out io.Writer) error {
	safeDest, gerr := guardPath(guard, dest)
	if gerr != nil {
		fmt.Fprintf(out, "skip:     %s (refused destination: %v)\n", dest, gerr)
		return nil
	}
	dest = safeDest

	content, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // an absent source piece is fine (e.g. no coding.md yet)
		}
		return err
	}
	if existing, rerr := os.ReadFile(dest); rerr == nil {
		if bytes.Equal(existing, content) {
			return nil
		}
		fmt.Fprintf(out, "skip:     %s (exists in the repo and differs — reconcile manually)\n", dest)
		return nil
	}
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, content, fi.Mode().Perm()); err != nil {
		return err
	}
	fmt.Fprintf(out, "imported: %s\n", dest)
	return nil
}

// Bridge is one $HOME-side symlink that points into the adopted instruction
// directory — the old sync.sh-era deploy mechanism ferry replaces with
// managed copies.
type Bridge struct {
	Path string // absolute symlink path under $HOME
	Dest string // the link's lexically resolved absolute destination
	// Dir marks a DIRECTORY-level bridge (the link resolves to a directory,
	// e.g. a symlinked ~/.claude or a whole-dir ~/.claude/hooks link). The
	// caller must refuse to migrate these transactionally: replacing the link
	// leaves a real DIRECTORY where the baseline recorded a symlink, and a
	// directory cannot be snapshotted/restored by the backup engine — so the
	// swap would not be reversible. They are surfaced for a loud refusal with
	// manual instructions, never written through.
	Dir bool
}

// FindBridges scans the $HOME locations the agents domain manages — every
// resolved harness target, the optional devtree file, and every resolved
// asset mapping's target directory (the directory itself plus its immediate
// entries) — and returns each one that is currently a symlink resolving into
// adoptedDir. DIRECTORY-level bridges are detected too: every ancestor
// directory of a managed location (strictly below $HOME) is checked, so a
// setup that symlinked ~/.claude itself into the instruction directory is
// surfaced rather than written through. A bridge nested under another bridge
// is dropped (removing the outermost link retires the whole subtree).
//
// It looks ONLY at those known locations: it never walks $HOME at large and
// never goes near ~/.ssh (harness and asset targets are built through the
// same validation as the planner, which refuses ~/.ssh).
func FindBridges(home, adoptedDir string, cfg config.AgentsConfig) ([]Bridge, error) {
	// The instruction destinations come from the SAME enumeration the planner
	// deploys (instructionSpecs), so adopt can never scan a different set of
	// harness/devtree paths than apply manages; the asset locations come from
	// the SAME resolved mapping registry.
	specs, err := instructionSpecs(cfg)
	if err != nil {
		return nil, err
	}
	mappings, err := ResolveAssets(cfg)
	if err != nil {
		return nil, err
	}

	var candidates []string
	for _, spec := range specs {
		if dest, ok := bridgeCandidate(home, spec.Rel); ok {
			candidates = append(candidates, dest)
		}
	}
	for _, m := range mappings {
		dir, ok := bridgeCandidate(home, m.Target)
		if !ok {
			continue
		}
		candidates = append(candidates, dir)
		if ents, rerr := os.ReadDir(dir); rerr == nil {
			for _, ent := range ents {
				candidates = append(candidates, filepath.Join(dir, ent.Name()))
			}
		}
	}

	// Directory-level bridges: check every ancestor of a managed location,
	// strictly below $HOME, so a symlinked ~/.claude (or a symlinked devtree
	// segment) pointing into the adopted directory is found — a leaf-only
	// scan would write THROUGH such a link into the source it is migrating.
	cleanHome := filepath.Clean(home)
	ancestorSet := map[string]bool{}
	for _, cand := range candidates {
		for d := filepath.Dir(cand); d != cleanHome && strictlyWithin(cleanHome, d); d = filepath.Dir(d) {
			ancestorSet[d] = true
		}
	}
	for d := range ancestorSet {
		candidates = append(candidates, d)
	}

	adopted, err := filepath.Abs(adoptedDir)
	if err != nil {
		return nil, err
	}
	adopted = filepath.Clean(adopted)

	var bridges []Bridge
	seen := map[string]bool{}
	for _, cand := range candidates {
		if seen[cand] {
			continue
		}
		seen[cand] = true
		fi, lerr := os.Lstat(cand)
		if lerr != nil || fi.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		target, rerr := os.Readlink(cand)
		if rerr != nil {
			continue
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(cand), target)
		}
		target = filepath.Clean(target)
		if pathWithin(adopted, target) {
			// Resolved-containment gate: the candidate is a symlink into the
			// adopted dir, but if its PARENT chain resolves outside $HOME or
			// under ~/.ssh, journal-removing it would delete a file that
			// physically lives outside $HOME. NestedTarget applies exactly the
			// planner's containment (lexical + parent chain resolved, leaf left
			// unresolved so the bridge symlink itself stays enumerable); a
			// refusal drops the candidate. A directory-level bridge (the symlink
			// IS the leaf, parent chain in-$HOME) passes and is surfaced below.
			rel, rerr := filepath.Rel(cleanHome, cand)
			if rerr != nil {
				continue
			}
			if _, verr := dotfile.NestedTarget(cleanHome, rel, "agents/bridge"); verr != nil {
				continue
			}
			// os.Stat follows the link: a destination that is a directory marks
			// a directory-level bridge (refused by the caller, never migrated).
			isDir := false
			if sfi, serr := os.Stat(cand); serr == nil && sfi.IsDir() {
				isDir = true
			}
			bridges = append(bridges, Bridge{Path: cand, Dest: target, Dir: isDir})
		}
	}
	return dropNestedBridges(bridges), nil
}

// dropNestedBridges removes any bridge that sits UNDER another bridge's path:
// removing the outermost symlink retires the whole subtree, and a nested
// entry's path only existed through the outer link anyway. Output order is
// deterministic (sorted by path).
func dropNestedBridges(bridges []Bridge) []Bridge {
	sort.Slice(bridges, func(i, j int) bool { return bridges[i].Path < bridges[j].Path })
	var out []Bridge
	for _, b := range bridges {
		nested := false
		for _, outer := range out {
			if b.Path != outer.Path && pathWithin(outer.Path, b.Path) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, b)
		}
	}
	return out
}

// strictlyWithin reports whether p is a strict descendant of base, by pure
// path arithmetic.
func strictlyWithin(base, p string) bool {
	return p != base && pathWithin(base, p)
}

// bridgeCandidate joins a home-relative destination LEXICALLY for bridge
// scanning, applying only the lexical containment rules (strictly inside
// $HOME, never at/under ~/.ssh). It deliberately does NOT run the planner's
// symlink-RESOLVING validation (dotfile.NestedTarget): an out-pointing parent
// symlink is exactly the bridge adopt exists to find, so a candidate must
// stay enumerable when its parent is a symlink into the adopted directory.
// Nothing at the candidate is read here — the caller only Lstats it.
func bridgeCandidate(home, rel string) (string, bool) {
	if filepath.IsAbs(rel) {
		return "", false
	}
	cleanHome := filepath.Clean(home)
	dest := filepath.Clean(filepath.Join(cleanHome, rel))
	back, err := filepath.Rel(cleanHome, dest)
	if err != nil || back == "." || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", false
	}
	first := back
	if i := strings.IndexRune(back, filepath.Separator); i >= 0 {
		first = back[:i]
	}
	if first == ".ssh" {
		return "", false
	}
	return dest, true
}

// pathWithin reports whether p equals base or is a descendant of it, by pure
// path arithmetic.
func pathWithin(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}
