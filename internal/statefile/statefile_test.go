package statefile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPeekVersion(t *testing.T) {
	cases := []struct {
		name      string
		data      string
		wantVer   int
		versioned bool
	}{
		{"versioned envelope", `{"version":1,"applied":{}}`, 1, true},
		{"future version", `{"version":99,"applied":{}}`, 99, true},
		{"legacy bare map", `{"zshrc":"deadbeef"}`, 0, false},
		{"legacy map with string version key", `{"version":"deadbeef"}`, 0, false},
		{"not an object", `["a","b"]`, 0, false},
		{"garbage", `not json`, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, ok := PeekVersion([]byte(c.data))
			if v != c.wantVer || ok != c.versioned {
				t.Fatalf("PeekVersion(%s) = (%d,%v), want (%d,%v)", c.data, v, ok, c.wantVer, c.versioned)
			}
		})
	}
}

func TestResolveLegacyMigrates(t *testing.T) {
	v, migrate, err := Resolve("/x/state.json", []byte(`{"zshrc":"h"}`), 1)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if v != 1 || !migrate {
		t.Fatalf("legacy: got version=%d migrate=%v, want 1,true", v, migrate)
	}
}

func TestResolveCurrentNoMigrate(t *testing.T) {
	v, migrate, err := Resolve("/x/state.json", []byte(`{"version":1,"applied":{}}`), 1)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if v != 1 || migrate {
		t.Fatalf("current: got version=%d migrate=%v, want 1,false", v, migrate)
	}
}

func TestResolveInvalidVersionRefused(t *testing.T) {
	// A declared version below 1 is corrupt, never a legacy form: accepting it
	// as current would decode an empty envelope and the next save would
	// permanently overwrite the store with no backup. It must be a clean error
	// naming the file and the invalid version, with no migration flagged.
	for _, v := range []string{"0", "-3"} {
		data := []byte(`{"version":` + v + `,"applied":{}}`)
		_, migrate, err := Resolve("/x/state.json", data, 1)
		if err == nil {
			t.Fatalf("Resolve(version=%s) = nil error; want a corrupt-version refusal", v)
		}
		if migrate {
			t.Fatalf("Resolve(version=%s) flagged migrate; a corrupt file must never be rewritten", v)
		}
		for _, want := range []string{"/x/state.json", v} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q missing %q", err, want)
			}
		}
	}
}

func TestResolveFutureRefused(t *testing.T) {
	_, _, err := Resolve("/x/state.json", []byte(`{"version":99,"applied":{}}`), 1)
	var fv *FutureVersionError
	if !errors.As(err, &fv) {
		t.Fatalf("want *FutureVersionError, got %v", err)
	}
	if fv.Found != 99 || fv.Supported != 1 || fv.Path != "/x/state.json" {
		t.Fatalf("FutureVersionError fields wrong: %+v", fv)
	}
	// The message must name the file and both versions so an operator (or agent)
	// hitting it can self-correct without reading the source.
	msg := fv.Error()
	for _, want := range []string{"/x/state.json", "99", "version 1", "left untouched"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q missing %q", msg, want)
		}
	}
}

func TestBackupForMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	original := []byte(`{"zshrc":"original"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	bak, err := BackupForMigration(path, 1)
	if err != nil {
		t.Fatalf("BackupForMigration: %v", err)
	}
	if bak != path+".pre-v1.bak" {
		t.Fatalf("backup path = %q, want %q", bak, path+".pre-v1.bak")
	}
	got, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("backup content = %q, want %q", got, original)
	}
	if info, err := os.Stat(bak); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v (err %v), want 0600", info.Mode().Perm(), err)
	}

	// A second call must NOT clobber the first, original backup even if the live
	// file has since changed.
	if err := os.WriteFile(path, []byte(`{"zshrc":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupForMigration(path, 1); err != nil {
		t.Fatalf("second BackupForMigration: %v", err)
	}
	got2, _ := os.ReadFile(bak)
	if string(got2) != string(original) {
		t.Fatalf("backup was clobbered: got %q, want original %q", got2, original)
	}
}
