package work

import "path/filepath"

// The registry maps each known harness and project layout to its work-state
// locations and receive policies. The source locations are DATA, not code —
// v1 ships the abcd layout and the Claude Code harness; adding another harness
// is a registry entry, never a redesign (modelled on the agents domain's
// Builtins() harness registry).

// ItemKind is the on-disk shape of a cargo item.
type ItemKind string

const (
	KindFile ItemKind = "file"
	KindDir  ItemKind = "dir"
)

// Policy is a cargo item's receive policy, decided by the plan:
//
//   - guarded-overwrite: refuse when the destination changed since this
//     account last held the baton (content hash vs the recorded baseline)
//     unless --force; a first receive into an already-populated destination
//     likewise refuses.
//   - union-merge: copy files absent at the destination, skip files already
//     present, never delete (immutable, uniquely named session files).
type Policy string

const (
	PolicyGuardedOverwrite Policy = "guarded-overwrite"
	PolicyUnionMerge       Policy = "union-merge"
)

// Stable cargo item names. They are manifest identifiers — a persisted
// contract — so they never change spelling.
const (
	ItemNext        = "next"
	ItemRunJournal  = "run-journal"
	ItemAgentMemory = "agent-memory"
	ItemTranscripts = "transcripts"
)

// Locator carries the account-local facts an item needs to resolve its
// absolute path on THIS account. Items only ever recompute forward from these
// — never invert a store key back to a path.
type Locator struct {
	// Home is the account's $HOME.
	Home string
	// ProjectDir is the absolute path of the project repo's main worktree.
	ProjectDir string
	// StoreKey is Identity.Key (the abcd-compatible root SHA).
	StoreKey string
}

// Item is one cargo item in the registry.
type Item struct {
	Name string
	Kind ItemKind
	// Policy is how receive lands this item at the destination.
	Policy Policy
	// Required makes pack refuse when the item is missing (the handover note:
	// "nothing to hand over — write the handover note first"); optional items
	// are tolerated and noted in the manifest.
	Required bool
	// Locate resolves the item's absolute path for one account.
	Locate func(lc Locator) (string, error)
}

// BuiltinItems returns the v1 cargo registry: the abcd layout's in-repo work
// tier plus the Claude Code harness's per-project memory and the redacted
// transcript store.
func BuiltinItems() []Item {
	return []Item{
		{
			Name: ItemNext, Kind: KindFile, Policy: PolicyGuardedOverwrite, Required: true,
			Locate: func(lc Locator) (string, error) {
				return filepath.Join(lc.ProjectDir, ".abcd", ".work.local", "NEXT.md"), nil
			},
		},
		{
			Name: ItemRunJournal, Kind: KindFile, Policy: PolicyGuardedOverwrite,
			Locate: func(lc Locator) (string, error) {
				return filepath.Join(lc.ProjectDir, ".abcd", ".work.local", "run-journal.json"), nil
			},
		},
		{
			Name: ItemAgentMemory, Kind: KindDir, Policy: PolicyGuardedOverwrite,
			Locate: func(lc Locator) (string, error) {
				return filepath.Join(lc.Home, ".claude", "projects", ClaudeProjectsKey(lc.ProjectDir), "memory"), nil
			},
		},
		{
			Name: ItemTranscripts, Kind: KindDir, Policy: PolicyUnionMerge,
			Locate: func(lc Locator) (string, error) {
				return filepath.Join(lc.Home, ".abcd", "history", lc.StoreKey), nil
			},
		},
	}
}

// ClaudeProjectsKey flattens an absolute project path to the harness's
// per-project directory key: every character outside [A-Za-z0-9] becomes '-'.
// This is an observed convention of one tool, not a contract — lossy (a dash
// in a directory name is indistinguishable from a separator) and
// collision-prone on case-insensitive APFS — so ferry only ever recomputes it
// forward from a destination path and verifies the target before writing; it
// never inverts a key. This function is the one owner of the encoding rule:
// a harness-side scheme change is a one-line fix.
func ClaudeProjectsKey(absProjectPath string) string {
	var out []rune
	for _, r := range absProjectPath {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}
