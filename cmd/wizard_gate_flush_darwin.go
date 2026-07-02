//go:build darwin

package cmd

import "golang.org/x/sys/unix"

// bsdFREAD is <sys/fcntl.h> FREAD (0x0001) — the TIOCFLUSH argument selecting
// the INPUT queue. x/sys/unix does not export it for darwin.
const bsdFREAD = 0x0001

// flushTTYInputQueue discards the KERNEL tty input queue on the fd (BSD
// TIOCFLUSH with FREAD): one syscall, works in cooked AND raw mode, and —
// unlike a read()-based drain — also removes newline-less bytes the line
// discipline is still withholding from read(2). Errors (ENOTTY on a pipe)
// tell the caller there is no tty queue to flush.
func flushTTYInputQueue(fd int) error {
	return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, bsdFREAD)
}
