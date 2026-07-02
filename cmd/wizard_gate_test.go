package cmd

// v0.3.2 field-critical regression tests: the wizard's WRITE decision must be
// un-phantomable. Leaked terminal capability-query replies (DECRPM ends in
// `$y` — huh Confirm's Accept key) must never pass the typed gate; on a REAL
// tty the pre-prompt drain is a termios input-queue flush (read deadlines are
// unsupported on stdio fds, and cooked mode withholds newline-less bytes from
// read(2) — a pipe cannot exercise that path, so the flush arms run on a
// creack/pty pair and the pipe arms pin exactly what pipes DO exercise: the
// bufio-discard and the gate's fail-safe line comparison).

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// decrpmGarbage is the reply shape a real terminal sends to the mode-2026/2027
// queries the TUI programs emit (the exact byte class from the field report).
const decrpmGarbage = "\x1b[?2026;0$y\x1b[?2027;0$y\x1b[?u\x1b[>4;m"

// watchdog force-closes f after d so a stalled blocking read becomes a test
// FAILURE (the read returns and the assertion fires) instead of a package
// timeout. Returns a stop func for the happy path.
func watchdog(t *testing.T, f *os.File, d time.Duration) func() {
	t.Helper()
	timer := time.AfterFunc(d, func() {
		t.Errorf("watchdog fired after %s: a blocking read stalled; force-closing %s", d, f.Name())
		f.Close()
	})
	return func() { timer.Stop() }
}

// The gate refuses a stdin stream of leaked terminal replies followed by EOF:
// NO write, clean abort (the phantom-accept repro).
func TestConfirmTypedWrite_LeakedRepliesAbort(t *testing.T) {
	var out bytes.Buffer
	in := bufio.NewReader(strings.NewReader(decrpmGarbage))
	if confirmTypedWrite(in, nil, &out, "Write this plan?") {
		t.Fatal("DECRPM-shaped garbage passed the typed write gate (phantom confirm)")
	}
	if !strings.Contains(out.String(), `Type "yes" to write`) {
		t.Errorf("gate did not print the typed-confirmation prompt:\n%s", out.String())
	}

	// The killer detail: a bare `y` (huh's Accept key, the tail of every DECRPM
	// reply) must NOT be accepted — only the full word "yes".
	if confirmTypedWrite(bufio.NewReader(strings.NewReader("y\n")), nil, &out, "Write?") {
		t.Fatal("a bare 'y' passed the typed gate — the DECRPM tail byte would phantom-confirm")
	}
	// Empty stdin / immediate EOF aborts.
	if confirmTypedWrite(bufio.NewReader(strings.NewReader("")), nil, &out, "Write?") {
		t.Fatal("EOF passed the typed gate")
	}
	// A reply-polluted line that HAPPENS to contain yes-adjacent bytes aborts
	// (the whole trimmed line must equal "yes").
	if confirmTypedWrite(bufio.NewReader(strings.NewReader(decrpmGarbage+"yes\n")), nil, &out, "Write?") {
		t.Fatal("a polluted line containing 'yes' passed the typed gate (must fail safe)")
	}
}

// The gate accepts a typed "yes" — trimmed, case-insensitive — and nothing else.
func TestConfirmTypedWrite_TypedYesProceeds(t *testing.T) {
	var out bytes.Buffer
	for _, yes := range []string{"yes\n", "YES\n", "  Yes  \n"} {
		if !confirmTypedWrite(bufio.NewReader(strings.NewReader(yes)), nil, &out, "Write?") {
			t.Fatalf("%q was refused (trimmed, case-insensitive contract)", yes)
		}
	}
	for _, no := range []string{"no\n", "n\n", "yess\n", " \n", "q\n"} {
		if confirmTypedWrite(bufio.NewReader(strings.NewReader(no)), nil, &out, "Write?") {
			t.Fatalf("%q passed the typed gate", strings.TrimSpace(no))
		}
	}
	// The drop-consent gate shares the same core.
	if confirmTypedDrop(bufio.NewReader(strings.NewReader("y\n")), nil, &out, 2) {
		t.Fatal("a bare 'y' passed the drop-consent gate")
	}
	if !confirmTypedDrop(bufio.NewReader(strings.NewReader("yes\n")), nil, &out, 2) {
		t.Fatal("a typed 'yes' was refused by the drop-consent gate")
	}
	if !strings.Contains(out.String(), "2 block(s) will be REMOVED") {
		t.Errorf("drop-consent prompt does not name the dropped count:\n%s", out.String())
	}
}

// PIPE ARM — pins what a pipe actually exercises: there is NO tty input queue
// to flush (the termios call fails with ENOTTY), so pre-prompt garbage stays
// on the line and the gate FAILS SAFE: the garbage-prefixed line is not "yes".
func TestConfirmTypedWrite_PipeGarbagePrefixedLineRefused(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	stop := watchdog(t, w, 5*time.Second)
	defer stop()

	go func() {
		defer w.Close()
		_, _ = w.WriteString(decrpmGarbage)
		time.Sleep(4 * wizardDrainSettle)
		_, _ = w.WriteString("yes\n") // completes the polluted line
	}()

	var out bytes.Buffer
	if confirmTypedWrite(bufio.NewReader(r), r, &out, "Write this plan?") {
		t.Fatal("a garbage-prefixed line passed the typed gate on a pipe (must fail safe)")
	}
}

// PIPE ARM — the bufio-discard half of the drain: bytes the shared reader has
// ALREADY BUFFERED before the prompt are always cleared, on every fd type.
func TestDrainPendingInput_DiscardsBufferedBytes(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	stop := watchdog(t, w, 5*time.Second)
	defer stop()

	if _, err := w.WriteString(decrpmGarbage); err != nil {
		t.Fatal(err)
	}
	in := bufio.NewReader(r)
	// Force the reader to buffer the pending garbage (as a prior shared read
	// would), then drain: the buffered bytes must be gone.
	if _, err := in.Peek(1); err != nil {
		t.Fatal(err)
	}
	if in.Buffered() == 0 {
		t.Fatal("setup: garbage was not buffered")
	}
	drainPendingInput(in, r, wizardDrainSettle)
	if in.Buffered() != 0 {
		t.Fatalf("drain left %d buffered bytes", in.Buffered())
	}
	go func() {
		defer w.Close()
		_, _ = w.WriteString("later\n")
	}()
	line, _ := in.ReadString('\n')
	if strings.TrimSpace(line) != "later" {
		t.Errorf("post-drain read = %q, want the fresh line", line)
	}
}

// PTY ARM — the real flush path (the field platform's fd type): newline-less
// reply bytes sit in the KERNEL input queue, withheld from read(2) by the
// cooked-mode line discipline; the termios flush discards them, and a "yes"
// typed after the drain window passes the gate clean.
func TestConfirmTypedWrite_PTYFlushThenYesProceeds(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available in this environment: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	stop := watchdog(t, master, 10*time.Second)
	defer stop()

	// Leaked replies land in the slave's input queue before the gate runs.
	if _, err := master.WriteString(decrpmGarbage); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // let the bytes reach the queue

	go func() {
		// The human types "yes" AFTER the flush->settle->flush window.
		time.Sleep(4 * wizardDrainSettle)
		_, _ = master.WriteString("yes\n")
	}()

	var out bytes.Buffer
	if !confirmTypedWrite(bufio.NewReader(slave), slave, &out, "Write this plan?") {
		t.Fatal("the flush did not clear the leaked replies: the human's real 'yes' arrived garbage-prefixed and was refused (the field repro)")
	}
}

// PTY ARM — drainPendingInput alone: the queue is flushed even though nothing
// is readable via read(2) (no newline yet), and only post-drain input remains.
func TestDrainPendingInput_FlushesTTYInputQueue(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available in this environment: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	stop := watchdog(t, master, 10*time.Second)
	defer stop()

	if _, err := master.WriteString(decrpmGarbage); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)

	in := bufio.NewReader(slave)
	drainPendingInput(in, slave, wizardDrainSettle)

	go func() { _, _ = master.WriteString("later\n") }()
	line, err := in.ReadString('\n')
	if err != nil {
		t.Fatalf("read after drain: %v", err)
	}
	if strings.TrimSpace(line) != "later" {
		t.Errorf("queue flush left pre-drain bytes on the line: got %q", line)
	}
}
