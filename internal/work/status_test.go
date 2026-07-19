package work

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStatus_CleanAfterPack(t *testing.T) {
	fx := newPackFixture(t)
	if _, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts()); err != nil {
		t.Fatal(err)
	}
	s, err := Status(fx.st, fx.lc, fx.id, fx.state)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.Key != fx.id.Key || len(s.Bundles) != 1 || len(s.TopTie) != 0 {
		t.Errorf("status = key %s, %d bundles, tie %d", s.Key, len(s.Bundles), len(s.TopTie))
	}
	if s.Marker == nil || len(s.MarkerDirty) != 0 {
		t.Errorf("marker = %+v dirty %v, want clean marker", s.Marker, s.MarkerDirty)
	}
	if len(s.Diverged) != 0 {
		t.Errorf("diverged = %v, want none", s.Diverged)
	}
	if s.StoreBytes <= 0 {
		t.Errorf("store bytes = %d, want > 0", s.StoreBytes)
	}
}

func TestStatus_MarkerDirtyAfterEdit(t *testing.T) {
	fx := newPackFixture(t)
	if _, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts()); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(fx.repo, ".abcd", ".work.local", "NEXT.md"), "edited after handover\n")
	s, err := Status(fx.st, fx.lc, fx.id, fx.state)
	if err != nil {
		t.Fatal(err)
	}
	var dirty bool
	for _, d := range s.MarkerDirty {
		if strings.Contains(d, "NEXT.md") {
			dirty = true
		}
	}
	if !dirty {
		t.Errorf("MarkerDirty = %v, want NEXT.md flagged", s.MarkerDirty)
	}
	// The same edit also diverges from the pack baseline.
	var diverged bool
	for _, d := range s.Diverged {
		if strings.Contains(d, "NEXT.md") {
			diverged = true
		}
	}
	if !diverged {
		t.Errorf("Diverged = %v, want NEXT.md flagged", s.Diverged)
	}
}

func TestStatus_SurfacesEqualSeqTie(t *testing.T) {
	fx := newPackFixture(t)
	if _, err := Pack(fx.st, fx.lc, fx.id, fx.state, defaultOpts()); err != nil {
		t.Fatal(err)
	}
	forkName := "000001-" + strings.Repeat("9", 64) + ".ferrywork"
	writeFileT(t, filepath.Join(fx.st.ProjectDir(fx.id.Key), forkName), "fork")
	s, err := Status(fx.st, fx.lc, fx.id, fx.state)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.TopTie) != 2 {
		t.Errorf("TopTie = %+v, want the fork pair", s.TopTie)
	}
}

func TestAllWrittenPathsAt_UnionsProjects(t *testing.T) {
	root := t.TempDir()
	s1, err := LoadStateAt(root, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	s1.RecordWritten("/home/x/one", "/home/x/shared")
	if err := s1.Save(); err != nil {
		t.Fatal(err)
	}
	s2, err := LoadStateAt(root, strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	s2.RecordWritten("/home/x/two", "/home/x/shared")
	if err := s2.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := AllWrittenPathsAt(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/home/x/one", "/home/x/shared", "/home/x/two"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("AllWrittenPathsAt = %v, want %v", got, want)
	}

	// An empty or missing state root is an empty union, not an error.
	if got, err := AllWrittenPathsAt(t.TempDir()); err != nil || len(got) != 0 {
		t.Errorf("empty root: %v, %v", got, err)
	}
}
