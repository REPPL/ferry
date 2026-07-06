package dotfile

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestTargetForResolvedContainment pins that TargetFor runs the symlink-RESOLVING
// containment guard, not only the lexical one: a nested manifest name such as
// ".config/foo" whose intermediate parent (~/.config) is a symlink escaping $HOME
// or resolving into ~/.ssh must be refused, exactly as NestedTarget already
// refuses it. Lexical validation alone would hand back a Target whose write lands
// through the link. The legitimate cases (real parent, flat dotfile) still pass.
func TestTargetForResolvedContainment(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, home, outside string)
		decl    string
		wantErr error // nil = must succeed
	}{
		{
			name: "flat dotfile is allowed",
			decl: ".zshrc",
		},
		{
			name: "nested name with real parent is allowed",
			setup: func(t *testing.T, home, _ string) {
				mkdirAll(t, filepath.Join(home, ".config"))
			},
			decl: ".config/foo",
		},
		{
			name: "nested name with in-home symlinked parent is allowed",
			setup: func(t *testing.T, home, _ string) {
				mkdirAll(t, filepath.Join(home, "real-config"))
				symlink(t, filepath.Join(home, "real-config"), filepath.Join(home, ".config"))
			},
			decl: ".config/foo",
		},
		{
			name: "nested name with parent symlinked outside home is refused",
			setup: func(t *testing.T, home, outside string) {
				symlink(t, outside, filepath.Join(home, ".config"))
			},
			decl:    ".config/foo",
			wantErr: ErrPathEscapesHome,
		},
		{
			name: "nested name with parent symlinked into ~/.ssh is refused",
			setup: func(t *testing.T, home, _ string) {
				// Pure path arithmetic; ~/.ssh itself is never created or touched.
				symlink(t, filepath.Join(home, ".ssh"), filepath.Join(home, ".config"))
			},
			decl:    ".config/foo",
			wantErr: ErrForbiddenSSHPath,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			outside := t.TempDir()
			repo := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, home, outside)
			}
			_, err := TargetFor(repo, home, tt.decl)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("TargetFor(%q) = %v, want success", tt.decl, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("TargetFor(%q) = %v, want %v", tt.decl, err, tt.wantErr)
			}
		})
	}
}
