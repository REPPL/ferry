package cmd

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/ghcli"
	"github.com/REPPL/ferry/internal/paths"
	"github.com/REPPL/ferry/internal/secret"
)

// defaultManagedRepoName is the name ferry proposes when `init --github` is run
// with no [name] positional.
const defaultManagedRepoName = "ferry-config"

// maxRepoNameLen is GitHub's cap on a repository name (100 chars).
const maxRepoNameLen = 100

// repoNameChars is the strict grammar for a managed repo basename: only
// [A-Za-z0-9._-]. Anything else (slash, whitespace, URL punctuation, '$', ...) is
// rejected before any gh call.
var repoNameChars = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateRepoName enforces the strict, personal-repo, basename-only grammar for
// a managed repo name (PLAN step 2). It rejects — with a clear message — any
// slash (incl. the org/repo form), URL, `.git` suffix, whitespace, out-of-grammar
// character, over-length name, and the reserved exact name `ferry`. It runs
// BEFORE any gh subcommand so a bad name never reaches the network.
func validateRepoName(name string) error {
	if name == "" {
		return nil // caller substitutes the default; empty is not user-supplied.
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("repo name %q must be a bare NAME, not an owner/repo path or URL — ferry manages a PERSONAL repo under your own account (e.g. `ferry init --github ferry-config`)", name)
	}
	if strings.Contains(name, "://") || strings.Contains(name, "github.com") {
		return fmt.Errorf("repo name %q looks like a URL — pass just the repo NAME (e.g. `ferry init --github ferry-config`)", name)
	}
	if strings.HasSuffix(strings.ToLower(name), ".git") {
		return fmt.Errorf("repo name %q must not carry a `.git` suffix — pass just the name", name)
	}
	if strings.ContainsAny(name, " \t\n\r") {
		return fmt.Errorf("repo name %q must not contain whitespace", name)
	}
	if !repoNameChars.MatchString(name) {
		return fmt.Errorf("repo name %q contains characters outside [A-Za-z0-9._-] — GitHub repo names allow only letters, digits, '.', '_' and '-'", name)
	}
	if len(name) > maxRepoNameLen {
		return fmt.Errorf("repo name is %d characters — GitHub caps repo names at %d", len(name), maxRepoNameLen)
	}
	if name == "ferry" {
		return fmt.Errorf("the name `ferry` is reserved — choose another (e.g. `ferry-config`)")
	}
	return nil
}

// initGitHub implements `ferry init --github [name] [--yes]` (route 2): create a
// PRIVATE GitHub repo via the gh CLI's existing auth and wire it as ferry's
// HTTPS remote. ferry stores NO token; gh owns the credential. The steps are the
// 10 from .work/PLAN-v0.2.2-route2-github.md, in order.
func initGitHub(c *cobra.Command, in *bufio.Reader, out io.Writer, name string) error {
	yes, _ := c.Flags().GetBool("yes")

	// STEP 2 (grammar, part 1): reject a bad name BEFORE any gh call. Running this
	// first means a grammar-rejected name records ZERO gh invocations.
	if err := validateRepoName(name); err != nil {
		return err
	}
	if name == "" {
		name = defaultManagedRepoName
	}

	gh := ghcli.New()

	// STEP 1 (preflight): gh on PATH, then authenticated. Both actionable.
	if err := gh.EnsureGH(); err != nil {
		return err
	}
	if err := gh.AuthStatus(); err != nil {
		return err
	}

	// STEP 3 (resolve owner): the authenticated login is the owner. PERSONAL only.
	owner, err := gh.Login()
	if err != nil {
		return err
	}
	resolved := fmt.Sprintf("%s/%s/%s", ghcli.Host, owner, name)

	// STEP 4 (existing-local-repo guard): if a config repo is already configured,
	// REFUSE — never push old/unreviewed local content into a new managed remote.
	if existing, ok := existingConfiguredRepo(); ok {
		return fmt.Errorf("a ferry config repo already exists at %s — `init --github` sets up a NEW managed repo; remove or rename the existing one first (or use `ferry capture` to push to it)", existing)
	}

	// STEP 3 (confirmation): print the FULL resolved <host>/<owner>/<name>. On a
	// TTY require confirmation unless --yes; non-interactive REQUIRE --yes.
	fmt.Fprintf(out, "ferry will create a PRIVATE GitHub repo: %s\n", resolved)
	if !yes {
		if stdinIsTerminal() {
			fmt.Fprintf(out, "Create %s and manage it? [y/N]: ", resolved)
			if !readYesNo(in, false) {
				return fmt.Errorf("not creating %s (re-run with --yes to confirm)", resolved)
			}
		} else {
			return fmt.Errorf("refusing to create %s non-interactively without confirmation — re-run with --yes if that is the intended account and repo", resolved)
		}
	}

	// STEP 5 (check-and-avoid): does <owner>/<name> already exist? If so ABORT and
	// tell the user to pass a different name — ferry NEVER reuses a repo and NEVER
	// auto-derives an alternative. A network/auth error surfaces as-is (not "exists").
	exists, err := gh.RepoExists(owner, name)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%s/%s already exists — ferry won't reuse it. Re-run with a different name: `ferry init --github <other-name>`", owner, name)
	}

	// STEP 6 (SECRET GATE before commit — CRITICAL): before initFresh writes and
	// COMMITS the adopted ~/.zshrc, run the SAME blocking gate capture/export use.
	// A high-confidence secret must NEVER enter the local commit, let alone the
	// remote. Abort before creating a git repo, staging, committing, or pushing if
	// the content that would be committed looks like a secret. (This runs BEFORE
	// `gh repo create`, so a blocked secret never even creates the remote.)
	if err := gateManagedContentBeforeCommit(); err != nil {
		return err
	}

	// STEP 7 (create private): `gh repo create <owner>/<name> --private`. A failure
	// (incl. a create-race where the name now exists) ABORTS — never fall through.
	if err := gh.CreatePrivate(owner, name); err != nil {
		return err
	}
	createdURL := fmt.Sprintf("https://%s/%s/%s", ghcli.Host, owner, name)

	// repoPath is the local managed working tree (populated at step 9). Declared here
	// so the partialFailure closure can name it in its recovery text once set (it is
	// empty until step 9, and the recovery message reads correctly either way).
	var repoPath string

	// From here the repo EXISTS on GitHub. Any later failure is a PARTIAL FAILURE:
	// report the created repo + recovery, do NOT mark managed, do NOT create a
	// second repo on retry (STEP 5 will refuse next time).
	partialFailure := func(stage string, cause error) error {
		cfgPath := "~/.config/ferry/config.toml"
		if p, perr := paths.ConfigFile(); perr == nil {
			cfgPath = p
		}
		return fmt.Errorf("the private repo %s was created, but the %s step failed: %v.\n"+
			"Recovery: the repo exists but is empty. ferry may already have written a LOCAL config "+
			"(%s) and repo (%s) recording this partial setup — a straight re-run would abort on that "+
			"local state (and on the now-existing remote) before doing anything. To retry cleanly: delete the "+
			"remote (`gh repo delete %s/%s`) AND remove/rename the local config (%s) and repo dir (%s), then "+
			"re-run — or finish the push manually into the existing repo. ferry did NOT mark it managed.",
			createdURL, stage, cause, cfgPath, repoPath, owner, name, cfgPath, repoPath)
	}

	// STEP 8 (verify private + identity): parse ONE `repo view --json
	// nameWithOwner,isPrivate,url`. Assert private, correct owner/name, url present.
	view, err := gh.ViewJSON(owner, name)
	if err != nil {
		return partialFailure("verification", err)
	}
	if !view.IsPrivate {
		return partialFailure("verification", fmt.Errorf("the created repo is NOT private (isPrivate=false) — ferry only manages private repos and will not push"))
	}
	if !view.HasNameWithOwner() {
		return partialFailure("verification", fmt.Errorf("the `repo view --json` verification is missing the nameWithOwner field"))
	}
	if !view.HasURL() {
		return partialFailure("verification", fmt.Errorf("the `repo view --json` verification is missing the url field"))
	}
	if view.NameWithOwner != owner+"/"+name {
		return partialFailure("verification", fmt.Errorf("identity mismatch: created %q but expected %q — refusing to push to an unexpected account/repo", view.NameWithOwner, owner+"/"+name))
	}

	// STEP 9 (local repo + HTTPS origin): reuse initFresh (fresh repo + adopt
	// ~/.zshrc, already secret-gated in step 6). Then set origin to the repo's
	// HTTPS clone URL — VALIDATE the scheme ourselves (must be https://).
	repoPath, err = initFresh(out, "")
	if err != nil {
		return partialFailure("local repo setup", err)
	}
	if err := ensureLocalLayerIgnored(repoPath); err != nil {
		return partialFailure("local repo setup", err)
	}

	// CONSTRUCT the canonical origin ourselves from the ALREADY-VERIFIED owner/name
	// (step 8 asserted view.NameWithOwner == owner+"/"+name). We NEVER trust gh's
	// reported view.URL as the origin: it could be `https://ghp_TOKEN@github.com/...`
	// (userinfo-embedded token) or point at a different owner/repo — either would
	// defeat the no-token-embedded and verified-identity invariants. The validator
	// below is defense-in-depth over this constructed string.
	originURL := "https://" + ghcli.Host + "/" + owner + "/" + name + ".git"
	if err := validateManagedOrigin(originURL, owner, name); err != nil {
		return partialFailure("remote wiring", err)
	}
	if out2, gerr := runGitIn(repoPath, "remote", "add", "origin", originURL); gerr != nil {
		return partialFailure("remote wiring", fmt.Errorf("git remote add origin: %v\n%s", gerr, out2))
	}

	// Write the machine config (unmanaged for now) AFTER origin is set. managed is
	// flipped only after a successful push (STEP 10).
	hostname, herr := os.Hostname()
	if herr != nil || strings.TrimSpace(hostname) == "" {
		hostname = "unknown"
	}
	if err := config.SaveMachineConfig(config.MachineConfig{Hostname: hostname, Repo: repoPath, Managed: false}); err != nil {
		return partialFailure("config write", err)
	}
	if err := ensureLocalManifest(out, repoPath); err != nil {
		return partialFailure("manifest", err)
	}

	// STEP 6 (belt-and-braces): re-run the secret gate over the committed tree just
	// before push. A secret must never reach the remote.
	if err := gateRepoTreeBeforePush(repoPath); err != nil {
		return partialFailure("pre-push secret gate", err)
	}

	// STEP 10 (first push over HTTPS, noninteractive). Mark managed ONLY on success.
	fmt.Fprintf(out, "pushing the initial commit to %s over HTTPS...\n", originURL)
	if err := gh.GitPush(repoPath); err != nil {
		return partialFailure("push", err)
	}

	if err := config.SaveMachineConfig(config.MachineConfig{Hostname: hostname, Repo: repoPath, Managed: true}); err != nil {
		return partialFailure("config finalise", err)
	}
	fmt.Fprintf(out, "done: %s is a managed private GitHub repo. `ferry capture` pushes changes; `ferry apply` on another machine pulls them.\n", resolved)
	return nil
}

// validateManagedOrigin is a defense-in-depth guard over the origin ferry
// CONSTRUCTED itself (https://github.com/<verified-owner>/<verified-name>.git).
// ferry never trusts gh's reported clone URL; the origin is built from the
// step-8-verified owner/name, and this validator asserts the constructed string
// is exactly the canonical shape and carries NO credential or foreign host. It
// requires, via net/url:
//   - scheme == "https" (rejects ssh://, git://, http://, scp-style);
//   - host == "github.com" EXACTLY (no other host, no port, no userinfo host);
//   - url.User == nil — NO userinfo (rejects https://ghp_TOKEN@github.com/... and
//     https://user:pass@...), the embedded-token vector;
//   - empty query and fragment;
//   - path is exactly /<owner>/<name> or /<owner>/<name>.git for the VERIFIED
//     owner/name (rejects a wrong-owner or wrong-repo path).
//
// Any deviation is rejected so NO origin is set and NO push happens.
func validateManagedOrigin(originURL, owner, name string) error {
	u, err := url.Parse(strings.TrimSpace(originURL))
	if err != nil {
		return fmt.Errorf("refusing an unparseable remote URL %q: ferry sets a canonical HTTPS origin only", originURL)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("refusing a non-https remote scheme (%q): ferry sets an HTTPS origin only and never touches ~/.ssh (got %q)", u.Scheme, originURL)
	}
	if u.User != nil {
		return fmt.Errorf("refusing a remote URL that embeds userinfo (a token/credential): ferry sets a plain HTTPS origin with no embedded credential (got %q)", originURL)
	}
	if u.Host != ghcli.Host {
		return fmt.Errorf("refusing a remote on host %q: ferry only manages a %s repo (got %q)", u.Host, ghcli.Host, originURL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("refusing a remote URL with a query or fragment: ferry sets a bare canonical HTTPS origin only (got %q)", originURL)
	}
	want := "/" + owner + "/" + name
	if u.Path != want && u.Path != want+".git" {
		return fmt.Errorf("refusing a remote whose path %q is not the verified %s/%s: ferry pushes only to the verified owner/repo (got %q)", u.Path, owner, name, originURL)
	}
	return nil
}

// gateManagedContentBeforeCommit runs the SAME blocking secret gate as
// capture/export over EVERY file the initial commit will contain — NOT just the
// adopted ~/.zshrc. The CRITICAL no-secret-in-repo invariant must not depend on
// "only ~/.zshrc happens to carry user content today": ferry enumerates the EXACT
// set of files initFresh will `git add -A` + commit (the generated manifest, the
// gitignore/local-layer templates, and the adopted ~/.zshrc source) and gates each
// one. If ANY is a high-confidence secret it ABORTS before `gh repo create`, so no
// remote is created, nothing is staged, committed or pushed. The message points
// the user at an out-of-band path.
func gateManagedContentBeforeCommit() error {
	files := plannedCommitContents()
	for label, data := range files {
		if secret.IsBlockedFromRepo(data) {
			// Materialise the neutral repo dir EMPTY (no git init, no commit, no secret)
			// so ferry's workspace exists but carries nothing — the secret never enters a
			// repo, tree, or history. Then abort.
			if dir, derr := defaultRepoDir(); derr == nil {
				_ = os.MkdirAll(dir, 0o755)
			}
			return fmt.Errorf("the file %s that ferry would commit contains what looks like a secret (e.g. a private key or token); ferry won't commit or push it to GitHub.\n"+
				"Move the secret to a secret store or ~/.zshrc.local and re-run, or use `ferry init` for a purely local repo", label)
		}
	}
	return nil
}

// plannedCommitContents returns, keyed by a human label, the content of EVERY file
// the initial managed commit will contain — mirroring exactly what initFresh
// writes and commits (git add -A). Keeping this in lockstep with initFresh is what
// lets the pre-create gate scan the WHOLE commit, not just ~/.zshrc. The adopted
// ~/.zshrc source is included only when there is real content to adopt (same
// condition initFresh seeds it under).
func plannedCommitContents() map[string]string {
	files := map[string]string{
		config.SharedManifestName: "[manage]\ndotfiles = [\".zshrc\"]\n",
		".gitignore":              config.LocalManifestName + "\nlocal/\n",
	}
	if data, ok := readExistingZshrc(); ok {
		files[dotfile.RepoSubdir+"/zshrc"] = string(data)
	}
	return files
}

// gateRepoTreeBeforePush re-runs the blocking secret gate over EVERY tracked file
// in the repo working tree just before push (belt-and-braces, PLAN step 5). Any
// high-confidence secret aborts the push. The repo was already gated before
// commit; this guards against a source ferry seeded that a first gate missed.
func gateRepoTreeBeforePush(repo string) error {
	err := filepath.Walk(repo, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if secret.IsBlockedFromRepo(string(data)) {
			rel, _ := filepath.Rel(repo, path)
			return fmt.Errorf("refusing to push: the repo file %s looks like it contains a secret", rel)
		}
		return nil
	})
	return err
}
