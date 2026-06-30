package secret

import (
	"strings"
	"testing"
)

// FAKE secret material — never a real key. Constructed so the scanner's
// named-pattern detectors fire without embedding anything sensitive.
const fakePEMKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nFAKEKEYMATERIALabc123NOTREAL\n-----END OPENSSH PRIVATE KEY-----"

func TestScanText_DetectsPEMPrivateKey(t *testing.T) {
	fs := ScanText(fakePEMKey)
	if !fs.HasHigh() {
		t.Fatalf("expected high-confidence finding for PEM key, got %+v", fs)
	}
	if fs[0].Rule != "pem-private-key" {
		t.Errorf("expected pem-private-key rule, got %q", fs[0].Rule)
	}
	if fs[0].Line != 1 {
		t.Errorf("expected line 1, got %d", fs[0].Line)
	}
}

func TestScanText_DetectsWireGuardKeys(t *testing.T) {
	// FAKE base64-shaped WireGuard values.
	content := strings.Join([]string{
		"[Interface]",
		"PrivateKey = aFAKEkey0000000000000000000000000000000000=",
		"Address = 10.0.0.2/32",
		"[Peer]",
		"PresharedKey = bFAKEpsk1111111111111111111111111111111111=",
		"PublicKey = cNOTSECRET2222222222222222222222222222222=",
	}, "\n")
	fs := ScanText(content)
	if !fs.HasHigh() {
		t.Fatalf("expected high findings for WireGuard keys, got %+v", fs)
	}
	var priv, pre bool
	for _, f := range fs {
		if f.Rule == "wireguard-key" && f.Line == 2 {
			priv = true
		}
		if f.Rule == "wireguard-key" && f.Line == 5 {
			pre = true
		}
		if f.Line == 6 {
			t.Errorf("PublicKey (line 6) must NOT be flagged: %+v", f)
		}
	}
	if !priv || !pre {
		t.Errorf("expected both PrivateKey and PresharedKey flagged, got %+v", fs)
	}
}

func TestScanText_DetectsCredentialAssignment(t *testing.T) {
	cases := []string{
		`password = "hunter2hunter2"`,
		`export API_KEY=sk_live_FAKE000111222333abc`,
		`client_secret: superSecretValue123`,
		`token=ghp_FAKE0000aaaa1111bbbb`,
	}
	for _, c := range cases {
		fs := ScanText(c)
		if !fs.HasHigh() {
			t.Errorf("expected high finding for %q, got %+v", c, fs)
		}
	}
}

func TestScanText_DetectsHighEntropyToken(t *testing.T) {
	// A long, mixed-case + digit, base64-shaped opaque token on an otherwise
	// innocuous line. FAKE.
	line := `AWS_THING xQ7vR2pLkM9zNbW4tHcF8jYs3dG6aP1eUiOoZ0X=`
	fs := ScanText(line)
	if !fs.HasHigh() {
		t.Fatalf("expected high-entropy finding, got %+v", fs)
	}
	found := false
	for _, f := range fs {
		if f.Rule == "high-entropy-token" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected high-entropy-token rule, got %+v", fs)
	}
}

// TestScanText_NoFalsePositives is the false-positive discipline gate: ordinary
// dotfile lines must NEVER be flagged.
func TestScanText_NoFalsePositives(t *testing.T) {
	clean := []string{
		`export EDITOR=nvim`,
		`export PATH="$HOME/.local/bin:$PATH"`,
		`alias ll='ls -alh'`,
		`alias gs='git status'`,
		`export LANG=en_US.UTF-8`,
		`# this is a comment about my password manager`,
		`source "$HOME/.zshrc.local"`,
		`PublicKey = cNOTSECRET2222222222222222222222222222222=`,
		`bindkey '^R' history-incremental-search-backward`,
		`export HOMEBREW_PREFIX=/opt/homebrew`,
		`zstyle ':completion:*' menu select`,
		`password = ""`,                         // empty value
		`api_key = "${API_KEY}"`,                // env interpolation
		`secret = "{{ferry.secret \"x.y\"}}"`,   // ferry placeholder
		`token = changeme`,                      // placeholder word
		`color = "#1a2b3c4d"`,                   // short hex-ish, under length gate
		`path = /usr/local/share/some/deep/dir`, // path, not a secret
	}
	for _, line := range clean {
		fs := ScanText(line)
		if fs.HasHigh() {
			t.Errorf("FALSE POSITIVE: %q flagged: %+v", line, fs)
		}
	}
}

func TestScanText_LineNumbers(t *testing.T) {
	content := "line one\nline two\npassword = realSecretValue99\nline four"
	fs := ScanText(content)
	if !fs.HasHigh() {
		t.Fatalf("expected a finding, got none")
	}
	if fs[0].Line != 3 {
		t.Errorf("expected finding on line 3, got %d", fs[0].Line)
	}
}

func TestScanValue_WholeValue(t *testing.T) {
	// Opaque/plist-style whole value containing a PEM key — Line should be 0.
	fs := ScanValue(fakePEMKey)
	if !fs.HasHigh() {
		t.Fatalf("expected high finding for value scan, got %+v", fs)
	}
	if fs[0].Line != 0 {
		t.Errorf("expected line 0 for value scan, got %d", fs[0].Line)
	}
}

func TestScanValue_CleanValue(t *testing.T) {
	if ScanValue("Solarized Dark").HasHigh() {
		t.Errorf("clean plist value must not be flagged")
	}
}

// TestScanValue_WireGuardKeyOnNonFirstLine checks the (?m) multiline fix: in a
// whole multi-line opaque value (ScanValue, Line 0), a PrivateKey assignment
// that is NOT on the first line must still be flagged.
func TestScanValue_WireGuardKeyOnNonFirstLine(t *testing.T) {
	value := "[Interface]\nAddress = 10.0.0.2/32\nPrivateKey = aFAKEkey0000000000000000000000000000000000=\n"
	fs := ScanValue(value)
	if !fs.HasHigh() {
		t.Fatalf("expected a high finding for a WireGuard key on a non-first line, got %+v", fs)
	}
	found := false
	for _, f := range fs {
		if f.Rule == "wireguard-key" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a wireguard-key finding, got %+v", fs)
	}
}

func TestFindingDetailNeverLeaksValue(t *testing.T) {
	secret := "realSecretValue99887766"
	fs := ScanText(`password = ` + secret)
	for _, f := range fs {
		if strings.Contains(f.Detail, secret) {
			t.Errorf("finding detail leaked the secret value: %q", f.Detail)
		}
	}
}
