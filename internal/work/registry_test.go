package work

import (
	"path/filepath"
	"testing"
)

func TestClaudeProjectsKey(t *testing.T) {
	cases := []struct{ in, want string }{
		// The observed convention: every character outside [A-Za-z0-9]
		// becomes '-'.
		{"/Users/alice/dev/ferry", "-Users-alice-dev-ferry"},
		{"/Users/alice/my.project", "-Users-alice-my-project"},
		{"/Users/alice/my-project", "-Users-alice-my-project"},
		{"/Users/alice/über", "-Users-alice--ber"},
	}
	for _, c := range cases {
		if got := ClaudeProjectsKey(c.in); got != c.want {
			t.Errorf("ClaudeProjectsKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuiltinItems(t *testing.T) {
	items := BuiltinItems()
	byName := map[string]Item{}
	for _, it := range items {
		if _, dup := byName[it.Name]; dup {
			t.Fatalf("duplicate item name %q", it.Name)
		}
		byName[it.Name] = it
	}

	lc := Locator{
		Home:       "/home/bob",
		ProjectDir: "/home/bob/src/proj",
		StoreKey:   "abc123",
	}

	wantPaths := map[string]string{
		ItemNext:        "/home/bob/src/proj/.abcd/.work.local/NEXT.md",
		ItemRunJournal:  "/home/bob/src/proj/.abcd/.work.local/run-journal.json",
		ItemAgentMemory: "/home/bob/.claude/projects/-home-bob-src-proj/memory",
		ItemTranscripts: "/home/bob/.abcd/history/abc123",
	}
	for name, want := range wantPaths {
		it, ok := byName[name]
		if !ok {
			t.Errorf("builtin item %q missing", name)
			continue
		}
		got, err := it.Locate(lc)
		if err != nil {
			t.Errorf("%s.Locate: %v", name, err)
			continue
		}
		if got != filepath.FromSlash(want) {
			t.Errorf("%s.Locate = %q, want %q", name, got, want)
		}
	}

	// Policies and shapes pinned by the plan.
	if it := byName[ItemNext]; it.Policy != PolicyGuardedOverwrite || it.Kind != KindFile || !it.Required {
		t.Errorf("next item = %+v, want guarded-overwrite required file", it)
	}
	if it := byName[ItemRunJournal]; it.Policy != PolicyGuardedOverwrite || it.Kind != KindFile || it.Required {
		t.Errorf("run-journal item = %+v, want guarded-overwrite optional file", it)
	}
	if it := byName[ItemAgentMemory]; it.Policy != PolicyGuardedOverwrite || it.Kind != KindDir {
		t.Errorf("agent-memory item = %+v, want guarded-overwrite dir", it)
	}
	if it := byName[ItemTranscripts]; it.Policy != PolicyUnionMerge || it.Kind != KindDir {
		t.Errorf("transcripts item = %+v, want union-merge dir", it)
	}
}
