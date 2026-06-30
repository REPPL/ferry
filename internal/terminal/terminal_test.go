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
	d := NewITerm2("/repo/iterm2", fr)

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
	d := NewITerm2("/repo/iterm2", fr)

	if _, absent, err := d.Backup(); err == nil || absent {
		t.Fatalf("Backup = (absent=%v, err=%v), want a real error", absent, err)
	}
}

func TestRestoreImportsDomain(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Restore is darwin-only; covered by TestNonDarwinSkips on this host")
	}
	fr := &fakeRunner{}
	d := NewITerm2("/repo/iterm2", fr)
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
	d := NewITerm2("/repo/iterm2", fr)

	if err := d.Restore(nil, true); err != nil {
		t.Fatalf("Restore(absent): %v", err)
	}
	c := fr.last()
	wantArgs := []string{"delete", ITerm2Domain}
	if !equalArgs(c.args, wantArgs) {
		t.Fatalf("absent Restore args = %v, want %v", c.args, wantArgs)
	}
}

// TestRestoreAbsentAlreadyMissingIsNoError: deleting an already-absent domain
// (delete reports "does not exist") is a no-op success.
func TestRestoreAbsentAlreadyMissingIsNoError(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Restore is darwin-only")
	}
	fr := &fakeRunner{err: ErrDomainAbsent}
	d := NewITerm2("/repo/iterm2", fr)
	if err := d.Restore(nil, true); err != nil {
		t.Fatalf("Restore(absent) on already-missing domain = %v, want nil", err)
	}
}

func TestITerm2ApplySetsKeys(t *testing.T) {
	if !platform.IsDarwin() {
		t.Skip("Apply mutation is darwin-only; covered by TestNonDarwinSkips on this host")
	}
	fr := &fakeRunner{}
	d := NewITerm2("/repo/iterm2", fr)

	res := Apply(d)
	if res.Err != nil {
		t.Fatalf("Apply: %v", res.Err)
	}
	if !res.Applied || res.Skipped {
		t.Fatalf("Apply result = %+v, want Applied", res)
	}
	if res.Note == "" || !strings.Contains(res.Note, "relaunch") {
		t.Fatalf("Apply note = %q, want a relaunch caveat", res.Note)
	}
	if len(fr.calls) != 2 {
		t.Fatalf("got %d defaults calls, want 2 (PrefsCustomFolder + LoadPrefsFromCustomFolder)", len(fr.calls))
	}
	// First call: PrefsCustomFolder -> repo path.
	want1 := []string{"write", ITerm2Domain, "PrefsCustomFolder", "-string", "/repo/iterm2"}
	if !equalArgs(fr.calls[0].args, want1) {
		t.Fatalf("call 0 args = %v, want %v", fr.calls[0].args, want1)
	}
	// Second call: LoadPrefsFromCustomFolder -> true.
	want2 := []string{"write", ITerm2Domain, "LoadPrefsFromCustomFolder", "-bool", "true"}
	if !equalArgs(fr.calls[1].args, want2) {
		t.Fatalf("call 1 args = %v, want %v", fr.calls[1].args, want2)
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
	d := NewITerm2("/repo/iterm2", fr)
	res := Apply(d)
	if res.Err == nil || res.Applied {
		t.Fatalf("Apply result = %+v, want an error", res)
	}
}

func TestPlanMarksPreferenceDomain(t *testing.T) {
	for _, d := range []*PreferenceDomain{
		NewITerm2("/repo/iterm2", &fakeRunner{}),
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
	if got := NewITerm2("/x", &fakeRunner{}).Plan().Domain; got != ITerm2Domain {
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
	d := NewITerm2("/repo/iterm2", fr)

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
