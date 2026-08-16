package cmd

// Gate-input coverage for the whole-plist terminal preference domains.
//
// Apple Terminal stores every customised profile attribute (colours, font,
// cursor) as an NSKeyedArchiver blob, which `defaults export` renders as an XML
// plist <data> element: wrapped base64 lines that clear the entropy detector's
// length and shape floors. Scanning that base64 as text blocked EVERY customised
// Apple Terminal domain from the repo permanently — the only routes left were
// reject and the out-of-repo secret store, and the secret store makes the domain
// non-portable (another machine reports a missing ref forever).
//
// The gate itself is mandatory and stays pre-write; what changes is the INPUT:
// the base64 payload inside <data> elements is masked before the gate sees it,
// while every <string> value — where a pasteable token would actually live —
// stays fully scanned.

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/secret"
)

// archivedDataPayload renders n bytes of DETERMINISTIC pseudo-binary as the
// wrapped base64 body of a plist <data> element, the way `defaults export`
// writes an NSKeyedArchiver blob. The bytes come from a fixed LCG, so the
// fixture's entropy is identical on every run and the test cannot flake.
func archivedDataPayload(n int) string {
	raw := make([]byte, n)
	x := uint32(0x9E3779B9)
	for i := range raw {
		x = x*1664525 + 1013904223
		raw[i] = byte(x >> 24)
	}
	enc := base64.StdEncoding.EncodeToString(raw)
	var b strings.Builder
	for i := 0; i < len(enc); i += 68 {
		end := i + 68
		if end > len(enc) {
			end = len(enc)
		}
		b.WriteString("\t\t\t\t" + enc[i:end] + "\n")
	}
	return b.String()
}

// appleTerminalPlist builds an Apple Terminal-shaped `defaults export` blob: a
// Window Settings profile carrying NSKeyedArchiver <data> attributes, plus
// whatever extra key/value markup a case wants inside the profile dict.
func appleTerminalPlist(extra string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Default Window Settings</key>
	<string>Ferry</string>
	<key>Window Settings</key>
	<dict>
		<key>Ferry</key>
		<dict>
			<key>BackgroundColor</key>
			<data>
` + archivedDataPayload(180) + `			</data>
			<key>Font</key>
			<data>
` + archivedDataPayload(240) + `			</data>
			<key>name</key>
			<string>Ferry</string>
` + extra + `		</dict>
	</dict>
</dict>
</plist>
`
}

// (a) A customised Apple Terminal profile is CAPTURABLE: its archived <data>
// blobs must not block the domain from the repo.
func TestTerminalGateInput_ArchivedDataDoesNotBlock(t *testing.T) {
	blob := appleTerminalPlist("")

	// Guard the fixture: the RAW export must be blocked, otherwise this test
	// proves nothing about the gate-input reduction.
	if !secret.GateValue(blob).BlockedFromRepo {
		t.Fatalf("fixture does not trip the raw gate; it cannot demonstrate the reduction")
	}

	if secret.GateValue(terminalGateInput([]byte(blob))).BlockedFromRepo {
		t.Errorf("a customised Apple Terminal profile is blocked from the repo by its archived <data> blobs; the domain would be permanently uncapturable")
	}
}

// (b) Masking <data> loses no real coverage: a pasteable token in a <string>
// value still blocks the whole domain.
func TestTerminalGateInput_TokenInStringStillBlocks(t *testing.T) {
	cases := map[string]string{
		"named token": "			<key>CommandString</key>\n" +
			"			<string>export GH=ghp_ABCdefGHIjklMNOpqrSTUvwx0123</string>\n",
		"PEM header": "			<key>CommandString</key>\n" +
			"			<string>-----BEGIN OPENSSH PRIVATE KEY-----</string>\n",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			blob := appleTerminalPlist(extra)
			if !secret.GateValue(terminalGateInput([]byte(blob))).BlockedFromRepo {
				t.Errorf("a secret in a <string> value no longer blocks the domain; the gate-input reduction masks more than the base64 payload")
			}
		})
	}
}

// (c) A plist with no <data> element at all is untouched by the reduction.
func TestTerminalGateInput_NoDataElementStillBlocks(t *testing.T) {
	blob := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>CommandString</key>
	<string>ghp_ABCdefGHIjklMNOpqrSTUvwx0123</string>
</dict>
</plist>
`
	if !secret.GateValue(terminalGateInput([]byte(blob))).BlockedFromRepo {
		t.Errorf("a token in a <data>-free plist is not blocked")
	}
}

// (d) Malformed input fails toward scanning MORE: an unterminated <data> has no
// known extent, so everything after it stays unmasked and fully scanned.
func TestTerminalGateInput_UnterminatedDataStaysScanned(t *testing.T) {
	blob := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Broken</key>
	<data>
` + archivedDataPayload(120) + `	<key>CommandString</key>
	<string>ghp_ABCdefGHIjklMNOpqrSTUvwx0123</string>
</dict>
</plist>
`
	if !secret.GateValue(terminalGateInput([]byte(blob))).BlockedFromRepo {
		t.Errorf("content after an unterminated <data> was masked; malformed input must fail toward scanning more, never less")
	}
}
