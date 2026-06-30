package terminal

import (
	"bytes"
	"errors"
	"os/exec"
)

// Runner is the injectable seam over the `defaults` binary. Production code uses
// ExecRunner (real `defaults`); tests pass a fake that records invocations and
// never shells out. args are the arguments to `defaults` (e.g.
// {"export", "com.apple.Terminal", "-"}); stdin is fed to the process (used by
// `defaults import <domain> -`); the returned stdout is the captured output
// (used by `defaults export <domain> -`).
type Runner interface {
	Run(stdin []byte, args ...string) (stdout []byte, err error)
}

// ErrNotDarwin is returned by Apply/Backup/Restore on a non-darwin host. It is a
// clean skip signal, never an internal failure — callers report it as "macOS
// only; skipped on this platform".
var ErrNotDarwin = errors.New("terminal: macOS only; skipped on this platform")

// ExecRunner shells out to the real `defaults` binary via PATH lookup.
type ExecRunner struct{}

// Run invokes `defaults <args...>`, feeding stdin to the process and returning
// its stdout. A non-zero exit surfaces as an error including stderr.
func (ExecRunner) Run(stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.Command("defaults", args...)
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if errBuf.Len() > 0 {
			return nil, errors.New("defaults: " + errBuf.String())
		}
		return nil, err
	}
	return out.Bytes(), nil
}
