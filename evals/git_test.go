package evals

// git config plugin (v0.7.0 Phase B — the FLAGSHIP) exercised through the REAL
// binary. git rides the dotfiles FileDomain as an include-sidecar dotfile: a
// shared ~/.gitconfig whose LAST directive is ferry's injected git-INI
// `[include]` block sourcing ~/.gitconfig.local (git last-wins), plus the
// machine identity partitioned to that never-shared .local layer. These evals are
// the plan's STOP-condition proofs:
//   - identity keys (user.email/name/signingkey, gpg.program, credential.helper)
//     and [includeIf …] blocks NEVER reach the shared ~/.gitconfig or repo;
//   - a URL-embedded token / http.extraHeader credential NEVER reaches the shared
//     repo (stored out-of-band as a column-grained span, rendered back on deploy);
//   - credential.helper = store is refused/warned; ~/.git-credentials is untouched.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const gitManifest = `[manage]
dotfiles = [".gitconfig"]
brew = false
iterm2 = false
fonts = false
`

// gitToken / gitHeaderToken are GitHub-token-shaped High-confidence secrets (no
// placeholder words), so the recogniser stores them and IsNonPlaceholderSecret
// accepts them as real.
const gitToken = "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"
const gitHeaderToken = "ghp_Z9y8X7w6V5u4T3s2R1q0P9o8N7m6L5k4J3h2"

// sharedGitconfig is an identity-FREE shared config (aliases, core.*,
// init.defaultBranch) — exactly what belongs in the shared repo.
const sharedGitconfig = "[core]\n\teditor = vim\n[init]\n\tdefaultBranch = main\n[alias]\n\tst = status\n\tlg = log --oneline\n"

// aliceLocal / bobLocal are two machines' private identity layers — the
// ~/.gitconfig.local content, gitignored and never shared.
const aliceLocal = "[user]\n\temail = alice@example.com\n\tname = Alice Example\n\tsigningkey = ABCD1234\n[gpg]\n\tprogram = gpg2\n[credential \"https://ghe.example.com\"]\n\tusername = alice-login\n[includeIf \"gitdir:~/work/\"]\n\tpath = ~/work/.gitconfig\n"
const bobLocal = "[user]\n\temail = bob@example.net\n\tname = Bob Other\n"

// seedGitRepo writes the manifest, the shared source (both layout paths), and a
// .gitignore for local/ so the private identity layer is never committed.
func seedGitRepo(t *testing.T, s *Sandbox, shared string) {
	t.Helper()
	s.SeedSharedManifest(t, gitManifest)
	s.WriteRepoFile(t, filepath.Join("dotfiles", "gitconfig"), shared)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".gitconfig"), shared)
	s.WriteRepoFile(t, ".gitignore", "local/\n")
}

// assertNoIdentity fails if any identity token appears in text.
func assertNoIdentity(t *testing.T, where, text string) {
	t.Helper()
	for _, id := range []string{
		"alice@example.com", "Alice Example", "ABCD1234", "gpg2", "alice-login",
		"[user]", "[gpg]", "[includeIf", "gitdir:", "signingkey", "username",
	} {
		if strings.Contains(text, id) {
			t.Errorf("IDENTITY LEAK in %s: contains %q\n%s", where, id, text)
		}
	}
}

// TestGit_IdentityNeverShared is the load-bearing identity firewall proof
// (plan §7): identity lives ONLY in ~/.gitconfig.local; the deployed shared
// ~/.gitconfig, the committed shared repo source, and git history carry NONE of
// it — and a second machine (bob) proves one machine's identity never appears in
// the shared representation.
func TestGit_IdentityNeverShared(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	seedGitRepo(t, s, sharedGitconfig)
	gitCommitAll(t, s.Repo, "baseline shared gitconfig (no identity)")

	// Machine A: alice's private identity layer (gitignored, uncommitted).
	s.WriteRepoFile(t, filepath.Join("local", "git", "gitconfig.local"), aliceLocal)

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}

	sharedTarget := s.HomePath(".gitconfig")
	sidecarTarget := s.HomePath(".gitconfig.local")

	deployedShared, err := os.ReadFile(sharedTarget)
	if err != nil {
		t.Fatalf("apply did not deploy ~/.gitconfig: %v", err)
	}
	// The shared file carries the native [include] overlay as its last directive.
	if !strings.Contains(string(deployedShared), "[include]") ||
		!strings.Contains(string(deployedShared), "path = ~/.gitconfig.local") {
		t.Fatalf("~/.gitconfig missing the git [include] overlay directive:\n%s", deployedShared)
	}
	// Non-identity shared content is present.
	if !strings.Contains(string(deployedShared), "st = status") {
		t.Fatalf("~/.gitconfig lost the shared aliases:\n%s", deployedShared)
	}
	// Identity is NOWHERE in the shared deployed file...
	assertNoIdentity(t, "deployed ~/.gitconfig", string(deployedShared))
	// ...nor in the committed shared repo source...
	repoShared, err := os.ReadFile(s.RepoPath("dotfiles", "gitconfig"))
	if err != nil {
		t.Fatalf("read repo shared: %v", err)
	}
	assertNoIdentity(t, "repo dotfiles/gitconfig", string(repoShared))
	// ...nor anywhere in git history.
	assertNoIdentityInGitHistory(t, s)

	// Identity DOES live in the private ~/.gitconfig.local sidecar.
	sidecar, err := os.ReadFile(sidecarTarget)
	if err != nil {
		t.Fatalf("apply did not materialise ~/.gitconfig.local: %v", err)
	}
	for _, want := range []string{"alice@example.com", "Alice Example", "gitdir:~/work/", "username = alice-login"} {
		if !strings.Contains(string(sidecar), want) {
			t.Errorf("~/.gitconfig.local missing identity %q:\n%s", want, sidecar)
		}
	}

	// Machine B: swap in bob's identity, re-apply. The shared deployed file and the
	// shared repo source must STILL carry no identity (bob's or alice's).
	s.WriteRepoFile(t, filepath.Join("local", "git", "gitconfig.local"), bobLocal)
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("machine-B apply exited %d; stderr:\n%s", code, errOut)
	}
	deployedB, _ := os.ReadFile(sharedTarget)
	for _, id := range []string{"bob@example.net", "Bob Other", "alice@example.com"} {
		if strings.Contains(string(deployedB), id) {
			t.Errorf("IDENTITY LEAK: machine-B ~/.gitconfig contains %q\n%s", id, deployedB)
		}
	}
	sidecarB, _ := os.ReadFile(sidecarTarget)
	if !strings.Contains(string(sidecarB), "bob@example.net") {
		t.Errorf("machine-B ~/.gitconfig.local missing bob's identity:\n%s", sidecarB)
	}
}

// TestGit_IdentityInSharedSourceStrippedOnDeploy is the DEFENSIVE half: even a
// shared repo source that MISTAKENLY carries identity keys never deploys them
// into ~/.gitconfig — apply's firewall strips them.
func TestGit_IdentityInSharedSourceStrippedOnDeploy(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	// A shared source polluted with identity (a mis-commit).
	polluted := sharedGitconfig + aliceLocal
	seedGitRepo(t, s, polluted)
	gitCommitAll(t, s.Repo, "shared gitconfig with mistakenly-committed identity")
	s.WriteRepoFile(t, filepath.Join("local", "git", "gitconfig.local"), bobLocal)

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	deployed, err := os.ReadFile(s.HomePath(".gitconfig"))
	if err != nil {
		t.Fatalf("apply did not deploy ~/.gitconfig: %v", err)
	}
	assertNoIdentity(t, "deployed ~/.gitconfig (defensive strip)", string(deployed))
}

// TestGit_InlineAndContinuationIdentityStrippedOnDeploy proves the parser models
// git's real grammar end-to-end: identity written in the INLINE `[section] key =`
// form (with and without spaces) and across a BACKSLASH value-continuation is
// firewalled out of the deployed shared ~/.gitconfig, exactly like the canonical
// multi-line form (ruthless-review findings 1 & 2).
func TestGit_InlineAndContinuationIdentityStrippedOnDeploy(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	// A shared source polluted with identity in every awkward spelling git accepts.
	polluted := sharedGitconfig +
		"[user] email = inline@leak.example\n" + // inline, spaced
		"[gpg]program=/leak/gpg\n" + // inline, no spaces
		"[user]\n\tname = frag\\\nmented@leak.example\n" + // backslash continuation
		"[credential] helper = store\n" // inline credential.helper = store
	seedGitRepo(t, s, polluted)
	gitCommitAll(t, s.Repo, "shared gitconfig with inline + continuation identity")
	s.WriteRepoFile(t, filepath.Join("local", "git", "gitconfig.local"), bobLocal)

	out, errOut, code := s.Ferry("apply")
	if code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	deployed, err := os.ReadFile(s.HomePath(".gitconfig"))
	if err != nil {
		t.Fatalf("apply did not deploy ~/.gitconfig: %v", err)
	}
	for _, leak := range []string{
		"inline@leak.example", "/leak/gpg", "mented@leak.example", "frag",
		"helper = store", "program",
	} {
		if strings.Contains(string(deployed), leak) {
			t.Errorf("IDENTITY LEAK: deployed ~/.gitconfig contains %q (inline/continuation firewall gap)\n%s", leak, deployed)
		}
	}
	// The inline credential.helper = store must still be warned about.
	if !containsAnyFold(out+errOut, "plaintext", "osxkeychain", "git-credentials") {
		t.Errorf("inline credential.helper = store was not warned about:\n%s", out+errOut)
	}
	// Non-identity shared content survives.
	if !strings.Contains(string(deployed), "st = status") {
		t.Errorf("deploy dropped shared aliases:\n%s", deployed)
	}
}

// TestGit_URLTokenNeverReachesRepo is the load-bearing secret gate for git: a
// literal token embedded in a url.*.insteadOf value and an http.extraHeader
// Bearer credential, edited into the live ~/.gitconfig, are routed to the
// out-of-repo store as column-grained spans (the URL/header syntax preserved), a
// placeholder is committed instead, and a redeploy renders both back verbatim.
func TestGit_URLTokenNeverReachesRepo(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	seedGitRepo(t, s, sharedGitconfig)
	gitCommitAll(t, s.Repo, "baseline shared gitconfig")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}

	// Edit the live file: add a URL-embedded token and a Bearer extraHeader.
	insteadOfLine := "[url \"https://github.com/\"]\n\tinsteadOf = https://" + gitToken + "@github.com/\n"
	headerLine := "[http]\n\textraHeader = Authorization: Bearer " + gitHeaderToken + "\n"
	deployedShared, _ := os.ReadFile(s.HomePath(".gitconfig"))
	if err := os.WriteFile(s.HomePath(".gitconfig"), append(deployedShared, []byte(insteadOfLine+headerLine)...), 0o600); err != nil {
		t.Fatalf("stage git secret: %v", err)
	}

	// Accept the drifted hunk (y — both edits land in one contiguous hunk), route
	// the blocked secret span(s) to the store (x), then route the
	// placeholder-bearing change to shared (s).
	out, errOut, _ := s.FerryWithInput("y\nx\ns\n", "capture")
	combined := out + errOut

	// HARD: neither literal token lands in any repo file or git history.
	s.AssertNoSecretInRepo(t, gitToken)
	s.AssertNoSecretInRepo(t, gitHeaderToken)

	repoShared, err := os.ReadFile(s.RepoPath("dotfiles", "gitconfig"))
	if err != nil {
		t.Fatalf("read repo shared gitconfig: %v", err)
	}
	if strings.Contains(string(repoShared), gitToken) || strings.Contains(string(repoShared), gitHeaderToken) {
		t.Fatalf("a literal git token reached the shared repo:\n%s", repoShared)
	}
	// The insteadOf line keeps its URL scheme/host, only the token swapped for a
	// placeholder; likewise the Bearer prefix is preserved.
	if !strings.Contains(string(repoShared), "https://{{ferry.secret") {
		t.Errorf("insteadOf line did not gain a column-grained placeholder (URL syntax preserved):\n%s\ncapture:\n%s", repoShared, combined)
	}
	if !strings.Contains(string(repoShared), "Bearer {{ferry.secret") {
		t.Errorf("extraHeader line did not gain a column-grained placeholder (Bearer prefix preserved):\n%s", repoShared)
	}

	// Redeploy renders both placeholders back to the literal tokens.
	if err := os.Remove(s.HomePath(".gitconfig")); err != nil {
		t.Fatalf("remove live gitconfig: %v", err)
	}
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("redeploy apply exited %d; stderr:\n%s", code, errOut)
	}
	redeployed, err := os.ReadFile(s.HomePath(".gitconfig"))
	if err != nil {
		t.Fatalf("read redeployed ~/.gitconfig: %v", err)
	}
	if !strings.Contains(string(redeployed), "https://"+gitToken+"@github.com/") {
		t.Errorf("deploy did not render the URL token back literally:\n%s", redeployed)
	}
	if !strings.Contains(string(redeployed), "Bearer "+gitHeaderToken) {
		t.Errorf("deploy did not render the Bearer token back literally:\n%s", redeployed)
	}
}

// TestGit_EnvRefLeftVerbatim is the negative: a ${GIT_TOKEN} env-ref in an
// insteadOf URL is NOT a literal secret, so it is never gate-blocked and reaches
// the shared repo verbatim.
func TestGit_EnvRefLeftVerbatim(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	seedGitRepo(t, s, sharedGitconfig)
	gitCommitAll(t, s.Repo, "baseline shared gitconfig")
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("setup apply exited %d; stderr:\n%s", code, errOut)
	}

	envLine := "[url \"https://github.com/\"]\n\tinsteadOf = https://${GIT_TOKEN}@github.com/\n"
	deployed, _ := os.ReadFile(s.HomePath(".gitconfig"))
	if err := os.WriteFile(s.HomePath(".gitconfig"), append(deployed, []byte(envLine)...), 0o600); err != nil {
		t.Fatalf("stage env-ref: %v", err)
	}
	s.FerryWithInput("y\ns\n", "capture")

	repoShared, err := os.ReadFile(s.RepoPath("dotfiles", "gitconfig"))
	if err != nil {
		t.Fatalf("read repo shared gitconfig: %v", err)
	}
	if !strings.Contains(string(repoShared), "https://${GIT_TOKEN}@github.com/") {
		t.Errorf("the ${GIT_TOKEN} env-ref was not carried to the shared repo verbatim:\n%s", repoShared)
	}
}

// TestGit_CredentialHelperStoreWarns proves credential.helper = store is
// refused/warned and never deployed to the shared ~/.gitconfig (it is identity,
// stripped), and that ferry never creates or reads ~/.git-credentials.
func TestGit_CredentialHelperStoreWarns(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	shared := sharedGitconfig + "[credential]\n\thelper = store\n"
	seedGitRepo(t, s, shared)
	gitCommitAll(t, s.Repo, "shared gitconfig with credential.helper = store")

	out, errOut, code := s.Ferry("apply")
	if code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	combined := out + errOut
	if !strings.Contains(strings.ToLower(combined), "credential.helper") || !containsAnyFold(combined, "plaintext", "osxkeychain", "git-credentials") {
		t.Errorf("apply did not warn about credential.helper = store:\n%s", combined)
	}
	// helper=store is identity → stripped from the deployed shared file.
	deployed, err := os.ReadFile(s.HomePath(".gitconfig"))
	if err != nil {
		t.Fatalf("read deployed gitconfig: %v", err)
	}
	if strings.Contains(string(deployed), "helper = store") {
		t.Errorf("credential.helper = store leaked into the deployed shared ~/.gitconfig:\n%s", deployed)
	}
	// ferry never creates ~/.git-credentials.
	if _, err := os.Stat(s.HomePath(".git-credentials")); err == nil {
		t.Errorf("ferry created ~/.git-credentials — it must never touch it")
	}
}

// TestGit_RoundTripByteStable mirrors the zsh/tmux byte-stable migration eval:
// apply materialises the git include split (a shared ~/.gitconfig ending in the
// `[include]` block, plus the ~/.gitconfig.local sidecar), and a no-op capture +
// status leaves BOTH home files and BOTH repo sources byte-, mode-, size- AND
// mtime-identical, reporting clean.
func TestGit_RoundTripByteStable(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	seedGitRepo(t, s, sharedGitconfig)
	s.WriteRepoFile(t, filepath.Join("local", "git", "gitconfig.local"), bobLocal)
	gitCommitAll(t, s.Repo, "baseline gitconfig + identity overlay")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("apply exited %d; stderr:\n%s", code, errOut)
	}
	sharedTarget := s.HomePath(".gitconfig")
	sidecarTarget := s.HomePath(".gitconfig.local")
	got, err := os.ReadFile(sharedTarget)
	if err != nil {
		t.Fatalf("apply did not deploy ~/.gitconfig: %v", err)
	}
	if !strings.Contains(string(got), "[include]\n\tpath = ~/.gitconfig.local") {
		t.Fatalf("~/.gitconfig is missing ferry's git [include] directive:\n%s", got)
	}

	sharedTW := s.SnapshotFile(t, sharedTarget)
	sidecarTW := s.SnapshotFile(t, sidecarTarget)
	repoSharedTW := s.SnapshotFile(t, s.RepoPath("dotfiles", ".gitconfig"))
	repoOverlayTW := s.SnapshotFile(t, s.RepoPath("local", "git", "gitconfig.local"))

	if _, errOut, code := s.Ferry("capture"); code != 0 {
		t.Fatalf("no-op capture exited %d; stderr:\n%s", code, errOut)
	}
	statusOut, statusErr, statusCode := s.Ferry("status")
	statusCombined := statusOut + statusErr
	if statusCode != 0 {
		t.Errorf("clean status exited %d\n%s", statusCode, statusCombined)
	}
	if !containsAnyFold(statusCombined,
		"no drift", "clean", "up to date", "up-to-date", "no changes", "nothing to", "in sync") {
		t.Errorf("clean status gave no positive no-drift signal after a no-op capture\n%s", statusCombined)
	}

	sharedTW.AssertUnchanged(t)
	sidecarTW.AssertUnchanged(t)
	repoSharedTW.AssertUnchanged(t)
	repoOverlayTW.AssertUnchanged(t)
}

// assertNoIdentityInGitHistory greps the repo's full git history for alice's
// identity — the AssertNoSecretInRepo-style scan for the identity key, restricted
// to committed history (the gitignored ~/.gitconfig.local legitimately holds it).
func assertNoIdentityInGitHistory(t *testing.T, s *Sandbox) {
	t.Helper()
	cmd := exec.Command("git", "-C", s.Repo, "log", "-p", "--all", "--full-history")
	cmd.Env = gitIsolatedEnv("GIT_PAGER=cat")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return
	}
	for _, id := range []string{"alice@example.com", "Alice Example", "ABCD1234"} {
		if strings.Contains(string(out), id) {
			t.Errorf("IDENTITY LEAK: %q found in git history", id)
		}
	}
}
