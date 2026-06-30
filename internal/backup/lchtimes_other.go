//go:build !darwin && !linux

package backup

import "time"

// lchtimes is a best-effort no-op on platforms without a NOFOLLOW time-set
// primitive wired up here. ferry's supported platforms are darwin and linux (see
// lchtimes_unix.go, which restamps the symlink's own mtime via unix.Lutimes); on
// any other GOOS a restored symlink keeps its freshly-created mtime. Target and
// creation are still preserved by restoreState.
func lchtimes(path string, mt time.Time) error { return nil }
