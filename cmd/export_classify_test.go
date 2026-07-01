package cmd

// Unit coverage for classifyExportEntry's gate ORDER and content policy — the two
// review findings that are pure per-entry logic:
//
//   - the secret-shaped PATH gate runs FIRST, so a secret-shaped filename that is
//     ALSO a symlink (or otherwise refusable) is withheld via the REDACTED
//     reasonSecretPath sentinel and can never reach a branch that echoes the path
//     (which would re-leak the secret token);
//   - a binary (non-text) tracked file is INCLUDED (its vetted bytes returned),
//     skipping only the text content secret scan — not withheld.
//
// classifyExportEntry needs only a plain directory on disk (no git), so these are
// deterministic and fast.

import (
	"os"
	"path/filepath"
	"testing"
)

// A high-entropy token used as a FILENAME component; secretInPath must flag it. The
// `.txt` form is what the export path-secret gate keys on (matches the eval).
const secretShapedName = "AKIAIOSFODNN7EXAMPLEXYZ.txt"

// TestClassifySecretPathFirst_SymlinkNoLeak asserts that a path whose component is
// secret-shaped AND which is a symlink is withheld with the REDACTED sentinel — the
// path-secret gate runs before the symlink/safeRepoPath guard, so the symlink
// "refused: ..." message (which would echo the secret-shaped name) is never reached.
func TestClassifySecretPathFirst_SymlinkNoLeak(t *testing.T) {
	repo := t.TempDir()
	// A tracked symlink whose NAME is secret-shaped. If safeRepoPath ran first it
	// would return `refused: ...` and the caller would print `rel` (the secret name).
	target := filepath.Join(repo, "target.txt")
	if err := os.WriteFile(target, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repo, secretShapedName)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	data, reason, ok := classifyExportEntry(repo, secretShapedName, false)
	if ok {
		t.Fatalf("secret-shaped symlink was INCLUDED (must be withheld)")
	}
	if data != nil {
		t.Errorf("withheld entry returned content bytes")
	}
	// The reason must be the REDACTED sentinel, NOT a "refused: ..." message that
	// would carry the secret-shaped filename.
	if reason != reasonSecretPath {
		t.Errorf("expected the redacted reasonSecretPath sentinel, got %q (leaks the path?)", reason)
	}
}

// TestClassifySecretPathFirst_RegularNoLeak is the plain (non-symlink) case: a
// secret-shaped regular filename is likewise withheld via the redacted sentinel.
func TestClassifySecretPathFirst_RegularNoLeak(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, secretShapedName), []byte("benign\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, reason, ok := classifyExportEntry(repo, secretShapedName, false)
	if ok || reason != reasonSecretPath {
		t.Errorf("secret-shaped regular file: want (withheld, reasonSecretPath), got ok=%v reason=%q", ok, reason)
	}
}

// TestClassifyBinaryIncluded asserts a binary (NUL-bearing) tracked file is INCLUDED
// with its bytes returned — it passes the path/regular-file gates but skips the
// text-only content secret scan, and is NOT withheld.
func TestClassifyBinaryIncluded(t *testing.T) {
	repo := t.TempDir()
	body := []byte{0x00, 0x01, 0x02, 'x', 0x00, 'y'} // NUL byte ⇒ binary
	if err := os.WriteFile(filepath.Join(repo, "blob.bin"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	data, reason, ok := classifyExportEntry(repo, "blob.bin", false)
	if !ok {
		t.Fatalf("binary file was WITHHELD (%q) — must be included", reason)
	}
	if string(data) != string(body) {
		t.Errorf("binary content not passed through verbatim:\nwant %v\ngot  %v", body, data)
	}
}

// TestClassifyTextSecretStillWithheld guards the policy boundary: a TEXT file whose
// content carries a high-confidence secret is still withheld (the binary carve-out
// must not weaken the text content scan).
func TestClassifyTextSecretStillWithheld(t *testing.T) {
	repo := t.TempDir()
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUFAKEFAKEFAKEFAKEFAKEdeadbeefcafe0000\n" +
		"-----END OPENSSH PRIVATE KEY-----\n"
	if err := os.WriteFile(filepath.Join(repo, "secret.txt"), []byte(pem), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, ok := classifyExportEntry(repo, "secret.txt", false)
	if ok {
		t.Errorf("text file with an embedded secret was INCLUDED (must be withheld)")
	}
}
