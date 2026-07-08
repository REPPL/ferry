package iterm2profiles

import (
	"errors"
	"os/exec"
	"strings"

	"github.com/REPPL/ferry/internal/platform"
)

// ErrNotDarwin is returned by Linter.Lint on a non-macOS host: `plutil` ships only
// with macOS, so the property-list validation is skipped cleanly there (the pure-Go
// encoding/json validity check still runs on every platform). It mirrors
// terminal.ErrNotDarwin's and keybindings.ErrNotDarwin's clean-skip contract.
//
// NOTE (consolidation): this Linter/PlutilLinter pair mirrors the one in
// internal/keybindings, but validates DIFFERENTLY on purpose. `plutil -lint`
// guesses the OLD-STYLE (NeXTSTEP) parser for `{`-leading content and REJECTS valid
// JSON, so a Dynamic Profile is validated with `plutil -convert xml1 -o /dev/null`
// (which does parse JSON) instead. keybindings' dict is genuinely old-style and uses
// `-lint`. A third plutil consumer should hoist a shared helper parametrised by the
// check rather than copy either.
var ErrNotDarwin = errors.New("iterm2profiles: plutil validation is macOS only; skipped on this platform")

// Linter is the injectable seam over the macOS `plutil` property-list validator.
// Production uses PlutilLinter (the real tool on macOS); tests pass a fake so the
// darwin-only path is exercised deterministically on any host.
type Linter interface {
	// Lint validates the JSON property-list file at path. nil means well-formed;
	// ErrNotDarwin means the check was skipped (non-macOS host); any other error is
	// a validation failure carrying the tool's message.
	Lint(path string) error
}

// PlutilLinter validates a Dynamic Profile with the real `plutil`. It is
// darwin-only: on any other platform it returns ErrNotDarwin (a clean skip).
type PlutilLinter struct{}

// Lint runs `plutil -convert xml1 -o /dev/null <path>`: it parses the file through
// plutil's property-list reader (which accepts JSON) and discards the output, so a
// zero exit means the JSON is a well-formed property list and a non-zero exit
// surfaces plutil's own message. `-lint` is NOT used: it rejects valid JSON.
func (PlutilLinter) Lint(path string) error {
	if !platform.IsDarwin() {
		return ErrNotDarwin
	}
	out, err := exec.Command("plutil", "-convert", "xml1", "-o", "/dev/null", path).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}
