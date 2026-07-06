package deps

import (
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/platform"
)

// The apt root rail (apt-get install, dpkg-query probes) runs as root under
// `sudo ferry apply --deps`. F19: the dpkg-query probe must carry a `--`
// end-of-options separator for parity with the install rail, so a package name
// can never be read as a dpkg-query option even before ValidateAptName runs.
func TestAptInstalledSet_DpkgQueryHasEndOfOptions(t *testing.T) {
	r := newFakeRunner()
	// dpkg-query returns "not installed" so the probe is exercised for each pkg.
	aptInstalledSet(r, []string{"zsh", "git"})
	sawSeparator := false
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "dpkg-query" {
			joined := strings.Join(c, " ")
			if strings.Contains(joined, " -- zsh") || strings.Contains(joined, " -- git") {
				sawSeparator = true
			} else {
				t.Errorf("dpkg-query call missing `--` end-of-options separator: %v", c)
			}
		}
	}
	if !sawSeparator {
		t.Fatalf("dpkg-query probe never ran with `--`: %v", r.calls)
	}
}

// F11: the root rail resolves apt-get / dpkg-query through the `lookManager` seam
// so production can pin them to a sanitized absolute path (a `sudo` without
// secure_path must not let a poisoned PATH hijack apt-get as root), while unit
// tests inject a shim. brew is NOT a root rail and stays PATH-resolved.
func TestLookManager_SeamResolvesRootRailOnly(t *testing.T) {
	// Production default searches sanitized system dirs, never the inherited PATH.
	for _, dir := range rootRailDirs {
		if !strings.HasPrefix(dir, "/") {
			t.Errorf("rootRailDirs entry %q is not an absolute path", dir)
		}
	}
	if !isRootRailManager(aptGetBin) || !isRootRailManager(dpkgQueryBin) {
		t.Errorf("apt-get and dpkg-query must be classified as root-rail managers")
	}
	if isRootRailManager(brewBin) {
		t.Errorf("brew must NOT be a root-rail manager (it stays PATH-resolved for eval shimming)")
	}

	// The seam is a reassignable package var: a test shim is honoured.
	orig := lookManager
	defer func() { lookManager = orig }()
	lookManager = func(name string) (string, error) { return "/shim/" + name, nil }
	got, err := lookManager("apt-get")
	if err != nil || got != "/shim/apt-get" {
		t.Errorf("lookManager seam not injectable: got %q err %v", got, err)
	}
	_ = platform.ManagerApt
}
