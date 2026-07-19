package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMachineConfig_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	want := MachineConfig{Hostname: "test-host", Repo: filepath.Join(dir, "clone")}
	if err := saveMachineConfigTo(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := loadMachineConfigFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Hostname != want.Hostname {
		t.Errorf("hostname: got %q want %q", got.Hostname, want.Hostname)
	}
	if got.Repo != want.Repo {
		t.Errorf("repo: got %q want %q", got.Repo, want.Repo)
	}

	// The persisted file must carry BOTH the identity key and the repo-path key
	// (AC-loc-config-toml requires both).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	body := string(data)
	if !contains(body, "hostname") {
		t.Errorf("config.toml missing hostname identity key:\n%s", body)
	}
	if !contains(body, "repo") {
		t.Errorf("config.toml missing repo key:\n%s", body)
	}
}

func TestMachineConfig_MissingFileIsNotExist(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "absent.toml")
	_, err := loadMachineConfigFrom(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing config should report os.ErrNotExist (first-run signal); got %v", err)
	}
}

func TestMachineConfig_RequiresBothFields(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")

	if err := saveMachineConfigTo(path, MachineConfig{Repo: "/x"}); err == nil {
		t.Error("save without hostname should fail")
	}
	if err := saveMachineConfigTo(path, MachineConfig{Hostname: "h"}); err == nil {
		t.Error("save without repo should fail")
	}
}

func TestMachineConfig_LoadValidatesRequiredFields(t *testing.T) {
	t.Parallel()
	// A hand-written config.toml that parses cleanly but omits a required field
	// must fail to load — otherwise an empty repo path reaches manifest resolution.
	cases := []struct {
		name string
		body string
	}{
		{"missing repo", "hostname = \"h\"\n"},
		{"missing hostname", "repo = \"/x\"\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := loadMachineConfigFrom(path); err == nil {
				t.Errorf("load of %s config should error", c.name)
			}
		})
	}
}

func TestMachineConfig_MalformedTOML(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("hostname = \"unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMachineConfigFrom(path); err == nil {
		t.Error("malformed config.toml should error, not panic")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestMachineConfig_WorkTableOptional(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Without [work] the config loads with no work settings.
	plain := MachineConfig{Hostname: "h", Repo: filepath.Join(dir, "clone")}
	if err := saveMachineConfigTo(path, plain); err != nil {
		t.Fatal(err)
	}
	got, err := loadMachineConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Work != nil {
		t.Errorf("Work = %+v, want nil when the table is absent", got.Work)
	}

	// With [work] the settings round-trip.
	withWork := plain
	withWork.Work = &WorkConfig{Store: "/Users/Shared/ferry-cargo", Keep: 3, AllowSyncRoot: true}
	if err := saveMachineConfigTo(path, withWork); err != nil {
		t.Fatal(err)
	}
	got, err = loadMachineConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Work == nil || got.Work.Store != "/Users/Shared/ferry-cargo" || got.Work.Keep != 3 || !got.Work.AllowSyncRoot {
		t.Errorf("Work = %+v", got.Work)
	}

	// A hand-written [work] table parses too.
	handWritten := "hostname = \"h\"\nrepo = \"/r\"\n\n[work]\nstore = \"/cargo\"\n"
	if err := os.WriteFile(path, []byte(handWritten), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = loadMachineConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Work == nil || got.Work.Store != "/cargo" || got.Work.Keep != 0 {
		t.Errorf("hand-written Work = %+v", got.Work)
	}
}
