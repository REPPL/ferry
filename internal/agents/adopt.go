package agents

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/dotfile"
)

// sstTopFiles / sstTrees are the source-of-truth pieces adopt imports from an
// existing instruction directory into the config repo's agents/ area. A
// generated combined.md and the old bin/ scripts are deliberately NOT
// imported: combined is derived by ferry, and the scripts are what ferry
// replaces.
var (
	sstTopFiles = []string{"general.md", "coding.md"}
	sstTrees    = []string{"templates", "skills", "agents", "hooks"}
)

// ImportSST copies an existing instruction directory's source files into the
// config repo's agents/ area, NON-DESTRUCTIVELY on both sides: srcDir is only
// ever read, and an existing repo file with DIFFERENT content is skipped with
// a message (never overwritten) so a re-run cannot clobber repo edits. An
// identical file is a quiet no-op, so ImportSST is idempotent. Executable
// bits are preserved (hooks stay runnable); symlinks inside srcDir are
// skipped with a note (managed content is copied, never symlinked).
func ImportSST(srcDir, destDir string, out io.Writer) error {
	for _, name := range sstTopFiles {
		if err := importFile(filepath.Join(srcDir, name), filepath.Join(destDir, name), out); err != nil {
			return err
		}
	}
	for _, tree := range sstTrees {
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
			return importFile(path, filepath.Join(destDir, tree, rel), out)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// importFile copies one regular file src -> dest (mode preserved), creating
// parent directories. An identical existing dest is a quiet no-op; a
// differing one is skipped with a message.
func importFile(src, dest string, out io.Writer) error {
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
}

// FindBridges scans the $HOME locations the agents domain manages — every
// resolved harness target, the optional devtree file, and the
// ~/.claude/{skills,agents,hooks} asset locations (the location itself plus
// its immediate entries) — and returns each one that is currently a symlink
// resolving into adoptedDir. It looks ONLY at those known locations: it never
// walks $HOME at large and never goes near ~/.ssh (harness targets are built
// through the same validation as the planner, which refuses ~/.ssh).
func FindBridges(home, adoptedDir string, cfg config.AgentsConfig) ([]Bridge, error) {
	harnesses, err := Resolve(cfg)
	if err != nil {
		return nil, err
	}

	var candidates []string
	for _, h := range harnesses {
		if t, terr := dotfile.NestedTarget(home, h.Target, "agents/"+h.Name); terr == nil {
			candidates = append(candidates, t.Home)
		}
	}
	if cfg.Devtree != "" {
		if t, terr := dotfile.NestedTarget(home, filepath.Join(cfg.Devtree, "CLAUDE.md"), "agents/devtree"); terr == nil {
			candidates = append(candidates, t.Home)
		}
	}
	for _, tree := range assetTrees {
		dir := filepath.Join(home, assetHomeRoot, tree)
		candidates = append(candidates, dir)
		if ents, rerr := os.ReadDir(dir); rerr == nil {
			for _, ent := range ents {
				candidates = append(candidates, filepath.Join(dir, ent.Name()))
			}
		}
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
			bridges = append(bridges, Bridge{Path: cand, Dest: target})
		}
	}
	return bridges, nil
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
