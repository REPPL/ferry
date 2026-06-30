//go:build darwin || linux

package backup

import (
	"time"

	"golang.org/x/sys/unix"
)

// lchtimes sets path's modification (and access) time WITHOUT following a final
// symlink, so a symlink's OWN mtime is restamped rather than its target's. The
// stdlib os.Chtimes follows symlinks, so this uses unix.Lutimes (lutimes /
// utimensat with AT_SYMLINK_NOFOLLOW), available on both supported platforms
// (darwin + linux). atime is set to the same instant as mtime; only mtime is
// observable in the "leaves no trace" promise.
func lchtimes(path string, mt time.Time) error {
	tv := []unix.Timeval{
		unix.NsecToTimeval(mt.UnixNano()),
		unix.NsecToTimeval(mt.UnixNano()),
	}
	return unix.Lutimes(path, tv)
}
