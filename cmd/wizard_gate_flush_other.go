//go:build !darwin && !linux

package cmd

import "errors"

// flushTTYInputQueue has no implementation on this platform (ferry ships
// darwin/linux); reporting an error makes the drain skip the flush, and the
// typed gate still fails safe on a polluted line.
func flushTTYInputQueue(fd int) error {
	return errors.New("tty input-queue flush unsupported on this platform")
}
