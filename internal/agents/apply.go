package agents

import (
	"os"

	"github.com/REPPL/ferry/internal/dotfile"
)

// instrPerm / hookPerm are the modes for a first-ever write of an agents
// target: instruction/asset files are plain 0644; a source with an executable
// bit (hooks are scripts) materialises 0755 so it stays runnable. An existing
// destination's mode is preserved by the shared apply core, matching the
// dotfile domain.
const (
	instrPerm os.FileMode = 0o644
	hookPerm  os.FileMode = 0o755
)

// ApplyItem materialises one planned agents item onto its $HOME destination
// through the dotfile domain's OWN apply core (dotfile.ApplyContentDeferred):
// the item's derived Content stands in for the repo side, and the three-way
// decision table, first-touch adoption, conflict refusal, the
// empty-over-substantial data-loss guard, and the Backuper-mediated atomic
// write are all the shared machinery — no parallel state machine. The only
// domain-specific input is the fresh-write mode (0755 for executable hooks,
// 0644 otherwise); the caller supplies the domain's repo-authoritative
// wording:
//
//   - locally-drifted returns ActionSkipped without force — the domain is
//     repo-authoritative, but ferry still never silently discards a live edit;
//   - conflict returns *dotfile.ConflictError unless force.
//
// Writes never persist last-applied directly: the hash rides on
// Result.PendingHash for the caller to commit AFTER the journal commit
// (dotfile.CommitLastApplied), so a crash can never leave last-applied ahead
// of a rolled-back file.
func ApplyItem(it Item, store *dotfile.Store, b dotfile.Backuper, force bool) (dotfile.Result, error) {
	perm := instrPerm
	if it.Exec {
		perm = hookPerm
	}
	return dotfile.ApplyContentDeferred(it.Target, it.Content, perm, store, b, force)
}
