package evals

// Platform ACs. AC-platform-macos is the sole GATING platform assertion (macOS
// happy path works). AC-platform-linux-core is a NON-GATING documentation-
// traceability note — it makes NO assertion against the binary.

import (
	"runtime"
	"testing"
)

// TestPlatformMacOS covers AC-platform-macos (GATING on a macOS host): the binary
// runs and performs its documented core functions on macOS today. The concrete
// happy-path behaviour is covered by the other behavioral ACs (apply, capture,
// restore, etc.); here we assert only that the binary runs at all on macOS via a
// trivial `--help` smoke. On non-darwin hosts this is skipped (Linux deferred).
func TestPlatformMacOS_AC_platform_macos(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("AC-platform-macos: gating only on a macOS host; non-darwin is out of scope this run")
	}
	s := NewSandbox(t)
	_, errOut, code := s.Ferry("--help")
	if code != 0 {
		t.Errorf("AC-platform-macos: binary does not run on macOS (`ferry --help` exited %d)\n%s", code, errOut)
	}
	// The full documented core happy path is asserted by the other behavioral ACs.
}

// TestPlatformLinuxCore covers AC-platform-linux-core: NON-GATING documentation-
// traceability note ONLY. The docs say "macOS today. Linux coming soon" — the core
// is described as cross-platform-capable but Linux availability is explicitly
// deferred. This entry exists so the deferred Linux promise is not silently
// dropped; it makes NO assertion against the binary and never fails the gate.
func TestPlatformLinuxCore_AC_platform_linux_core(t *testing.T) {
	t.Parallel()
	t.Skip("AC-platform-linux-core: NON-GATING documentation-traceability note. " +
		"Docs defer Linux availability (\"Linux coming soon\"); Linux behaviour is OUT OF SCOPE for " +
		"live verification this run. When Linux ships, a Linux host should run the documented core " +
		"happy path (dotfiles, deps via present PM, dev-tree scaffold, backup→restore) under the same " +
		"behavioral ACs. No assertion is made against the binary here.")
}
