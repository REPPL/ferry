package terminal

import (
	"fmt"

	"github.com/REPPL/ferry/internal/platform"
)

// PlanEntry is one line of `ferry diff` plan for a terminal preference domain.
// It marks the change as a NATIVE PREFERENCE DOMAIN (not a file copy) — this is
// what AC-terminal-config gates on: an in-scope terminal domain must appear in
// the diff plan as a preference/terminal domain, distinct from a generic
// dotfile/file-copy entry.
type PlanEntry struct {
	// Domain is the preference domain identifier (e.g. com.googlecode.iterm2).
	Domain string
	// Kind is always "preference-domain" — the discriminator a diff renderer
	// (and the AC tripwire) reads to tell this apart from a file copy.
	Kind string
	// Native marks this as deployed via the macOS-native preference mechanism
	// (`defaults`/cfprefs), never a naive file copy.
	Native bool
	// Skipped is true on a non-darwin host (the domain is macOS-only).
	Skipped bool
	// Summary is a human-readable one-liner for the plan output.
	Summary string
}

// IsPreferenceDomain reports that this plan entry is a native preference-domain
// change rather than a file copy. The AC-terminal-config gate keys off this:
// the domain must surface as a preference domain in `ferry diff`, never as a
// generic dotfile/file-copy entry.
func (p PlanEntry) IsPreferenceDomain() bool {
	return p.Kind == "preference-domain" && p.Native
}

// String renders the plan entry for `ferry diff` output, explicitly tagging it
// as a native preference domain so it is observably NOT a file copy.
func (p PlanEntry) String() string {
	if p.Skipped {
		return fmt.Sprintf("[terminal preference domain] %s — skipped (macOS only)", p.Domain)
	}
	return fmt.Sprintf("[terminal preference domain] %s — %s (native macOS preference mechanism, not a file copy)", p.Domain, p.Summary)
}

// Plan returns the diff/plan entry for a domain. On a non-darwin host the entry
// is marked Skipped (still observably a preference domain, never a file copy),
// so the plan honestly shows the domain as macOS-only rather than dropping it.
func (d *PreferenceDomain) Plan() PlanEntry {
	if !platform.IsDarwin() {
		return PlanEntry{
			Domain:  d.domain,
			Kind:    "preference-domain",
			Native:  true,
			Skipped: true,
			Summary: "macOS only; skipped on this platform",
		}
	}
	var summary string
	switch d.domain {
	case ITerm2Domain:
		summary = "set custom prefs folder + load-from-custom-folder via defaults write"
	case AppleTerminalDomain:
		summary = "import preferences via defaults import"
	default:
		summary = "apply preferences via defaults"
	}
	return PlanEntry{
		Domain:  d.domain,
		Kind:    "preference-domain",
		Native:  true,
		Summary: summary,
	}
}
