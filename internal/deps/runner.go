package deps

import (
	"errors"
	"os/exec"
	"strings"
)

// CommandRunner runs an external command (the detected package manager) and
// returns its combined stdout+stderr and any error. It is the single injection
// point that lets tests drive Install / ReDumpManifest with a fake runner — no
// real package manager is ever invoked under test.
//
// Implementations MUST resolve the binary through PATH (not an absolute prefix)
// so a stubbed manager earlier on PATH shadows a real install. The args slice is
// the command and its arguments (args[0] is the program name, e.g. "brew").
type CommandRunner interface {
	Run(args ...string) (output string, err error)
}

// ExecRunner is the production CommandRunner: it shells out via os/exec,
// resolving the program through PATH. The package manager binary name (e.g.
// "brew", "apt-get") is args[0]; PATH lookup is what makes the manager-present
// model work and what lets the eval harness shadow brew with a stub.
type ExecRunner struct{}

// Run executes args[0] with the remaining args and returns combined output.
func (ExecRunner) Run(args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("deps: Run called with no command")
	}
	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // args[0] is a fixed manager name, not user input
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// joinArgs renders args for log/report messages without quoting subtleties.
func joinArgs(args []string) string { return strings.Join(args, " ") }
