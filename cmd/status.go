package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/platform"
	"github.com/REPPL/ferry/internal/terminal"
)

// runStatus reports config drift for every in-scope managed target. It is fully
// read-only: it reuses apply's planning (buildPlan) so the resolution is
// IDENTICAL to apply's — effective source (zsh shared+source-line, or whole-file
// local-wins overlay), {{ferry.secret ...}} placeholders rendered the same way
// (render-or-SKIP), and the three-way dotfile.Classify computed per target. It
// takes NO lock and writes NOTHING: buildPlan opens the last-applied store
// read-only (never mkdirs ~/.local/state/ferry) and secret rendering is a pure
// read. A clean tree prints a positive no-drift line and exits 0.
//
// Because status shares apply's plan, two previously-divergent cases now agree:
//   - secret-aware: a cleanly-applied file whose source contains a PRESENT
//     {{ferry.secret X}} reports CLEAN (the rendered bytes match live), and a
//     MISSING secret reports "blocked" (apply would SKIP it) rather than false
//     repo-ahead drift;
//   - zsh sidecar: when a zsh domain has a local overlay, apply materialises
//     ~/.<bare>.local from local/zsh/<bare>.local — buildPlan emits that sidecar
//     as its own kindOverlay item, so status classifies it too and reports drift
//     if the user edited ~/.<bare>.local, alongside the main ~/.<bare> file.
func runStatus(c *cobra.Command, _ []string) error {
	ctx, err := loadContext()
	if err != nil {
		return err
	}

	out := c.OutOrStdout()

	// Same read-only plan apply/diff compute. buildPlan opens both the secret
	// store and the last-applied store read-only and creates no ferry state.
	plan, warnings, err := buildPlan(ctx)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(out, w)
	}

	var drifted int
	var reported int

	for _, it := range plan {
		// Terminal preference DOMAINS are reconciled natively (not the three-way file
		// classify), but status must STILL report them — otherwise a scope with only
		// terminal domains in it (and no dotfiles) wrongly prints "no managed config".
		// Report each in-scope terminal domain's managed/applied state read-only.
		if it.kind == kindPreference {
			reported++
			if reportTerminalStatus(out, ctx.RepoPath, it) {
				drifted++
			}
			continue
		}
		if it.kind != kindDotfile && it.kind != kindOverlay {
			continue
		}
		reported++

		// A missing secret means apply would SKIP this target — report it as
		// blocked (held back), NOT as drift, so it is not a false action item.
		if it.skip {
			fmt.Fprintf(out, "  %-22s blocked (missing secret: %s)\n", it.domain, strings.Join(it.missing, ", "))
			continue
		}

		if reportStatus(out, it.domain, it.state) {
			drifted++
		}
	}

	if reported == 0 {
		fmt.Fprintln(out, "no managed config in scope; nothing to report")
		return nil
	}
	if drifted == 0 {
		fmt.Fprintln(out, "clean: no drift detected; all managed files are in sync")
	} else {
		fmt.Fprintf(out, "%d managed file(s) drifted from the repo\n", drifted)
	}
	return nil
}

// reportTerminalStatus prints one status line for an in-scope terminal preference
// DOMAIN and reports whether it counts as drift. It is READ-ONLY: it never creates
// ferry state. The managed/applied signal is it.prefApplied — computed by buildPlan
// from a NON-MUTATING baseline stat (baselineHasBeenApplied → HasBaselineReadOnly),
// so no engine and no state dir is ever built here. On darwin it ALSO compares the
// live exported domain to the repo's committed/overlay plist via a read-only
// `defaults export` (terminalLiveDiffers): a managed domain whose live state has
// drifted from the repo is surfaced as a capture candidate. On linux the live
// comparison is skipped (no `defaults`), so only the managed/applied state is shown.
//
// States:
//   - not yet applied (no baseline)      -> "not yet applied (run `ferry apply`)" [drift]
//   - applied AND live drifted from repo -> "drifted (live differs; run `ferry capture`)" [drift]
//   - applied AND in sync (or linux)     -> "managed (applied)" [clean]
func reportTerminalStatus(out io.Writer, repo string, it planItem) (isDrift bool) {
	if !it.prefApplied {
		fmt.Fprintf(out, "  %-22s not yet applied (run `ferry apply`)\n", it.domain)
		return true
	}
	if platform.IsDarwin() && terminalLiveDiffers(repo, it.domain) {
		fmt.Fprintf(out, "  %-22s drifted (live differs from repo; run `ferry capture` to record it)\n", it.domain)
		return true
	}
	fmt.Fprintf(out, "  %-22s managed (applied)\n", it.domain)
	return false
}

// terminalLiveDiffers reports whether the live exported terminal domain differs
// from the repo copy apply would deploy (local overlay wins, else shared). It is
// READ-ONLY: `defaults export` reads the live domain without mutating it, and the
// repo read is a plain file read. An absent live domain or any export failure is
// treated as "no observable drift" (not-differs) so status never errors or hangs on
// the read-only path — the not-yet-applied case is already handled by prefApplied.
// darwin-only: callers guard platform.IsDarwin(); on linux this is never reached.
func terminalLiveDiffers(repo, domain string) bool {
	prefID, ok := terminalPrefDomainID(domain)
	if !ok {
		return false
	}
	// Export the live domain through the same PreferenceDomain apply/capture use.
	var d *terminal.PreferenceDomain
	switch domain {
	case "iterm2":
		d = terminal.NewITerm2(filepath.Join(repo, "iterm2"), terminal.ExecRunner{})
	default: // "terminal" / Apple Terminal
		d = terminal.NewAppleTerminal(nil, terminal.ExecRunner{})
	}
	liveBlob, absent, err := d.Backup()
	if err != nil || absent {
		// ErrNotDarwin / export failure / never-configured domain: nothing to
		// compare. Report no drift (prefApplied already gates the unmanaged case).
		_ = errors.Is(err, terminal.ErrNotDarwin)
		return false
	}
	// Compare against the bytes apply would deploy: the local overlay when present
	// (local wins whole-domain), else the shared committed plist. An absent repo
	// copy reads as empty, so a populated live domain differs (a capture candidate).
	statusSrc := terminalRepoStatusSource(repo, domain, prefID)
	// Refuse a symlinked/escaping plist before the symlink-following read so status
	// never reads ~/.ssh through a repo symlink. On refusal treat as no observable
	// drift (read-only path must not error or read the poisoned target).
	if _, serr := safeRepoPath(repo, statusSrc); serr != nil {
		return false
	}
	repoBytes, _ := os.ReadFile(statusSrc)
	if domain == "iterm2" {
		// iTerm2 ONLY: compare LIKE-FOR-LIKE with the machine-local control keys
		// (PrefsCustomFolder / LoadPrefsFromCustomFolder) STRIPPED from BOTH sides. Those
		// keys are how ferry POINTS iTerm2 at the repo (apply sets them via `defaults
		// write`), NOT user prefs — `defaults export` can carry them, so status must
		// exclude them or it would mis-report drift solely because the live domain holds
		// the pointer ferry itself wrote. The repo plist apply deploys never contains
		// them; stripping both sides compares the actual settings. The exact
		// custom-folder-vs-export round-trip fidelity is Layer-2-deferred per
		// AC-terminal-config (see terminal.StripITerm2ControlKeys).
		liveBlob = terminal.StripITerm2ControlKeys(liveBlob)
		repoBytes = terminal.StripITerm2ControlKeys(repoBytes)
	}
	return string(repoBytes) != string(liveBlob)
}

// terminalRepoStatusSource resolves the repo plist status compares the live domain
// against, mirroring apply's local-wins resolution: the per-machine local overlay
// (local/<domain>/<id>.plist) when present, else the shared committed copy
// (iterm2/<id>.plist or terminal/<id>.plist).
func terminalRepoStatusSource(repo, domain, prefID string) string {
	local := filepath.Join(repo, "local", domain, prefID+".plist")
	// Guard-first probe: regularRepoFile routes `local` through safeRepoPath BEFORE
	// any stat, so a symlinked repo plist (or a symlinked PARENT into ~/.ssh) is
	// never stat'd here — it refuses before the read. Falling through to the shared
	// copy keeps status read-only and safe.
	if regularRepoFile(repo, local) {
		return local
	}
	if domain == "iterm2" {
		return filepath.Join(repo, "iterm2", prefID+".plist")
	}
	return filepath.Join(repo, "terminal", prefID+".plist")
}

// reportStatus prints one git-status-like line for a target and reports whether
// it counts as drift (a non-clean state the user should act on). Names the target
// and its state: clean / locally-drifted (capture candidate) / repo-ahead /
// conflict / missing.
func reportStatus(out io.Writer, name string, state dotfile.State) (isDrift bool) {
	switch state {
	case dotfile.StateClean:
		fmt.Fprintf(out, "  %-22s clean\n", name)
		return false
	case dotfile.StateLocallyDrifted:
		fmt.Fprintf(out, "  %-22s drifted (locally modified; run `ferry capture` to record it)\n", name)
		return true
	case dotfile.StateConflict:
		fmt.Fprintf(out, "  %-22s conflict (modified locally AND in the repo; `ferry capture` or `ferry apply --force`)\n", name)
		return true
	case dotfile.StateRepoAhead:
		fmt.Fprintf(out, "  %-22s repo-ahead (repo changed; run `ferry apply` to deploy it)\n", name)
		return true
	case dotfile.StateMissing:
		fmt.Fprintf(out, "  %-22s missing (not yet deployed; run `ferry apply`)\n", name)
		return true
	default:
		fmt.Fprintf(out, "  %-22s %s\n", name, state)
		return true
	}
}
