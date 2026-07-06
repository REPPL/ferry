package termcfg

import (
	"os"

	"github.com/REPPL/ferry/internal/dotfile"
)

// filePerm / execPerm are the modes for a first-ever write of a terminal config
// file: plain 0644, or 0755 when the repo source carries an executable bit (a
// terminal config tree may ship a helper script). An existing destination's mode
// is preserved by the shared apply core, matching the dotfile domain.
const (
	filePerm os.FileMode = 0o644
	execPerm os.FileMode = 0o755
)

// ApplyItem materialises one planned terminal item onto its $HOME destination
// through the dotfile domain's OWN apply core (dotfile.ApplyContentDeferred):
// the item's overlay-or-shared Content stands in for the repo side, and the
// three-way decision table, first-touch adoption, conflict refusal, the
// empty-over-substantial data-loss guard, and the Backuper-mediated atomic
// write are all the shared machinery — no parallel state machine. Terminal
// config is carried LIKE A DOTFILE: a locally-drifted target is a capture
// candidate (ActionSkipped without force), not repo-authoritative.
//
// Writes never persist last-applied directly: the hash rides on
// Result.PendingHash for the caller to commit AFTER the journal commit
// (dotfile.CommitLastApplied), so a crash can never leave last-applied ahead of
// a rolled-back file.
func ApplyItem(it Item, store *dotfile.Store, b dotfile.Backuper, force bool) (dotfile.Result, error) {
	perm := filePerm
	if it.Exec {
		perm = execPerm
	}
	// A secret-routed target's Content is already-rendered plaintext credentials.
	// Declare the routing to the apply core (it.SecretRouted): it strips group/other
	// access from the written mode — even when preserving an existing file's mode —
	// so the credential is never world-/group-readable (0644 -> 0600, an executable
	// 0755 -> 0700, keeping the exec bit), and records only the content hash.
	return dotfile.ApplyContentDeferred(it.Target, it.Content, perm, store, b, force, it.SecretRouted)
}
