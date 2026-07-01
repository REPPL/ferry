package cmd

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/ghcli"
	"github.com/REPPL/ferry/internal/secret"
)

// syncBranchName is the branch ferry sync integrates and pushes. ferry manages a
// single-branch config repo; the default branch is `main` (git init -b main).
const syncBranchName = "main"

// runSync implements `ferry sync`: publish locally-captured changes and pull
// remote ones in ONE command, for the CONFIGURED managed repo, WITHOUT ever
// losing local work or force-pushing. It is ferry's most git-dangerous command,
// so the flow is deterministic and every failure rolls the machine back to the
// exact pre-sync bytes. See .work/PLAN-v0.2.3-ferry-sync.md.
//
// Flow: preflight (git/origin/https/no-ssh env) -> managed gate -> SNAPSHOT the
// exact pre-sync state -> integrate remote FIRST (fetch, ff-or-rebase, untracked
// guard) -> pre-commit secret gate + commit -> full push-range secret gate ->
// push a single explicit ref -> report from commit counts. Never runs `apply`.
func runSync(c *cobra.Command, _ []string) error {
	if err := preflightGit(); err != nil {
		return err
	}

	ctx, err := loadContext()
	if err != nil {
		return err
	}
	repo := ctx.RepoPath
	out := c.OutOrStdout()

	msg, _ := c.Flags().GetString("message")
	allowUnmanaged, _ := c.Flags().GetBool("allow-unmanaged")

	// STEP 2: managed-only by default. A route-1 repo is not touched unless the user
	// opts in. The override still enforces HTTPS origin + the push-range gate + a
	// single explicit-ref push (nothing below is relaxed).
	if !ctx.MachineConfig.Managed && !allowUnmanaged {
		return fmt.Errorf("`ferry sync` acts on a managed repo (route 2, `ferry init --github`); this repo is not marked managed. Re-run with `--allow-unmanaged` to sync it anyway (the same HTTPS-only + secret-gate + no-force-push rules still apply)")
	}

	// STEP 1: preflight the origin remote + its scheme. No origin => clear failure,
	// nothing pushed. Non-https scheme => refuse (never reads ~/.ssh); a file:// /
	// local-path origin is allowed ONLY under the test allowance env var.
	origin, err := syncOriginURL(repo)
	if err != nil {
		return err
	}
	if err := checkOriginScheme(origin); err != nil {
		return err
	}

	// STEP 2.5: refuse to run on a repo ALREADY mid-operation. The rollback path
	// unconditionally `rebase --abort`s and `reset --hard`s; if the user was already
	// mid-rebase/merge/cherry-pick/revert (or has an unmerged index), running sync
	// and rolling back would DESTROY that in-progress work. Refuse up front.
	if err := refuseInProgressGitOp(repo); err != nil {
		return err
	}

	// STEP 3: snapshot the EXACT pre-sync state (HEAD sha + a tracked-only stash for
	// the staged/unstaged split + a temp-dir backup of untracked & ignored file
	// contents). ANY failure below rolls back byte-for-byte to this.
	snap, err := takeSnapshot(repo)
	if err != nil {
		return err
	}
	defer snap.cleanup()

	// STEP 4a: fetch the remote into origin/<branch> from the CLEAN tracked worktree
	// the snapshot leaves (the tracked stash was popped into a held commit).
	if o, ferr := gitSync(repo, "fetch", "origin"); ferr != nil {
		return rollback(snap, repo, fmt.Errorf("sync: `git fetch origin` failed: %s", ghcli.Redact(strings.TrimSpace(o))))
	}

	upstream := "refs/remotes/origin/" + syncBranchName
	_, upstreamExists := gitSyncOK(repo, "rev-parse", "--verify", upstream)

	pulled := 0

	// STEP 4b/c: integrate the remote FIRST, from the clean baseline. Before any
	// checkout/rebase, guard against a remote change that would clobber a local
	// untracked/ignored file (the snapshot's stash removed our tracked changes but a
	// remote-added path could still collide with a backed-up ignored file). Then
	// fast-forward if we are strictly behind with no local commits, else rebase our
	// local commits onto origin/<branch>. A CONFLICT anywhere rolls back EXACTLY.
	if upstreamExists {
		if err := guardUntrackedClobber(repo, snap, upstream); err != nil {
			return rollback(snap, repo, err)
		}

		ahead, behind := aheadBehind(repo, "HEAD", upstream)
		switch {
		case behind > 0 && ahead == 0:
			// Pure fast-forward: reset the worktree to the remote tip. --hard is safe
			// here because the snapshot already stashed every local change and this
			// branch has no local-only commits.
			if o, rerr := gitSync(repo, "merge", "--ff-only", upstream); rerr != nil {
				return rollback(snap, repo, syncConflict(o))
			}
			pulled = behind
		case behind > 0 && ahead > 0:
			// Divergent: rebase local commits onto the remote tip. A conflict leaves a
			// half-rebase — rollback aborts it and restores the snapshot exactly.
			if o, rerr := gitSync(repo, "rebase", upstream); rerr != nil {
				return rollback(snap, repo, syncConflict(o))
			}
			pulled = behind
		default:
			// ahead-only or up-to-date: nothing to pull.
		}
	}

	// STEP 4d: re-apply the stashed capture changes on top of the integrated tree.
	// A conflict here (a local edit vs a pulled change to the same lines) also rolls
	// back to the exact pre-sync state — never a half-applied tree.
	if snap.hasStash {
		if o, aerr := gitSync(repo, "stash", "apply", "--index", snap.stashSHA); aerr != nil {
			return rollback(snap, repo, syncConflict(o))
		}
		// The tracked capture is cleanly back on the integrated tree; the snapshot stash
		// is now redundant and safe for cleanup() to drop.
		snap.stashApplied = true
	}

	// STEP 5: pre-commit secret gate over the worktree/index changes to be committed,
	// THEN commit. A secret in the worktree BLOCKS with NO commit created (roll back
	// to the snapshot). Skip the commit entirely if nothing is staged/changed.
	if worktreeHasChanges(repo) {
		blocker, found, scanErr := scanWorktreeForSecret(repo)
		if scanErr != nil {
			// FAIL CLOSED: a scan that could not read a file might have skipped a secret.
			// Never commit/push on an incomplete scan — roll back and abort.
			return rollback(snap, repo, fmt.Errorf("sync: the pre-commit secret scan could not read a changed file: %s — refusing to commit or push (fail closed). Re-run once the file is readable", ghcli.Redact(scanErr.Error())))
		}
		if found {
			return rollback(snap, repo, fmt.Errorf("sync: refusing to commit — %s looks like it contains a secret (e.g. a private key or token); nothing was committed or pushed. Move it to a secret store or `~/.<name>.local` and re-run", redactSecretPath(filepath.ToSlash(blocker))))
		}
		commitMsg := msg
		if strings.TrimSpace(commitMsg) == "" {
			commitMsg = "ferry sync: capture local changes"
		}
		// `git add -A` then commit: capture may write NEW (untracked) files, which
		// `commit -a` ignores. Staging everything first makes the committed set EXACTLY
		// the set the pre-commit gate above scanned (worktree status), so a captured new
		// file is both gated AND committed — never scanned-then-left-behind.
		if o, aerr := gitSync(repo, "add", "-A"); aerr != nil {
			return rollback(snap, repo, fmt.Errorf("sync: `git add -A` failed: %s", ghcli.Redact(strings.TrimSpace(o))))
		}
		if o, cerr := gitSync(repo, "commit", "-m", commitMsg); cerr != nil {
			return rollback(snap, repo, fmt.Errorf("sync: `git commit` failed: %s", ghcli.Redact(strings.TrimSpace(o))))
		}
	}

	// STEP 6: FULL push-range secret gate. Compute the EXACT commit range that would
	// be pushed and gate EVERY file in EVERY commit in it — not just the new change.
	// A secret in a PRE-EXISTING unpushed commit blocks the push; nothing leaves.
	rangeCommits, rerr := pushRangeCommits(repo, upstream, upstreamExists)
	if rerr != nil {
		return rollback(snap, repo, rerr)
	}
	commit, file, found, scanErr := scanCommitRangeForSecret(repo, rangeCommits)
	if scanErr != nil {
		// FAIL CLOSED: a read/plumbing failure mid-scan might have skipped a secret in the
		// push range. Never push on an incomplete scan. The integration already succeeded
		// (commits are local-only and consistent); do NOT roll it back — just refuse the push.
		return fmt.Errorf("sync: the push-range secret scan failed: %s — refusing to push (fail closed). Nothing left your machine; re-run once resolved", ghcli.Redact(scanErr.Error()))
	}
	if found {
		// Leave the (now cleanly-integrated) local commits intact — nothing leaves —
		// but report which commit/file blocked. Do NOT roll back the integration: the
		// pull already succeeded and the secret was pre-existing local history. The file
		// path is redacted in case a token-shaped filename would otherwise leak.
		return fmt.Errorf("sync: refusing to push — commit %s file %s looks like it contains a secret (e.g. a private key or token); nothing was pushed. Rewrite that commit to remove the secret, then re-run", commit[:min(len(commit), 12)], redactSecretPath(filepath.ToSlash(file)))
	}

	// STEP 7: push a SINGLE explicit ref. NEVER --force / --force-with-lease / any
	// ref-fanout flag. On network failure, report a safe idempotent re-run.
	pushed := 0
	needPush := len(rangeCommits) > 0
	if needPush {
		if o, perr := gitSyncPush(repo, "HEAD:"+syncBranchName); perr != nil {
			return fmt.Errorf("sync: `git push` failed: %s\nre-running `ferry sync` is safe: it will fetch and reconcile before pushing again (the remote may or may not have received the push)", ghcli.Redact(strings.TrimSpace(o)))
		}
		pushed = len(rangeCommits)
	}

	// STEP 8: report from commit COUNTS (not parsed porcelain).
	reportSync(out, pulled, pushed)
	return nil
}

// syncOriginURL returns the configured origin remote URL, or a clear error naming
// the missing origin/remote when none is set. No push is possible without it.
func syncOriginURL(repo string) (string, error) {
	o, ok := gitSyncOK(repo, "remote", "get-url", "origin")
	if !ok || strings.TrimSpace(o) == "" {
		return "", fmt.Errorf("sync: this repo has no `origin` remote — `ferry sync` needs a remote to pull from and push to. Set one up with `ferry init --github`, or add an HTTPS origin and re-run")
	}
	return strings.TrimSpace(o), nil
}

// refuseInProgressGitOp refuses to run sync when the repo is ALREADY in the middle
// of a git operation (rebase / merge / cherry-pick / revert) or has an unmerged
// index. Sync's rollback path unconditionally aborts a rebase and hard-resets, so
// running it against a half-finished operation would silently destroy the user's
// in-progress work. Also enforces an attached `refs/heads/<syncBranchName>`: sync
// pushes HEAD to that branch, so a detached HEAD or a different current branch would
// integrate/push the wrong thing. Every check runs BEFORE the snapshot.
func refuseInProgressGitOp(repo string) error {
	gitDir, ok := gitSyncOK(repo, "rev-parse", "--git-dir")
	if !ok || gitDir == "" {
		return fmt.Errorf("sync: could not locate the git directory — is this a git repo?")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repo, gitDir)
	}
	inProgress := []struct{ path, what string }{
		{"rebase-merge", "an interactive/merge rebase"},
		{"rebase-apply", "a rebase (git rebase / git am)"},
		{"MERGE_HEAD", "a merge"},
		{"CHERRY_PICK_HEAD", "a cherry-pick"},
		{"REVERT_HEAD", "a revert"},
	}
	for _, p := range inProgress {
		if _, statErr := os.Stat(filepath.Join(gitDir, p.path)); statErr == nil {
			return fmt.Errorf("sync: refusing to run — this repo is in the middle of %s. Finish or abort your in-progress git operation first (e.g. `git rebase --abort`, `git merge --abort`), then re-run `ferry sync` (your machine is unchanged)", p.what)
		}
	}

	// An unmerged index (conflict markers staged) is another mid-operation state that
	// rollback would clobber. `git ls-files -u` lists unmerged entries.
	if unmerged, ok := gitSyncOK(repo, "ls-files", "--unmerged"); ok && strings.TrimSpace(unmerged) != "" {
		return fmt.Errorf("sync: refusing to run — this repo has unmerged (conflicted) paths in the index. Resolve or abort the conflict first, then re-run `ferry sync` (your machine is unchanged)")
	}

	// Require an ATTACHED HEAD pointing at the branch sync integrates and pushes. A
	// detached HEAD or a different branch would push the wrong commits to <branch>.
	branch, ok := gitSyncOK(repo, "symbolic-ref", "--quiet", "--short", "HEAD")
	if !ok || branch == "" {
		return fmt.Errorf("sync: refusing to run on a detached HEAD — check out the `%s` branch first (`git checkout %s`), then re-run `ferry sync`", syncBranchName, syncBranchName)
	}
	if branch != syncBranchName {
		return fmt.Errorf("sync: refusing to run — HEAD is on branch %q, but `ferry sync` integrates and pushes `%s`. Check out `%s` first, then re-run", ghcli.Redact(branch), syncBranchName, syncBranchName)
	}
	return nil
}

// checkOriginScheme enforces the HTTPS-only production posture. A file:// or
// local-path origin is accepted ONLY when FERRY_ALLOW_FILE_ORIGIN=1 is set (the
// test allowance); every other non-https scheme (ssh://, git@, git://, http://)
// is refused so ferry never reads ~/.ssh and never talks plaintext http.
func checkOriginScheme(origin string) error {
	allowFile := os.Getenv("FERRY_ALLOW_FILE_ORIGIN") == "1"

	// A local filesystem path or file:// URL: allowed only under the test allowance.
	if isLocalPathOrigin(origin) {
		if allowFile {
			return nil
		}
		return fmt.Errorf("sync: refusing a file:// / local-path origin (%q) — ferry sync is HTTPS-only in production", ghcli.Redact(origin))
	}

	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" {
		// scp-like `git@host:owner/repo` has no scheme — refuse it (it is SSH).
		return fmt.Errorf("sync: refusing a non-HTTPS origin (%q) — ferry sync talks HTTPS only and never reads ~/.ssh. Set an https:// remote and re-run", ghcli.Redact(origin))
	}
	if strings.ToLower(u.Scheme) != "https" {
		return fmt.Errorf("sync: refusing a %q origin scheme (%q) — ferry sync talks HTTPS only and never reads ~/.ssh (no ssh://, git://, http://). Set an https:// remote and re-run", u.Scheme, ghcli.Redact(origin))
	}
	return nil
}

// isLocalPathOrigin reports whether origin is a file:// URL or a bare local
// filesystem path (absolute or relative) rather than a network URL. A scp-like
// `git@host:...` is NOT local (it is SSH); an `https://...` is not local.
func isLocalPathOrigin(origin string) bool {
	if strings.HasPrefix(origin, "file://") {
		return true
	}
	// A scheme://... network URL is not a local path.
	if i := strings.Index(origin, "://"); i >= 0 {
		return false
	}
	// scp-like git@host:owner/repo (a colon before any slash) is SSH, not local.
	if strings.Contains(origin, "@") {
		return false
	}
	if colon := strings.Index(origin, ":"); colon >= 0 {
		slash := strings.Index(origin, "/")
		if slash < 0 || colon < slash {
			return false // host:path form => remote (ssh/scp-like)
		}
	}
	return strings.HasPrefix(origin, "/") || strings.HasPrefix(origin, ".") || strings.HasPrefix(origin, "~") || !strings.Contains(origin, ":")
}

// gitSync runs a git subprocess rooted at repo with the NO-SSH, NO-PROMPT posture
// the plan pins: GIT_TERMINAL_PROMPT=0 (git never prompts for credentials) AND
// GIT_SSH_COMMAND=/bin/false (so even a stray ssh path can't read ~/.ssh). It
// ALSO neutralizes hooks (`-c core.hooksPath=/dev/null`) so no commit/rebase/push
// hook can run arbitrary code (incl. reading ~/.ssh) or bypass the gate. It returns
// combined output + error. This is the ONLY way sync spawns git.
func gitSync(repo string, args ...string) (string, error) {
	// -c core.hooksPath=/dev/null goes in the git GLOBAL-options slot (before the
	// subcommand) so it applies to every operation and cannot be overridden by
	// repo/user config-driven hooks.
	full := append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=/bin/false",
		"GIT_PAGER=cat",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// gitSyncPush pushes a SINGLE explicit ref with every config-driven fanout disabled:
// no hooks (via gitSync), no tag fanout (`--no-follow-tags` + `push.followTags=false`,
// so a `push.followTags=true` config cannot push tags OUTSIDE the gated commit range)
// and no submodule fanout (`push.recurseSubmodules=no`). The hardening `-c` flags sit
// in the git global-options slot (before `push`); the only positionals are the remote
// and the one refspec. NEVER --force / --force-with-lease / --tags / --all / --mirror.
func gitSyncPush(repo, refspec string) (string, error) {
	return gitSync(repo,
		"-c", "push.followTags=false",
		"-c", "push.recurseSubmodules=no",
		"push", "--no-follow-tags",
		"origin", refspec,
	)
}

// gitSyncOK is gitSync for observations that may legitimately fail (e.g. rev-parse
// on a missing upstream): returns (trimmed output, ok).
func gitSyncOK(repo string, args ...string) (string, bool) {
	o, err := gitSync(repo, args...)
	return strings.TrimSpace(o), err == nil
}

// snapshot holds the exact pre-sync state so ANY failure rolls back byte-for-byte:
//   - the original HEAD sha;
//   - a stash commit of TRACKED-modified changes + the index, held by sha, so the
//     staged/unstaged split restores exactly (`stash apply --index`);
//   - a temp-dir backup of clobberable UNTRACKED and IGNORED files.
//
// CRITICAL: the stash is TRACKED-ONLY (no --include-untracked). An untracked file
// carrying a secret must NEVER be objectified into git (a stash commit is
// recoverable from the object store — a leak). Untracked + ignored files are left
// IN the worktree (their contents are backed up out-of-band for restore) and are
// never touched unless a checkout would clobber them — which the untracked guard
// aborts BEFORE it happens, so on a clean run they keep their exact bytes+mtime.
type snapshot struct {
	repo      string
	headSHA   string
	hasStash  bool
	stashSHA  string
	untracked map[string]bool   // repo-rel path of each local untracked file (pre-sync)
	backupDir string            // temp dir holding backed-up untracked+ignored files
	backups   map[string]string // repo-rel path -> backup abs path (untracked + ignored)
	modes     map[string]os.FileMode
	symlinks  map[string]string // repo-rel path -> link target (backed-up symlinks)

	// stashApplied records that the tracked stash was cleanly re-applied — either on a
	// happy run (STEP 4d) or during a successful rollback. cleanup() only DROPS the
	// snapshot stash when this is true; otherwise the stash is KEPT (never lose work).
	stashApplied bool
}

// takeSnapshot records the pre-sync state. It backs up untracked + ignored file
// CONTENTS out-of-band (never into git), then stashes ONLY tracked-modified + index
// changes so integration starts from a clean TRACKED baseline while untracked and
// ignored files stay in place untouched.
func takeSnapshot(repo string) (*snapshot, error) {
	head, ok := gitSyncOK(repo, "rev-parse", "HEAD")
	if !ok {
		return nil, fmt.Errorf("sync: could not read HEAD — is this a git repo with at least one commit?")
	}
	s := &snapshot{repo: repo, headSHA: head, untracked: map[string]bool{}, backups: map[string]string{}, modes: map[string]os.FileMode{}, symlinks: map[string]string{}}

	// Back up untracked + ignored file contents to a ferry-owned temp dir (out of
	// git — never objectified). Best-effort: an unreadable file is simply not
	// backed up (it was not ours to lose).
	if err := s.backupOutOfBand(repo); err != nil {
		return nil, err
	}

	// Record the pre-push `refs/stash` sha (empty if no stash exists yet). This is how
	// we identify OUR stash UNAMBIGUOUSLY: after the push, refs/stash is "ours" ONLY if
	// it changed to a NEW sha (i.e. THIS push actually created a stash entry). A CLEAN
	// tracked tree pushes nothing, leaving refs/stash unchanged — even when a PRIOR
	// failed-rollback left an old `ferry-sync-snapshot` stash at the top. Identifying by
	// message-at-top would mistake that stale stash for ours; the sha delta cannot.
	preStash, _ := gitSyncOK(repo, "rev-parse", "--verify", "-q", "refs/stash")

	// Stash TRACKED-modified changes + the index into a commit we hold by sha. NO
	// --include-untracked: untracked/ignored files stay in the worktree (backed up
	// above) so no untracked secret ever enters a git object. The stash leaves the
	// TRACKED worktree + index clean, so integration rebases/ff's from a clean base.
	if o, err := gitSync(repo, "stash", "push", "-m", "ferry-sync-snapshot"); err != nil {
		return nil, fmt.Errorf("sync: could not snapshot local changes (`git stash`): %s", ghcli.Redact(strings.TrimSpace(o)))
	}
	// A clean tracked tree yields "No local changes to save" and no NEW stash entry.
	// Treat a stash as OURS only when `git stash push` actually created one THIS run:
	// refs/stash must now resolve to a sha that DIFFERS from the pre-push sha. A clean
	// tree (refs/stash unchanged, even if an old ferry-sync-snapshot sits on top) leaves
	// hasStash=false — so cleanup()/restore() never touch a stash this run did not make.
	if postStash, ok := gitSyncOK(repo, "rev-parse", "--verify", "-q", "refs/stash"); ok && postStash != "" && postStash != preStash {
		s.hasStash = true
		s.stashSHA = postStash
	}
	return s, nil
}

// backupOutOfBand copies every UNTRACKED and IGNORED file in the worktree to a
// ferry-owned temp dir, keyed by repo-relative path, so a checkout that would
// clobber one can be restored on rollback. `git status --porcelain --ignored`
// enumerates both (untracked as "?? ", ignored as "!! "). The files stay in the
// worktree; this is a byte backup for restore only.
func (s *snapshot) backupOutOfBand(repo string) error {
	// NUL-delimited (`-z`) so paths with spaces/newlines/quotes are handled verbatim;
	// the human-porcelain form quotes/escapes such paths and would corrupt them.
	out, err := gitSync(repo, "status", "--porcelain", "--ignored", "-z")
	if err != nil {
		// FAIL CLOSED: if we cannot enumerate untracked/ignored files, we cannot know
		// which files a checkout might clobber, so we cannot guarantee a byte-for-byte
		// rollback. Abort the snapshot rather than proceed with an incomplete backup.
		return fmt.Errorf("sync: could not enumerate untracked/ignored files for backup (`git status --ignored`): %s — refusing to proceed (your machine is unchanged)", ghcli.Redact(strings.TrimSpace(out)))
	}
	var untrackedRels, otherRels []string
	for _, entry := range strings.Split(out, "\x00") {
		if len(entry) < 3 {
			continue
		}
		// `-z` status entries are "XY <path>" with a single space after the 2-char code.
		code := entry[:2]
		rel := entry[3:]
		var untracked bool
		switch code {
		case "??":
			untracked = true
		case "!!":
			untracked = false
		default:
			continue
		}
		if rel == "" {
			continue
		}
		if strings.HasSuffix(rel, "/") {
			// A whole untracked/ignored directory: enumerate its files individually
			// (lstat-based, so nested symlinks are recorded as links, not followed).
			// FAIL CLOSED: a walk error means we could not fully enumerate this directory,
			// so a file inside it might go un-backed-up and un-restorable — abort rather
			// than silently skip it.
			if werr := filepath.Walk(filepath.Join(repo, rel), func(p string, info os.FileInfo, werr error) error {
				if werr != nil {
					return werr
				}
				if info.IsDir() {
					return nil
				}
				if r, e := filepath.Rel(repo, p); e == nil {
					if untracked {
						untrackedRels = append(untrackedRels, r)
					} else {
						otherRels = append(otherRels, r)
					}
				}
				return nil
			}); werr != nil {
				return fmt.Errorf("sync: could not enumerate the untracked/ignored directory %q for backup: %s — refusing to proceed (your machine is unchanged)", ghcli.Redact(rel), ghcli.Redact(werr.Error()))
			}
			continue
		}
		if untracked {
			untrackedRels = append(untrackedRels, rel)
		} else {
			otherRels = append(otherRels, rel)
		}
	}
	for _, r := range untrackedRels {
		s.untracked[r] = true
	}
	all := append(untrackedRels, otherRels...)
	if len(all) == 0 {
		return nil
	}
	dir, err := os.MkdirTemp("", "ferry-sync-backup-")
	if err != nil {
		return fmt.Errorf("sync: could not create backup dir: %w", err)
	}
	s.backupDir = dir
	for _, rel := range all {
		if err := s.backupOne(repo, dir, rel); err != nil {
			// FAIL CLOSED: a file we saw but could not back up means a later rollback
			// could not restore it — refuse the whole snapshot rather than half-snapshot.
			return fmt.Errorf("sync: could not back up local file %q for safe rollback: %w — refusing to proceed (your machine is unchanged)", ghcli.Redact(rel), err)
		}
	}
	return nil
}

// backupOne backs up a single untracked/ignored path, preserving TYPE and MODE:
// a symlink is recorded as its target (never followed); a regular file's bytes and
// permission bits are copied faithfully. lstat (not stat) so a symlink is a symlink.
func (s *snapshot) backupOne(repo, dir, rel string) error {
	src := filepath.Join(repo, rel)
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, lerr := os.Readlink(src)
		if lerr != nil {
			return lerr
		}
		s.symlinks[rel] = target
		return nil
	}
	if !info.Mode().IsRegular() {
		// A device/socket/fifo among untracked files is not something a git checkout
		// clobbers with content; skip it (recording it would be meaningless to restore).
		return nil
	}
	data, rerr := os.ReadFile(src)
	if rerr != nil {
		return rerr
	}
	dst := filepath.Join(dir, rel)
	if werr := os.WriteFile(dst, data, info.Mode().Perm()); werr != nil {
		return werr
	}
	s.backups[rel] = dst
	s.modes[rel] = info.Mode().Perm()
	return nil
}

// restore rolls the repo back to the EXACT pre-sync state: reset --hard to the
// original HEAD sha (drops any auto-commit / half-rebase), re-apply the stashed
// TRACKED changes WITH the staged/unstaged split (--index), and copy any
// untracked/ignored file a checkout clobbered back from the out-of-band backup.
// It VERIFIES each step and returns an error naming what failed, so the caller can
// surface a recovery path (and cleanup() can KEEP the stash if it was not applied).
// Never force-pushes, never leaves a rebase.
func (s *snapshot) restore(repo string) error {
	// Abort any half-rebase (ignore "no rebase in progress"), then hard-reset to the
	// exact pre-sync HEAD. A failed reset is a real problem — the tree may be wrong.
	_, _ = gitSync(repo, "rebase", "--abort")
	if o, err := gitSync(repo, "reset", "--hard", s.headSHA); err != nil {
		return fmt.Errorf("could not reset to the pre-sync commit %s: %s", s.headSHA, ghcli.Redact(strings.TrimSpace(o)))
	}

	// Restore untracked/ignored files/symlinks a checkout may have clobbered, BEFORE
	// re-applying the stash (regular files first; then verify).
	if err := s.restoreOutOfBand(repo); err != nil {
		return err
	}

	if s.hasStash {
		// Re-apply the held tracked stash with --index so the exact staged/unstaged
		// split is restored. The stash commit is held by sha regardless of any earlier
		// apply in this run. If THIS fails, the tracked work is still safe IN the stash
		// — do NOT mark stashApplied, so cleanup() keeps the stash and the caller can
		// point the user at it.
		if o, err := gitSync(repo, "stash", "apply", "--index", s.stashSHA); err != nil {
			return fmt.Errorf("could not re-apply your stashed local changes (%s): %s", s.stashSHA, ghcli.Redact(strings.TrimSpace(o)))
		}
		s.stashApplied = true
	}
	return nil
}

// restoreOutOfBand copies every backed-up untracked/ignored regular file and symlink
// back into the worktree, preserving type + mode. Already-exact regular files are
// left untouched (mtime preserved). Any failure is surfaced (rollback incomplete).
func (s *snapshot) restoreOutOfBand(repo string) error {
	for rel, target := range s.symlinks {
		dst := filepath.Join(repo, rel)
		if cur, err := os.Readlink(dst); err == nil && cur == target {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("could not restore symlink %q: %w", ghcli.Redact(rel), err)
		}
		_ = os.Remove(dst)
		if err := os.Symlink(target, dst); err != nil {
			return fmt.Errorf("could not restore symlink %q: %w", ghcli.Redact(rel), err)
		}
	}
	for rel, backup := range s.backups {
		data, err := os.ReadFile(backup)
		if err != nil {
			return fmt.Errorf("could not read the backup of %q: %w", ghcli.Redact(rel), err)
		}
		dst := filepath.Join(repo, rel)
		mode := s.modes[rel]
		if mode == 0 {
			mode = 0o644
		}
		cur, cerr := os.ReadFile(dst)
		if cerr == nil && string(cur) == string(data) {
			// Content already exact; still ensure the mode matches (cheap, preserves mtime).
			if fi, ferr := os.Lstat(dst); ferr == nil && fi.Mode().Perm() != mode {
				_ = os.Chmod(dst, mode)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("could not restore %q: %w", ghcli.Redact(rel), err)
		}
		if err := os.WriteFile(dst, data, mode); err != nil {
			return fmt.Errorf("could not restore %q: %w", ghcli.Redact(rel), err)
		}
	}
	return nil
}

// cleanup removes the out-of-band backup temp dir and drops the held snapshot stash
// ONLY when its tracked contents were VERIFIED re-applied (stashApplied) — either on
// a happy run or during a successful rollback. If restoration did NOT cleanly apply
// the stash (a failed rollback), the stash is the user's ONLY copy of that tracked
// work, so cleanup KEEPS it. This is the data-loss guard: never drop an un-applied
// snapshot stash. Called deferred.
func (s *snapshot) cleanup() {
	if s.backupDir != "" {
		_ = os.RemoveAll(s.backupDir)
	}
	if !s.hasStash || s.repo == "" || !s.stashApplied {
		return
	}
	// Drop OUR snapshot stash entry so the user's stash list is left as it was. Identify
	// it by SHA, not by message: only drop the top entry when its commit sha is EXACTLY
	// the stash sha we created this run (s.stashSHA) AND its contents were re-applied. A
	// stale ferry-sync-snapshot left by a prior failed rollback has a different sha and is
	// never dropped here — dropping by message-at-top could silently destroy that work.
	if top, ok := gitSyncOK(s.repo, "rev-parse", "--verify", "-q", "refs/stash"); ok && top == s.stashSHA {
		_, _ = gitSync(s.repo, "stash", "drop", "stash@{0}")
	}
}

// guardUntrackedClobber aborts BEFORE any checkout/rebase if integrating the remote
// would overwrite a local file that git integration would not itself reconcile: a
// local UNTRACKED, IGNORED, or STAGED-ADDED (new-in-index, not yet committed) file at
// a path the remote ADDS. It compares CONTENT: a collision whose bytes already MATCH
// the remote's is a no-op (not a clobber) and is allowed through; only a differing-
// bytes collision aborts, so the guard never cries wolf on an identical file.
func guardUntrackedClobber(repo string, s *snapshot, upstream string) error {
	// Paths present in the remote tip but not on our HEAD (candidate new files).
	added, ok := gitSyncOK(repo, "diff", "--name-only", "--diff-filter=A", "HEAD", upstream)
	if !ok || strings.TrimSpace(added) == "" {
		return nil
	}
	remoteAdded := map[string]bool{}
	for _, p := range strings.Split(added, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			remoteAdded[p] = true
		}
	}

	// Build the local-collision candidate set: untracked + backed-up ignored + staged-
	// added-new. Each maps to the LOCAL bytes to compare against the remote's version.
	local := map[string][]byte{}
	for rel := range s.untracked {
		if remoteAdded[rel] {
			local[rel] = readBackupOrWorktree(repo, s, rel)
		}
	}
	for rel := range s.backups {
		if remoteAdded[rel] {
			local[rel] = readBackupOrWorktree(repo, s, rel)
		}
	}
	// Staged-added (A in the index): new files the user staged but did not commit. The
	// snapshot stash removed them from the worktree, but `stash apply` would re-add them
	// and collide with the remote's version. Their bytes come from the stash blob.
	for _, rel := range stagedAddedPaths(repo) {
		if remoteAdded[rel] {
			if b, ok := stashBlob(repo, s, rel); ok {
				local[rel] = b
			} else {
				local[rel] = nil // unknown bytes → treat as differing (fail safe)
			}
		}
	}

	for rel, localBytes := range local {
		remoteBytes, rok := gitBlobBytes(repo, upstream+":"+rel)
		// If we cannot read either side's bytes, fail SAFE: treat as a clobber rather
		// than silently overwriting.
		if !rok || localBytes == nil || string(localBytes) != string(remoteBytes) {
			return untrackedClobberErr(rel)
		}
	}
	return nil
}

// stagedAddedPaths lists paths added (new) in the index vs HEAD — staged but not yet
// committed. NUL-delimited so odd paths survive.
func stagedAddedPaths(repo string) []string {
	o, ok := gitSyncOK(repo, "diff", "--cached", "--name-only", "--diff-filter=A", "-z", "HEAD")
	if !ok {
		return nil
	}
	var out []string
	for _, p := range strings.Split(o, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// readBackupOrWorktree returns the LOCAL bytes for an untracked/ignored path: the
// out-of-band backup if present, else the live worktree file (nil on any error).
func readBackupOrWorktree(repo string, s *snapshot, rel string) []byte {
	if bp, ok := s.backups[rel]; ok {
		if b, err := os.ReadFile(bp); err == nil {
			return b
		}
	}
	if b, err := os.ReadFile(filepath.Join(repo, rel)); err == nil {
		return b
	}
	return nil
}

// stashBlob returns the bytes of a path as captured in the snapshot stash commit.
func stashBlob(repo string, s *snapshot, rel string) ([]byte, bool) {
	if !s.hasStash {
		return nil, false
	}
	return gitBlobBytes(repo, s.stashSHA+":"+rel)
}

// gitBlobBytes reads a blob's raw bytes via `git show <rev>:<path>` (cat-file -p
// semantics). Returns (bytes, ok); ok is false on any git error (missing path/rev).
func gitBlobBytes(repo, spec string) ([]byte, bool) {
	o, err := gitSync(repo, "show", spec)
	if err != nil {
		return nil, false
	}
	return []byte(o), true
}

func untrackedClobberErr(rel string) error {
	r := ghcli.Redact(rel)
	return fmt.Errorf("sync: the remote adds %q, but you have a local untracked/ignored/staged file at that path with different content — refusing to overwrite it. Move or commit your local %q, then re-run (your machine is unchanged)", r, r)
}

// aheadBehind returns how many commits `ref` is ahead of and behind `base`
// (git rev-list --count --left-right base...ref). Zero/zero on error.
func aheadBehind(repo, ref, base string) (ahead, behind int) {
	o, ok := gitSyncOK(repo, "rev-list", "--left-right", "--count", base+"..."+ref)
	if !ok {
		return 0, 0
	}
	f := strings.Fields(o)
	if len(f) != 2 {
		return 0, 0
	}
	behind = atoiSafe(f[0]) // commits in base not in ref
	ahead = atoiSafe(f[1])  // commits in ref not in base
	return ahead, behind
}

// pushRangeCommits computes the EXACT list of commit oids that a push would make
// visible on origin, so the secret gate can cover the WHOLE range:
//   - normal (origin/<branch> exists): origin/<branch>..HEAD;
//   - FIRST push (no origin/<branch>): `git rev-list HEAD --not --remotes=origin`
//     — commits not yet on the TARGET remote origin (NOT --remotes, so a commit
//     only on a second remote like upstream is STILL gated, because it would
//     still become visible on origin).
func pushRangeCommits(repo, upstream string, upstreamExists bool) ([]string, error) {
	var args []string
	if upstreamExists {
		args = []string{"rev-list", upstream + "..HEAD"}
	} else {
		args = []string{"rev-list", "HEAD", "--not", "--remotes=origin"}
	}
	o, err := gitSync(repo, args...)
	if err != nil {
		return nil, fmt.Errorf("sync: could not compute the push range (`git %s`): %s", strings.Join(args, " "), ghcli.Redact(strings.TrimSpace(o)))
	}
	var commits []string
	for _, l := range strings.Fields(o) {
		if l != "" {
			commits = append(commits, l)
		}
	}
	return commits, nil
}

// scanCommitRangeForSecret gates EVERY file in EVERY commit in the range through
// internal/secret. It walks each commit's tree and scans each blob's content; the
// FIRST high-confidence secret blocks the push. Returns (commit, file, found, err).
// FAILS CLOSED: if ls-tree or cat-file errors, it returns a non-nil error so the
// caller aborts the push rather than skipping a possibly-secret-bearing blob.
func scanCommitRangeForSecret(repo string, commits []string) (string, string, bool, error) {
	for _, commit := range commits {
		// List every file in this commit's tree with its blob oid.
		o, lerr := gitSync(repo, "ls-tree", "-r", commit)
		if lerr != nil {
			return "", "", false, fmt.Errorf("`git ls-tree %s`: %s", commit[:min(len(commit), 12)], strings.TrimSpace(o))
		}
		for _, line := range strings.Split(o, "\n") {
			// Format: <mode> <type> <oid>\t<path>
			tab := strings.IndexByte(line, '\t')
			if tab < 0 {
				continue
			}
			meta := strings.Fields(line[:tab])
			if len(meta) < 3 || meta[1] != "blob" {
				continue
			}
			oid := meta[2]
			path := line[tab+1:]
			// Gate the PATH first: a committed file whose name is secret-shaped (a token
			// used as a path component) blocks the push exactly like secret content, for
			// parity with export and init --github. ls-tree emits forward-slash paths.
			if secretInPath(path) {
				return commit, path, true, nil
			}
			blob, berr := gitSync(repo, "cat-file", "-p", oid)
			if berr != nil {
				return "", "", false, fmt.Errorf("`git cat-file` for %s: %s", oid, strings.TrimSpace(blob))
			}
			if secret.IsBlockedFromRepo(blob) {
				return commit, path, true, nil
			}
		}
	}
	return "", "", false, nil
}

// worktreeHasChanges reports whether the worktree has staged or unstaged changes
// (something to commit). Untracked-only files also count (commit -a won't add them,
// but `git add -A` semantics: sync commits tracked changes; untracked new repo
// files are a capture concern committed via the -a path only if tracked). We use
// `status --porcelain` non-empty to decide there is work.
func worktreeHasChanges(repo string) bool {
	o, ok := gitSyncOK(repo, "status", "--porcelain")
	return ok && strings.TrimSpace(o) != ""
}

// scanWorktreeForSecret gates the worktree/index changes to be committed BEFORE
// `git commit`. It scans every changed (staged, unstaged, or untracked) file's
// current content; the FIRST high-confidence secret blocks the commit. Returns
// (offending repo-rel path, found, err). FAILS CLOSED: if a changed file that should
// exist cannot be read, it returns a non-nil error so the caller aborts rather than
// committing/pushing an unscanned file. A DELETED file (status `D`) is expected to be
// absent and is skipped, not treated as a scan failure.
func scanWorktreeForSecret(repo string) (string, bool, error) {
	o, ok := gitSyncOK(repo, "status", "--porcelain", "-z")
	if !ok {
		return "", false, fmt.Errorf("`git status` failed")
	}
	for _, entry := range strings.Split(o, "\x00") {
		if len(entry) < 4 {
			continue
		}
		xy := entry[:2]
		path := entry[3:]
		// A rename's `-z` form emits "R  new" then the old path as the NEXT NUL field;
		// the old field has no XY code, so len<4 (or it starts unlike a status) — the
		// slice above already scans the new path, and the stray old field is skipped.
		if path == "" {
			continue
		}
		// Gate the PATH itself first: a filename component that is secret-shaped (a token
		// used as a path) blocks the commit exactly like secret CONTENT, matching export
		// and init --github. This runs before (and independent of) reading the file, so a
		// secret-shaped name on a deleted/unreadable path is still caught.
		if secretInPath(filepath.ToSlash(path)) {
			return path, true, nil
		}
		// A pure deletion (both index+worktree marks are D/space-D) has no file to read.
		if xy == "D " || xy == " D" || xy == "DD" || xy == "AD" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repo, path))
		if err != nil {
			if os.IsNotExist(err) {
				// A rename/delete race: the path is gone, nothing to scan.
				continue
			}
			return "", false, fmt.Errorf("could not read changed file %q: %w", ghcli.Redact(path), err)
		}
		if secret.IsBlockedFromRepo(string(data)) {
			return path, true, nil
		}
	}
	return "", false, nil
}

// rollback restores the snapshot and returns the appropriate error. On a CLEAN
// restore it returns cause (the original failure — machine unchanged). If restore
// FAILS, it returns a data-safety error that names WHERE the user's tracked work is
// preserved (the held snapshot stash sha and/or the out-of-band backup dir) plus the
// exact `git stash apply` recovery command — never leaving the user with lost work
// and no pointer to it. cleanup() keeps the stash whenever restore did not apply it.
func rollback(s *snapshot, repo string, cause error) error {
	if rerr := s.restore(repo); rerr != nil {
		var where strings.Builder
		if s.hasStash && !s.stashApplied {
			fmt.Fprintf(&where, "\nyour tracked local changes are PRESERVED in stash %s — recover them with `git -C %s stash apply --index %s`", s.stashSHA, redactPath(repo), s.stashSHA)
		}
		if s.backupDir != "" {
			fmt.Fprintf(&where, "\nyour untracked/ignored files are backed up under %s", redactPath(s.backupDir))
		}
		return fmt.Errorf("sync: %w\nBUT rolling back cleanly ALSO failed: %s.%s\nInspect the repo with git before re-running", cause, ghcli.Redact(rerr.Error()), where.String())
	}
	return cause
}

// redactPath makes a filesystem path safe to surface in a recovery message: it
// collapses the user's home directory to `~` (so a home path like /Users/<name>/... is
// not echoed raw) and then runs the boundary token scrub (so a token-shaped path
// component is masked too). The result stays a usable path for the recovery command.
func redactPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if p == home {
			p = "~"
		} else if strings.HasPrefix(p, home+string(os.PathSeparator)) {
			p = "~" + p[len(home):]
		}
	}
	return ghcli.Redact(p)
}

// redactSecretPath makes a repo-relative path safe to echo in a secret-block message.
// A path can itself BE the secret (a high-entropy filename that secretInPath flags),
// and ghcli.Redact only masks GitHub-token/auth/userinfo shapes — not the broader
// secret-shaped components secretInPath detects via secret.GateValue. So this masks
// any secret-shaped path COMPONENT to `<redacted>`, keeping the rest visible so the
// user can still locate the file. Applied at every secret-block message site.
func redactSecretPath(slash string) string {
	parts := strings.Split(slash, "/")
	for i, comp := range parts {
		if comp != "" && secretInPath(comp) {
			parts[i] = "<redacted>"
		}
	}
	return ghcli.Redact(strings.Join(parts, "/"))
}

// syncConflict is the uniform conflict abort: a clear "machine unchanged; resolve
// with git then re-run" message, after the caller has already restored the
// snapshot exactly. NEVER force-pushes, NEVER leaves a half-rebased tree.
func syncConflict(gitOut string) error {
	return fmt.Errorf("sync hit a conflict integrating remote changes; your machine is unchanged — resolve it with git manually, then re-run `ferry sync`.\n%s", ghcli.Redact(strings.TrimSpace(gitOut)))
}

// reportSync prints the pull/push summary from commit COUNTS (not parsed porcelain).
func reportSync(out io.Writer, pulled, pushed int) {
	switch {
	case pulled > 0 && pushed > 0:
		fmt.Fprintf(out, "sync: pulled %d commit(s) from origin and pushed %d — both sides up to date.\nRun `ferry apply` to deploy the pulled changes.\n", pulled, pushed)
	case pulled > 0:
		fmt.Fprintf(out, "sync: pulled %d commit(s) from origin; nothing to push (up to date).\nRun `ferry apply` to deploy the pulled changes.\n", pulled)
	case pushed > 0:
		fmt.Fprintf(out, "sync: nothing to pull; pushed %d commit(s) to origin.\n", pushed)
	default:
		fmt.Fprintln(out, "sync: already up to date; nothing to pull or push.")
	}
}

// atoiSafe parses a small non-negative integer, returning 0 on any error.
func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
