package evals

// B1 — the macOS Cocoa key-bindings file carried as a single repo-authoritative
// nested target. These evals drive the real binary and assert observable
// outcomes: a valid old-style dict deploys as a regular-file COPY (never a
// symlink) from keybindings/ to ~/Library/KeyBindings/DefaultKeyBinding.dict when
// the domain is in scope; the domain is gated by [manage] keybindings; a binary
// plist (bplist00) source is refused; and status reports clean after apply and
// drift after a live edit (repo-authoritative, no capture pass).

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// keybindingsManifest enables the key-bindings domain plus the dotfiles domain
// (so scope has a second domain to prove independence).
const keybindingsManifest = `[manage]
dotfiles = [".zshrc"]
keybindings = true
brew = false
iterm2 = false
fonts = false
`

// validKeyDict is a minimal readable old-style (NeXT/ASCII) key-bindings dict.
const validKeyDict = "{\n  \"~f\" = \"moveWordForward:\";\n  \"~b\" = \"moveWordBackward:\";\n}\n"

// keybindingsRel is the home-relative destination the source deploys to.
var keybindingsRel = []string{"Library", "KeyBindings", "DefaultKeyBinding.dict"}

// TestKeybindingsApplies proves an in-scope key-bindings source deploys from
// keybindings/ to its $HOME destination as a regular-file copy, not a symlink.
func TestKeybindingsApplies(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, keybindingsManifest)
	s.WriteRepoFile(t, filepath.Join("keybindings", "DefaultKeyBinding.dict"), validKeyDict)
	gitCommitAll(t, s.Repo, "keybindings")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	target := s.HomePath(keybindingsRel...)
	assertFileContains(t, target, "moveWordForward:")
	// Deployed as a REGULAR FILE copy — ferry never symlinks under $HOME.
	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("lstat target: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("%s is a symlink; ferry must deploy a regular-file copy", target)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("%s is not a regular file (mode %v)", target, fi.Mode())
	}
}

// TestKeybindingsScopeGated proves the domain is OFF by default: with no
// `keybindings` key in scope, a repo key-bindings source is never deployed.
func TestKeybindingsScopeGated(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest) // no `keybindings` key
	s.WriteRepoFile(t, filepath.Join("keybindings", "DefaultKeyBinding.dict"), validKeyDict)
	gitCommitAll(t, s.Repo, "keybindings not in scope")

	target := s.HomePath(keybindingsRel...)
	tw := s.SnapshotFile(t, target) // absent
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	tw.AssertUnchanged(t) // still absent — domain out of scope
}

// TestKeybindingsRefusesBinaryPlist proves a bplist00 (binary plist) source is
// refused, never deployed: an editor silently saving the dict as binary must not
// clobber the live file with an unreadable form.
func TestKeybindingsRefusesBinaryPlist(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, keybindingsManifest)
	// bplist00 magic header + arbitrary trailing bytes.
	s.WriteRepoFile(t, filepath.Join("keybindings", "DefaultKeyBinding.dict"), "bplist00\x01\x02\x03binary")
	gitCommitAll(t, s.Repo, "binary keybindings")

	target := s.HomePath(keybindingsRel...)
	tw := s.SnapshotFile(t, target) // absent
	stdout, errOut, code := s.Ferry("apply")
	if code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	tw.AssertUnchanged(t) // the binary plist was never deployed
	combined := stdout + errOut
	if !strings.Contains(combined, "BINARY plist") && !strings.Contains(combined, "bplist00") {
		t.Errorf("expected a binary-plist refusal in output; got:\n%s", combined)
	}
}

// TestKeybindingsRefusesMalformedPlist proves the production `plutil -lint` gate
// is LIVE on macOS: a UTF-8 dict that is not a well-formed property list (so the
// pure-Go bplist00/BOM/UTF-8 checks all pass) is still refused before the copy
// lands. Skipped off darwin, where plutil does not exist and the lint is a clean
// no-op by design.
func TestKeybindingsRefusesMalformedPlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plutil -lint is macOS only")
	}
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, keybindingsManifest)
	// Valid UTF-8, no BOM, no bplist00 header — but an unbalanced brace, so only
	// plutil catches it.
	s.WriteRepoFile(t, filepath.Join("keybindings", "DefaultKeyBinding.dict"), "{ \"~f\" = \"moveWordForward:\"; ")
	gitCommitAll(t, s.Repo, "malformed keybindings")

	target := s.HomePath(keybindingsRel...)
	tw := s.SnapshotFile(t, target) // absent
	stdout, errOut, code := s.Ferry("apply")
	if code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	tw.AssertUnchanged(t) // the malformed dict was never deployed
	if !strings.Contains(stdout+errOut, "lint failed") {
		t.Errorf("expected a plutil lint refusal in output; got:\n%s", stdout+errOut)
	}
}

// TestKeybindingsStatusCleanThenDrift proves status reports clean right after
// apply and drift after a live edit — the repo-authoritative reconcile (the OS
// never rewrites the file, so drift appears only when the user edits it).
func TestKeybindingsStatusCleanThenDrift(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, keybindingsManifest)
	s.WriteRepoFile(t, filepath.Join("keybindings", "DefaultKeyBinding.dict"), validKeyDict)
	gitCommitAll(t, s.Repo, "keybindings")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	// Clean immediately after apply: the domain reports in sync, no drift, and no
	// spurious "no longer managed" de-scope warning for the still-managed target.
	stdout, errOut, code := s.Ferry("status")
	if code != 0 {
		t.Fatalf("status exited %d; stderr:\n%s", code, errOut)
	}
	if !strings.Contains(stdout, "no drift detected") {
		t.Errorf("status did not report clean right after apply; stdout:\n%s", stdout)
	}
	if strings.Contains(stdout, "no longer managed") {
		t.Errorf("clean status falsely warned the managed keybindings target is de-scoped; stdout:\n%s", stdout)
	}

	// A live edit must surface as drift (repo-authoritative: apply would skip it).
	target := s.HomePath(keybindingsRel...)
	if err := os.WriteFile(target, []byte("{\n  \"~d\" = \"deleteWordForward:\";\n}\n"), 0o644); err != nil {
		t.Fatalf("live edit: %v", err)
	}
	stdout, errOut, code = s.Ferry("status")
	if code != 0 {
		t.Fatalf("status after edit exited %d; stderr:\n%s", code, errOut)
	}
	if strings.Contains(stdout, "no drift detected") || !strings.Contains(stdout, "drift") {
		t.Errorf("status did not report drift after a live edit; stdout:\n%s", stdout)
	}
}
