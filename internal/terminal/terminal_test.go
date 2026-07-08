package terminal

import (
	"errors"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/platform"
)

// fakeRunner records every `defaults` invocation and returns canned output,
// so tests never shell out to the real `defaults` binary.
type fakeRunner struct {
	calls  []call
	output []byte // returned as stdout from Run
	err    error  // returned as the error from Run
}

type call struct {
	stdin []byte
	args  []string
}

func (f *fakeRunner) Run(stdin []byte, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{stdin: stdin, args: args})
	return f.output, f.err
}

// last returns the most recent recorded call.
func (f *fakeRunner) last() call { return f.calls[len(f.calls)-1] }

// fakeProc is a ProcessController stub: it reports a canned running state and
// records whether the cfprefsd flush was invoked, so the iTerm2 running-guard and
// cache flush are exercised without shelling out to pgrep/killall.
type fakeProc struct {
	running  bool
	runErr   error
	flushErr error
	flushed  bool
}

func (f *fakeProc) Running() (bool, error) { return f.running, f.runErr }
func (f *fakeProc) FlushPrefsCache() error { f.flushed = true; return f.flushErr }

// newITerm2 builds an iTerm2 domain for tests with a not-running process stub, so
// existing backup/restore/plan tests need not care about the running-guard.
func newITerm2(blob []byte, r Runner) *PreferenceDomain {
	return NewITerm2(blob, r, &fakeProc{})
}

func TestBackupExportsDomain(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Backup is darwin-only; covered by TestNonDarwinSkips on this host")
	}
	fr := &fakeRunner{output: []byte("<plist><dict><key>X</key></dict></plist>")}
	d := NewAppleTerminal(nil, fr)

	blob, absent, err := d.Backup()
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if absent {
		t.Fatal("Backup reported absent for a domain with content")
	}
	if string(blob) != "<plist><dict><key>X</key></dict></plist>" {
		t.Fatalf("blob = %q, want exported bytes", blob)
	}
	c := fr.last()
	wantArgs := []string{"export", AppleTerminalDomain, "-"}
	if !equalArgs(c.args, wantArgs) {
		t.Fatalf("Backup args = %v, want %v", c.args, wantArgs)
	}
}

// TestBackupAbsentDomainReportsAbsent covers the fresh-machine case: `defaults
// export` for a never-configured domain errors with "does not exist". Backup
// must report absent=true (a normal pre-ferry state), NOT a fatal error.
func TestBackupAbsentDomainReportsAbsent(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Backup is darwin-only")
	}
	fr := &fakeRunner{err: ErrDomainAbsent}
	d := newITerm2(nil, fr)

	blob, absent, err := d.Backup()
	if err != nil {
		t.Fatalf("Backup returned an error for an absent domain: %v", err)
	}
	if !absent {
		t.Fatal("Backup absent = false, want true for a missing domain")
	}
	if blob != nil {
		t.Fatalf("Backup blob = %q, want nil for an absent domain", blob)
	}
}

// TestBackupRealExportErrorIsFatal: a non-absence export failure (e.g.
// permission) is still surfaced as an error.
func TestBackupRealExportErrorIsFatal(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Backup is darwin-only")
	}
	fr := &fakeRunner{err: errors.New("permission denied")}
	d := newITerm2(nil, fr)

	if _, absent, err := d.Backup(); err == nil || absent {
		t.Fatalf("Backup = (absent=%v, err=%v), want a real error", absent, err)
	}
}

func TestRestoreImportsDomain(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Restore is darwin-only; covered by TestNonDarwinSkips on this host")
	}
	fr := &fakeRunner{}
	d := newITerm2(nil, fr)
	blob := []byte("<plist>captured</plist>")

	if err := d.Restore(blob, false); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	c := fr.last()
	wantArgs := []string{"import", ITerm2Domain, "-"}
	if !equalArgs(c.args, wantArgs) {
		t.Fatalf("Restore args = %v, want %v", c.args, wantArgs)
	}
	if string(c.stdin) != string(blob) {
		t.Fatalf("Restore stdin = %q, want the captured blob", c.stdin)
	}
}

// TestRestoreAbsentBaselineDeletesDomain: restoring an absent baseline must
// REMOVE the domain (`defaults delete <domain>`), returning the machine to its
// pre-ferry (absent) state, NOT import a blob.
func TestRestoreAbsentBaselineDeletesDomain(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Restore is darwin-only")
	}
	fr := &fakeRunner{}
	d := newITerm2(nil, fr)

	if err := d.Restore(nil, true); err != nil {
		t.Fatalf("Restore(absent): %v", err)
	}
	c := fr.last()
	wantArgs := []string{"delete", ITerm2Domain}
	if !equalArgs(c.args, wantArgs) {
		t.Fatalf("absent Restore args = %v, want %v", c.args, wantArgs)
	}
}

// TestITerm2RestoreRefusesWhenRunning: Restore must honour the SAME running-guard
// as Apply. With iTerm2 running, restoring the iTerm2 domain would `defaults
// import`/`delete` into a live domain that rewrites itself on quit — silently
// lost. Restore must REFUSE with ErrITerm2Running, shell out to `defaults` zero
// times, and NOT flush cfprefsd. It also must NOT be a hard failure: the error
// reports itself as a resource-restore skip.
func TestITerm2RestoreRefusesWhenRunning(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Restore mutation is darwin-only")
	}
	for _, absent := range []bool{false, true} {
		fr := &fakeRunner{}
		proc := &fakeProc{running: true}
		d := NewITerm2(nil, fr, proc)

		err := d.Restore([]byte("<plist>x</plist>"), absent)
		if !errors.Is(err, ErrITerm2Running) {
			t.Fatalf("Restore(absent=%v) while running = %v, want ErrITerm2Running", absent, err)
		}
		var skip interface{ ResourceRestoreSkipped() bool }
		if !errors.As(err, &skip) || !skip.ResourceRestoreSkipped() {
			t.Fatalf("Restore(absent=%v) error is not a resource-restore skip: %v", absent, err)
		}
		if len(fr.calls) != 0 {
			t.Fatalf("Restore(absent=%v) shelled `defaults` %d times into a running iTerm2, want 0", absent, len(fr.calls))
		}
		if proc.flushed {
			t.Fatalf("Restore(absent=%v) flushed cfprefsd despite refusing", absent)
		}
	}
}

// TestITerm2RestoreProceedsAndFlushesWhenNotRunning: with iTerm2 NOT running,
// Restore imports the captured blob AND flushes cfprefsd (mirroring Apply) so the
// restored values are not masked by the daemon's cache.
func TestITerm2RestoreProceedsAndFlushesWhenNotRunning(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Restore mutation is darwin-only")
	}
	fr := &fakeRunner{}
	proc := &fakeProc{running: false}
	d := NewITerm2(nil, fr, proc)
	blob := []byte("<plist>captured</plist>")

	if err := d.Restore(blob, false); err != nil {
		t.Fatalf("Restore while not running: %v", err)
	}
	c := fr.last()
	if !equalArgs(c.args, []string{"import", ITerm2Domain, "-"}) {
		t.Fatalf("Restore args = %v, want import", c.args)
	}
	if string(c.stdin) != string(blob) {
		t.Fatalf("Restore stdin = %q, want the captured blob", c.stdin)
	}
	if !proc.flushed {
		t.Fatalf("cfprefsd was not flushed after a not-running iTerm2 restore")
	}
}

// TestITerm2RestoreProbeErrorFailsClosed: an inconclusive running probe must fail
// closed (surface the error, mutate nothing) rather than assume "not running" and
// import into a possibly-live iTerm2.
func TestITerm2RestoreProbeErrorFailsClosed(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Restore mutation is darwin-only")
	}
	fr := &fakeRunner{}
	proc := &fakeProc{runErr: errors.New("pgrep exploded")}
	d := NewITerm2([]byte("<plist>x</plist>"), fr, proc)

	if err := d.Restore([]byte("<plist>x</plist>"), false); err == nil {
		t.Fatal("Restore with a probe error returned nil, want a failure")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("Restore shelled `defaults` despite an inconclusive running probe (%d calls)", len(fr.calls))
	}
}

// TestAppleTerminalRestoreUnaffectedByRunningGuard: Apple Terminal carries no
// ProcessController (proc is nil) and no iTerm2 concern, so its Restore imports
// unconditionally and never probes running or flushes cfprefsd — even though a
// hypothetical iTerm2 would be "running" (there is nothing to consult here).
func TestAppleTerminalRestoreUnaffectedByRunningGuard(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Restore mutation is darwin-only")
	}
	fr := &fakeRunner{}
	d := NewAppleTerminal(nil, fr)
	blob := []byte("<plist>terminal</plist>")

	if err := d.Restore(blob, false); err != nil {
		t.Fatalf("Apple Terminal Restore: %v", err)
	}
	c := fr.last()
	if !equalArgs(c.args, []string{"import", AppleTerminalDomain, "-"}) {
		t.Fatalf("Apple Terminal Restore args = %v, want import", c.args)
	}
	if string(c.stdin) != string(blob) {
		t.Fatalf("Apple Terminal Restore stdin = %q, want the captured blob", c.stdin)
	}
}

// TestRestoreAbsentAlreadyMissingIsNoError: deleting an already-absent domain
// (delete reports "does not exist") is a no-op success.
func TestRestoreAbsentAlreadyMissingIsNoError(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Restore is darwin-only")
	}
	fr := &fakeRunner{err: ErrDomainAbsent}
	d := newITerm2(nil, fr)
	if err := d.Restore(nil, true); err != nil {
		t.Fatalf("Restore(absent) on already-missing domain = %v, want nil", err)
	}
}

// TestITerm2ApplyImportsAndFlushes: with iTerm2 NOT running, Apply imports the
// prepared export blob via `defaults import` and then flushes cfprefsd.
func TestITerm2ApplyImportsAndFlushes(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Apply mutation is darwin-only; covered by TestNonDarwinSkips on this host")
	}
	blob := []byte("<plist>global-iterm2</plist>")
	fr := &fakeRunner{}
	proc := &fakeProc{running: false}
	d := NewITerm2(blob, fr, proc)

	res := Apply(d)
	if res.Err != nil {
		t.Fatalf("Apply: %v", res.Err)
	}
	if !res.Applied || res.Skipped {
		t.Fatalf("Apply result = %+v, want Applied", res)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("got %d defaults calls, want 1 (import)", len(fr.calls))
	}
	wantArgs := []string{"import", ITerm2Domain, "-"}
	if !equalArgs(fr.last().args, wantArgs) {
		t.Fatalf("Apply args = %v, want %v", fr.last().args, wantArgs)
	}
	if string(fr.last().stdin) != string(blob) {
		t.Fatalf("Apply stdin = %q, want the rendered export blob", fr.last().stdin)
	}
	if !proc.flushed {
		t.Fatalf("cfprefsd was not flushed after a successful import")
	}
}

// TestITerm2ApplyRefusesWhenRunning: a running iTerm2 must SKIP (ErrITerm2Running),
// never import — a `defaults import` would be silently lost on quit. Nothing is
// imported and cfprefsd is not flushed.
func TestITerm2ApplyRefusesWhenRunning(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Apply mutation is darwin-only")
	}
	fr := &fakeRunner{}
	proc := &fakeProc{running: true}
	d := NewITerm2([]byte("<plist>x</plist>"), fr, proc)

	res := Apply(d)
	if !res.Skipped || !errors.Is(res.Err, ErrITerm2Running) {
		t.Fatalf("Apply while running = %+v, want Skipped with ErrITerm2Running", res)
	}
	if res.Applied {
		t.Fatalf("Apply reported Applied while iTerm2 was running")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("imported into a running iTerm2 (%d defaults calls, want 0)", len(fr.calls))
	}
	if proc.flushed {
		t.Fatalf("flushed cfprefsd despite skipping the import")
	}
}

// TestITerm2ApplyNilBlobNoImport: a nil blob manages backup/restore only and never
// imports (nor probes running / flushes).
func TestITerm2ApplyNilBlobNoImport(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Apply mutation is darwin-only")
	}
	fr := &fakeRunner{}
	proc := &fakeProc{running: true} // even "running" is irrelevant with no blob
	d := NewITerm2(nil, fr, proc)
	res := Apply(d)
	if res.Err != nil || !res.Applied {
		t.Fatalf("Apply result = %+v, want Applied (no-op import)", res)
	}
	if len(fr.calls) != 0 || proc.flushed {
		t.Fatalf("nil-blob iTerm2 Apply shelled out (calls=%d flushed=%v), want none", len(fr.calls), proc.flushed)
	}
}

func TestAppleTerminalApplyImports(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Apply mutation is darwin-only; covered by TestNonDarwinSkips on this host")
	}
	blob := []byte("<plist>repo-terminal</plist>")
	fr := &fakeRunner{}
	d := NewAppleTerminal(blob, fr)

	res := Apply(d)
	if res.Err != nil || !res.Applied {
		t.Fatalf("Apply result = %+v, want Applied with no error", res)
	}
	c := fr.last()
	wantArgs := []string{"import", AppleTerminalDomain, "-"}
	if !equalArgs(c.args, wantArgs) {
		t.Fatalf("Apply args = %v, want %v", c.args, wantArgs)
	}
	if string(c.stdin) != string(blob) {
		t.Fatalf("Apply stdin = %q, want the repo export blob", c.stdin)
	}
}

func TestAppleTerminalApplyNilBlobNoImport(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Apply mutation is darwin-only")
	}
	fr := &fakeRunner{}
	d := NewAppleTerminal(nil, fr)
	res := Apply(d)
	if res.Err != nil || !res.Applied {
		t.Fatalf("Apply result = %+v, want Applied (no-op import)", res)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("nil-blob Apple Terminal Apply made %d calls, want 0", len(fr.calls))
	}
}

func TestApplyPropagatesRunnerError(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Apply mutation is darwin-only")
	}
	fr := &fakeRunner{err: errors.New("defaults boom")}
	d := NewITerm2([]byte("<plist>x</plist>"), fr, &fakeProc{})
	res := Apply(d)
	if res.Err == nil || res.Applied {
		t.Fatalf("Apply result = %+v, want an error", res)
	}
}

func TestPlanMarksPreferenceDomain(t *testing.T) {
	for _, d := range []*PreferenceDomain{
		newITerm2(nil, &fakeRunner{}),
		NewAppleTerminal(nil, &fakeRunner{}),
	} {
		p := d.Plan()
		if p.Kind != "preference-domain" || !p.Native {
			t.Fatalf("%s plan = %+v, want a native preference-domain entry", d.Domain(), p)
		}
		if !p.IsPreferenceDomain() {
			t.Fatalf("%s: IsPreferenceDomain() = false, want true", d.Domain())
		}
		s := p.String()
		if !strings.Contains(s, "preference domain") {
			t.Fatalf("%s plan string = %q, want it marked a preference domain", d.Domain(), s)
		}
		if strings.Contains(strings.ToLower(s), "file copy") && !strings.Contains(s, "not a file copy") {
			t.Fatalf("%s plan string = %q, must not present as a file copy", d.Domain(), s)
		}
	}
}

func TestPlanDomainIDs(t *testing.T) {
	if got := newITerm2(nil, &fakeRunner{}).Plan().Domain; got != ITerm2Domain {
		t.Fatalf("iTerm2 plan domain = %q, want %q", got, ITerm2Domain)
	}
	if got := NewAppleTerminal(nil, &fakeRunner{}).Plan().Domain; got != AppleTerminalDomain {
		t.Fatalf("Apple Terminal plan domain = %q, want %q", got, AppleTerminalDomain)
	}
}

// TestNonDarwinSkips asserts a clean skip path on Linux: Apply skips with
// ErrNotDarwin, Backup/Restore return ErrNotDarwin, and the runner is never
// touched. On darwin this exercises the darwin path instead (no skip), so the
// runner-untouched assertion is gated on the platform.
func TestNonDarwinSkips(t *testing.T) {
	fr := &fakeRunner{}
	d := newITerm2(nil, fr)

	if platform.IsDarwin() {
		// On darwin there is no skip; just confirm Apply runs the runner.
		if res := Apply(d); res.Skipped {
			t.Fatalf("on darwin Apply should not skip: %+v", res)
		}
		return
	}

	res := Apply(d)
	if !res.Skipped || !errors.Is(res.Err, ErrNotDarwin) {
		t.Fatalf("non-darwin Apply = %+v, want Skipped with ErrNotDarwin", res)
	}
	if _, _, err := d.Backup(); !errors.Is(err, ErrNotDarwin) {
		t.Fatalf("non-darwin Backup err = %v, want ErrNotDarwin", err)
	}
	if err := d.Restore([]byte("x"), false); !errors.Is(err, ErrNotDarwin) {
		t.Fatalf("non-darwin Restore err = %v, want ErrNotDarwin", err)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("non-darwin path shelled out %d times, want 0", len(fr.calls))
	}
	// The plan must still surface the domain as a (skipped) preference domain.
	p := d.Plan()
	if !p.Skipped || p.Kind != "preference-domain" {
		t.Fatalf("non-darwin plan = %+v, want skipped preference-domain", p)
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
