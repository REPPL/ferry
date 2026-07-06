package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/backup"
	"github.com/REPPL/ferry/internal/deps"
	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/paths"
	"github.com/REPPL/ferry/internal/platform"
	"github.com/REPPL/ferry/internal/terminal"
)

func init() {
	// restore-only flags. Registered here so commands.go stays owned by the
	// skeleton wave. --packages and --purge-without-recovery are explicit opt-ins;
	// --yes makes a destructive-ish restore non-interactive-safe (and is implied
	// when stdin is not a terminal, so evals never hang on empty stdin).
	restoreCmd.Flags().Bool("packages", false, "also uninstall packages ferry recorded as self-installed")
	restoreCmd.Flags().Bool("yes", false, "skip the confirmation prompt")
	restoreCmd.Flags().Bool("purge-without-recovery", false, "remove ferry's own config AND the backup store after restore — DESTROYS the ability to undo this restore or re-restore later (irreversible; the default keeps the backup store)")
}

// runRestore reverses ferry's changes back to the immutable pre-ferry baseline.
//
// Default `ferry restore` reverts FILES + preference domains: every managed path
// returns byte+mode+symlink-identical to its baseline (a path that did not exist
// pre-ferry is removed). The engine snapshots current state FIRST, so an unwanted
// restore is itself reversible (the snapshot lives in the backup store).
//
// `ferry restore <domain>` scopes the revert to one named domain. `--packages`
// additionally uninstalls ONLY the packages ferry recorded as self-installed
// (Homebrew/the package manager itself is never removed). `--purge-without-recovery`
// removes ferry's own dirs INCLUDING the backup store AFTER a successful restore
// (opt-in; the default keeps the backup store intact because deleting the only
// backup makes the restore permanently un-undoable — the single irreversible op).
func runRestore(c *cobra.Command, args []string) error {
	doPackages, _ := c.Flags().GetBool("packages")
	yes, _ := c.Flags().GetBool("yes")
	purge, _ := c.Flags().GetBool("purge-without-recovery")

	// RESTORE deliberately does NOT use loadContext(): a full/scoped revert reads
	// purely from the immutable baseline under ~/.local/state/ferry and needs
	// neither the repo clone, ferry.toml, nor a merged Scope. loadRestoreContext()
	// constructs the engine from the state store directly, so a DELETED, corrupt,
	// or de-scoped repo (or a broken ferry.toml) no longer strands the user — they
	// can still revert managed files from the backup store. The repo path it
	// carries is best-effort and used only for the darwin terminal-resource
	// registration, where the recorded blob (not the repo) is what restore replays.
	ctx, err := loadRestoreContext()
	if err != nil {
		return err
	}

	out := c.OutOrStdout()

	// A full restore reverts the machine, so confirm — but stay non-interactive
	// safe: proceed without prompting when --yes is set or stdin is not a TTY.
	if !confirmRestore(c, yes, args, out) {
		fmt.Fprintln(out, "restore: aborted")
		return nil
	}

	// If ferry never applied anything there is no baseline to revert to. Report it
	// clearly (a missing repo is NOT the cause and must not be reported as one) and
	// skip the file/domain restore — but still allow an explicit --packages
	// uninstall below, which reads the repo-independent installed set from state.
	baselineExists, err := hasAnyBaseline()
	if err != nil {
		return fmt.Errorf("check for ferry baseline: %w", err)
	}

	if baselineExists {
		// Register the terminal preference domains so their resource entries (captured
		// at apply time) restore through the Resource.Restore hook. Both full Restore()
		// and ScopedRestore() fold registered resource entries in; an unregistered
		// resource baseline would be unrestorable. darwin-only (no-op on linux).
		if err := registerTerminalDomains(ctx); err != nil {
			return err
		}

		if len(args) > 0 {
			if err := scopedRestore(ctx, args, out); err != nil {
				return err
			}
		} else {
			if err := fullRestore(ctx, out); err != nil {
				return err
			}
		}
	} else {
		fmt.Fprintln(out, "restore: nothing to restore: no ferry baseline found")
	}

	// Packages are a SEPARATE, explicit opt-in. Default restore = files only.
	// This reads the persisted ferry-installed set from state (deps-installed.txt),
	// which is repo-independent, so it runs even with no file baseline.
	if doPackages {
		if err := restorePackages(out); err != nil {
			return err
		}
	}

	// --purge-without-recovery removes ferry's own state INCLUDING the backup
	// store AFTER a successful restore. Off by default so the backup store (and
	// the just-written snapshot) survive. This is the one irreversible op in
	// ferry, so it carries its OWN confirmation gate — distinct from the restore
	// confirm above — so a stray --yes meant for the restore can never silently
	// wipe the backup store.
	if purge {
		if !confirmPurge(c, yes, out) {
			fmt.Fprintln(out, "restore: purge aborted; backup store kept")
			return nil
		}
		if err := purgeFerryState(out); err != nil {
			return err
		}
	}

	return nil
}

// confirmPurge is the EXTRA, irreversible-op gate for --purge-without-recovery.
// It returns true only when purge should proceed. With --yes it proceeds (the
// documented skip-confirmation path). Without --yes on an interactive terminal it
// demands an explicitly typed "yes" — anything else aborts, keeping the backup
// store. Without --yes on a NON-tty it aborts (never purges silently), because a
// piped/empty stdin cannot consent to destroying the only backup.
func confirmPurge(c *cobra.Command, yes bool, out io.Writer) bool {
	if yes {
		return true
	}
	if !stdinIsTerminal() {
		fmt.Fprintln(out, "restore: --purge-without-recovery needs --yes on a non-interactive stdin; refusing to delete the backup store")
		return false
	}
	fmt.Fprint(out, "This permanently deletes ferry's backup store — you will NOT be able to undo this restore. Type 'yes' to proceed: ")
	reader := bufio.NewReader(c.InOrStdin())
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line) == "yes"
}

// fullRestore reverts every managed path to its baseline. The engine snapshots
// current state first (built into Restore), so the revert is itself reversible.
func fullRestore(ctx *cmdContext, out io.Writer) error {
	eng, err := ctx.Engine()
	if err != nil {
		return err
	}
	snapID, err := eng.Restore()
	if err != nil {
		return fmt.Errorf("restore to baseline: %w", err)
	}
	fmt.Fprintln(out, "restore: reverted all managed paths to their pre-ferry baseline")
	if snapID != "" {
		fmt.Fprintf(out, "restore: pre-restore state snapshotted (%s); undo is possible if needed\n", snapID)
	}
	return nil
}

// scopedRestore reverts only the named domains. A terminal scope name resolves
// to the REAL preference-domain resource path (e.g. "iterm2" ->
// resource://com.googlecode.iterm2); any other name is treated as a dotfile and
// resolves to its home file path (e.g. ".zshrc" -> ~/.zshrc). Paths without a
// baseline are skipped by the engine — and we only REPORT domains that actually
// had a baseline (so we never claim to revert something untouched).
func scopedRestore(ctx *cmdContext, domains []string, out io.Writer) error {
	eng, err := ctx.Engine()
	if err != nil {
		return err
	}
	// The "agents" domain fans out to MANY file paths (one per harness target,
	// devtree file, and asset copy), resolved from ferry's PERSISTED target
	// record (agents-targets.json) — never the live manifest — so it covers
	// later-de-scoped targets and needs NO config repo (restore's
	// repo-independence guarantee holds). It is expanded here, BEFORE
	// resolveScopedPaths would misread the name as a dotfile (~/.agents).
	var rest []string
	agentsRequested := false
	for _, d := range domains {
		if d == "agents" {
			agentsRequested = true
			continue
		}
		rest = append(rest, d)
	}

	// resolveScopedPaths refuses ~/.ssh + path-traversal dotfile names (the security
	// boundary): a refused name is reported and dropped, never restored to a ~/.ssh
	// path. validDomains stays parallel to absPaths for the report loop below.
	validDomains, absPaths, refusals := resolveScopedPaths(rest)
	if agentsRequested {
		apaths, aerr := agentsRestorePaths()
		if aerr != nil {
			refusals = append(refusals, fmt.Sprintf("restore: cannot resolve the agents domain's targets (%v); run a full `ferry restore` instead", aerr))
		}
		for _, p := range apaths {
			validDomains = append(validDomains, "agents")
			absPaths = append(absPaths, p)
		}
	}
	for _, r := range refusals {
		fmt.Fprintln(out, r)
	}

	// Report only the domains that actually had a baseline to restore. A
	// fanned-out domain (agents) contributes many paths under one name, so
	// the report deduplicates.
	var restored []string
	seen := map[string]bool{}
	for i, d := range validDomains {
		if eng.HasBaseline(absPaths[i]) && !seen[d] {
			seen[d] = true
			restored = append(restored, d)
		}
	}

	snapID, err := eng.ScopedRestore(absPaths)
	if err != nil {
		return fmt.Errorf("scoped restore: %w", err)
	}
	if len(restored) == 0 {
		fmt.Fprintf(out, "restore: nothing to revert for %s (no baseline recorded)\n", strings.Join(domains, ", "))
		return nil
	}
	fmt.Fprintf(out, "restore: reverted %s to baseline\n", strings.Join(restored, ", "))
	if snapID != "" {
		fmt.Fprintf(out, "restore: pre-restore state snapshotted (%s)\n", snapID)
	}
	return nil
}

// resolveScopedPaths maps each requested domain name to the absolute store path
// the engine keys baseline entries on: the synthetic resource path for a known
// terminal PREFERENCE domain (mapped to its REAL macOS domain id, NOT the
// user-facing scope name), or the home file path for a dotfile.
//
// SECURITY BOUNDARY: a dotfile name is resolved through TargetFor, which refuses
// any ~/.ssh or path-traversal target. A refused name is DROPPED (never restored
// to a ~/.ssh path) and surfaced as a refusal message. The returned validDomains
// and absPaths stay index-parallel for the caller's report loop. os.UserHomeDir
// failure is fatal and panics out via the (rare) error path below — but since the
// signature is now error-free for the common path, it is handled by treating a
// home-dir failure as refusing every dotfile name with that error.
func resolveScopedPaths(domains []string) (validDomains, absPaths, refusals []string) {
	home, err := os.UserHomeDir()
	for _, d := range domains {
		if prefID, ok := terminalPrefDomainID(d); ok {
			// Key on the REAL preference domain id (com.googlecode.iterm2 /
			// com.apple.Terminal) — what apply's BackupResource recorded — not the
			// user-facing scope name. This is the name-mapping fix.
			validDomains = append(validDomains, d)
			absPaths = append(absPaths, backup.ResourcePath(prefID))
			continue
		}
		if err != nil {
			refusals = append(refusals, refusalWarning(d, err))
			continue
		}
		// Treat as a dotfile name (with or without a leading dot). TargetFor
		// normalises to the canonical ~/.<name> home path AND refuses ~/.ssh /
		// path-traversal names before any path is restored.
		t, terr := dotfile.TargetFor("", home, d)
		if terr != nil {
			refusals = append(refusals, refusalWarning(d, terr))
			continue
		}
		validDomains = append(validDomains, d)
		absPaths = append(absPaths, t.Home)
	}
	return validDomains, absPaths, refusals
}

// terminalPrefDomainID maps a user-facing terminal scope name to its real macOS
// preference domain id, returning ok=false for any non-terminal name.
func terminalPrefDomainID(scopeName string) (string, bool) {
	switch scopeName {
	case "iterm2":
		return terminal.ITerm2Domain, true
	case "terminal", "apple-terminal":
		return terminal.AppleTerminalDomain, true
	default:
		return "", false
	}
}

// registerTerminalDomains attaches the iTerm2 + Apple Terminal preference domains
// to the engine so their resource baseline entries restore through the
// Resource.Restore hook (folded into both full Restore and ScopedRestore). The
// runner/blob are irrelevant for restore (the engine replays the captured blob via
// Restore), so we register both with the production runner. darwin-only no-op
// elsewhere — internal/terminal is platform-guarded and builds clean on linux.
func registerTerminalDomains(ctx *cmdContext) error {
	if !platform.IsDarwin() {
		return nil
	}
	eng, err := ctx.Engine()
	if err != nil {
		return err
	}
	eng.Register(terminal.NewITerm2(filepath.Join(ctx.RepoPath, "iterm2"), terminal.ExecRunner{}))
	eng.Register(terminal.NewAppleTerminal(nil, terminal.ExecRunner{}))
	return nil
}

// restorePackages uninstalls ONLY the packages ferry recorded as self-installed
// (the newline list apply --deps persisted). Homebrew / the package manager
// itself is NEVER removed. An empty or absent record means there is nothing to do.
func restorePackages(out io.Writer) error {
	pkgs, err := readInstalledSet()
	if err != nil {
		return fmt.Errorf("read recorded installed packages: %w", err)
	}
	if len(pkgs) == 0 {
		fmt.Fprintln(out, "restore --packages: no ferry-installed packages recorded; nothing to uninstall")
		return nil
	}

	mgr := platform.DetectPackageManager()
	if mgr == platform.ManagerNone {
		fmt.Fprintln(out, "restore --packages: no package manager present; cannot uninstall (recorded set left intact)")
		return nil
	}

	runner := deps.ExecRunner{}
	if err := uninstallPackages(runner, mgr, pkgs); err != nil {
		return fmt.Errorf("uninstall packages: %w", err)
	}
	fmt.Fprintf(out, "restore --packages: uninstalled %d ferry-installed package(s)\n", len(pkgs))

	// The packages are gone; clear the record so a re-run does not try again.
	if err := clearInstalledSet(); err != nil {
		return fmt.Errorf("clear installed record: %w", err)
	}
	return nil
}

// uninstallPackages removes exactly the recorded packages via the detected
// manager. The manager binary itself is never a target. Brew formula directives
// recorded as `brew "x"` / `cask "y"` are reduced to their package name first.
func uninstallPackages(runner deps.CommandRunner, mgr platform.PackageManager, pkgs []string) error {
	switch mgr {
	case platform.ManagerBrew:
		names := brewPackageNames(pkgs)
		if len(names) == 0 {
			return nil
		}
		// `--` ends brew's option parsing: everything after it is a package name,
		// never an option — so a recorded name beginning with "-" cannot be read as
		// a brew flag when this runs under sudo.
		args := append([]string{"brew", "uninstall", "--"}, names...)
		if out, err := runner.Run(args...); err != nil {
			return fmt.Errorf("brew uninstall: %w (%s)", err, strings.TrimSpace(out))
		}
		return nil
	case platform.ManagerApt:
		// deps-installed.txt is ferry-written and 0700/symlink-hardened, but this
		// runs apt-get as root under `sudo ferry restore --packages`, so every
		// recorded entry is re-validated as a plain apt package name before it
		// reaches argv. This closes the same argument-injection boundary as the
		// install rail, plus the trailing-"+" INSTALL modifier specific to this
		// rail: a tampered entry such as `-oDPkg::Pre-Invoke::=touch /tmp/x` (a
		// leading "-" apt reads as an option), `ufw-` (a trailing "-" apt reads as
		// its REMOVE modifier), or `openssh-server+` (a trailing "+" apt reads as its
		// INSTALL modifier, which would INSTALL the package on the remove rail) is
		// refused, aborting the whole uninstall with the record left intact. The `--`
		// separator is defence in depth on top.
		for _, p := range pkgs {
			if err := deps.ValidateAptRemoveName(p); err != nil {
				return err
			}
		}
		args := append([]string{"apt-get", "remove", "-y", "--"}, pkgs...)
		if out, err := runner.Run(args...); err != nil {
			return fmt.Errorf("apt-get remove: %w (%s)", err, strings.TrimSpace(out))
		}
		return nil
	default:
		return fmt.Errorf("unsupported package manager %q", mgr)
	}
}

// brewPackageNames reduces recorded brew directives to bare package names,
// tolerating both raw names ("ripgrep") and Brewfile directives (`brew "ripgrep"`,
// `cask "iterm2"`). `tap`/other directives are dropped — they are not packages to
// uninstall, and brew never installs the manager itself this way.
func brewPackageNames(entries []string) []string {
	var names []string
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		fields := strings.Fields(e)
		// A directive form: <kind> "name". Only brew/cask name packages.
		if len(fields) >= 2 {
			kind := fields[0]
			if kind != "brew" && kind != "cask" {
				continue
			}
			name := strings.Trim(fields[1], `"'`)
			if name != "" {
				names = append(names, name)
			}
			continue
		}
		// A bare package name.
		names = append(names, strings.Trim(e, `"'`))
	}
	return names
}

// readInstalledSet reads the persisted ferry-installed package set written by
// apply --deps (~/.local/state/ferry/deps-installed.txt, newline-delimited). An
// absent file means an empty set (not an error).
func readInstalledSet() ([]string, error) {
	path, err := installedSetPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var pkgs []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			pkgs = append(pkgs, line)
		}
	}
	return pkgs, sc.Err()
}

// clearInstalledSet removes the persisted installed-package record once those
// packages have been uninstalled.
func clearInstalledSet() error {
	path, err := installedSetPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func installedSetPath() (string, error) {
	stateDir, err := paths.StateDir()
	if err != nil {
		return "", err
	}
	// Symlink-harden ~/.local/state/ferry BEFORE any open/remove of deps-installed.txt
	// (readInstalledSet / clearInstalledSet), so a state dir symlinked into ~/.ssh or a
	// system path is refused, never read or removed through. Hardening here makes both
	// callers structurally safe. Lexical, creates nothing, no-op for a test temp dir.
	if herr := paths.HardenStoreDir(stateDir); herr != nil {
		return "", herr
	}
	return filepath.Join(stateDir, "deps-installed.txt"), nil
}

// purgeFerryState removes ferry's own config and state directories AFTER a
// successful restore — the explicit "leaves no trace" cleanup. It runs only with
// --purge-without-recovery because the default must keep the backup store (and the
// pre-restore snapshot) intact. The Homebrew install / package manager is never
// touched here.
func purgeFerryState(out io.Writer) error {
	cfgDir, err := paths.ConfigDir()
	if err != nil {
		return err
	}
	stateDir, err := paths.StateDir()
	if err != nil {
		return err
	}
	for _, d := range []string{cfgDir, stateDir} {
		// Symlink-harden BEFORE os.RemoveAll. This is the most dangerous FS op in
		// ferry: os.RemoveAll resolves THROUGH a symlinked parent, so if ~/.config or
		// ~/.local (or any component down to the ferry dir) is a symlink, RemoveAll
		// could delete OUTSIDE the real ferry dirs — e.g. into ~/.ssh or a system
		// path. HardenStoreDir refuses if ANY component from $HOME down to d is a
		// symlink, so a symlinked parent makes the whole purge REFUSE here. We never
		// RemoveAll through a symlinked parent: purge only ever deletes the genuine
		// ~/.config/ferry and ~/.local/state/ferry real directories.
		if err := paths.HardenStoreDir(d); err != nil {
			return fmt.Errorf("refusing purge: %w", err)
		}
		if err := os.RemoveAll(d); err != nil {
			return fmt.Errorf("purge %s: %w", d, err)
		}
	}
	fmt.Fprintln(out, "restore --purge-without-recovery: removed ferry's config and state incl. the backup store (no trace left; this restore can no longer be undone)")
	return nil
}

// confirmRestore returns true when restore should proceed. It proceeds without
// prompting when --yes is set or when stdin is not an interactive terminal (so
// evals with empty stdin and CI never hang). On a real interactive terminal it
// asks for a y/N confirmation.
func confirmRestore(c *cobra.Command, yes bool, domains []string, out io.Writer) bool {
	if yes || !stdinIsTerminal() {
		return true
	}

	scope := "all managed files"
	if len(domains) > 0 {
		scope = strings.Join(domains, ", ")
	}
	fmt.Fprintf(out, "restore will revert %s to the pre-ferry baseline. Continue? [y/N]: ", scope)

	reader := bufio.NewReader(c.InOrStdin())
	line, _ := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// stdinIsTerminal reports whether stdin is an interactive character device. A
// non-terminal stdin (pipe, empty reader in evals/CI) means "non-interactive":
// restore proceeds without prompting rather than blocking on a read that never
// returns input.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
