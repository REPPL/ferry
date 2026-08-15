package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/config"
)

// ignoredLines returns the trimmed, non-empty lines of the repo's .gitignore.
func ignoredLines(t *testing.T, repo string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// wantIgnorePatterns is the FULL per-machine ignore set every repo ferry creates
// or adopts must carry: the local manifest, the local/ layer, and the per-machine
// deps overlay deps/Brewfile.<goos>.local. The overlay is documented as a
// per-machine, gitignored file ("belongs to one machine only"); if it is not
// ignored, `ferry sync`'s `git add -A` commits it, every other machine clones and
// installs it via `apply --deps`, and `bundle export` (tracked-set driven) ships
// it — the exact opposite of the documented promise.
var wantIgnorePatterns = []string{config.LocalManifestName, "local/", "deps/Brewfile.*.local"}

// TestEnsureLocalLayerIgnored_CoversPerMachineDepsOverlay is the regression for
// the missing deps overlay pattern: the .gitignore ferry writes must ignore all
// three per-machine artefacts.
func TestEnsureLocalLayerIgnored_CoversPerMachineDepsOverlay(t *testing.T) {
	repo := t.TempDir()
	if err := ensureLocalLayerIgnored(repo); err != nil {
		t.Fatalf("ensureLocalLayerIgnored: %v", err)
	}
	got := ignoredLines(t, repo)
	for _, want := range wantIgnorePatterns {
		if !containsLine(got, want) {
			t.Errorf("fresh .gitignore does not ignore %q (per-machine artefact would be committed); got %v", want, got)
		}
	}
}

// TestEnsureLocalLayerIgnored_IdempotentAndPreserving pins the append-only,
// idempotent contract: a second call adds nothing, and pre-existing user entries
// (including one of the wanted patterns already present) survive untouched.
func TestEnsureLocalLayerIgnored_IdempotentAndPreserving(t *testing.T) {
	repo := t.TempDir()
	// A repo that already ignores something of its own AND already carries one of
	// ferry's patterns (no trailing newline, to exercise the newline fixup).
	seed := "# user entries\nnotes.private\ndeps/Brewfile.*.local"
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureLocalLayerIgnored(repo); err != nil {
		t.Fatalf("ensureLocalLayerIgnored: %v", err)
	}
	first := ignoredLines(t, repo)
	if !containsLine(first, "notes.private") {
		t.Errorf("pre-existing user entry was dropped: %v", first)
	}
	for _, want := range wantIgnorePatterns {
		if !containsLine(first, want) {
			t.Errorf(".gitignore does not ignore %q after adoption; got %v", want, first)
		}
	}
	if n := countLine(first, "deps/Brewfile.*.local"); n != 1 {
		t.Errorf("already-present pattern duplicated %d times: %v", n, first)
	}

	before, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureLocalLayerIgnored(repo); err != nil {
		t.Fatalf("ensureLocalLayerIgnored (second call): %v", err)
	}
	after, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("ensureLocalLayerIgnored is not idempotent:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestPlannedCommitGate_GitignoreMatchesWriter keeps the pre-create secret gate's
// MODEL of the initial commit in lockstep with what ensureLocalLayerIgnored
// actually writes: the planned .gitignore must carry the same pattern set, or the
// gate scans a file that differs from the one committed.
func TestPlannedCommitGate_GitignoreMatchesWriter(t *testing.T) {
	planned := plannedCommitContents(declareOnlyPlan(""))[".gitignore"]
	for _, want := range wantIgnorePatterns {
		if !containsLine(ignoreLinesOf(planned), want) {
			t.Errorf("plannedCommitContents .gitignore missing %q (gate model diverges from ensureLocalLayerIgnored); got %q", want, planned)
		}
	}
	// Byte-for-byte agreement with the real writer on a fresh repo.
	repo := t.TempDir()
	if err := ensureLocalLayerIgnored(repo); err != nil {
		t.Fatalf("ensureLocalLayerIgnored: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != planned {
		t.Errorf("planned .gitignore != written .gitignore\nplanned: %q\nwritten: %q", planned, written)
	}
}

func ignoreLinesOf(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func containsLine(lines []string, want string) bool {
	return countLine(lines, want) > 0
}

func countLine(lines []string, want string) int {
	n := 0
	for _, l := range lines {
		if l == want {
			n++
		}
	}
	return n
}
