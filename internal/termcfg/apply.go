package termcfg

import "os"

// filePerm / execPerm are the modes for a first-ever write of a terminal config
// file: plain 0644, or 0755 when the repo source carries an executable bit (a
// terminal config tree may ship a helper script). An existing destination's mode
// is preserved by the shared apply core (dotfile.ApplyContentDeferred), matching
// the dotfile domain. Terminal items reach that core directly through the
// converged kindFile apply arm; these consts name the first-write mode policy the
// mode-clamp regression tests validate.
const (
	filePerm os.FileMode = 0o644
	execPerm os.FileMode = 0o755
)
