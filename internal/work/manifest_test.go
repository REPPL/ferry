package work

import (
	"errors"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/statefile"
)

func validManifest() *Manifest {
	return &Manifest{
		FerryVersion: "v0.10.0-dev",
		Key:          strings.Repeat("a", 40),
		Roots:        []string{strings.Repeat("a", 40)},
		RepoPath:     "/home/alice/src/proj",
		Home:         "/home/alice",
		Seq:          3,
		PackedBy:     "alice@studio",
		PackedAt:     "2026-07-18T10:00:00Z",
		ScanVerdict:  ScanVerdictClean,
		Items: []ManifestItem{
			{Name: ItemNext, Included: true, Files: []ManifestFile{
				{Path: "NEXT.md", Size: 5, SHA256: strings.Repeat("b", 64)},
			}},
			{Name: ItemTranscripts, Included: false, Reason: "missing"},
		},
	}
}

func TestManifestRoundTrip(t *testing.T) {
	m := validManifest()
	data, err := m.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeManifest(data)
	if err != nil {
		t.Fatalf("DecodeManifest: %v", err)
	}
	if got.Version != manifestVersion {
		t.Errorf("Version = %d, want %d", got.Version, manifestVersion)
	}
	if got.Key != m.Key || got.Seq != 3 || len(got.Items) != 2 {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.Items[0].Files[0].SHA256 != m.Items[0].Files[0].SHA256 {
		t.Errorf("file hash lost in round trip")
	}
}

func TestDecodeManifest_FutureVersionRefused(t *testing.T) {
	m := validManifest()
	data, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	bumped := strings.Replace(string(data), `"version": 1`, `"version": 99`, 1)
	if bumped == string(data) {
		t.Fatal("test setup: version field not found to bump")
	}
	_, err = DecodeManifest([]byte(bumped))
	var fve *statefile.FutureVersionError
	if !errors.As(err, &fve) {
		t.Fatalf("err = %v, want *statefile.FutureVersionError", err)
	}
}

func TestDecodeManifest_UnversionedRefused(t *testing.T) {
	if _, err := DecodeManifest([]byte(`{"key":"abc"}`)); err == nil {
		t.Fatal("unversioned manifest accepted, want refusal")
	}
	if _, err := DecodeManifest([]byte(`not json`)); err == nil {
		t.Fatal("non-JSON manifest accepted, want refusal")
	}
}

func TestDecodeManifest_BadPathsRefused(t *testing.T) {
	for _, bad := range []string{"../escape", "/abs", `a\b`, "", "a/../b", "./x"} {
		m := validManifest()
		m.Items[0].Files[0].Path = bad
		data, err := m.Encode()
		if err != nil {
			// Encode may refuse too; that also protects the contract.
			continue
		}
		if _, err := DecodeManifest(data); err == nil {
			t.Errorf("manifest with file path %q accepted, want refusal", bad)
		}
	}
}

func TestDecodeManifest_BadIdentityRefused(t *testing.T) {
	m := validManifest()
	m.Key = "not-hex"
	if data, err := m.Encode(); err == nil {
		if _, err := DecodeManifest(data); err == nil {
			t.Error("manifest with non-hex key accepted, want refusal")
		}
	}
	m = validManifest()
	m.Roots = nil
	if data, err := m.Encode(); err == nil {
		if _, err := DecodeManifest(data); err == nil {
			t.Error("manifest with no roots accepted, want refusal")
		}
	}
}

func TestDecodeManifest_UnknownFieldRefused(t *testing.T) {
	m := validManifest()
	data, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"ferry_version"`, `"stray": 1, "ferry_version"`, 1)
	if _, err := DecodeManifest([]byte(tampered)); err == nil {
		t.Fatal("manifest with unknown top-level field accepted, want refusal")
	}
}

func TestDecodeManifest_CaseFoldDuplicatePathsRefused(t *testing.T) {
	m := validManifest()
	m.Items[0].Files = append(m.Items[0].Files,
		ManifestFile{Path: "next.MD", Size: 1, SHA256: strings.Repeat("c", 64)},
		ManifestFile{Path: "NEXT.md", Size: 1, SHA256: strings.Repeat("d", 64)},
	)
	if data, err := m.Encode(); err == nil {
		if _, err := DecodeManifest(data); err == nil {
			t.Error("case-fold duplicate file paths accepted, want refusal")
		}
	}
}
