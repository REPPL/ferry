package dotfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestNestedTargetResolvedContainment pins the symlink-RESOLVING containment
// boundary: a nested destination whose intermediate directory is a symlink
// must be refused when the link resolves outside $HOME or under ~/.ssh —
// lexical validation alone would happily hand back a Target whose write lands
// through the link. In-home links and not-yet-existing parents stay allowed.
func TestNestedTargetResolvedContainment(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, home, outside string) // create links under home
		rel     string
		wantErr error // nil = must succeed
	}{
		{
			name: "fresh parents (nothing exists) are allowed",
			rel:  ".codex/AGENTS.md",
		},
		{
			name: "real intermediate directory is allowed",
			setup: func(t *testing.T, home, _ string) {
				mkdirAll(t, filepath.Join(home, ".claude"))
			},
			rel: ".claude/CLAUDE.md",
		},
		{
			name: "symlinked parent resolving WITHIN home is allowed",
			setup: func(t *testing.T, home, _ string) {
				mkdirAll(t, filepath.Join(home, "real-config"))
				symlink(t, filepath.Join(home, "real-config"), filepath.Join(home, ".claude"))
			},
			rel: ".claude/CLAUDE.md",
		},
		{
			name: "symlinked parent escaping home is refused",
			setup: func(t *testing.T, home, outside string) {
				symlink(t, outside, filepath.Join(home, ".claude"))
			},
			rel:     ".claude/CLAUDE.md",
			wantErr: ErrPathEscapesHome,
		},
		{
			name: "relative symlinked parent escaping home is refused",
			setup: func(t *testing.T, home, _ string) {
				symlink(t, filepath.Join("..", "elsewhere"), filepath.Join(home, "w"))
			},
			rel:     "w/CLAUDE.md",
			wantErr: ErrPathEscapesHome,
		},
		{
			name: "symlinked parent resolving under ~/.ssh is refused",
			setup: func(t *testing.T, home, _ string) {
				// The link is created by path arithmetic only; ~/.ssh itself is
				// never created or touched.
				symlink(t, filepath.Join(home, ".ssh"), filepath.Join(home, "w"))
			},
			rel:     "w/RULES.md",
			wantErr: ErrForbiddenSSHPath,
		},
		{
			name: "symlinked parent resolving under ~/.SSH (case-folded) is refused",
			setup: func(t *testing.T, home, _ string) {
				// A parent link resolving to ~/.SSH: on a case-insensitive
				// filesystem that IS ~/.ssh. The link is pure path arithmetic;
				// neither ~/.ssh nor ~/.SSH is created or touched.
				symlink(t, filepath.Join(home, ".SSH"), filepath.Join(home, "w"))
			},
			rel:     "w/RULES.md",
			wantErr: ErrForbiddenSSHPath,
		},
		{
			name: "deep chain: in-home link to a dir whose child links out is refused",
			setup: func(t *testing.T, home, outside string) {
				mkdirAll(t, filepath.Join(home, "hop"))
				symlink(t, filepath.Join(home, "hop"), filepath.Join(home, "a"))
				symlink(t, outside, filepath.Join(home, "hop", "b"))
			},
			rel:     "a/b/FILE.md",
			wantErr: ErrPathEscapesHome,
		},
		{
			name: "link-to-link chain escaping home is refused",
			setup: func(t *testing.T, home, outside string) {
				symlink(t, outside, filepath.Join(home, "b"))
				symlink(t, filepath.Join(home, "b"), filepath.Join(home, "a"))
			},
			rel:     "a/FILE.md",
			wantErr: ErrPathEscapesHome,
		},
		{
			name: "symlink cycle fails closed",
			setup: func(t *testing.T, home, _ string) {
				symlink(t, filepath.Join(home, "b"), filepath.Join(home, "a"))
				symlink(t, filepath.Join(home, "a"), filepath.Join(home, "b"))
			},
			rel:     "a/FILE.md",
			wantErr: ErrPathEscapesHome,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			outside := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, home, outside)
			}
			_, err := NestedTarget(home, tt.rel, "test/key")
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("NestedTarget(%q) = %v, want success", tt.rel, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NestedTarget(%q) = %v, want %v", tt.rel, err, tt.wantErr)
			}
		})
	}
}

// TestNestedTargetCycleDoesNotHang guards the hop bound: a two-link cycle in
// the parent chain must refuse (fail closed), not loop forever.
func TestNestedTargetLexicalStillEnforced(t *testing.T) {
	home := t.TempDir()
	for rel, want := range map[string]error{
		"../outside/FILE.md": ErrPathEscapesHome,
		".ssh/config":        ErrForbiddenSSHPath,
	} {
		if _, err := NestedTarget(home, rel, "k"); !errors.Is(err, want) {
			t.Errorf("NestedTarget(%q) = %v, want %v", rel, err, want)
		}
	}
	if _, err := NestedTarget(home, string(filepath.Separator)+"abs/FILE.md", "k"); !errors.Is(err, ErrPathEscapesHome) {
		t.Errorf("absolute rel not refused: %v", err)
	}
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
