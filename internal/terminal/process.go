package terminal

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrITerm2Running is returned by the iTerm2 domain's Apply AND Restore when
// iTerm2 is currently running. Mutating the global preference plist while iTerm2
// runs is silently lost: iTerm2 holds its preferences in memory and REWRITES
// com.googlecode.iterm2 on quit, and cfprefsd caches the domain, so a
// `defaults import`/`delete` while it runs is overwritten the moment the app
// quits. Apply and Restore therefore REFUSE (a clean skip, not a failure) and ask
// the user to quit iTerm2 first — mirroring ErrNotDarwin's clean-skip contract.
//
// Its concrete type additionally satisfies backup's structural resource-skip
// contract (ResourceRestoreSkipped), so when Restore refuses because iTerm2 is
// running the restore/rollback engine treats it as a CLEAN SKIP (continue past
// this one domain) rather than a hard failure that aborts a multi-domain restore.
// errors.Is(err, ErrITerm2Running) still holds (a single comparable sentinel
// value), so every existing errors.Is check is unchanged.
var ErrITerm2Running error = iterm2RunningError{}

// iterm2RunningError is ErrITerm2Running's concrete type. It is an empty struct so
// the sentinel stays comparable (errors.Is works by value equality), and it
// implements ResourceRestoreSkipped so backup can recognise a Restore refusal as a
// skip WITHOUT the backup package importing this one.
type iterm2RunningError struct{}

func (iterm2RunningError) Error() string {
	return "terminal: iTerm2 is running; quit it before applying or restoring its global preferences"
}

// ResourceRestoreSkipped reports that a Restore returning this error DECLINED the
// restore (iTerm2 running) rather than failing — the signal backup's
// ResourceRestoreSkipper interface reads to skip-and-continue instead of aborting.
func (iterm2RunningError) ResourceRestoreSkipped() bool { return true }

// ProcessController is the injectable seam over the iTerm2 lifecycle checks the
// global-plist apply needs — is iTerm2 running, and flush cfprefsd's cache after
// an import. It is separate from Runner (which shells `defaults`) because these
// operate on PROCESSES (`pgrep`/`killall`), not the preference tool. Production
// uses ExecProcessController; tests pass a fake that records calls and never
// shells out, so the running-guard and the cache flush are exercised
// deterministically on any host.
type ProcessController interface {
	// Running reports whether iTerm2 is currently running. A true result blocks
	// the global-plist import (see ErrITerm2Running).
	Running() (bool, error)
	// FlushPrefsCache restarts cfprefsd (`killall cfprefsd`) so a freshly imported
	// preference domain is not masked by the daemon's in-memory cache. It is
	// best-effort: "no matching processes" (cfprefsd not running) is a success.
	FlushPrefsCache() error
}

// ExecProcessController is the production ProcessController: it probes iTerm2 via
// `pgrep` and flushes the preferences cache via `killall cfprefsd`. Both are
// darwin tools; callers only construct it on the darwin apply path (the domain
// itself is darwin-guarded via ErrNotDarwin).
type ExecProcessController struct{}

// Running reports whether an iTerm2 process is alive via `pgrep -x iTerm2`. A
// zero exit (match) is running; exit 1 (no match) is not running; any other
// outcome (e.g. exit 2, or pgrep missing) is surfaced as an error so apply can
// fail closed rather than assume "not running" and import into a live iTerm2.
func (ExecProcessController) Running() (bool, error) {
	err := exec.Command("pgrep", "-x", "iTerm2").Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("pgrep iTerm2: %w", err)
}

// FlushPrefsCache runs `killall cfprefsd`. cfprefsd is normally running, so this
// usually succeeds; when it is not running `killall` exits 1 with "No matching
// processes", which is treated as success (there is nothing to flush). Any other
// failure is surfaced, but the caller treats a flush failure as non-fatal — the
// import already landed on disk.
func (ExecProcessController) FlushPrefsCache() error {
	out, err := exec.Command("killall", "cfprefsd").CombinedOutput()
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(string(out)), "no matching process") {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		// killall exits 1 when it matched nothing to signal — nothing to flush.
		return nil
	}
	return fmt.Errorf("killall cfprefsd: %w", err)
}
