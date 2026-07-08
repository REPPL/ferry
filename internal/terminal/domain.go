package terminal

import (
	"errors"
	"fmt"
	"strings"

	"github.com/REPPL/ferry/internal/platform"
)

// Preference domain identifiers (the macOS `defaults` domains).
const (
	ITerm2Domain        = "com.googlecode.iterm2"
	AppleTerminalDomain = "com.apple.Terminal"
)

// PreferenceDomain models one macOS terminal preference DOMAIN as a backup
// resource. It implements backup.Resource (Domain/Backup/Restore) so the engine
// captures pre-mutation state and can roll back, and it carries an Apply
// closure that performs the domain-specific mutation through the same Runner.
//
// It is constructed via NewITerm2 / NewAppleTerminal. The zero value is not
// usable. All operations are darwin-only (see ErrNotDarwin).
type PreferenceDomain struct {
	domain string
	runner Runner
	// apply performs the domain-specific mutation via the runner. Set by the
	// constructors; both iTerm2 and Apple Terminal import a prepared export blob
	// via `defaults import` (iTerm2 additionally refuses when it is running and
	// flushes cfprefsd afterwards — see NewITerm2).
	apply func(r Runner) error
	// note is the human-readable caveat surfaced in the apply result (e.g. the
	// iTerm2 relaunch / cfprefsd-cache note).
	note string
}

// NewITerm2 builds the iTerm2 GLOBAL preference domain. On Apply it imports the
// prepared, allowlist-filtered export blob into com.googlecode.iterm2 via
// `defaults import <domain> -` (mirroring Apple Terminal), then flushes cfprefsd
// so the change is not masked by the daemon's cache. Pass nil to manage the domain
// for backup/restore only (Apply then no-ops the import).
//
// CRITICAL: iTerm2 keeps its preferences in memory and REWRITES the domain on
// quit, and cfprefsd caches it — so importing while iTerm2 is RUNNING is silently
// lost. Apply therefore consults proc.Running() first and, when iTerm2 is up,
// REFUSES with ErrITerm2Running (a clean skip; live config left intact) so the
// user can quit iTerm2 and re-run. After a successful import into a not-running
// iTerm2 it calls proc.FlushPrefsCache() (best-effort `killall cfprefsd`).
//
// exportBlob is the RENDERED bytes (any {{ferry.secret ...}} already substituted
// by cmd/apply.go, which skips this domain on a missing secret or a refused repo
// plist so a placeholder is never imported). This retires the previous
// custom-prefs-folder mechanism (v0.7.0 D4): ferry no longer points iTerm2 at a
// folder — it imports the domain like Apple Terminal.
func NewITerm2(exportBlob []byte, runner Runner, proc ProcessController) *PreferenceDomain {
	d := &PreferenceDomain{
		domain: ITerm2Domain,
		runner: runner,
		note:   "iTerm2 preferences imported; relaunch iTerm2 for them to take effect (cfprefsd was flushed).",
	}
	d.apply = func(r Runner) error {
		if len(exportBlob) == 0 {
			return nil // manage backup/restore only; nothing to import.
		}
		// REFUSE if iTerm2 is running: a running iTerm2 overwrites the domain on
		// quit, silently losing the import. Fail closed on a probe error too.
		running, err := proc.Running()
		if err != nil {
			return fmt.Errorf("iterm2: check whether iTerm2 is running: %w", err)
		}
		if running {
			return ErrITerm2Running
		}
		if _, err := r.Run(exportBlob, "import", ITerm2Domain, "-"); err != nil {
			return fmt.Errorf("iterm2: import: %w", err)
		}
		// Flush cfprefsd so the imported values are not masked by its cache. This is
		// best-effort: the bytes are already on disk, so a flush hiccup must not fail
		// (or roll back) an otherwise successful import.
		_ = proc.FlushPrefsCache()
		return nil
	}
	return d
}

// NewAppleTerminal builds the Apple Terminal preference domain. On Apply it
// imports the prepared export blob (the repo's committed com.apple.Terminal
// export) into the live domain via `defaults import <domain> -`. Pass the bytes
// of a `defaults export com.apple.Terminal -` to deploy; pass nil to manage the
// domain for backup/restore only (Apply then no-ops the import).
func NewAppleTerminal(exportBlob []byte, runner Runner) *PreferenceDomain {
	d := &PreferenceDomain{
		domain: AppleTerminalDomain,
		runner: runner,
		note:   "Apple Terminal may need to be relaunched for imported settings to take effect (cfprefsd may cache the old value).",
	}
	d.apply = func(r Runner) error {
		if len(exportBlob) == 0 {
			return nil
		}
		if _, err := r.Run(exportBlob, "import", AppleTerminalDomain, "-"); err != nil {
			return fmt.Errorf("apple terminal: import: %w", err)
		}
		return nil
	}
	return d
}

// Domain returns the preference domain identifier (backup.Resource).
func (d *PreferenceDomain) Domain() string { return d.domain }

// Name returns the user-facing [manage] scope key for this preference domain
// (iTerm2 -> "iterm2", Apple Terminal -> "terminal") — distinct from Domain(),
// which is the native `defaults` identifier (com.googlecode.iterm2 /
// com.apple.Terminal). It is the key Scope.IsManaged gates on and the converged
// domains.ResourceDomain registry enumerates by, replacing the hardcoded
// {"iterm2","terminal"} literal that drove the preference-domain arm.
func (d *PreferenceDomain) Name() string {
	switch d.domain {
	case ITerm2Domain:
		return "iterm2"
	case AppleTerminalDomain:
		return "terminal"
	default:
		return ""
	}
}

// Backup captures the domain's CURRENT state via `defaults export <domain> -`
// (backup.Resource). It is darwin-only: on a non-darwin host it returns
// ErrNotDarwin and never shells out. Run BEFORE the domain is mutated.
//
// A domain that does NOT yet exist (the common case on a fresh machine — iTerm2
// has never been configured) is a normal, expected pre-ferry state, NOT a
// failure: `defaults export <missing-domain> -` either errors with a "does not
// exist" message or emits an empty/template plist. Backup reports that as
// absent=true (with a nil blob) so the engine can record an absent baseline and
// apply can still proceed to create/configure the domain. Only an unexpected
// export failure (permission denied, `defaults` missing) is returned as err.
func (d *PreferenceDomain) Backup() (blob []byte, absent bool, err error) {
	if !platform.IsDarwin() {
		return nil, false, ErrNotDarwin
	}
	out, err := d.runner.Run(nil, "export", d.domain, "-")
	if err != nil {
		if isDomainAbsentErr(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("terminal: export %s: %w", d.domain, err)
	}
	// A successful export that yields no real content (empty output, or the
	// empty-dict template `defaults` prints for an unknown domain) also means
	// the domain holds nothing pre-ferry — treat it as absent.
	if isEmptyExport(out) {
		return nil, true, nil
	}
	return out, false, nil
}

// Restore returns the domain to a previously captured state (the engine's
// rollback path). darwin-only.
//
// When absent is true the baseline recorded that the domain did NOT exist
// pre-ferry, so Restore REMOVES the domain via `defaults delete <domain>` to
// return the machine to that pre-ferry (absent) state rather than importing an
// empty blob. A missing-domain delete (already absent) is not an error. When
// absent is false a present baseline is re-imported via `defaults import`.
func (d *PreferenceDomain) Restore(blob []byte, absent bool) error {
	if !platform.IsDarwin() {
		return ErrNotDarwin
	}
	if absent {
		if _, err := d.runner.Run(nil, "delete", d.domain); err != nil {
			// Deleting an already-absent domain is a no-op success, not a failure.
			if isDomainAbsentErr(err) {
				return nil
			}
			return fmt.Errorf("terminal: delete %s: %w", d.domain, err)
		}
		return nil
	}
	if _, err := d.runner.Run(blob, "import", d.domain, "-"); err != nil {
		return fmt.Errorf("terminal: import %s: %w", d.domain, err)
	}
	return nil
}

// ErrDomainAbsent is the sentinel meaning a `defaults` domain does not exist.
// It is not returned from Backup (absence is a normal bool there); it exists so
// tests and a fake runner can signal "domain does not exist" the way real
// `defaults` does.
var ErrDomainAbsent = errors.New("defaults: domain does not exist")

// isDomainAbsentErr reports whether a failed `defaults export`/`delete`
// indicates the domain simply does not exist (a normal pre-ferry state on a
// fresh machine), as opposed to a real failure (permission, missing tool). It
// matches the message real `defaults` prints ("Domain ... does not exist") and
// the ErrDomainAbsent sentinel used by the test runner. A real failure (empty or
// unrelated stderr) is NOT treated as absence — only the success path inspects
// output emptiness (see isEmptyExport).
func isDomainAbsentErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrDomainAbsent) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "does not exist")
}

// isEmptyExport reports whether `defaults export` output carries no real
// preferences: empty output, or the empty-dictionary plist `defaults` prints for
// a domain with nothing in it (an XML `<dict/>`/`<dict></dict>` body).
func isEmptyExport(out []byte) bool {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return true
	}
	// Strip the plist/XML scaffolding and look for any key. An empty domain
	// exports a plist whose root <dict> has no <key> children.
	if strings.Contains(s, "<plist") && !strings.Contains(s, "<key>") {
		return true
	}
	return false
}
