package terminal

import (
	"strings"
	"testing"
)

// a representative `defaults export com.googlecode.iterm2 -` body: two allowlisted
// keys (one scalar, one nested dict), plus volatile keys that MUST be dropped — a
// NoSync* one-shot flag, a window-geometry frame, and the retired custom-prefs
// pointer.
const sampleITerm2Export = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PromptOnQuit</key>
	<false/>
	<key>NoSyncSuppressAnnoyingBellOffer</key>
	<true/>
	<key>NSWindow Frame iTerm Window 0</key>
	<string>0 0 800 600 0 0 1440 900</string>
	<key>PrefsCustomFolder</key>
	<string>/some/machine/local/path</string>
	<key>LoadPrefsFromCustomFolder</key>
	<true/>
	<key>PointerActions</key>
	<dict>
		<key>Button,1,1,,</key>
		<dict>
			<key>Action</key>
			<string>kContextMenuPointerAction</string>
		</dict>
	</dict>
</dict>
</plist>
`

func TestFilterAllowlistKeepsAllowedDropsVolatile(t *testing.T) {
	out := string(FilterAllowlist([]byte(sampleITerm2Export)))

	// Allowlisted keys survive.
	for _, keep := range []string{"PromptOnQuit", "PointerActions", "kContextMenuPointerAction"} {
		if !strings.Contains(out, keep) {
			t.Errorf("allowlisted content %q was dropped\n%s", keep, out)
		}
	}
	// Volatile keys are gone.
	for _, drop := range []string{
		"NoSyncSuppressAnnoyingBellOffer",
		"NSWindow Frame",
		"PrefsCustomFolder",
		"LoadPrefsFromCustomFolder",
		"/some/machine/local/path",
	} {
		if strings.Contains(out, drop) {
			t.Errorf("volatile content %q survived the allowlist filter\n%s", drop, out)
		}
	}
	// Still a plist.
	if !strings.Contains(out, "<plist") || !strings.Contains(out, "<dict>") {
		t.Errorf("filtered output is not a plist\n%s", out)
	}
}

// TestFilterAllowlistIdempotent: filtering an already-filtered plist yields the
// SAME bytes — the property status/capture rely on to compare like-for-like
// without spurious drift.
func TestFilterAllowlistIdempotent(t *testing.T) {
	once := FilterAllowlist([]byte(sampleITerm2Export))
	twice := FilterAllowlist(once)
	if string(once) != string(twice) {
		t.Errorf("FilterAllowlist is not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

// TestFilterAllowlistDeterministicOrder: two exports with the same keys in a
// different source order filter to identical bytes (keys are emitted sorted).
func TestFilterAllowlistDeterministicOrder(t *testing.T) {
	a := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
	<key>PromptOnQuit</key><true/>
	<key>HideScrollbar</key><false/>
</dict></plist>`
	b := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
	<key>HideScrollbar</key><false/>
	<key>PromptOnQuit</key><true/>
</dict></plist>`
	if fa, fb := FilterAllowlist([]byte(a)), FilterAllowlist([]byte(b)); string(fa) != string(fb) {
		t.Errorf("filter order not deterministic:\n%s\n---\n%s", fa, fb)
	}
}

// TestFilterAllowlistNonXMLUnchanged: bytes that are not XML are returned as-is.
func TestFilterAllowlistNonXMLUnchanged(t *testing.T) {
	junk := []byte("not xml at all")
	if got := FilterAllowlist(junk); string(got) != string(junk) {
		t.Errorf("non-XML input mutated: %q", got)
	}
	if got := FilterAllowlist(nil); got != nil {
		t.Errorf("nil input = %q, want nil", got)
	}
}
