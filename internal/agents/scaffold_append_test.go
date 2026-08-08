package agents

// appendLineOnce writes ignore patterns into `.git/info/exclude`, the documented
// hand-edit surface. Ignore-file parsing is line-based, so appending to a file
// whose last line carries no terminator fuses the two patterns: the user's final
// pattern is destroyed AND ferry's never takes effect, while scaffold reports
// "excluded: … via git info/exclude".

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MAJOR: an existing file with no trailing newline keeps its last pattern intact,
// and the appended pattern lands on its own line.
func TestAppendLineOnceNormalisesMissingTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exclude")
	if err := os.WriteFile(path, []byte("# user patterns\nbuild"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := appendLineOnce(path, ".abcd/.work.local/"); err != nil {
		t.Fatalf("appendLineOnce: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var sawBuild, sawNew bool
	for _, l := range lines {
		switch l {
		case "build":
			sawBuild = true
		case ".abcd/.work.local/":
			sawNew = true
		}
		if strings.Contains(l, "build.abcd") {
			t.Fatalf("the append fused two patterns into %q — the user's ignore rule is destroyed and ferry's never applies", l)
		}
	}
	if !sawBuild {
		t.Errorf("the pre-existing `build` pattern did not survive the append:\n%s", data)
	}
	if !sawNew {
		t.Errorf("the appended pattern is not present as its own line:\n%s", data)
	}
}

// The dedup contract still holds after normalisation: a second call is a no-op.
func TestAppendLineOnceStaysIdempotentAfterNormalising(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exclude")
	if err := os.WriteFile(path, []byte("build"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := appendLineOnce(path, ".abcd/.work.local/"); err != nil {
			t.Fatalf("appendLineOnce call %d: %v", i, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), ".abcd/.work.local/"); got != 1 {
		t.Fatalf("pattern written %d times, want exactly 1:\n%s", got, data)
	}
}

// A newline-terminated file (git's own stock template) is unchanged in shape: no
// blank line is introduced.
func TestAppendLineOnceLeavesTerminatedFileUnpadded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exclude")
	if err := os.WriteFile(path, []byte("# comment\nbuild\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := appendLineOnce(path, ".abcd/.work.local/"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "# comment\nbuild\n.abcd/.work.local/\n"; string(data) != want {
		t.Fatalf("got %q, want %q", data, want)
	}
}

// An absent file is created with just the pattern — no leading blank line.
func TestAppendLineOnceCreatesFileWithoutLeadingBlank(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "exclude")
	if err := appendLineOnce(path, ".abcd/.work.local/"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := ".abcd/.work.local/\n"; string(data) != want {
		t.Fatalf("got %q, want %q", data, want)
	}
}
