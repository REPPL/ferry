// Package release holds regression tests for ferry's release-tooling shell
// scripts (under scripts/). It has no runtime code: the release flow lives in
// the Makefile, the GitHub Actions release workflow, and those shell scripts.
// The tests here run a script against stubbed gh/git commands so that
// `go test ./...` guards invariants that are otherwise only exercised in CI —
// notably that pruning superseded releases never deletes a git tag.
package release
