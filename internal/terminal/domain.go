package terminal

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/REPPL/ferry/internal/platform"
)

// Preference domain identifiers (the macOS `defaults` domains).
const (
	ITerm2Domain        = "com.googlecode.iterm2"
	AppleTerminalDomain = "com.apple.Terminal"
)

// ITerm2ControlKeys are the MACHINE-LOCAL keys ferry writes to POINT iTerm2 at the
// repo (the custom-prefs-folder pointer), NOT user preferences. apply sets them via
// `defaults write` (see NewITerm2). They are how ferry deploys the repo plist, so
// capture must never ingest them into the repo and status must never report drift
// because of them. capture/status strip these from BOTH the live export and the repo
// plist before comparing/capturing (compare like-for-like).
var ITerm2ControlKeys = []string{"PrefsCustomFolder", "LoadPrefsFromCustomFolder"}

// iterm2ControlKeyRE matches one `<key>NAME</key>` plus the value element that
// immediately follows it in a `defaults export`/plist XML body, for each control
// key. The value is either a self-closing element (`<true/>`) or an open/close pair
// (`<string>…</string>`); both forms are removed together so no orphan key or value
// is left behind. The keys carry scalar string/bool values only, so nested
// containers are not handled — that is acceptable here (and the exact custom-folder
// vs `defaults export` round-trip is Layer-2-deferred per AC-terminal-config).
var iterm2ControlKeyRE = func() *regexp.Regexp {
	alt := strings.Join(ITerm2ControlKeys, "|")
	// (?s) so `.` spans newlines between the key and its value element.
	return regexp.MustCompile(`(?s)[\t ]*<key>(?:` + alt + `)</key>\s*(?:<[a-zA-Z]+/>|<([a-zA-Z]+)>.*?</[a-zA-Z]+>)\s*\n?`)
}()

// StripITerm2ControlKeys removes the machine-local iTerm2 control keys
// (PrefsCustomFolder / LoadPrefsFromCustomFolder) and their values from a plist /
// `defaults export` XML body, returning the remaining content. It is a TEXT-LEVEL
// strip over the opaque export bytes (the codebase treats these exports as opaque
// byte blobs; there is no plist parser), used by capture/status so the live domain
// is compared LIKE-FOR-LIKE against the repo plist with the pointer keys excluded
// from both sides. Non-iTerm2 / absent inputs are returned unchanged.
//
// NOTE: This is a deliberate consistency fix — never capturing the pointer state and
// comparing both sides without it. The deeper reconciliation of iTerm2's
// custom-prefs-FOLDER plist vs the `defaults export` domain (which can diverge in
// edge cases) is Layer-2-deferred per ACCEPTANCE.md's AC-terminal-config, which
// gates the native-mechanism differential and defers concrete plist key/value
// semantics. Do not grow this into a full custom-folder differ.
func StripITerm2ControlKeys(export []byte) []byte {
	if len(export) == 0 {
		return export
	}
	return iterm2ControlKeyRE.ReplaceAll(export, nil)
}

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
	// constructors; iTerm2 sets PrefsCustomFolder + LoadPrefsFromCustomFolder,
	// Apple Terminal imports a prepared export blob.
	apply func(r Runner) error
	// note is the human-readable caveat surfaced in the apply result (e.g. the
	// iTerm2 relaunch / cfprefsd-cache note).
	note string
}

// NewITerm2 builds the iTerm2 preference domain. On Apply it points iTerm2 at
// prefsFolder by setting PrefsCustomFolder to that folder and
// LoadPrefsFromCustomFolder to true via `defaults write`. The returned result
// notes that iTerm2 must be relaunched (and cfprefsd may cache) for the change
// to take effect.
//
// prefsFolder is the ferry-owned RENDERED STAGING FOLDER (e.g.
// ~/.local/state/ferry/rendered/iterm2/) into which apply has materialised the
// repo plist with its {{ferry.secret ...}} placeholders already substituted —
// NOT the raw repo iterm2/ folder. iTerm2 loads com.googlecode.iterm2.plist from
// whatever folder it is pointed at; pointing it at the staging folder guarantees
// it reads the RENDERED plist and never the raw placeholder one. cmd/apply.go
// stages that plist (refused leaf / missing secret → it skips this domain and
// never calls NewITerm2).
func NewITerm2(prefsFolder string, runner Runner) *PreferenceDomain {
	d := &PreferenceDomain{
		domain: ITerm2Domain,
		runner: runner,
		note:   "iTerm2 must be relaunched for the custom prefs folder to take effect (cfprefsd may cache the old value).",
	}
	d.apply = func(r Runner) error {
		// PrefsCustomFolder -> rendered staging folder (a string default).
		if _, err := r.Run(nil, "write", ITerm2Domain, "PrefsCustomFolder", "-string", prefsFolder); err != nil {
			return fmt.Errorf("iterm2: set PrefsCustomFolder: %w", err)
		}
		// LoadPrefsFromCustomFolder -> true (a bool default).
		if _, err := r.Run(nil, "write", ITerm2Domain, "LoadPrefsFromCustomFolder", "-bool", "true"); err != nil {
			return fmt.Errorf("iterm2: set LoadPrefsFromCustomFolder: %w", err)
		}
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
