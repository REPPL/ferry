package keybindings

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/REPPL/ferry/internal/dotfile"
)

// RepoSubdir is the config-repo subdirectory holding the key-bindings source.
const RepoSubdir = "keybindings"

// SourceName is the single source file this domain carries, under RepoSubdir.
const SourceName = "DefaultKeyBinding.dict"

// TargetRel is the home-relative destination the source deploys to. It is inside
// $HOME, so dotfile.NestedTarget's containment guard passes it.
const TargetRel = "Library/KeyBindings/DefaultKeyBinding.dict"

// KeyPrefix namespaces the domain's records in the shared last-applied store, so
// the de-scope pass can tell them apart from dotfiles/agents/terminals targets.
const KeyPrefix = "keybindings/"

// Key is the stable last-applied store key for the single carried file.
const Key = KeyPrefix + SourceName

// Label is the human-facing domain name reports print.
const Label = "keybindings"

// Item is the (content, target) pair the domain deploys. It mirrors
// termcfg.Item's shape so the command layer's per-target planning is identical,
// but the domain carries exactly one Item (a single fixed file) with no secret
// routing and no per-machine overlay.
type Item struct {
	// Key is the stable last-applied store key ("keybindings/DefaultKeyBinding.dict").
	Key string
	// Label is the name reports print ("keybindings").
	Label string
	// Target is the validated $HOME destination (built via dotfile.NestedTarget,
	// so ~/.ssh and $HOME-escapes are impossible).
	Target dotfile.Target
	// Content is the exact bytes to materialise (the repo source; there is no
	// overlay and no secret rendering for this domain).
	Content []byte
}

// PlanInput carries the planner's inputs. Guard validates a repo-side path
// before it is read (the caller passes its symlink-refusing repo guard) and
// returns the safe path; nil means no extra validation (tests). Linter validates
// the plist format on macOS; nil skips the plutil check (tests / already
// validated).
type PlanInput struct {
	RepoRoot string
	Home     string
	Guard    func(candidate string) (string, error)
	Linter   Linter
}

// Plan expands the key-bindings domain into its at-most-one (content, target)
// item. An absent source deploys nothing (the domain is enabled but the file was
// never committed). A symlinked or non-regular source is refused. The source is
// validated for format hygiene — a binary plist (bplist00), a UTF-8 BOM,
// non-UTF-8 bytes, or (on macOS) a `plutil -lint` failure — before it is carried;
// a hygiene failure becomes a warning and the item is skipped, never an abort. It
// reads only the config repo, never $HOME.
func Plan(in PlanInput) (items []Item, warnings []string, err error) {
	src := filepath.Join(in.RepoRoot, RepoSubdir, SourceName)
	rel := filepath.Join(RepoSubdir, SourceName)

	safe, gerr := guardPath(in.Guard, src)
	if gerr != nil {
		return nil, []string{refusal("source", rel, gerr)}, nil
	}
	fi, serr := os.Lstat(safe)
	if serr != nil {
		// An absent (or unreadable-as-absent) source deploys nothing.
		if errors.Is(serr, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, serr
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, []string{fmt.Sprintf(
			"keybindings: refusing %s: symlink not allowed in the managed repo tree (copy the real file in)", rel)}, nil
	}
	if !fi.Mode().IsRegular() {
		return nil, []string{fmt.Sprintf(
			"keybindings: refusing %s: not a regular file", rel)}, nil
	}

	data, rerr := os.ReadFile(safe)
	if rerr != nil {
		return nil, nil, rerr
	}
	if w := validateFormat(data, in.Linter, safe); w != "" {
		return nil, []string{w}, nil
	}

	t, terr := dotfile.NestedTarget(in.Home, TargetRel, Key)
	if terr != nil {
		return nil, []string{refusal("target", Label, terr)}, nil
	}
	return []Item{{Key: Key, Label: Label, Target: t, Content: data}}, nil, nil
}

// validateFormat enforces the domain's format hygiene, returning a user-facing
// warning when the source is not the readable old-style/UTF-8 dict ferry carries
// (empty means the file passed every check). The pure-Go checks (bplist00 magic,
// UTF-8 BOM, valid UTF-8) run on EVERY platform; the `plutil -lint` check runs
// only on macOS (a nil or ErrNotDarwin lint result passes).
func validateFormat(data []byte, linter Linter, path string) string {
	if bytes.HasPrefix(data, []byte("bplist00")) {
		return fmt.Sprintf(
			"keybindings: refusing %s: file is a BINARY plist (bplist00 header) — ferry carries only the readable old-style/UTF-8 dict; re-save it as text (never `plutil -convert binary1`)", SourceName)
	}
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		return fmt.Sprintf(
			"keybindings: refusing %s: file has a UTF-8 byte-order mark (BOM) — save it as plain UTF-8 with no BOM", SourceName)
	}
	if !utf8.Valid(data) {
		return fmt.Sprintf(
			"keybindings: refusing %s: file is not valid UTF-8 — the key-bindings dict must be a UTF-8 text property list", SourceName)
	}
	if linter != nil {
		if err := linter.Lint(path); err != nil && !errors.Is(err, ErrNotDarwin) {
			return fmt.Sprintf(
				"keybindings: refusing %s: property-list lint failed (%v) — fix the dict, then re-run `ferry apply`", SourceName, err)
		}
	}
	return ""
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

// refusal renders a clear, user-facing warning for a skipped key-bindings
// target, mirroring the dotfile and termcfg domains' refusal wording.
func refusal(what, name string, err error) string {
	switch {
	case errors.Is(err, dotfile.ErrForbiddenSSHPath):
		return fmt.Sprintf("keybindings: refusing %s %s: ferry never manages paths under ~/.ssh", what, name)
	case errors.Is(err, dotfile.ErrPathEscapesHome):
		return fmt.Sprintf("keybindings: refusing %s %s: invalid managed path (escapes $HOME)", what, name)
	default:
		return fmt.Sprintf("keybindings: refusing %s %s: %v", what, name, err)
	}
}
