package cmd

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/paths"
)

func init() {
	// init-only flags, registered here (not commands.go, which the skeleton wave owns).
	//   --fresh    force the fresh path (new repo) even if a source arg looks present;
	//              optionally takes a positional dir to place the repo (see below)
	//   --yes      assume "yes" for the closing apply confirmation
	//   --apply    actually run apply at the end (default: show the plan and stop)
	initCmd.Flags().Bool("fresh", false, "set up a NEW config repo (capture this machine) instead of cloning")
	initCmd.Flags().Bool("yes", false, "assume yes for the closing apply confirmation")
	initCmd.Flags().Bool("apply", false, "run apply at the end of init (default: show the plan and stop)")
}

// defaultRepoDir returns ferry's neutral, ferry-owned default location for a
// fresh/cloned config repo: ~/.config/ferry/repo, a subdir of the config dir ferry
// already owns and hardens. No personal folder taxonomy is baked in and no prompt
// is needed — ferry owns this path in its own config space by default. An explicit
// override (a positional dir after --fresh, or an existing-repo clone destination
// argument handling) is layered on by the callers.
func defaultRepoDir() (string, error) {
	cfgDir, err := paths.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfgDir, "repo"), nil
}

// runInit is the once-per-machine first-run setup. It preflights git, then takes
// one of two starting points:
//
//   - Existing: `ferry init <repo-url-or-path>` clones the given repo (over HTTPS
//     for a remote — no SSH key needed; file:// or a local path is the offline
//     fast-path) into a working tree under ferry's own space and records that clone.
//   - Fresh:    `ferry init` (no arg) or `ferry init --fresh [dir]` initialises a NEW
//     config repo (git init + a minimal ferry.toml) so "capture this machine" works.
//     With no dir it lands at ferry's neutral default (~/.config/ferry/repo); an
//     optional positional dir places it elsewhere.
//
// It writes ~/.config/ferry/config.toml (hostname + repo path) and ends by showing
// the apply plan (only mutating when --apply --yes). It never reads or requires ~/.ssh.
func runInit(c *cobra.Command, args []string) error {
	out := c.OutOrStdout()
	in := bufio.NewReader(c.InOrStdin())

	// 1. git preflight (shared with capture): absent git => actionable install
	//    guidance and a non-zero exit, never a crash.
	if err := preflightGit(); err != nil {
		return err
	}

	fresh, _ := c.Flags().GetBool("fresh")
	arg := ""
	if len(args) > 0 {
		arg = strings.TrimSpace(args[0])
	}
	// With --fresh, a positional arg is an OPTIONAL destination dir for the new repo
	// (not a clone source). Without --fresh, it is an existing-repo clone source.
	source := ""
	freshDir := ""
	if fresh {
		freshDir = arg
	} else {
		source = arg
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// 2. Resolve the repo. Priority:
	//    a. an explicit source argument (clone a URL / wire an existing local clone),
	//    b. an already-configured repo (config.toml from a prior init or the harness),
	//    c. otherwise initialise a fresh repo (at the neutral default or an explicit dir).
	var repoPath string
	switch {
	case source != "" && !fresh:
		repoPath, err = cloneExisting(out, source)
	case !fresh:
		if existing, ok := existingConfiguredRepo(); ok {
			// Guard the configured repo path BEFORE runInit reads/writes it via
			// ensureLocalLayerIgnored/ensureLocalManifest: a config.toml pointing
			// under ~/.ssh must be refused before any FS op on that path.
			if repoPath, err = guardRepoPath("configured repo path", existing); err != nil {
				return err
			}
			fmt.Fprintf(out, "using already-configured config repo at %s\n", repoPath)
		} else {
			repoPath, err = initFresh(out, freshDir)
		}
	default:
		repoPath, err = initFresh(out, freshDir)
	}
	if err != nil {
		return err
	}

	// Ensure the per-machine .local layer is gitignored in the resolved repo. This
	// is idempotent and safe on an already-configured repo (capture's local-route
	// writes ferry.local.toml + local/, which must never be committed).
	if err := ensureLocalLayerIgnored(repoPath); err != nil {
		return err
	}

	// 3. Write ~/.config/ferry/config.toml (identity + repo clone path). This is
	//    what loadContext() reads on every later command.
	hostname, herr := os.Hostname()
	if herr != nil || strings.TrimSpace(hostname) == "" {
		hostname = "unknown"
	}
	if err := config.SaveMachineConfig(config.MachineConfig{Hostname: hostname, Repo: repoPath}); err != nil {
		return fmt.Errorf("write machine config: %w", err)
	}
	cfgPath := filepath.Join(home, ".config", "ferry", "config.toml")
	fmt.Fprintf(out, "wrote ferry config: %s (repo: %s)\n", cfgPath, repoPath)

	// 4. Create/confirm the per-machine manifest (ferry.local.toml) BEFORE any
	//    mutation, so first-run scope is explicit (never "broad defaults mutate
	//    everything"). This must run before finishWithApply triggers the plan.
	if err := ensureLocalManifest(out, repoPath); err != nil {
		return err
	}

	// 5. End by surfacing the apply plan. By default init is non-mutating: it shows
	//    the plan and stops (safe on a non-tty / empty stdin). It only applies when
	//    --apply is given (and confirmed, or --yes).
	if err := finishWithApply(c, in, out); err != nil {
		return err
	}

	return nil
}

// cloneExisting resolves an existing-repo source into a usable working clone and
// returns its path. Two sub-cases:
//
//   - A bare local PATH that is ALREADY a git working tree is WIRED directly (its
//     own path recorded) — "set ferry up against this existing clone". No re-clone.
//   - An accepted URL (https:// or file://) — or a local path that is not yet a
//     repo — is CLONED into a fresh working tree at ferry's neutral default location
//     (~/.config/ferry/repo). Out-of-scope remotes (ssh://, git://, http://,
//     scp-style) are rejected before this point by checkCloneSource.
//
// Cloning a remote uses whatever scheme git is handed (HTTPS for a public repo), so
// no SSH key is read or required for an HTTPS/file source.
func cloneExisting(out io.Writer, source string) (string, error) {
	// Enforce the clone contract BEFORE the clone-vs-wire decision so git never
	// receives a remote outside it: ferry clones over HTTPS only (plus a local
	// path / file:// for the offline-fresh path) and is hands-off ~/.ssh. An
	// ssh:// URL or an scp-style git@host:repo remote would have git read SSH key
	// material; git:// is insecure; http:// is not HTTPS — all out of scope.
	if err := checkCloneSource(source); err != nil {
		return "", err
	}

	if !hasURLScheme(source) {
		// Guard the local SOURCE path BEFORE isGitWorkTree runs git -C on it, so a
		// local repo/clone path under ~/.ssh is rejected without any read there.
		if err := rejectIfUnderSSH("clone source", source); err != nil {
			return "", err
		}
		if abs, err := filepath.Abs(source); err == nil {
			if isGitWorkTree(abs) {
				fmt.Fprintf(out, "using existing config repo at %s\n", abs)
				return abs, nil
			}
		}
	}

	// Clone into ferry's own neutral space (~/.config/ferry/repo), not a personal
	// $HOME folder taxonomy.
	dest, err := defaultRepoDir()
	if err != nil {
		return "", err
	}

	// Harden the config dir chain (mirrors HardenStoreDir on the rest of ~/.config/ferry):
	// a symlinked ~/.config component must refuse before any MkdirAll/git clone writes
	// a tree through it.
	if err := hardenConfigDirForRepo(dest); err != nil {
		return "", err
	}

	// Guard the clone DESTINATION BEFORE freeCloneDest (which ReadDirs dest and
	// its siblings): a symlinked destination under ~/.ssh must be refused before
	// any ReadDir reads through it. Even with an https:// source, git clone must
	// never write a tree under ~/.ssh.
	if _, err := guardRepoPath("clone destination", dest); err != nil {
		return "", err
	}

	// If the default destination is already a non-empty directory, fall back to a
	// sibling so we never clobber existing content.
	dest = freeCloneDest(dest)

	// Re-guard the final chosen path: freeCloneDest may pick a "-N" sibling, so
	// the symlink-aware check must clear the exact path git clone will write to.
	if _, err := guardRepoPath("clone destination", dest); err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("prepare clone destination: %w", err)
	}

	fmt.Fprintf(out, "cloning %s -> %s\n", source, dest)
	// git clone handles https://, file:// and bare local paths uniformly. The
	// resulting working tree (NOT bare) is what we record. We deliberately do not
	// touch ~/.ssh: an HTTPS/file clone of a public repo needs no SSH material.
	cmd := exec.Command("git", "clone", source, dest)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("could not clone the config repo from %s: %w — check the URL is correct and reachable over HTTPS (ferry does not use SSH)", source, err)
	}
	return dest, nil
}

// initFresh creates a NEW config repo: git init a working tree, seed a minimal
// shared manifest (ferry.toml) so scope exists, ignore the per-machine .local
// layer, and make an initial commit so HEAD is attached and the tree is clean.
// The result is a real committable working clone "capture this machine" writes into.
func initFresh(out io.Writer, freshDir string) (string, error) {
	// Default fresh-repo location is ferry-owned and neutral (~/.config/ferry/repo);
	// an explicit `ferry init --fresh <dir>` override places it elsewhere.
	var base string
	if freshDir != "" {
		expanded, err := expandUser(freshDir)
		if err != nil {
			return "", err
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			return "", fmt.Errorf("resolve fresh repo dir %q: %w", freshDir, err)
		}
		base = abs
	} else {
		d, err := defaultRepoDir()
		if err != nil {
			return "", err
		}
		base = d
		// The default lives under ~/.config/ferry: harden the config-dir chain
		// (mirrors HardenStoreDir) so a symlinked ~/.config component refuses before
		// any MkdirAll/git init writes a repo tree through it.
		if err := hardenConfigDirForRepo(base); err != nil {
			return "", err
		}
	}
	// Guard the destination BEFORE freeCloneDest (which ReadDirs it) and before
	// MkdirAll/git init: a symlinked destination -> ~/.ssh must be refused before
	// ferry reads or writes a repo tree under ~/.ssh.
	if _, err := guardRepoPath("fresh repo destination", base); err != nil {
		return "", err
	}
	dest := freeCloneDest(base)
	// Re-guard the final chosen path: freeCloneDest may pick a "-N" sibling, and
	// the symlink-aware check must clear the exact path ferry will write.
	if _, err := guardRepoPath("fresh repo destination", dest); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("create repo dir: %w", err)
	}

	fmt.Fprintf(out, "initialising a new config repo at %s\n", dest)
	if out2, err := runGitIn(dest, "init"); err != nil {
		return "", fmt.Errorf("git init %s failed: %w\n%s", dest, err, out2)
	}

	// ADOPT the existing ~/.zshrc (the confirmed-incident fix). Read the user's
	// real shell rc BEFORE writing the manifest, so the Fresh flow's scope tracks
	// what actually got seeded:
	//   - ~/.zshrc exists with content  -> seed dotfiles/zshrc with THOSE bytes and
	//     declare .zshrc in scope. A subsequent apply then sees repo == live (a
	//     StateClean adoption) and is a NO-OP — never a wipe.
	//   - ~/.zshrc absent (or empty)    -> seed NO deployable source and DO NOT
	//     declare .zshrc. There is no managed source, so a later apply can never
	//     deploy an empty file over a real ~/.zshrc the user creates afterward.
	// This never seeds a deployable EMPTY source (the empty-seed data-loss bug).
	adoptedZshrc, haveZshrc := readExistingZshrc()

	// Minimal shared manifest so a scope exists from the first capture. .zshrc is
	// the common starting target, so the Fresh "capture this machine" flow has
	// something in scope to route into the repo; the user edits ferry.toml to
	// add/remove more. Declaring it in scope is SAFE even with no repo source: a
	// declared dotfile with no source on disk is simply skipped by apply (nothing
	// to materialise), so it never deploys an empty file over a real one.
	manifest := "[manage]\ndotfiles = [\".zshrc\"]\n"
	if err := os.WriteFile(filepath.Join(dest, config.SharedManifestName), []byte(manifest), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", config.SharedManifestName, err)
	}

	// Seed the repo source ONLY when we adopted real content from an existing
	// ~/.zshrc: the Fresh flow then has a managed target that already matches the
	// live file (apply is a no-op), and `ferry capture` has a base to diff future
	// edits against. With NO existing ~/.zshrc we deliberately seed NOTHING
	// deployable — never an empty source that a later apply would zero a real file
	// with (the confirmed data-loss bug). The dotfile stays declared in scope; apply
	// skips it until a source exists, and capture is the path that first fills it.
	if haveZshrc {
		if err := os.MkdirAll(filepath.Join(dest, dotfile.RepoSubdir), 0o755); err != nil {
			return "", fmt.Errorf("create dotfiles dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dest, dotfile.RepoSubdir, "zshrc"), adoptedZshrc, 0o644); err != nil {
			return "", fmt.Errorf("seed dotfile source: %w", err)
		}
	}

	// Ignore the per-machine .local layer (ferry.local.toml + local/) so capture's
	// local-route writes never get committed. (Shared with the existing-repo path.)
	if err := ensureLocalLayerIgnored(dest); err != nil {
		return "", err
	}

	// Initial commit so the working tree starts clean and HEAD is attached. Use a
	// deterministic, non-PII identity scoped to THIS repo only (never global).
	for _, args := range [][]string{
		{"add", "-A"},
		{"-c", "user.name=ferry", "-c", "user.email=ferry@localhost", "commit", "-m", "ferry init: scaffold config repo"},
	} {
		if cout, err := runGitIn(dest, args...); err != nil {
			return "", fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, cout)
		}
	}
	return dest, nil
}

// readExistingZshrc reads the user's real ~/.zshrc for the Fresh-init adoption,
// returning its bytes and ok=true ONLY when it is present AND carries real
// content. It returns ok=false (adopt nothing) when the file is absent, empty,
// unreadable, or unsafe to read:
//   - A regular ~/.zshrc is read directly.
//   - A SYMLINKED ~/.zshrc is refused outright (ok=false) — never resolved. Its
//     target could point into ~/.ssh, and resolving it (EvalSymlinks / Stat) would
//     traverse that target, violating ferry's absolute "never touch ~/.ssh"
//     contract. Declining to adopt leaves the real file untouched, which is safe.
//   - A directory / device / other non-regular kind is refused.
//
// An absent or empty rc is the "no deployable seed" path; a substantial rc is the
// content the repo adopts so the first apply is a no-op, not a wipe.
func readExistingZshrc() ([]byte, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false
	}
	path := filepath.Join(home, ".zshrc")

	li, err := os.Lstat(path)
	if err != nil {
		return nil, false // absent (or unreadable) -> adopt nothing.
	}
	if li.Mode()&os.ModeSymlink != 0 {
		// A symlinked ~/.zshrc: adopt NOTHING. We must never resolve an untrusted
		// home symlink, because its target could point INTO ~/.ssh and any
		// resolution (EvalSymlinks / Stat-through-symlink) would stat/traverse that
		// target — violating ferry's absolute "never touch ~/.ssh" contract.
		// Adoption is a best-effort convenience; declining here leaves the user's
		// real file 100% untouched (init seeds no source; the defect-B empty-over
		// guard still prevents apply from deploying an empty over it), so refusing
		// is strictly safe. We deliberately do not os.Readlink either — there is no
		// need to read the target at all.
		return nil, false
	} else if !li.Mode().IsRegular() {
		return nil, false // a directory/device/etc. is not adoptable content.
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	if dotfile.IsNearEmpty(data) {
		// Empty, whitespace-only, or comments-only -> nothing worth managing. Uses
		// the SAME near-empty definition as the apply data-loss guard so init-adopt
		// and the guard agree on what counts as substantial content.
		return nil, false
	}
	return data, true
}

// checkCloneSource enforces ferry's clone contract on a source argument. It
// ACCEPTS an https:// URL, a file:// URL, or a bare local filesystem path (the
// offline/fresh fast-path). It REJECTS everything else with a clear, actionable
// error: any ssh:// URL, an scp-style "[user@]host:path" remote (the
// git@github.com:owner/repo.git shorthand), git:// (insecure), http:// (not
// HTTPS), and any other non-https/non-file scheme. All scheme comparisons are
// case-insensitive (SSH://, Git://, HTTP:// are caught too).
//
// The git remote contract: init clones over HTTPS only (SSH would read ~/.ssh;
// git:// is insecure; http:// is not HTTPS) — so git never receives an
// out-of-scope remote from init.
func checkCloneSource(source string) error {
	reject := func() error {
		return fmt.Errorf("ferry clones over HTTPS only; SSH/git remotes are out of scope — use an https:// URL (got %q)", source)
	}

	// Explicit "scheme://..." URL: accept https/file, reject everything else
	// (ssh, git, http, ...). Scheme compared lowercased so case variants are caught.
	if scheme, ok := urlScheme(source); ok {
		switch scheme {
		case "https":
			return nil
		case "file":
			// A file:// source is a local path on disk. PARSE the URL properly so
			// every valid form maps to its real filesystem path before the guard:
			// file:///p, file://localhost/p and file://host/p. A naive
			// TrimPrefix("file://") would leave "localhost/..." or "host/..." as
			// the "path" and let file://localhost/$HOME/.ssh/repo slip past the
			// guard. Reject a non-local host outright (ferry has no remote-file
			// fetch), then guard the real local path so any file:// under ~/.ssh is
			// caught.
			local, err := fileURLLocalPath(source)
			if err != nil {
				return err
			}
			return rejectIfUnderSSH("clone source", local)
		default:
			return reject()
		}
	}

	// No scheme: either an scp-style remote ([user@]host:path) or a local path.
	if isSCPStyleRemote(source) {
		return reject()
	}
	// A bare local filesystem path (absolute, relative, or plain name) is the
	// accepted offline/fresh source — reject one resolving under ~/.ssh.
	return rejectIfUnderSSH("clone source", source)
}

// fileURLLocalPath parses a file:// source and returns the real LOCAL filesystem
// path it denotes. It accepts the three local forms — file:///p (empty host),
// file://localhost/p, and a bare file://host/p where host is empty/localhost —
// and REJECTS a non-local host (e.g. file://example.com/p): ferry has no
// remote-file fetch, so a non-local file:// host is out of scope. Parsing via
// net/url (not string trimming) is what makes file://localhost/$HOME/.ssh/x
// resolve to its real $HOME/.ssh/x path so the ~/.ssh guard catches it.
func fileURLLocalPath(source string) (string, error) {
	u, err := url.Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse file:// source %q: %w", source, err)
	}
	if h := u.Host; h != "" && !strings.EqualFold(h, "localhost") {
		return "", fmt.Errorf("ferry only clones LOCAL file:// sources; %q has a non-local host %q", source, u.Host)
	}
	// u.Path is the decoded filesystem path for a local file:// URL.
	return u.Path, nil
}

// isSCPStyleRemote reports whether a scheme-less source is the scp-style git
// remote shorthand "[user@]host:path" (e.g. git@github.com:owner/repo.git),
// which makes git read ~/.ssh. Detection: a ':' that comes BEFORE any '/'. A
// path with a '/' ahead of the ':' ("./host:thing", "a/b:c"), a Windows drive
// letter ("C:\..." / "C:/..."), and a colon-less path are NOT matched. Callers
// pass only scheme-less input (urlScheme already peeled real URLs off).
func isSCPStyleRemote(source string) bool {
	colon := strings.Index(source, ":")
	if colon <= 0 {
		return false
	}
	// A '/' before the ':' means it is a path, not an scp host:path remote.
	if slash := strings.Index(source, "/"); slash >= 0 && slash < colon {
		return false
	}
	// Exclude a Windows drive letter ("C:\..." / "C:/..."): single-char host
	// before ':' is a drive, not a remote host.
	if colon == 1 {
		return false
	}
	return true
}

// urlScheme returns the lowercased "scheme" of a "scheme://..." source and true
// when source has a valid URL-scheme prefix; ("", false) for a scheme-less path.
// The scheme is lowercased so callers compare case-insensitively.
func urlScheme(source string) (string, bool) {
	i := strings.Index(source, "://")
	if i <= 0 {
		return "", false
	}
	for _, r := range source[:i] {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '+' || r == '-' || r == '.') {
			return "", false
		}
	}
	return strings.ToLower(source[:i]), true
}

// hasURLScheme reports whether source looks like a URL (has a "scheme://" prefix)
// rather than a bare local path. Used to decide clone-vs-wire for the existing path.
func hasURLScheme(source string) bool {
	_, ok := urlScheme(source)
	return ok
}

// isGitWorkTree reports whether dir is the root of a git working tree (so it can be
// wired directly rather than re-cloned).
func isGitWorkTree(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// existingConfiguredRepo returns the repo path from an already-present config.toml
// when it points at a usable git working tree. This lets a re-run of `ferry init`
// (or an init against a pre-seeded config) reuse the configured repo instead of
// creating a new fresh one and clobbering the existing setup.
func existingConfiguredRepo() (string, bool) {
	mc, err := config.LoadMachineConfig()
	if err != nil {
		// A hostname-less but repo-bearing config.toml still counts: re-read the
		// repo key directly (mirrors loadContext's tolerant loader).
		path, perr := paths.ConfigFile()
		if perr != nil {
			return "", false
		}
		// Symlink-harden ~/.config/ferry BEFORE the raw toml.DecodeFile fallback so a
		// poisoned config dir (e.g. one symlinked into ~/.ssh) cannot be read through.
		// The strict loader above already hardens; mirror it on the tolerant path.
		cfgDir, derr := paths.ConfigDir()
		if derr != nil {
			return "", false
		}
		if herr := paths.HardenStoreDir(cfgDir); herr != nil {
			return "", false
		}
		var raw config.MachineConfig
		if _, derr := toml.DecodeFile(path, &raw); derr != nil || raw.Repo == "" {
			return "", false
		}
		mc = raw
	}
	// A configured repo path that exists as a directory is ferry's own recorded
	// clone — reuse it (it may be a fresh git tree or a not-yet-committed seed). A
	// recorded path that no longer exists falls through to fresh/clone.
	if mc.Repo == "" {
		return "", false
	}
	// Guard the configured repo path BEFORE os.Stat touches it: a config.toml repo
	// under ~/.ssh must be rejected without any FS op on that path. If the guard
	// trips (or errors), do not treat it as an existing repo — runInit then routes
	// to the fresh path, whose own destination guard refuses an ~/.ssh target with
	// a clear error. The symlink-aware guard never touches ~/.ssh.
	if under, err := pathUnderSSH(mc.Repo); err != nil || under {
		return "", false
	}
	if fi, err := os.Stat(mc.Repo); err != nil || !fi.IsDir() {
		return "", false
	}
	return mc.Repo, true
}

// ensureLocalLayerIgnored makes sure the repo's .gitignore excludes the per-machine
// .local layer (ferry.local.toml and local/). It is idempotent: existing entries
// are kept and only the missing ones are appended, so it never disturbs a repo that
// already ignores them.
func ensureLocalLayerIgnored(repo string) error {
	gitignore := filepath.Join(repo, ".gitignore")
	// Guard the FULL .gitignore path BEFORE any read or write: a repo
	// .gitignore -> ~/.ssh/config symlink would otherwise be os.ReadFile'd (follows
	// symlinks, reading ~/.ssh) and then os.WriteFile'd THROUGH (overwriting
	// ~/.ssh). safeRepoPath refuses a symlinked/escaping path; on refusal we never
	// read or write through it. (Called from init AND capture's local routes.)
	if _, err := safeRepoPath(repo, gitignore); err != nil {
		return fmt.Errorf("refusing to manage .gitignore: %w", err)
	}
	existing, err := os.ReadFile(gitignore)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(line)] = true
	}

	var add []string
	for _, want := range []string{config.LocalManifestName, "local/"} {
		if !have[want] {
			add = append(add, want)
		}
	}
	if len(add) == 0 {
		return nil
	}

	body := string(existing)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += strings.Join(add, "\n") + "\n"
	if err := os.WriteFile(gitignore, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	return nil
}

// localManifestTemplate is the minimal, explicit, fully-commented per-machine
// override scaffold written when ferry.local.toml is absent. It parses as valid
// TOML with an empty effect (everything is commented), so it changes nothing on
// its own — its job is simply to EXIST as the explicit per-machine override
// surface. Effective scope = ferry.toml overlaid with ferry.local.toml.
const localManifestTemplate = `# ferry.local.toml — per-machine scope overrides for THIS machine only.
#
# This file is gitignored: it stays in the repo working tree (next to ferry.toml)
# but is never committed, so each machine keeps its own overrides.
#
# Effective scope = ferry.toml (shared baseline) overlaid with this file
# (local wins). With everything below commented out, this machine uses the
# shared ferry.toml scope unchanged.
#
# Uncomment and edit [manage] to override the shared scope on this machine, e.g.:
#
# [manage]
# iterm2   = false          # headless box: skip terminal-app config here
# brew     = true           # opt this machine into Homebrew management
# dotfiles = [".zshrc", ".gitconfig"]   # per-machine dotfile set (replaces the shared list)
`

// ensureLocalManifest creates or confirms the per-machine manifest
// (ferry.local.toml) in the repo BEFORE init triggers any apply, so first-run
// scope is explicit. If the file is absent it writes a minimal, commented
// template (valid TOML, empty effect — the override surface simply EXISTS). If
// it already exists it is CONFIRMED and left untouched (never overwritten). The
// file lives in the working tree but stays gitignored (ensureLocalLayerIgnored).
// This is a plain file write plus a presence check, so it is non-interactive and
// never blocks on a non-tty.
func ensureLocalManifest(out io.Writer, repo string) error {
	path := filepath.Join(repo, config.LocalManifestName)
	// Guard the FULL ferry.local.toml path BEFORE the os.Stat presence check (which
	// follows symlinks) and the os.WriteFile: a repo ferry.local.toml -> ~/.ssh/config
	// symlink must never be stat'd into ~/.ssh nor written through. safeRepoPath
	// refuses a symlinked/escaping path; on refusal we touch nothing.
	if _, err := safeRepoPath(repo, path); err != nil {
		return fmt.Errorf("refusing to manage %s: %w", config.LocalManifestName, err)
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(out, "per-machine manifest present: %s (left as-is)\n", path)
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", config.LocalManifestName, err)
	}

	if err := os.WriteFile(path, []byte(localManifestTemplate), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", config.LocalManifestName, err)
	}
	fmt.Fprintf(out, "created per-machine manifest: %s (commented template; gitignored)\n", path)
	return nil
}

// runGitIn runs a git command rooted at dir and returns combined output.
func runGitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// freeCloneDest returns dest if it is absent or an empty dir; otherwise it appends
// a numeric suffix until it finds a free path, so init never clobbers existing
// content at the default location.
func freeCloneDest(dest string) string {
	if dirEmptyOrAbsent(dest) {
		return dest
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", dest, i)
		if dirEmptyOrAbsent(cand) {
			return cand
		}
	}
}

// dirEmptyOrAbsent reports whether path does not exist or is an empty directory.
func dirEmptyOrAbsent(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return os.IsNotExist(err)
	}
	return len(entries) == 0
}

// hardenConfigDirForRepo runs the symlink-hardening walk over a repo path that lives
// under ferry's config dir (~/.config/ferry/repo). It mirrors the HardenStoreDir
// guard the rest of ~/.config/ferry goes through: any symlink component from $HOME
// down to the path (e.g. a symlinked ~/.config) is refused before any MkdirAll /
// git init / git clone writes a tree through it. Lexical, creates nothing, never
// touches ~/.ssh. A path NOT under $HOME (e.g. an explicit override in a test temp
// dir) has no $HOME-anchored chain and is accepted unchanged.
func hardenConfigDirForRepo(repo string) error {
	return paths.HardenStoreDir(repo)
}

// expandUser expands a leading ~ or ~/ in a path to the user's home directory, so
// `ferry init --fresh ~/somewhere` works when the shell has not already expanded it.
// A bare "~" maps to $HOME; anything else is returned unchanged.
func expandUser(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// finishWithApply ends init by surfacing what apply would do. By default it shows
// the plan and stops (non-mutating, safe on a non-tty). With --apply it runs apply,
// confirming first unless --yes (or non-interactive, in which case it declines and
// only prints the plan rather than mutating without consent).
func finishWithApply(c *cobra.Command, in *bufio.Reader, out io.Writer) error {
	applyFlag, _ := c.Flags().GetBool("apply")
	yes, _ := c.Flags().GetBool("yes")

	ctx, err := loadContext()
	if err != nil {
		return err
	}
	plan, warnings, err := buildPlan(ctx)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(out, w)
	}
	printPlan(out, plan)

	if !applyFlag {
		fmt.Fprintln(out, "init complete. Run `ferry apply` to reconcile this machine (or `ferry init --apply --yes`).")
		return nil
	}

	proceed := yes
	if !proceed {
		if stdinIsTerminal() {
			fmt.Fprint(out, "Apply this plan now? [y/N]: ")
			proceed = readYesNo(in, false)
		}
	}
	if !proceed {
		fmt.Fprintln(out, "not applying (run `ferry apply` when ready).")
		return nil
	}

	if err := applyPlan(ctx, plan, false, out); err != nil {
		return err
	}
	return nil
}

// readYesNo reads one line and interprets it as a yes/no answer. EOF / empty input
// returns def (the safe default), so an empty-stdin / non-tty caller never blocks.
func readYesNo(in *bufio.Reader, def bool) bool {
	line, err := in.ReadString('\n')
	ans := strings.ToLower(strings.TrimSpace(line))
	if ans == "" {
		// Either EOF with no data, or a blank line: fall back to the default.
		if err != nil {
			return def
		}
		return def
	}
	return ans == "y" || ans == "yes"
}
