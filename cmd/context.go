package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/REPPL/ferry/internal/backup"
	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/paths"
)

// maxSymlinkHops bounds symlink resolution in the lexical walk so a cyclic or
// pathological symlink chain (a -> b -> a) cannot loop forever. On exceeding it
// the guards fail closed (refuse).
const maxSymlinkHops = 40

// pathUnderSSH reports whether the (possibly relative, ~-prefixed, or symlinked)
// path p resolves to ~/.ssh itself or any descendant of it. It is the single
// shared guard enforcing ferry's absolute contract: ferry never reads, writes,
// or otherwise operates on anything under ~/.ssh. Both init (clone source +
// destination) and loadContext (the configured repo path) call it.
//
// CRITICAL: the guard itself must NEVER touch ~/.ssh — no stat, lstat, readlink,
// EvalSymlinks, open, or enumeration AT ~/.ssh or BELOW it. Otherwise the guard
// would violate the very contract it protects. So it works by PURE PATH
// arithmetic plus LEXICAL symlink resolution of ONLY the candidate's components
// that are strictly ABOVE ~/.ssh:
//
//   - homeSSH is computed by STRING join (filepath.Join(home, ".ssh")); it is
//     never stat'd, lstat'd, readlink'd, or EvalSymlink'd.
//   - The candidate is cleaned + absolutised (a leading ~ expanded), then walked
//     component-by-component from the filesystem root downward. Each component is
//     Lstat'd (which does NOT follow the final component); a SYMLINK component is
//     read with os.Readlink (which does NOT follow — it reads only the link text)
//     and its target resolved LEXICALLY (filepath.Join + Clean), never with
//     EvalSymlinks. We NEVER Lstat/Readlink a component that is already AT or
//     UNDER homeSSH — the moment lexical math lands a component at/under homeSSH
//     we conclude "under ssh" by STRING compare alone, without touching it.
//   - The final comparison is a clean string prefix (via filepath.Rel),
//     equality with homeSSH or a descendant => under ssh.
//
// This catches: relative paths, "."/".." segments, a symlinked ancestor (e.g.
// ~/.config -> ~/.ssh — Readlink reads the link text, lexical resolution
// shows it lands under ~/.ssh, detected by string compare WITHOUT stating
// ~/.ssh), and a direct ~/.ssh/x candidate (detected by string compare BEFORE
// any stat under ssh). A path that cannot be resolved (e.g. UserHomeDir fails)
// returns the error so callers fail closed.
func pathUnderSSH(p string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	// Canonicalise the base by EvalSymlinks of HOME — HOME is a TRUSTED ancestor
	// strictly ABOVE ~/.ssh (HOME is never under its own .ssh), so resolving it
	// never touches ~/.ssh. This is required for a correct STRING compare: the
	// lexical walk (below) resolves symlink targets that may already be in
	// resolved form (e.g. on macOS /var is a symlink to /private/var), so homeSSH
	// must be built on the same resolved HOME or a real poisoned symlink into
	// ~/.ssh could slip past the string match. homeSSH is then a pure STRING join
	// — ~/.ssh itself is never stat'd, readlink'd, or EvalSymlink'd.
	if resolvedHome, herr := filepath.EvalSymlinks(home); herr == nil {
		home = resolvedHome
	}
	homeSSH := filepath.Clean(filepath.Join(home, ".ssh"))

	resolved, stopped, err := resolveForSSHCheck(p, home, homeSSH)
	if err != nil {
		return false, err
	}
	// resolveForSSHCheck signals an early hit when a component resolved (lexically)
	// to homeSSH or a descendant of it; in that case it already concluded "under
	// ssh" by string compare without touching homeSSH.
	if stopped {
		return true, nil
	}
	return underOrEqual(homeSSH, resolved), nil
}

// underOrEqual reports whether path equals base or is a descendant of it, using
// pure path arithmetic (filepath.Rel). Both arguments must be clean+absolute.
func underOrEqual(base, path string) bool {
	if path == base {
		return true
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	// rel == "." means equal (handled above); a rel that starts with ".." (or is
	// exactly "..") escapes base, so path is NOT under base.
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// resolveForSSHCheck turns p into a cleaned, absolute path with symlinks resolved
// LEXICALLY (os.Readlink + string math) as far as the path exists on disk — but
// it NEVER stats, lstats, readlinks, or EvalSymlinks homeSSH or anything under
// it. A leading "~" is expanded to home.
//
// It walks the candidate component-by-component from the filesystem root
// downward, maintaining the invariant that the prefix walked so far is known NOT
// to be at/under homeSSH (so Lstat/Readlink on the NEXT component is safe). When
// a component is a symlink it reads the link text with os.Readlink (no follow),
// resolves the target LEXICALLY (relative targets joined onto the link's dir,
// then Clean), and restarts the walk on the rewritten path. The moment any
// lexical resolution lands a component at/under homeSSH it returns stopped=true
// so the caller concludes "under ssh" WITHOUT touching the filesystem at/below
// homeSSH. A not-yet-existing clone DESTINATION still resolves to a stable
// absolute path for the prefix comparison. Symlink resolution is bounded by
// maxSymlinkHops to defeat cycles; on exceeding it the function fails closed.
//
// Returns (resolvedPath, stopped, err). When stopped is true, resolvedPath is
// unspecified and the caller treats the result as "under ssh".
func resolveForSSHCheck(p, home, homeSSH string) (string, bool, error) {
	p = strings.TrimSpace(p)
	if p == "~" {
		p = home
	} else if strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, p[2:])
	}

	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false, err
	}
	abs = filepath.Clean(abs)

	// Pure-path short-circuit: if the cleaned candidate is already at/under
	// homeSSH, conclude "under ssh" WITHOUT any stat (this covers a direct
	// ~/.ssh/x candidate, caught before we would ever touch under ssh).
	if underOrEqual(homeSSH, abs) {
		return "", true, nil
	}

	// Walk abs component-by-component from the root. `resolved` holds the
	// already-walked, symlink-resolved prefix (a clean absolute path known NOT to
	// be under homeSSH); `rest` holds the components still to process. When a
	// component is a symlink we rewrite `rest` lexically from its Readlink target
	// and continue — never EvalSymlinks, never stat under homeSSH.
	resolved := string(os.PathSeparator)
	rest := strings.Split(strings.TrimPrefix(abs, string(os.PathSeparator)), string(os.PathSeparator))
	hops := 0
	for len(rest) > 0 {
		seg := rest[0]
		rest = rest[1:]
		if seg == "" || seg == "." {
			continue
		}
		cur := filepath.Join(resolved, seg)
		// Never Lstat/Readlink homeSSH or anything under it. If the next component
		// is already at/under homeSSH (by pure string math), the candidate is under
		// ssh — decide WITHOUT touching the filesystem there.
		if underOrEqual(homeSSH, cur) {
			return "", true, nil
		}
		fi, lerr := os.Lstat(cur)
		if lerr != nil {
			// A not-yet-existing tail (e.g. a fresh clone destination): no symlink to
			// resolve. Append the remaining components lexically and stop.
			full := filepath.Clean(filepath.Join(append([]string{cur}, rest...)...))
			if underOrEqual(homeSSH, full) {
				return "", true, nil
			}
			return full, false, nil
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			resolved = cur
			continue
		}
		// A symlink component: read its target TEXT (no follow) and resolve it
		// LEXICALLY. A relative target is anchored at the link's directory.
		hops++
		if hops > maxSymlinkHops {
			return "", false, fmt.Errorf("too many symlink levels resolving %q", abs)
		}
		target, rerr := os.Readlink(cur)
		if rerr != nil {
			return "", false, rerr
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(resolved, target)
		}
		target = filepath.Clean(target)
		if underOrEqual(homeSSH, target) {
			return "", true, nil
		}
		// Restart the walk from the lexically-resolved target, prepending it to the
		// not-yet-processed components.
		resolved = string(os.PathSeparator)
		rest = append(strings.Split(strings.TrimPrefix(target, string(os.PathSeparator)), string(os.PathSeparator)), rest...)
	}
	return filepath.Clean(resolved), false, nil
}

// rejectIfUnderSSH returns an error when path resolves to ~/.ssh or below. It is
// the enforcement wrapper around pathUnderSSH used by init and loadContext.
func rejectIfUnderSSH(label, path string) error {
	under, err := pathUnderSSH(path)
	if err != nil {
		return fmt.Errorf("validate %s %q against ~/.ssh: %w", label, path, err)
	}
	if under {
		return fmt.Errorf("ferry never operates under ~/.ssh (refusing %s %q)", label, path)
	}
	return nil
}

// guardRepoPath is the single chokepoint every repo-path entry point routes
// through: it runs the symlink-aware ~/.ssh guard on path BEFORE the FIRST
// filesystem operation touching it, then returns the (unchanged) path so callers
// can use it inline as `p, err := guardRepoPath("label", p)`. Routing every
// derive-and-use site through this one function guarantees no repo path — a
// configured repo, a fresh-init destination, or a clone source/destination — is
// ever read or written before the guard has cleared it. The guard itself is
// symlink-aware (it resolves each component LEXICALLY via os.Readlink, never
// EvalSymlinks) and never reads, stats, or enumerates ~/.ssh, so a symlinked
// ancestor (e.g. ~/.config -> ~/.ssh) is caught — by string compare on
// the link's lexically-resolved target — without the guard ever touching ~/.ssh.
func guardRepoPath(label, path string) (string, error) {
	if err := rejectIfUnderSSH(label, path); err != nil {
		return "", err
	}
	return path, nil
}

// safeRepoPath validates a REPO-SIDE managed source or destination (e.g.
// dotfiles/<name>, local/<domain>/<file>, iterm2/<id>.plist) BEFORE it is read or
// written, and returns the cleaned candidate when it is safe. The repo PATH itself
// is already guarded against being under ~/.ssh (guardRepoPath / loadContext); the
// NEW exposure this closes is a SYMLINK inside the repo (or a symlinked parent
// component) that points OUT to ~/.ssh or a system location — apply/diff/status
// would otherwise os.Stat (which follows symlinks) then os.ReadFile through it and
// READ ~/.ssh, and a shared capture would writeRepoFile THROUGH it and OVERWRITE
// ~/.ssh.
//
// Ferry materialises managed content by COPY, never by symlink, so a symlink in
// the managed repo tree is illegitimate. The rule is therefore strict: the
// candidate must be reachable from repoRoot WITHOUT traversing a symlink that
// leaves the repo, and the candidate itself must not be a symlink. Concretely we
// Lstat each path component from repoRoot down to the candidate; if a component is
// a symlink we read its target TEXT with os.Readlink (NO follow) and resolve it
// LEXICALLY (relative target joined onto the link's dir, then Clean) — never
// EvalSymlinks, which would stat the whole chain and could descend INTO ~/.ssh.
// The resolved target must stay strictly under repoRoot. Any escape — to ~/.ssh,
// to $HOME elsewhere, or to a system/admin location (/etc, /usr, /opt, ...) — is
// REFUSED with a clear error and the path is never opened.
//
// The check NEVER reads, stats, lstats, readlinks, or enumerates ~/.ssh: it
// Lstats only repo-side components (all strictly under repoRoot, which is not
// under ~/.ssh), and when a repo symlink resolves to a target it compares that
// LEXICALLY-resolved target against ~/.ssh by PURE STRING arithmetic via
// pathUnderSSH (belt-and-suspenders), without opening it.
func safeRepoPath(repoRoot, candidate string) (string, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", err
	}
	root = filepath.Clean(root)
	// resolvedRoot canonicalises the repo root through symlinks. The repo root is a
	// TRUSTED, legitimate clone path already cleared of ~/.ssh (guardRepoPath /
	// loadContext), so EvalSymlinks on IT is safe — it can never descend into
	// ~/.ssh. We need it so a LEXICALLY-resolved component target compares on the
	// same real filesystem (e.g. macOS /var -> /private/var). The plain root is kept
	// for the candidate's path arithmetic (the candidate was built by joining onto
	// the plain root, so containment must compare against it).
	resolvedRoot := root
	if r, rerr := filepath.EvalSymlinks(root); rerr == nil {
		resolvedRoot = r
	}

	cand, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	cand = filepath.Clean(cand)

	// The candidate must live under the repo root by pure path arithmetic before we
	// touch the filesystem.
	rel, err := filepath.Rel(root, cand)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing managed repo path %q: escapes repo root %q", candidate, repoRoot)
	}
	if rel == "." {
		return root, nil
	}

	// Walk each component from repoRoot down to the candidate. Lstat (does NOT follow
	// symlinks) each one; a symlink component is resolved and required to stay
	// strictly under repoRoot, never under ~/.ssh or a system location.
	cur := root
	for _, seg := range strings.Split(rel, string(os.PathSeparator)) {
		cur = filepath.Join(cur, seg)
		fi, lerr := os.Lstat(cur)
		if lerr != nil {
			// A not-yet-existing tail (e.g. a fresh dotfiles/<name> a capture creates)
			// is fine — there is no symlink to traverse. Stop walking.
			if os.IsNotExist(lerr) {
				break
			}
			return "", lerr
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		// A symlink component: read its target TEXT with os.Readlink (NO follow) and
		// resolve it LEXICALLY — never EvalSymlinks, which would stat the whole chain
		// and could descend INTO ~/.ssh while merely classifying the link.
		target, rerr := os.Readlink(cur)
		if rerr != nil {
			return "", fmt.Errorf("refusing managed repo path %q: symlink %q does not resolve: %w", candidate, cur, rerr)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(cur), target)
		}
		resolved := filepath.Clean(target)
		// Belt-and-suspenders: never under ~/.ssh (string compare only; never opens it).
		if under, uerr := pathUnderSSH(resolved); uerr != nil {
			return "", uerr
		} else if under {
			return "", fmt.Errorf("refusing managed repo path %q: symlink resolves under ~/.ssh", candidate)
		}
		r, rerr2 := filepath.Rel(resolvedRoot, resolved)
		if rerr2 != nil || r == ".." || strings.HasPrefix(r, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("refusing managed repo path %q: symlink escapes repo (resolves to %q)", candidate, resolved)
		}
		// Reject a symlink even if it stays under the repo: managed content is COPIED,
		// never symlinked, so a symlink in the managed tree is illegitimate.
		return "", fmt.Errorf("refusing managed repo path %q: symlink not allowed in managed repo tree", candidate)
	}

	return cand, nil
}

// cmdContext bundles the per-machine state every reconciling command needs: the
// machine config, the effective Scope (shared (+) local), the resolved repo
// clone path, and (lazily) a transactional backup Engine. Wave-2 commands
// (apply, diff, and later status/capture/restore) call loadContext() instead of
// re-wiring config + scope + engine each.
//
// The backup Engine is NOT constructed by loadContext: building it mkdirs+chmods
// ~/.local/state/ferry, which read-only commands (diff, status, apply --dry-run)
// must not do — they need to be write-free. Mutating callers obtain the engine
// via the Engine() accessor, which builds it on first use. Read-only callers
// simply never call Engine(), so loading a context creates no ferry state.
type cmdContext struct {
	MachineConfig config.MachineConfig
	Scope         config.Scope
	RepoPath      string

	engine *backup.Engine // lazily built by Engine(); nil until first mutating use
}

// Engine returns the transactional backup Engine, constructing it (and thus
// creating ~/.local/state/ferry) on first call. Only mutating commands (apply
// without --dry-run, capture, restore, init --apply) call this; read-only
// commands (diff, status, apply --dry-run) never do, so they stay write-free.
//
// The engine is cached on the context, so repeated calls within one command
// reuse the same engine (and the state dir is created at most once).
func (c *cmdContext) Engine() (*backup.Engine, error) {
	if c.engine != nil {
		return c.engine, nil
	}
	engine, err := backup.New()
	if err != nil {
		return nil, fmt.Errorf("init backup engine: %w", err)
	}
	c.engine = engine
	return engine, nil
}

// loadContext loads the machine config, resolves the repo clone path, and loads
// the merged scope from that repo. It deliberately does NOT construct the backup
// Engine (which would create ferry's state dir); callers that mutate obtain it
// lazily via cmdContext.Engine(). A missing config.toml is reported as a clear
// first-run error.
func loadContext() (*cmdContext, error) {
	mc, repo, err := loadMachineConfigTolerant()
	if err != nil {
		return nil, err
	}

	// Defense-in-depth: a config.toml whose repo path points under ~/.ssh would
	// make every reconciling command (apply/capture/status/diff/restore) read
	// under ~/.ssh via LoadScope. Refuse BEFORE any read on that path.
	if err := rejectIfUnderSSH("configured repo path", repo); err != nil {
		return nil, err
	}

	scope, err := config.LoadScope(repo)
	if err != nil {
		return nil, fmt.Errorf("could not read the config repo at %s: %w — check the repo exists and is intact, or re-run `ferry init`", repo, err)
	}

	return &cmdContext{
		MachineConfig: mc,
		Scope:         scope,
		RepoPath:      repo,
	}, nil
}

// loadRestoreContext builds the context a RESTORE needs WITHOUT requiring the
// repo clone, ferry.toml, or a merged Scope. A full (or scoped) restore reverts
// managed paths from the immutable baseline under ~/.local/state/ferry — it
// operates purely on the baseline+journal in the state store, so it must keep
// working even when the repo clone was deleted, corrupted, de-scoped, or has a
// broken ferry.toml. loadContext() (which apply/capture/status/diff/init need)
// would strand the user in exactly those cases by failing on LoadScope.
//
// It therefore:
//   - reads config.toml TOLERANTLY for the repo path only, and does NOT fail when
//     it is missing or broken (RepoPath is best-effort; restore needs it only for
//     the darwin terminal-resource registration, where a path is plumbed through
//     but the recorded blob — not the repo — is what restore replays).
//   - leaves Scope zero (no LoadScope, so a missing/corrupt repo never blocks).
//   - constructs the backup Engine lazily via cmdContext.Engine(), rooted at
//     paths.StateDir() directly — independent of the repo.
//
// The returned context is otherwise an ordinary cmdContext, so restore reuses the
// same Engine()/Register() plumbing as every other command.
func loadRestoreContext() (*cmdContext, error) {
	mc, repo := loadRepoPathBestEffort()

	// Keep loadContext's defence-in-depth: never let a ~/.ssh-pointing repo path
	// through (it is only used for terminal-resource registration here, but the
	// guard is cheap and preserves the invariant). A guard failure downgrades to
	// "no repo path" rather than blocking the baseline restore.
	if repo != "" {
		if err := rejectIfUnderSSH("configured repo path", repo); err != nil {
			repo = ""
			mc = config.MachineConfig{}
		}
	}

	return &cmdContext{
		MachineConfig: mc,
		RepoPath:      repo,
	}, nil
}

// loadRepoPathBestEffort returns the machine config and repo path from
// config.toml when present and parseable, and zero values otherwise. Unlike
// loadMachineConfigTolerant it NEVER errors: a restore from the baseline store
// does not need the repo, so an absent or unreadable config.toml is not fatal.
func loadRepoPathBestEffort() (config.MachineConfig, string) {
	if mc, err := config.LoadMachineConfig(); err == nil {
		return mc, mc.Repo
	}
	path, err := paths.ConfigFile()
	if err != nil {
		return config.MachineConfig{}, ""
	}
	// Harden ~/.config/ferry before the raw read so this tolerant fallback cannot be
	// tricked into reading config.toml through a symlinked ferry config dir (e.g. one
	// redirected into ~/.ssh). The strict loader above already hardens; mirror it here.
	if cfgDir, derr := paths.ConfigDir(); derr != nil {
		return config.MachineConfig{}, ""
	} else if herr := paths.HardenStoreDir(cfgDir); herr != nil {
		return config.MachineConfig{}, ""
	}
	var raw config.MachineConfig
	if _, derr := toml.DecodeFile(path, &raw); derr != nil {
		return config.MachineConfig{}, ""
	}
	return raw, raw.Repo
}

// hasAnyBaseline reports whether ferry has ever recorded an immutable baseline on
// this machine — i.e. whether there is anything to restore. It is fully read-only
// and does NOT create the state store (unlike New()/NewAt(), which eagerly
// mkdir+chmod the layout): a never-applied machine must surface a clear "nothing
// to restore" message rather than have restore create empty state and then claim
// success. It returns true when the baseline directory holds at least one entry
// the engine recognises as a complete baseline.
func hasAnyBaseline() (bool, error) {
	stateDir, err := paths.StateDir()
	if err != nil {
		return false, err
	}
	// Harden ~/.local/state/ferry before reading the baseline dir, so restore's
	// read-only baseline probe cannot be redirected through a symlinked state dir
	// (e.g. one pointing into ~/.ssh) into reading outside the ferry store.
	if herr := paths.HardenStoreDir(stateDir); herr != nil {
		return false, herr
	}
	baselineDir := filepath.Join(stateDir, "baseline")
	ents, err := os.ReadDir(baselineDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	for _, ent := range ents {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		// Each baseline meta file records the managed path it baselines. Reuse the
		// engine's read-only completeness check (HasBaselineReadOnly), keyed by that
		// recorded path, so a partial/corrupt .json does not count as restorable.
		var meta struct {
			Path string `json:"path"`
		}
		data, rerr := os.ReadFile(filepath.Join(baselineDir, name))
		if rerr != nil {
			return false, rerr
		}
		if json.Unmarshal(data, &meta) != nil || meta.Path == "" {
			continue
		}
		if backup.HasBaselineReadOnly(stateDir, meta.Path) {
			return true, nil
		}
	}
	return false, nil
}

// loadMachineConfigTolerant resolves the repo clone path from config.toml. It
// first tries the strict loader (config.LoadMachineConfig, which requires both
// hostname and repo). If the file exists but predates a written hostname (e.g.
// a config that only records the repo), it falls back to reading just the repo
// key so apply/diff — which only need the repo path — still work. A genuinely
// missing config.toml is the first-run signal.
func loadMachineConfigTolerant() (config.MachineConfig, string, error) {
	mc, err := config.LoadMachineConfig()
	if err == nil {
		return mc, mc.Repo, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return config.MachineConfig{}, "", errors.New("ferry is not configured on this machine: run `ferry init` first")
	}

	// The strict loader rejects a config.toml that omits the hostname. apply/diff
	// only need the repo path, so re-read it directly rather than failing.
	path, perr := paths.ConfigFile()
	if perr != nil {
		return config.MachineConfig{}, "", err
	}
	// Harden ~/.config/ferry before the raw repo-only read (same reason as the strict
	// loader): never read config.toml through a symlinked ferry config dir.
	if cfgDir, derr := paths.ConfigDir(); derr != nil {
		return config.MachineConfig{}, "", err
	} else if herr := paths.HardenStoreDir(cfgDir); herr != nil {
		return config.MachineConfig{}, "", err
	}
	var raw config.MachineConfig
	if _, derr := toml.DecodeFile(path, &raw); derr != nil || raw.Repo == "" {
		return config.MachineConfig{}, "", err // surface the original, clearer error
	}
	return raw, raw.Repo, nil
}
