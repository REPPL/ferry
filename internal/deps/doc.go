// Package deps is ferry's dependency domain: per-platform manifest selection
// plus the GATED install step that the apply --deps and capture commands compose
// in a later wave. It is pure logic — no cobra/command dependencies.
//
// Manifests live in a deps/ directory inside the config repo, one per
// platform/manager:
//
//   - deps/Brewfile.darwin — macOS Homebrew (incl. casks + fonts)
//   - deps/Brewfile.linux  — Linuxbrew (no casks/fonts)
//   - deps/apt.txt         — optional Debian/Ubuntu, Homebrew-less machines
//
// plus per-machine, gitignored overlays deps/Brewfile.<goos>.local that layer on
// top of the shared manifest. The applicable manifest is chosen by runtime.GOOS
// and the DETECTED package manager (internal/platform.DetectPackageManager) —
// detection is never re-derived here.
//
// Gating: dependency installs mutate system state (prompts, sudo, remote
// scripts), so they are NEVER part of a default unattended apply. Install runs
// ONLY when explicitly invoked by the apply --deps path. ferry uses whatever
// package manager is PRESENT and NEVER installs/bootstraps the manager itself:
// with no manager present, Install REPORTS the absence and does nothing.
//
// Testability: the package-manager invocation goes through an injectable
// CommandRunner, so unit tests assert "invokes brew with the selected Brewfile"
// and "no manager present -> reports, no bootstrap" WITHOUT a real package
// manager. The default runner shells out to the manager by NAME (resolved via
// PATH) so a stubbed binary earlier on PATH is honoured.
package deps
