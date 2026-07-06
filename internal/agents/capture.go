package agents

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/REPPL/ferry/internal/dotfile"
)

// LocalSubdir is the config-repo subdirectory holding the per-machine `.local`
// overlay layer, mirroring the dotfile domain's local/ tree. An agents asset's
// gitignored per-machine copy lives at local/agents/<source>/<relpath> — the
// SAME relative shape as its shared source under agents/<source>/<relpath> — so
// a capture-back local route and apply's local-wins deploy agree on one path.
const LocalSubdir = "local"

// CaptureKind classifies how a deployed agents target maps back to its config
// repo source, which decides whether — and where — a live edit can be captured.
type CaptureKind int

const (
	// CaptureAsset: a 1:1 file copy from agents/<source>/<relpath>. It reverses
	// cleanly, so a live edit routes to the shared repo source OR the per-machine
	// local overlay (local/agents/<source>/<relpath>).
	CaptureAsset CaptureKind = iota
	// CaptureGeneral: an instruction target whose deployed content is agents/
	// general.md verbatim (SourceGeneral). A live edit routes to general.md
	// (shared only — general.md feeds every general-sourced harness).
	CaptureGeneral
	// CaptureCoding: an instruction target whose deployed content is agents/
	// coding.md verbatim (SourceCoding). A live edit routes to coding.md.
	CaptureCoding
	// CaptureCombined: an instruction target whose deployed content is the
	// DERIVED general+coding concatenation (SourceCombined). It cannot be
	// decomposed back into two files, so a live edit is REFUSED — capture never
	// guesses which source a change belongs to.
	CaptureCombined
)

// CaptureTarget is one deployed agents destination described for capture-back:
// the derived content apply deploys (the repo side of the three-way compare),
// the validated $HOME destination, and where a captured edit would be routed.
// It is produced by CaptureTargets from the SAME spec enumeration + source
// rendering the planner uses, so capture can never disagree with apply about
// what the domain manages or what it deploys.
type CaptureTarget struct {
	Key     string
	Label   string
	Target  dotfile.Target // validated $HOME destination (Target.Name == Key)
	Content []byte         // derived content apply deploys (local overlay applied for assets)
	Kind    CaptureKind

	// RepoDest is the absolute, guard-validated config-repo source a SHARED
	// capture writes: the asset file agents/<source>/<relpath>, or the
	// instruction source agents/general.md / agents/coding.md. It is "" for a
	// CaptureCombined target (no single source to write).
	RepoDest string
	// LocalDest is the absolute per-machine overlay a LOCAL capture writes:
	// local/agents/<source>/<relpath>. Set for CaptureAsset targets only; "" for
	// instruction targets (which have no per-target local route).
	LocalDest string
	// Exec preserves the repo source's executable bit, so a captured hook script
	// keeps 0755 when apply re-deploys it.
	Exec bool
}

// CaptureTargets enumerates every deployed agents destination with the routing
// information a capture-back needs. It reuses enumerateSpecs (the single shared
// destination enumeration) and loadSources (the same rendered instruction
// content the planner deploys), so the derived Content it reports is byte-for-
// byte what apply would write — the repo side of capture's three-way compare.
//
// An asset target shadowed by a gitignored local overlay
// (local/agents/<source>/<relpath>) carries the OVERLAY bytes as Content, exactly
// as apply deploys (local wins), so classification compares the live file
// against the bytes actually on the machine. Recoverable per-target problems (a
// missing rendered source, a refused/symlinked repo read) become warnings and
// the target is skipped, mirroring Plan.
func CaptureTargets(in PlanInput) ([]CaptureTarget, []string, error) {
	specs, warnings, err := enumerateSpecs(in.RepoRoot, in.Config, in.Guard)
	if err != nil {
		return nil, nil, err
	}
	sources, sourceWarnings, err := loadSources(in)
	if err != nil {
		return nil, nil, err
	}
	warnings = append(warnings, sourceWarnings...)

	var targets []CaptureTarget
	for _, spec := range specs {
		ct := CaptureTarget{Key: spec.Key, Label: spec.Label, Exec: spec.Exec}

		if spec.RepoFile != "" {
			// An asset target: the derived content is the local overlay (when one
			// exists) else the repo file, exactly as apply resolves it.
			content, overlay, _, cerr := assetContent(in.RepoRoot, spec.RepoFile, in.Guard)
			if cerr != nil {
				warnings = append(warnings, refusal("asset", relRepo(in.RepoRoot, spec.RepoFile), cerr))
				continue
			}
			ct.Content = content
			ct.Kind = CaptureAsset
			ct.RepoDest = spec.RepoFile
			ct.LocalDest = overlay
		} else {
			content, ok := sources[spec.Source]
			if !ok {
				continue // a missing rendered source already produced a warning
			}
			ct.Content = content
			switch spec.Source {
			case SourceGeneral:
				ct.Kind = CaptureGeneral
				ct.RepoDest = filepath.Join(in.RepoRoot, RepoSubdir, "general.md")
			case SourceCoding:
				ct.Kind = CaptureCoding
				ct.RepoDest = filepath.Join(in.RepoRoot, RepoSubdir, "coding.md")
			default:
				ct.Kind = CaptureCombined
				ct.RepoDest = "" // derived from two files; no single source to write
			}
		}

		t, terr := dotfile.NestedTarget(in.Home, spec.Rel, spec.Key)
		if terr != nil {
			warnings = append(warnings, refusal("target", spec.Label, terr))
			continue
		}
		ct.Target = t
		targets = append(targets, ct)
	}
	return targets, warnings, nil
}

// assetContent resolves the bytes apply deploys for an asset target: the
// gitignored per-machine overlay at local/agents/<source>/<relpath> when it
// exists (local wins), else the shared repo file. Both reads are routed through
// guard (the caller's symlink-refusing repo guard), so a symlink inside the
// managed tree is refused, never read through. It returns the resolved content,
// the overlay path (always computed, so a LOCAL capture route knows where to
// write), and whether the overlay was used.
func assetContent(repoRoot, repoFile string, guard func(string) (string, error)) (content []byte, overlayPath string, usedOverlay bool, err error) {
	overlayPath = assetOverlayPath(repoRoot, repoFile)
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

// assetOverlayPath maps an asset's shared repo source
// (<repoRoot>/agents/<source>/<relpath>) to its per-machine overlay
// (<repoRoot>/local/agents/<source>/<relpath>) — the SAME relative shape under
// local/. It returns "" when repoFile is not under <repoRoot>/agents (defensive;
// every asset spec's RepoFile is), so callers treat that as "no overlay".
func assetOverlayPath(repoRoot, repoFile string) string {
	agentsRoot := filepath.Join(repoRoot, RepoSubdir)
	rel, err := filepath.Rel(agentsRoot, repoFile)
	if err != nil || rel == ".." || filepath.IsAbs(rel) ||
		len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
		return ""
	}
	return filepath.Join(repoRoot, LocalSubdir, RepoSubdir, rel)
}

// relRepo renders a repo-relative label for a warning (best effort).
func relRepo(repoRoot, path string) string {
	if rel, err := filepath.Rel(repoRoot, path); err == nil {
		return rel
	}
	return path
}

// AdoptCandidate is one agent-shaped file present in a tracked project that
// ferry has NEVER deployed — a new file capture can offer to bring under
// management. It carries the live path, the repo source a captured adopt writes
// (agents/<source>/<relpath>), and the executable bit to preserve.
type AdoptCandidate struct {
	Home     string // absolute live path
	RepoDest string // absolute repo source to create on adopt
	Label    string // repo-relative display label (agents/<source>/<relpath>)
	Exec     bool
}

// AdoptCandidates scans each resolved asset mapping's $HOME target directory for
// regular files the domain does NOT currently deploy — new agent-shaped files to
// offer for adoption. "Agent-shaped" is defined ENTIRELY by the asset-mapping
// REGISTRY (the mappings' target directories), so it grows as the registry
// grows and is never a hand-maintained filename list.
//
// A file already deployed (its live path is in the current plan's target set) is
// skipped — that is capture-back territory, not adoption. Symlinks are skipped
// (managed content is copied, never symlinked). The scan touches only the
// registry's target directories; it never walks $HOME at large and never nears
// ~/.ssh (asset targets are validated through the planner's containment).
func AdoptCandidates(in PlanInput) ([]AdoptCandidate, []string, error) {
	specs, warnings, err := enumerateSpecs(in.RepoRoot, in.Config, in.Guard)
	if err != nil {
		return nil, nil, err
	}
	deployed := map[string]bool{}
	for _, s := range specs {
		if t, terr := dotfile.NestedTarget(in.Home, s.Rel, s.Key); terr == nil {
			deployed[filepath.Clean(t.Home)] = true
		}
	}

	mappings, err := ResolveAssets(in.Config)
	if err != nil {
		return nil, nil, err
	}

	var candidates []AdoptCandidate
	for _, m := range mappings {
		targetDir := filepath.Join(in.Home, m.Target)
		fi, serr := os.Lstat(targetDir)
		if serr != nil || !fi.IsDir() {
			continue // an absent (or non-directory, or symlinked) target dir has nothing to adopt
		}
		walkErr := filepath.WalkDir(targetDir, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.Type()&fs.ModeSymlink != 0 {
				if d.IsDir() {
					return fs.SkipDir // never descend through a symlinked dir into e.g. ~/.ssh
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			clean := filepath.Clean(path)
			if deployed[clean] {
				return nil // already managed — capture-back handles its drift, not adoption
			}
			rel, rerr := filepath.Rel(targetDir, path)
			if rerr != nil {
				return nil
			}
			// Validate the live path through the planner's containment (refuses
			// ~/.ssh and $HOME escapes) before offering it.
			if _, verr := dotfile.NestedTarget(in.Home, filepath.Join(m.Target, rel), "agents/adopt"); verr != nil {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil {
				return ierr
			}
			repoDest := filepath.Join(in.RepoRoot, RepoSubdir, m.Source, rel)
			candidates = append(candidates, AdoptCandidate{
				Home:     clean,
				RepoDest: repoDest,
				Label:    filepath.ToSlash(filepath.Join(RepoSubdir, m.Source, rel)),
				Exec:     info.Mode()&0o111 != 0,
			})
			return nil
		})
		if walkErr != nil {
			return nil, nil, walkErr
		}
	}
	return candidates, warnings, nil
}
