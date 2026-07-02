//go:build linux

package cmd

import "golang.org/x/sys/unix"

// flushTTYInputQueue discards the KERNEL tty input queue on the fd (TCFLSH
// with TCIFLUSH): one syscall, works in cooked AND raw mode, and — unlike a
// read()-based drain — also removes newline-less bytes the line discipline is
// still withholding from read(2). Errors (ENOTTY on a pipe) tell the caller
// there is no tty queue to flush.
func flushTTYInputQueue(fd int) error {
	return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
}
