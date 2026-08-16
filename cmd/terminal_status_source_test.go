package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// terminalRepoStatusSource is the single local-wins resolution both status AND
// capture compare the live preference-domain export against (apply's
// terminalExportBlob mirrors it). Capture once compared only the shared copy,
// so a domain captured to the [l]ocal overlay was re-offered as drifted on
// every later run while status reported it clean — these tests pin the shared
// seam that fix relies on.
func TestTerminalRepoStatusSourcePrefersLocalOverlay(t *testing.T) {
	repo := t.TempDir()
	local := filepath.Join(repo, "local", "iterm2", "com.googlecode.iterm2.plist")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := terminalRepoStatusSource(repo, "iterm2", "com.googlecode.iterm2")
	if got != local {
		t.Errorf("with a local overlay present, source = %q, want the overlay %q", got, local)
	}
}

func TestTerminalRepoStatusSourceFallsBackToShared(t *testing.T) {
	repo := t.TempDir()
	for domain, shared := range map[string]string{
		"iterm2":   filepath.Join(repo, "iterm2", "com.googlecode.iterm2.plist"),
		"terminal": filepath.Join(repo, "terminal", "com.apple.Terminal.plist"),
	} {
		id := filepath.Base(shared)
		id = id[:len(id)-len(".plist")]
		if got := terminalRepoStatusSource(repo, domain, id); got != shared {
			t.Errorf("%s: with no local overlay, source = %q, want shared %q", domain, got, shared)
		}
	}
}

func TestTerminalRepoStatusSourceIgnoresSymlinkedOverlay(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere.plist")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	localDir := filepath.Join(repo, "local", "iterm2")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(localDir, "com.googlecode.iterm2.plist")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(repo, "iterm2", "com.googlecode.iterm2.plist")
	if got := terminalRepoStatusSource(repo, "iterm2", "com.googlecode.iterm2"); got != shared {
		t.Errorf("symlinked overlay must be refused, source = %q, want shared %q", got, shared)
	}
}

// Apply's terminalExportBlob accepts BOTH overlay spellings (<id>.plist and the
// extensionless <id>), so the resolution status and capture share must probe both
// too: an extensionless overlay that apply imports but status resolved past made
// status report drift forever while capture re-offered the domain forever.
func TestTerminalRepoStatusSourceAcceptsExtensionlessOverlay(t *testing.T) {
	repo := t.TempDir()
	local := filepath.Join(repo, "local", "terminal", "com.apple.Terminal")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := terminalRepoStatusSource(repo, "terminal", "com.apple.Terminal"); got != local {
		t.Errorf("with an extensionless local overlay present, source = %q, want the overlay %q (apply imports it)", got, local)
	}
}
