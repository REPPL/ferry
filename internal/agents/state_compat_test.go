package agents

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/REPPL/ferry/internal/statefile"
)

// The agents target record postdates v0.3.x (the agents domain did not exist at
// tag v0.3.2), so its "previous form" fixture is the real bare path->key map the
// pre-versioning code writes — produced by driving `ferry apply` with the agents
// domain enabled in a throwaway sandbox HOME. The absolute destinations are
// scrubbed to a "__HOME__" placeholder; the version logic treats them as opaque
// strings, so the fixture is used verbatim.
const (
	compatTargets = "testdata/compat/agents-targets.json"
	futureTargets = "testdata/compat/future-agents-targets.json"
)

func seedTargets(t *testing.T, fixture string) string {
	t.Helper()
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, targetsFileName), data, 0o600); err != nil {
		t.Fatalf("seed targets: %v", err)
	}
	return dir
}

// TestCompatMigratesAgentsTargets proves a mutating RecordTargets over a
// pre-versioning record migrates it forward (backing the original up first) and
// unions the new destination in without losing the recorded ones.
func TestCompatMigratesAgentsTargets(t *testing.T) {
	dir := seedTargets(t, compatTargets)
	path := filepath.Join(dir, targetsFileName)
	original, _ := os.ReadFile(path)

	if err := RecordTargets(dir, map[string]string{"agents/gemini": "__HOME__/.gemini/GEMINI.md"}); err != nil {
		t.Fatalf("RecordTargets: %v", err)
	}

	paths, err := RecordedTargetPaths(dir)
	if err != nil {
		t.Fatalf("RecordedTargetPaths: %v", err)
	}
	want := map[string]bool{
		"__HOME__/.claude/CLAUDE.md": true, // from the v0.3.x-style record
		"__HOME__/.codex/AGENTS.md":  true, // from the v0.3.x-style record
		"__HOME__/.gemini/GEMINI.md": true, // unioned this run
	}
	if len(paths) != len(want) {
		t.Fatalf("recorded paths = %v; want the 3 unioned destinations", paths)
	}
	for _, p := range paths {
		if !want[p] {
			t.Fatalf("unexpected recorded path %q", p)
		}
	}

	// The file is now the versioned envelope, and the original is preserved.
	migrated, _ := os.ReadFile(path)
	if v, versioned := statefile.PeekVersion(migrated); !versioned || v != targetsVersion {
		t.Fatalf("migrated record version = %d,%v; want %d", v, versioned, targetsVersion)
	}
	bak, err := os.ReadFile(path + ".pre-v1.bak")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(bak) != string(original) {
		t.Fatalf("backup content mismatch:\n got %q\nwant %q", bak, original)
	}
}

// TestCompatFutureAgentsRefused proves a record written by a newer ferry is
// refused on both the write and read paths and is left untouched.
func TestCompatFutureAgentsRefused(t *testing.T) {
	dir := seedTargets(t, futureTargets)
	path := filepath.Join(dir, targetsFileName)
	before, _ := os.ReadFile(path)

	err := RecordTargets(dir, map[string]string{"agents/gemini": "__HOME__/.gemini/GEMINI.md"})
	var fv *statefile.FutureVersionError
	if !errors.As(err, &fv) {
		t.Fatalf("RecordTargets on a future-version record: got %v; want *statefile.FutureVersionError", err)
	}

	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("future-version record was modified")
	}
	if _, err := os.Stat(path + ".pre-v99.bak"); !os.IsNotExist(err) {
		t.Fatalf("a refused future-version record must not be backed up")
	}

	if _, err := RecordedTargetPaths(dir); !errors.As(err, &fv) {
		t.Fatalf("RecordedTargetPaths on a future-version record must refuse; got %v", err)
	}
}
