package cmd

// The UN-PHANTOMABLE consent gates (v0.3.2 field-critical fix): every
// bubbletea program the wizard runs emits terminal capability queries; real
// terminals REPLY on stdin, and replies that land after one short-lived
// program exits are parsed as KEYSTROKES by the next (bubbletea#1627 leak
// class). DECRPM replies even end in `$y` — the Accept key of a huh Confirm.
// Safety-relevant decisions (the final write, the drop consent) therefore
// never ride a TUI keystroke: they require the word "yes" TYPED on a plain
// line read (trimmed, case-insensitive), with the tty's pending input FLUSHED
// first. No terminal reply contains a newline, so a straggler reply can only
// POLLUTE the typed line — which then fails safe (anything but "yes" aborts).

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// wizardDrainSettle is the pause between the two input-queue flushes: replies
// still in flight from the just-exited TUI program get a window to arrive and
// be discarded by the second flush.
const wizardDrainSettle = 50 * time.Millisecond

// confirmTypedWrite is the wizard's final, safety-critical write confirmation.
// Accepted answer: the word "yes" on its own line — trimmed and
// case-insensitive ("yes", "YES", " Yes "); EOF, an empty line, or a
// leaked-reply-polluted line aborts. tty is the raw stdin file when it is a
// terminal (flush target); nil skips the flush (pipes never answer
// capability queries).
func confirmTypedWrite(in *bufio.Reader, tty *os.File, out io.Writer, what string) bool {
	return confirmTypedLine(in, tty, out,
		fmt.Sprintf("%s\nType \"yes\" to write (anything else aborts): ", what))
}

// confirmTypedDrop is the same un-phantomable typed consent for per-block
// DROP routing: it runs immediately after the choices form completes, so a
// decline aborts before any further step.
func confirmTypedDrop(in *bufio.Reader, tty *os.File, out io.Writer, dropped int) bool {
	return confirmTypedLine(in, tty, out,
		fmt.Sprintf("%d block(s) will be REMOVED from the deployed config (they remain in your ~/.zshrc backup).\nType \"yes\" to continue (anything else aborts): ", dropped))
}

// confirmTypedLine drains pending input, prints the prompt, and requires a
// trimmed, case-insensitive "yes" line.
func confirmTypedLine(in *bufio.Reader, tty *os.File, out io.Writer, prompt string) bool {
	drainPendingInput(in, tty, wizardDrainSettle)
	fmt.Fprint(out, prompt)
	line, err := in.ReadString('\n')
	ans := strings.TrimSpace(line)
	if err != nil && ans == "" {
		return false
	}
	return strings.EqualFold(ans, "yes")
}

// drainPendingInput clears input that predates the prompt: first whatever the
// shared bufio.Reader has already buffered (always — pipes and ttys alike),
// then, for a real tty, the KERNEL input queue via a termios flush
// (flush -> settle -> flush, so replies still in flight from the just-exited
// TUI program are caught too). The termios flush is the load-bearing piece on
// real terminals: stdio fds are not poller-registered, so read deadlines are
// unavailable, and in cooked mode the line discipline withholds newline-less
// reply bytes from read(2) anyway — only a queue flush removes them. On a
// non-tty (pipes, evals) the flush is skipped: there is no terminal on the
// other end to answer capability queries, and the buffered-discard plus the
// gate's fail-safe line comparison cover leftover bytes.
func drainPendingInput(in *bufio.Reader, tty *os.File, settle time.Duration) {
	for in.Buffered() > 0 {
		_, _ = in.Discard(in.Buffered())
	}
	if tty == nil {
		return
	}
	fd := int(tty.Fd())
	if err := flushTTYInputQueue(fd); err != nil {
		return // not a tty (or unsupported platform): nothing more to flush
	}
	time.Sleep(settle)
	_ = flushTTYInputQueue(fd)
}

// wizardStdinTTY returns the raw stdin file for flushing when stdin is an
// interactive terminal, nil otherwise.
func wizardStdinTTY() *os.File {
	if stdinIsTerminal() {
		return os.Stdin
	}
	return nil
}
