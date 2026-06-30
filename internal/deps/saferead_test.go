package deps

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/REPPL/ferry/internal/platform"
)

// symlinkManifest creates a deps/ dir with the named manifest symlinked at a
// fake "~/.ssh/config"-like sentinel OUTSIDE the deps tree, and returns the
// depsDir, the symlinked manifest path, the sentinel path, and its body. The
// sentinel never lives under the real ~/.ssh.
func symlinkManifest(t *testing.T, name string) (depsDir, manifest, sentinel, body string) {
	t.Helper()
	dir := t.TempDir()
	depsDir = filepath.Join(dir, "deps")
	if err := os.MkdirAll(depsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel = filepath.Join(dir, "secret-config")
	body = "Host *\n  IdentityFile ~/.ssh/id_ed25519\n"
	if err := os.WriteFile(sentinel, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest = filepath.Join(depsDir, name)
	if err := os.Symlink(sentinel, manifest); err != nil {
		t.Fatal(err)
	}
	return depsDir, manifest, sentinel, body
}

// TestInstallBrew_RefusesSymlinkedBrewfile: a symlinked deps/Brewfile.<goos> must
// be refused — brew bundle is NEVER invoked on it and the symlink target is left
// byte-identical (never read through).
func TestInstallBrew_RefusesSymlinkedBrewfile(t *testing.T) {
	depsDir, manifest, sentinel, body := symlinkManifest(t, "Brewfile.darwin")

	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: manifest, Local: filepath.Join(depsDir, "Brewfile.darwin.local")}
	r := newFakeRunner()
	if _, err := install(m, r); err == nil {
		t.Fatal("install with symlinked Brewfile: want refusal error, got nil")
	}
	if r.invoked("bundle --file=" + manifest) {
		t.Errorf("install invoked brew bundle on a symlinked Brewfile — must refuse first: %v", r.calls)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("symlink target was modified: got %q want %q", got, body)
	}
}

// TestInstallApt_RefusesSymlinkedAptTxt: a symlinked deps/apt.txt must be refused
// before os.ReadFile — apt-get is never invoked and the target is untouched.
func TestInstallApt_RefusesSymlinkedAptTxt(t *testing.T) {
	_, manifest, sentinel, body := symlinkManifest(t, "apt.txt")

	m := Manifest{Manager: platform.ManagerApt, GOOS: "linux", Shared: manifest}
	r := newFakeRunner()
	if _, err := install(m, r); err == nil {
		t.Fatal("install with symlinked apt.txt: want refusal error, got nil")
	}
	if r.invoked("install -y") {
		t.Errorf("install invoked apt-get on a symlinked apt.txt — must refuse first: %v", r.calls)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("symlink target was modified: got %q want %q", got, body)
	}
}

// TestEntries_RefusesSymlinkedManifest: SelectManifest-derived Entries must refuse
// a symlinked manifest before reading it.
func TestEntries_RefusesSymlinkedManifest(t *testing.T) {
	_, manifest, _, _ := symlinkManifest(t, "apt.txt")
	m := Manifest{Manager: platform.ManagerApt, GOOS: "linux", Shared: manifest}
	if _, err := m.Entries(); err == nil {
		t.Fatal("Entries with symlinked manifest: want refusal error, got nil")
	}
}

// TestInstallBrew_RegularBrewfileWorks: a regular (non-symlink) Brewfile installs
// normally — the guard does not interfere.
func TestInstallBrew_RegularBrewfileWorks(t *testing.T) {
	dir := t.TempDir()
	depsDir := filepath.Join(dir, "deps")
	if err := os.MkdirAll(depsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(depsDir, "Brewfile.darwin")
	if err := os.WriteFile(shared, []byte("brew \"zoxide\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: shared, Local: filepath.Join(depsDir, "Brewfile.darwin.local")}
	r := newFakeRunner()
	if _, err := install(m, r); err != nil {
		t.Fatalf("install regular Brewfile: %v", err)
	}
	if !r.invoked("bundle --file=" + shared) {
		t.Errorf("install did not invoke brew bundle on the regular Brewfile: %v", r.calls)
	}
}

// symlinkDepsDir makes a repo whose deps/ is a SYMLINK to a secret directory
// (standing in for `<repo>/deps -> ~/.ssh`). The secret dir holds a fake ssh
// config-like file at the manifest's basename, so reading THROUGH the symlinked
// deps/ would leak it. Returns the repo's deps path (the symlink), the would-be
// manifest path under it, the secret target file, and its body. The secret dir
// never lives under the real ~/.ssh.
func symlinkDepsDir(t *testing.T, name string) (depsDir, manifest, secret, body string) {
	t.Helper()
	dir := t.TempDir()
	secretDir := filepath.Join(dir, "secret-dir")
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret = filepath.Join(secretDir, name)
	body = "Host *\n  IdentityFile ~/.ssh/id_ed25519\n"
	if err := os.WriteFile(secret, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	depsDir = filepath.Join(dir, "deps")
	if err := os.Symlink(secretDir, depsDir); err != nil {
		t.Fatal(err)
	}
	manifest = filepath.Join(depsDir, name)
	return depsDir, manifest, secret, body
}

// TestInstallBrew_RefusesSymlinkedDepsDir is Fix 1's core: a SYMLINKED deps/
// directory (deps -> a dir holding a ~/.ssh/config-like file) must be refused —
// the guard walks from the REPO ROOT so the deps component itself is Lstat'd.
// brew bundle is NEVER invoked and the symlink target is never read through.
func TestInstallBrew_RefusesSymlinkedDepsDir(t *testing.T) {
	depsDir, manifest, secret, body := symlinkDepsDir(t, "Brewfile.darwin")

	m := Manifest{Manager: platform.ManagerBrew, GOOS: "darwin", Shared: manifest, Local: filepath.Join(depsDir, "Brewfile.darwin.local")}
	r := newFakeRunner()
	if _, err := install(m, r); err == nil {
		t.Fatal("install with symlinked deps/ dir: want refusal error, got nil")
	}
	if r.invoked("bundle --file=" + manifest) {
		t.Errorf("install invoked brew bundle through a symlinked deps/ dir — must refuse first: %v", r.calls)
	}
	got, err := os.ReadFile(secret)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("secret target was modified: got %q want %q", got, body)
	}
}

// TestEntries_RefusesSymlinkedDepsDir: a SYMLINKED deps/ dir must also be refused
// on the apt read path before os.ReadFile reaches through it.
func TestEntries_RefusesSymlinkedDepsDir(t *testing.T) {
	_, manifest, _, _ := symlinkDepsDir(t, "apt.txt")
	m := Manifest{Manager: platform.ManagerApt, GOOS: "linux", Shared: manifest}
	if _, err := m.Entries(); err == nil {
		t.Fatal("Entries with symlinked deps/ dir: want refusal error, got nil")
	}
}
