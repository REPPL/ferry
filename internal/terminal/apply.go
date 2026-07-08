package terminal

import (
	"errors"

	"github.com/REPPL/ferry/internal/platform"
)

// ApplyResult reports the outcome of applying a terminal preference domain.
// Skipped is true (with ErrNotDarwin in Err) when run on a non-darwin host —
// a clean skip, not a failure. Note carries the relaunch/cfprefsd caveat.
type ApplyResult struct {
	Domain  string
	Applied bool
	Skipped bool
	Note    string
	Err     error
}

// Apply mutates the live preference domain through the domain's runner. The
// engine MUST have already captured the pre-mutation state via
// backup.BackupResource(run, d.Domain()) so a failure here can be rolled back
// via Restore.
//
// Order at the command layer (darwin):
//  1. engine.BackupResource(run, d.Domain())  // export current state first
//  2. terminal.Apply(d)                        // mutate via defaults write/import
//  3. on error, engine restore re-imports the captured blob via d.Restore
//
// On a non-darwin host Apply is a clean skip: it returns
// {Skipped: true, Err: ErrNotDarwin} and never shells out.
func Apply(d *PreferenceDomain) ApplyResult {
	if !platform.IsDarwin() {
		return ApplyResult{Domain: d.domain, Skipped: true, Err: ErrNotDarwin}
	}
	if err := d.apply(d.runner); err != nil {
		// A running iTerm2 is a clean SKIP, not a failure: importing would be
		// silently lost on quit. Report it as skipped (Applied=false) so the caller
		// leaves live config intact and never rolls back (nothing was imported).
		if errors.Is(err, ErrITerm2Running) {
			return ApplyResult{Domain: d.domain, Skipped: true, Err: err}
		}
		return ApplyResult{Domain: d.domain, Err: err, Note: d.note}
	}
	return ApplyResult{Domain: d.domain, Applied: true, Note: d.note}
}
