package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/REPPL/ferry/internal/sshguard"
)

// isContainmentRefusal reports whether err is a resolved-containment refusal —
// the target escapes $HOME or reaches ~/.ssh. Full restore uses it to SKIP such
// an entry (surfacing the error) rather than write through the redirected path,
// while other errors still abort the restore.
func isContainmentRefusal(err error) bool {
	return errors.Is(err, sshguard.ErrPathEscapesHome) || errors.Is(err, sshguard.ErrForbiddenSSHPath)
}

// guardResolvedContainment re-checks, at the WRITE BOUNDARY, that absPath's
// PARENT chain still resolves strictly within $HOME and not at/under ~/.ssh,
// with symlinks resolved. It closes the TOCTOU window in which a same-user
// process swaps an intermediate parent to a symlink AFTER plan time but BEFORE
// the engine mutates absPath — a swap that would otherwise redirect the
// delete+write outside $HOME or into ~/.ssh. It reuses the single shared
// sshguard.ResolvedContainment guard, the same one the dotfile deploy-target
// boundary runs at plan time, so the check is identical wherever it fires.
//
// A path that is NOT under $HOME — a test's t.TempDir() state root, or any
// engine used with an explicit non-home root — has no $HOME-anchored chain to
// validate and is accepted unchanged, mirroring paths.HardenStoreDir. This keeps
// the real-home write paths hardened while explicit-root tests keep working.
func guardResolvedContainment(absPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	home = filepath.Clean(home)
	clean := filepath.Clean(absPath)
	rel, err := filepath.Rel(home, clean)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// Not under $HOME (or is $HOME itself): no $HOME-anchored chain to guard.
		return nil
	}
	return sshguard.ResolvedContainment(home, clean)
}
