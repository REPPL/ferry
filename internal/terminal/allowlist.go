package terminal

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

// ITerm2GlobalAllowlist is the set of GLOBAL (non-profile) iTerm2 preference keys
// ferry carries in the git-tracked com.googlecode.iterm2 plist. It is an ALLOWLIST
// on purpose: capture keeps ONLY these keys and drops everything else, so the
// volatile machine state a whole-plist capture would sweep up — window geometry
// (`NSWindow Frame …`), the `NoSync…` one-shot dialog flags, the retired
// custom-prefs-folder pointer (`PrefsCustomFolder` / `LoadPrefsFromCustomFolder`),
// telemetry and per-run counters — can never reach the repo, because it is not on
// the list rather than because a denylist happened to name it.
//
// Profiles are NOT carried here — they are the separate Dynamic Profiles JSON
// FileDomain (iterm2-profiles). This domain is the app-wide behaviour that lives
// outside any profile.
//
// The set below is a curated STARTING POINT of stable, machine-agnostic global
// behaviour keys; extend it to taste in the repo's own review. A key absent from
// the list is simply not carried (a no-op), never an error.
var ITerm2GlobalAllowlist = map[string]bool{
	// Quit / close confirmations.
	"PromptOnQuit":             true,
	"OnlyWhenMoreTabs":         true,
	"QuitWhenAllWindowsClosed": true,
	// Startup / window restoration behaviour.
	"OpenArrangementAtStartup": true,
	"OpenNoWindowsAtStartup":   true,
	"SavePasteHistory":         true,
	// Tab bar / window chrome (behaviour, not geometry).
	"TabViewType":           true,
	"WindowStyle":           true,
	"HideTab":               true,
	"HideTabNumber":         true,
	"HideTabCloseButton":    true,
	"HideActivityIndicator": true,
	"HideScrollbar":         true,
	"UseBorder":             true,
	// Inactive-pane / background dimming.
	"DimInactiveSplitPanes":  true,
	"DimBackgroundWindows":   true,
	"AnimateDimming":         true,
	"SplitPaneDimmingAmount": true,
	// Clipboard / paste behaviour.
	"AllowClipboardAccess": true,
	// Software-update behaviour (Sparkle) — a preference, not machine identity.
	"SUEnableAutomaticChecks": true,
	"SUAutomaticallyUpdate":   true,
	// Mouse / pointer bindings (a nested dict of behaviour).
	"PointerActions": true,
	// The default profile pointer (a GUID into the Dynamic Profiles set).
	"Default Bookmark Guid": true,
}

// FilterAllowlist reduces a `defaults export com.googlecode.iterm2 -` XML plist to
// only the keys in ITerm2GlobalAllowlist, returning a canonical, deterministic XML
// plist of that subset. It is the capture-side filter that produces the git-tracked
// representation and the like-for-like comparison basis for status/capture: because
// it is an allowlist, volatile keys (NoSync*, window geometry, the retired
// custom-prefs-folder pointer) are dropped no matter what the live domain holds.
//
// Determinism + idempotency are contractual: keys are emitted in sorted order and
// insignificant whitespace is dropped, so FilterAllowlist(FilterAllowlist(x)) ==
// FilterAllowlist(x). That is what lets status compare a filtered live export
// against the filtered repo copy without spurious drift.
//
// Input that does not parse as XML (empty, or an unexpected binary export) is
// returned unchanged — the filter never fabricates content; a caller comparing two
// such blobs still compares like-for-like.
func FilterAllowlist(export []byte) []byte {
	if len(export) == 0 {
		return export
	}
	pairs, ok := parsePlistTopLevel(export)
	if !ok {
		return export
	}
	kept := pairs[:0]
	for _, p := range pairs {
		if ITerm2GlobalAllowlist[p.key] {
			kept = append(kept, p)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].key < kept[j].key })

	var buf bytes.Buffer
	buf.WriteString(xml.Header) // <?xml ...?>\n
	buf.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	buf.WriteString("<plist version=\"1.0\">\n<dict>\n")
	for _, p := range kept {
		fmt.Fprintf(&buf, "\t%s\n\t%s\n",
			encodeTokens([]xml.Token{
				xml.StartElement{Name: xml.Name{Local: "key"}},
				xml.CharData(p.key),
				xml.EndElement{Name: xml.Name{Local: "key"}},
			}),
			encodeTokens(p.value))
	}
	buf.WriteString("</dict>\n</plist>\n")
	return buf.Bytes()
}

// plistPair is one top-level key/value of the plist root <dict>; value holds the
// value element's full (possibly nested) token subtree, whitespace-stripped.
type plistPair struct {
	key   string
	value []xml.Token
}

// parsePlistTopLevel streams the plist and returns its root <dict>'s immediate
// key/value pairs. ok is false when the bytes are not a parseable XML plist (the
// caller then leaves the input unchanged).
func parsePlistTopLevel(export []byte) (pairs []plistPair, ok bool) {
	dec := xml.NewDecoder(bytes.NewReader(export))
	inPlist, inRoot, sawRoot := false, false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "plist" && !inPlist:
				inPlist = true
			case inPlist && !inRoot && t.Name.Local == "dict":
				inRoot, sawRoot = true, true
			case inRoot && t.Name.Local == "key":
				key, kerr := readElementText(dec, "key")
				if kerr != nil {
					return nil, false
				}
				val, verr := captureValue(dec)
				if verr != nil {
					return nil, false
				}
				pairs = append(pairs, plistPair{key: key, value: val})
			}
		case xml.EndElement:
			if inRoot && t.Name.Local == "dict" {
				// Root dict closed — done (ignore anything after).
				return pairs, true
			}
		}
	}
	// A real plist has a <plist><dict> root; without it the bytes were not a plist
	// (plain text tokenises as valid XML CharData, so absence of the root — not a
	// tokeniser error — is the signal to leave the input unchanged).
	return pairs, sawRoot
}

// readElementText consumes CharData up to the EndElement named name, returning the
// accumulated text (used to read a <key>…</key>'s name).
func readElementText(dec *xml.Decoder, name string) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			if t.Name.Local == name {
				return b.String(), nil
			}
		}
	}
}

// captureValue reads the next value element after a <key> (skipping insignificant
// whitespace) and returns its full token subtree, dropping whitespace-only CharData
// so the re-emitted plist is canonical and idempotent under FilterAllowlist.
func captureValue(dec *xml.Decoder) ([]xml.Token, error) {
	var tokens []xml.Token
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.CharData:
			if depth == 0 {
				if strings.TrimSpace(string(t)) == "" {
					continue // whitespace before the value element
				}
				return nil, fmt.Errorf("terminal: unexpected text before plist value")
			}
			if strings.TrimSpace(string(t)) == "" {
				continue // drop insignificant whitespace inside a container value
			}
			tokens = append(tokens, xml.CopyToken(t))
		case xml.StartElement:
			depth++
			tokens = append(tokens, xml.CopyToken(t))
		case xml.EndElement:
			tokens = append(tokens, xml.CopyToken(t))
			depth--
			if depth == 0 {
				return tokens, nil
			}
		default:
			if depth > 0 {
				tokens = append(tokens, xml.CopyToken(t))
			}
		}
	}
}

// encodeTokens serialises a token slice to canonical XML (used for the <key> and
// each captured value subtree).
func encodeTokens(tokens []xml.Token) string {
	var b strings.Builder
	enc := xml.NewEncoder(&b)
	for _, t := range tokens {
		_ = enc.EncodeToken(t)
	}
	_ = enc.Flush()
	return b.String()
}
