package deps

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
//
// The ROOT RAIL (apt-get, dpkg-query — both run as root under `sudo ferry apply
// --deps` / `restore --packages`) is resolved to a TRUSTED absolute path through
// the lookManager seam, NOT the inherited $PATH: a `sudo` configured without
// secure_path leaves the caller's $PATH in place, so a $PATH-resolved root rail
// could be hijacked by a poisoned PATH entry that then executes as root. brew is
// NOT a root rail (Homebrew refuses to run as root) and stays $PATH-resolved so
// the eval harness can shadow it with a stub.
func (ExecRunner) Run(args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("deps: Run called with no command")
	}
	prog := args[0]
	if isRootRailManager(prog) {
		resolved, err := lookManager(prog)
		if err != nil {
			return "", err
		}
		prog = resolved
	}
	cmd := exec.Command(prog, args[1:]...) //nolint:gosec // prog is a fixed manager name (root rail: sanitized-absolute), not user input
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// lookManager resolves a ROOT-RAIL manager binary (apt-get / dpkg-query) to a
// trusted absolute path. It is a package-var seam: unit tests reassign it to a
// shim; production keeps rootRailLookPath, which searches a SANITIZED directory
// list rather than the inherited $PATH (see ExecRunner.Run).
var lookManager = rootRailLookPath

// rootRailDirs is the sanitized search path for the apt root rail: the standard
// system binary directories, in order. It deliberately excludes any caller-
// supplied $PATH entry so a hijacked PATH cannot shadow apt-get/dpkg-query.
var rootRailDirs = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// rootRailLookPath returns the first executable named name found under
// rootRailDirs, or an error if none is present.
func rootRailLookPath(name string) (string, error) {
	for _, dir := range rootRailDirs {
		cand := filepath.Join(dir, name)
		fi, err := os.Stat(cand)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		return cand, nil
	}
	return "", fmt.Errorf("deps: %q not found on the sanitized root rail (%s)", name, strings.Join(rootRailDirs, string(os.PathListSeparator)))
}

// isRootRailManager reports whether name is an apt root-rail binary that must be
// resolved through the sanitized seam rather than the inherited $PATH.
func isRootRailManager(name string) bool {
	return name == aptGetBin || name == dpkgQueryBin
}

// joinArgs renders args for log/report messages without quoting subtleties.
func joinArgs(args []string) string { return strings.Join(args, " ") }
