package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// scriptPath returns the absolute path to scripts/prune-releases.sh, located
// relative to this test file so it is independent of the working directory.
func scriptPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/release/prune_test.go -> repo root is two dirs up.
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	return filepath.Join(root, "scripts", "prune-releases.sh")
}

// writeStub creates an executable stub script in dir and returns nothing; the
// stub appends its full argv to the file named by the STUB_LOG environment
// variable so the test can assert exactly how it was invoked.
func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

// TestPruneNeverDeletesTags is a regression guard for W2: pruning a superseded
// release must remove the GitHub Release only and NEVER the git tag. It runs the
// real prune script with gh and git replaced by stubs that record their argv,
// then asserts by ALLOWLIST: git is never invoked at all, and every gh
// invocation matches one of the exactly two commands the script legitimately
// runs (the release list, and a plain `release delete <tag> --yes`). Any other
// route to a tag deletion — `--cleanup-tag`, `git tag -d`, `git push --delete`,
// a colon-refspec push, `gh api -X DELETE .../git/refs/tags/...` — is an
// unrecognised invocation and fails. Against the previous script (which called
// `gh release delete --cleanup-tag`) this fails; against the current it passes.
func TestPruneNeverDeletesTags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("prune-releases.sh is a POSIX shell script")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	script := scriptPath(t)
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("prune script not found: %v", err)
	}

	tmp := t.TempDir()
	stubDir := filepath.Join(tmp, "stub-bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ghLog := filepath.Join(tmp, "gh.log")
	gitLog := filepath.Join(tmp, "git.log")

	// gh stub: records EVERY invocation (including `release list`, so the
	// allowlist below covers the script's complete gh surface), then answers
	// `release list` with three tags already filtered (mimicking the script's
	// --jq). Three releases in one line (v0.1.x) mean v0.1.0 and v0.1.1 get
	// pruned while the current v0.1.2 (the line keeper) is kept.
	writeStub(t, stubDir, "gh", `#!/usr/bin/env bash
echo "gh $*" >> "$GH_LOG"
if [ "$1" = "release" ] && [ "$2" = "list" ]; then
  printf 'v0.1.0\nv0.1.1\nv0.1.2\n'
fi
exit 0
`)

	// git stub: record every invocation. The script legitimately never invokes
	// git at all, so ANY recorded call fails the test below.
	writeStub(t, stubDir, "git", `#!/usr/bin/env bash
echo "git $*" >> "$GIT_LOG"
exit 0
`)

	cmd := exec.Command("bash", script, "--current", "v0.1.2")
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_LOG="+ghLog,
		"GIT_LOG="+gitLog,
		"GITHUB_TOKEN=stub-token",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prune script failed: %v\noutput:\n%s", err, out)
	}
	t.Logf("prune output:\n%s", out)

	gh := readOrEmpty(t, ghLog)
	git := readOrEmpty(t, gitLog)

	// It must actually have done pruning work — otherwise the test proves nothing.
	if !strings.Contains(gh, "gh release delete v0.1.0 --yes") ||
		!strings.Contains(gh, "gh release delete v0.1.1 --yes") {
		t.Fatalf("expected v0.1.0 and v0.1.1 to be deleted; gh invocations:\n%s", gh)
	}

	// ALLOWLIST, not blacklist: the script's complete legitimate command surface
	// is (1) the read-only release list and (2) a bare `release delete <tag>
	// --yes` — anchored, so any extra flag (e.g. --cleanup-tag) or any other gh
	// subcommand (e.g. `gh api -X DELETE .../git/refs/tags/...`) is rejected.
	allowedGH := []*regexp.Regexp{
		regexp.MustCompile(`^gh release list --limit 200 --json tagName,isDraft,isPrerelease --jq .*$`),
		regexp.MustCompile(`^gh release delete v[0-9]+\.[0-9]+\.[0-9]+ --yes$`),
	}
	for _, line := range strings.Split(strings.TrimRight(gh, "\n"), "\n") {
		ok := false
		for _, re := range allowedGH {
			if re.MatchString(line) {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("gh invoked outside the allowlist (possible tag-deletion route): %q\nall gh invocations:\n%s", line, gh)
		}
	}

	// The script legitimately never invokes git at all — any git call (tag -d,
	// push --delete, a colon-refspec push, anything) fails.
	if strings.TrimSpace(git) != "" {
		t.Errorf("git was invoked; the prune script must never call git (possible tag-deletion route):\n%s", git)
	}
}

// TestPruneDryRunListsReleasesOnly asserts the dry-run output enumerates the
// releases that would be pruned and states plainly that no tags are touched,
// while performing no gh delete and no git call at all.
func TestPruneDryRunListsReleasesOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("prune-releases.sh is a POSIX shell script")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	script := scriptPath(t)
	tmp := t.TempDir()
	stubDir := filepath.Join(tmp, "stub-bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ghLog := filepath.Join(tmp, "gh.log")
	gitLog := filepath.Join(tmp, "git.log")

	writeStub(t, stubDir, "gh", `#!/usr/bin/env bash
if [ "$1" = "release" ] && [ "$2" = "list" ]; then
  printf 'v0.1.0\nv0.1.1\nv0.1.2\n'
  exit 0
fi
echo "gh $*" >> "$GH_LOG"
exit 0
`)
	writeStub(t, stubDir, "git", `#!/usr/bin/env bash
echo "git $*" >> "$GIT_LOG"
exit 0
`)

	cmd := exec.Command("bash", script, "--current", "v0.1.2", "--dry-run")
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_LOG="+ghLog,
		"GIT_LOG="+gitLog,
		"GITHUB_TOKEN=stub-token",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prune dry-run failed: %v\noutput:\n%s", err, out)
	}
	text := string(out)
	t.Logf("dry-run output:\n%s", text)

	// The releases to be pruned are listed.
	if !strings.Contains(text, "v0.1.0") || !strings.Contains(text, "v0.1.1") {
		t.Errorf("dry-run output does not list the superseded releases:\n%s", text)
	}
	// No delete or git call happened.
	if gh := readOrEmpty(t, ghLog); strings.Contains(gh, "release delete") {
		t.Errorf("dry-run performed a real gh release delete:\n%s", gh)
	}
	if git := readOrEmpty(t, gitLog); strings.TrimSpace(git) != "" {
		t.Errorf("dry-run invoked git:\n%s", git)
	}
	// Output must not claim to have deleted tags.
	if strings.Contains(text, "cleanup-tag") || strings.Contains(strings.ToLower(text), "deleting release + tag") {
		t.Errorf("dry-run output references tag deletion:\n%s", text)
	}
}

func readOrEmpty(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
