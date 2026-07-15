// Package ghcli is a thin, mockable wrapper over the `gh` CLI used by
// `ferry init --github` (route 2, managed GitHub). ferry never embeds or stores
// a token: `gh` owns the credential in its own keyring, and every subprocess
// runs NONINTERACTIVELY (GIT_TERMINAL_PROMPT=0) so an auth prompt can never hang
// the flow or capture a typed credential.
//
// The Runner seam mirrors terminal.Runner: production code uses ExecRunner
// (real `gh`/`git` via PATH), and the evals inject a PATH-shim `gh`/`git` by
// putting their temp bin dir first on PATH — so the seam is the PATH, not an
// in-process fake. The methods here shell out through PATH with the
// noninteractive env applied.
package ghcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// inheritedEnv returns the parent process environment (so PATH — including the
// eval's mock-gh shim dir — is preserved for the subprocess).
func inheritedEnv() []string { return os.Environ() }

// Host is the GitHub host `gh` operates against. v0.2.2 is github.com only.
const Host = "github.com"

// noninteractiveEnv returns the extra env entries every gh/git subprocess runs
// with: GIT_TERMINAL_PROMPT=0 (git never prompts for credentials) and
// GH_PROMPT_DISABLED=1 (gh never prompts interactively). Appended to the parent
// environment so PATH (the mock shim) is preserved.
func noninteractiveEnv() []string {
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GH_PROMPT_DISABLED=1",
		"GH_NO_UPDATE_NOTIFIER=1",
	}
}

// Runner shells out to a binary on PATH with the noninteractive env applied and
// returns combined stdout, stderr, and the error. Injectable for tests, though
// the evals drive the real ExecRunner through a PATH shim. dir sets the working
// directory (empty = inherit); used for `git` so `push` is the first argv token
// (a `-C <repo>` prefix would hide the subcommand from a shim that keys off it).
type Runner interface {
	Run(name, dir string, args ...string) (stdout, stderr string, err error)
}

// ExecRunner runs the real binary resolved on PATH.
type ExecRunner struct{}

// Run executes name with args (in dir, if set), feeding the noninteractive env
// (on top of the inherited environment), returning captured stdout/stderr.
func (ExecRunner) Run(name, dir string, args ...string) (string, string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(inheritedEnv(), noninteractiveEnv()...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return out.String(), errBuf.String(), err
}

// Client is the ferry-facing gh/git surface for route 2. It holds a Runner so
// the underlying binary invocation is injectable, but by default shells the real
// gh/git on PATH.
type Client struct {
	Run Runner
}

// New returns a Client backed by the real gh/git on PATH.
func New() *Client { return &Client{Run: ExecRunner{}} }

// exitError reports whether err is a non-zero process exit (vs. a "binary not
// found" / spawn failure). Used to distinguish "gh present but returned 1" from
// "gh not on PATH".
func isNotFound(err error) bool {
	var ee *exec.Error
	return errors.As(err, &ee)
}

// EnsureGH verifies the `gh` binary is on PATH, returning an actionable error
// when it is not.
func (c *Client) EnsureGH() error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("the GitHub CLI `gh` is required for `ferry init --github` but was not found on PATH — install it from https://cli.github.com and run `gh auth login`, then re-run")
	}
	return nil
}

// AuthStatus runs `gh auth status`. A non-zero exit means unauthenticated; the
// error tells the user to run `gh auth login`.
func (c *Client) AuthStatus() error {
	_, stderr, err := c.Run.Run("gh", "", "auth", "status")
	if err != nil {
		if isNotFound(err) {
			return c.EnsureGH()
		}
		return fmt.Errorf("`gh` is not authenticated — run `gh auth login` (and `gh auth setup-git`) so ferry can create a repo over HTTPS, then re-run: %s", redact(strings.TrimSpace(stderr)))
	}
	return nil
}

// Login resolves the authenticated GitHub account login via `gh api user`.
// PERSONAL only; the login is used as the owner.
func (c *Client) Login() (string, error) {
	stdout, stderr, err := c.Run.Run("gh", "", "api", "user")
	if err != nil {
		return "", fmt.Errorf("could not resolve your GitHub account via `gh api user`: %s", redact(strings.TrimSpace(stderr)))
	}
	var payload struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		return "", fmt.Errorf("could not parse `gh api user` output: %s", redact(err.Error()))
	}
	if strings.TrimSpace(payload.Login) == "" {
		return "", fmt.Errorf("`gh api user` returned no login — is `gh` authenticated?")
	}
	return payload.Login, nil
}

// RepoExists reports whether <owner>/<name> already exists. It maps `gh repo
// view <owner>/<name>`:
//   - exit 0            => exists (true)
//   - a "not found"     => available (false)
//   - any OTHER failure => a network/rate-limit/auth error surfaced to the caller
//     (NEVER treated as "exists").
func (c *Client) RepoExists(owner, name string) (bool, error) {
	slug := owner + "/" + name
	_, stderr, err := c.Run.Run("gh", "", "repo", "view", slug)
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, c.EnsureGH()
	}
	if looksNotFound(stderr) {
		return false, nil
	}
	return false, fmt.Errorf("could not check whether %s is available — a network or API error (not a name collision): %s", slug, redact(strings.TrimSpace(stderr)))
}

// looksNotFound reports whether gh's stderr for a `repo view` is the clean
// "repository does not exist" signal (=> available) as opposed to a network or
// auth failure. gh emits a "Could not resolve to a Repository" GraphQL message
// (or a 404) for a genuinely absent repo.
func looksNotFound(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "could not resolve to a repository") ||
		strings.Contains(s, "not found") ||
		strings.Contains(s, "404")
}

// CreatePrivate creates <owner>/<name> as a PRIVATE repo via
// `gh repo create <owner>/<name> --private`. NEVER passes --public. A failure
// (including a create-race where the name now exists) is surfaced; the caller
// must ABORT and never fall through to push.
func (c *Client) CreatePrivate(owner, name string) error {
	slug := owner + "/" + name
	_, stderr, err := c.Run.Run("gh", "", "repo", "create", slug, "--private")
	if err != nil {
		return fmt.Errorf("creating the private repo %s failed (it may now exist — a race, or the name is taken): %s", slug, redact(strings.TrimSpace(stderr)))
	}
	return nil
}

// RepoView is the parsed post-create verification of a repo's identity and
// privacy from `gh repo view <owner>/<name> --json nameWithOwner,isPrivate,url`.
type RepoView struct {
	NameWithOwner string `json:"nameWithOwner"`
	IsPrivate     bool   `json:"isPrivate"`
	URL           string `json:"url"`
	// present tracks which fields the JSON actually carried, so a MISSING field
	// (vs. a zero value) can be detected for the verify step.
	hasNameWithOwner bool
	hasURL           bool
}

// HasNameWithOwner reports whether the JSON carried a nameWithOwner field.
func (v RepoView) HasNameWithOwner() bool { return v.hasNameWithOwner }

// HasURL reports whether the JSON carried a url field.
func (v RepoView) HasURL() bool { return v.hasURL }

// ViewJSON runs `gh repo view <owner>/<name> --json nameWithOwner,isPrivate,url`
// and parses the ONE JSON object. A parse error is surfaced so the caller aborts
// before push. Field presence is tracked so a missing url/nameWithOwner is
// distinguishable from an empty value.
func (c *Client) ViewJSON(owner, name string) (RepoView, error) {
	slug := owner + "/" + name
	stdout, stderr, err := c.Run.Run("gh", "", "repo", "view", slug, "--json", "nameWithOwner,isPrivate,url")
	if err != nil {
		return RepoView{}, fmt.Errorf("verifying %s via `gh repo view --json` failed: %s", slug, redact(strings.TrimSpace(stderr)))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return RepoView{}, fmt.Errorf("could not parse the `gh repo view --json` verification output for %s (invalid JSON): %s", slug, redact(err.Error()))
	}
	var v RepoView
	if b, ok := raw["isPrivate"]; ok {
		_ = json.Unmarshal(b, &v.IsPrivate)
	}
	if b, ok := raw["nameWithOwner"]; ok {
		if json.Unmarshal(b, &v.NameWithOwner) == nil && strings.TrimSpace(v.NameWithOwner) != "" {
			v.hasNameWithOwner = true
		}
	}
	if b, ok := raw["url"]; ok {
		if json.Unmarshal(b, &v.URL) == nil && strings.TrimSpace(v.URL) != "" {
			v.hasURL = true
		}
	}
	return v, nil
}

// gitPushHardening is the untrusted-transport hardening prepended (in the git
// global-options slot, before `push`) to the init-time push, mirroring sync's
// runHardenedGit. Without it this one push ran under the user's ambient git
// config, so a global `url."ext::sh -c evil".pushInsteadOf=https://github.com/`
// rewrite would be honored and execute — every OTHER ferry git call already
// blocks that. protocol.ext.allow=never is the decisive control (a rewritten
// ext:: URL is refused by git itself); the rest neutralise hooks, fsmonitor, the
// file transport, and any stray ssh. Set via `-c` (not env) so it targets only
// this call and stays a global option the eval shim skips over to find `push`.
func gitPushHardening() []string {
	return []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=user",
		"-c", "core.sshCommand=/bin/false",
	}
}

// GitPush runs `git push -u origin HEAD` (rooted at repo via cmd.Dir)
// NONINTERACTIVELY (GIT_TERMINAL_PROMPT=0) with the untrusted-transport hardening
// applied. The credential is provided by gh's git credential helper; ferry never
// passes a token on the argv. A non-zero exit is surfaced so the caller reports
// the partial failure (repo created, push failed).
func (c *Client) GitPush(repo string) error {
	args := append(gitPushHardening(), "push", "-u", "origin", "HEAD")
	_, stderr, err := c.Run.Run("git", repo, args...)
	if err != nil {
		return fmt.Errorf("git push failed: %s", redact(strings.TrimSpace(stderr)))
	}
	return nil
}
