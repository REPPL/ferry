package keybindings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/dotfile"
)

// validDict is a minimal readable old-style (NeXT/ASCII) key-bindings dict.
const validDict = "{\n  \"~f\" = \"moveWordForward:\";\n}\n"

// fakeLinter records whether Lint was called and returns a configurable result,
// so the darwin-only plutil path is exercised deterministically on any host.
type fakeLinter struct {
	called bool
	err    error
}

func (f *fakeLinter) Lint(string) error {
	f.called = true
	return f.err
}

// writeSource writes body to <repo>/keybindings/DefaultKeyBinding.dict.
func writeSource(t *testing.T, repo string, body []byte) {
	t.Helper()
	dir := filepath.Join(repo, RepoSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, SourceName), body, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
}

func TestPlan_SourceToTargetMapping(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	writeSource(t, repo, []byte(validDict))

	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: home})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	if it.Key != "keybindings/DefaultKeyBinding.dict" {
		t.Errorf("Key = %q", it.Key)
	}
	if it.Label != "keybindings" {
		t.Errorf("Label = %q", it.Label)
	}
	wantHome := filepath.Join(home, "Library", "KeyBindings", "DefaultKeyBinding.dict")
	if it.Target.Home != wantHome {
		t.Errorf("Target.Home = %q, want %q", it.Target.Home, wantHome)
	}
	if it.Target.Overlay != dotfile.OverlayWholeFileReplace {
		t.Errorf("Target.Overlay = %q, want whole-file-replace", it.Target.Overlay)
	}
	if string(it.Content) != validDict {
		t.Errorf("Content = %q, want the source bytes", it.Content)
	}
}

func TestPlan_AbsentSourceDeploysNothing(t *testing.T) {
	items, warnings, err := Plan(PlanInput{RepoRoot: t.TempDir(), Home: t.TempDir()})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 0 || len(warnings) != 0 {
		t.Errorf("absent source: items=%v warnings=%v, want none", items, warnings)
	}
}

func TestPlan_RejectsBinaryPlist(t *testing.T) {
	repo := t.TempDir()
	// A binary plist an editor silently saved: bplist00 magic header.
	writeSource(t, repo, append([]byte("bplist00"), 0x01, 0x02, 0x03))

	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: t.TempDir()})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("binary plist deployed %d items, want 0", len(items))
	}
	if len(warnings) != 1 || !contains(warnings[0], "BINARY plist") {
		t.Errorf("warnings = %v, want a bplist00 refusal", warnings)
	}
}

func TestPlan_RejectsUTF8BOM(t *testing.T) {
	repo := t.TempDir()
	writeSource(t, repo, append([]byte{0xEF, 0xBB, 0xBF}, []byte(validDict)...))

	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: t.TempDir()})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("BOM source deployed %d items, want 0", len(items))
	}
	if len(warnings) != 1 || !contains(warnings[0], "byte-order mark") {
		t.Errorf("warnings = %v, want a BOM refusal", warnings)
	}
}

func TestPlan_RejectsInvalidUTF8(t *testing.T) {
	repo := t.TempDir()
	writeSource(t, repo, []byte{0xff, 0xfe, 0x00})

	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: t.TempDir()})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("non-UTF-8 source deployed %d items, want 0", len(items))
	}
	if len(warnings) != 1 || !contains(warnings[0], "valid UTF-8") {
		t.Errorf("warnings = %v, want a UTF-8 refusal", warnings)
	}
}

func TestPlan_LintGatesTheCopy(t *testing.T) {
	repo := t.TempDir()
	writeSource(t, repo, []byte(validDict))

	// A lint failure refuses the copy.
	fl := &fakeLinter{err: errors.New("Malformed dict")}
	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: t.TempDir(), Linter: fl})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !fl.called {
		t.Error("linter was not called")
	}
	if len(items) != 0 || len(warnings) != 1 || !contains(warnings[0], "lint failed") {
		t.Errorf("lint failure: items=%v warnings=%v, want a lint refusal", items, warnings)
	}
}

func TestPlan_LintSkippedOnNonDarwin(t *testing.T) {
	repo := t.TempDir()
	writeSource(t, repo, []byte(validDict))

	// ErrNotDarwin means the lint was skipped (non-macOS host) — the copy proceeds.
	fl := &fakeLinter{err: ErrNotDarwin}
	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: t.TempDir(), Linter: fl})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("ErrNotDarwin lint produced warnings: %v", warnings)
	}
	if len(items) != 1 {
		t.Errorf("ErrNotDarwin lint skipped deploy: got %d items, want 1", len(items))
	}
}

func TestPlan_RefusesSymlinkedSource(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, RepoSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	realFile := filepath.Join(repo, "elsewhere.dict")
	if err := os.WriteFile(realFile, []byte(validDict), 0o644); err != nil {
		t.Fatalf("write real: %v", err)
	}
	if err := os.Symlink(realFile, filepath.Join(dir, SourceName)); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	items, warnings, err := Plan(PlanInput{RepoRoot: repo, Home: t.TempDir()})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("symlinked source deployed %d items, want 0", len(items))
	}
	if len(warnings) != 1 || !contains(warnings[0], "symlink not allowed") {
		t.Errorf("warnings = %v, want a symlink refusal", warnings)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
