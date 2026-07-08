package keybindings

import (
	"errors"
	"os/exec"
	"strings"

	"github.com/REPPL/ferry/internal/platform"
)

// ErrNotDarwin is returned by Linter.Lint on a non-macOS host: `plutil` ships
// only with macOS, so the property-list lint is skipped cleanly there (never an
// internal failure). Callers treat it as "lint not run on this platform" and
// proceed — the pure-Go hygiene checks (bplist00 header, UTF-8/BOM) still run on
// every platform. It mirrors terminal.ErrNotDarwin's clean-skip contract.
var ErrNotDarwin = errors.New("keybindings: plutil lint is macOS only; skipped on this platform")

// Linter is the injectable seam over `plutil -lint`. Production uses
// PlutilLinter (the real tool on macOS); tests pass a fake that never shells out
// so the darwin-only path is exercised deterministically on any host.
type Linter interface {
	// Lint validates the property-list file at path. A nil error means the file
	// is a well-formed plist; ErrNotDarwin means the check was skipped (non-macOS
	// host); any other error is a lint failure carrying the tool's message.
	Lint(path string) error
}

// PlutilLinter validates a property-list file with the real `plutil -lint`. It
// is darwin-only: on any other platform it returns ErrNotDarwin (a clean skip)
// rather than shelling out to a binary that does not exist there. It NEVER runs
// `plutil -convert` — convert would rewrite the readable old-style dict into
// xml1/binary1/json and destroy the reviewable diff.
type PlutilLinter struct{}

// Lint runs `plutil -lint <path>`. A zero exit is a valid plist (nil error); a
// non-zero exit surfaces plutil's own message so the user can fix the source.
func (PlutilLinter) Lint(path string) error {
	if !platform.IsDarwin() {
		return ErrNotDarwin
	}
	out, err := exec.Command("plutil", "-lint", path).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}
