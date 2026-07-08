package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/deps"
	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/platform"
	"github.com/REPPL/ferry/internal/terminal"
)

// stateColourer returns the shared banner colouriser (colourFor), so status,
// diff, and doctor colour their state words through the same fatih/color gate as
// the banner: green = good/clean, yellow = a pending or advisory change, red = a
// conflict/failure, and plain text whenever the destination is not an interactive
// terminal (a pipe, a file, NO_COLOR). Callers pass colGreen/colYellow/colRed.
func stateColourer(w io.Writer) func(*color.Color, string) string {
	return colourFor(w)
}

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

	colour := stateColourer(out)

	var drifted int
	var reported int

	for _, it := range plan {
		// Terminal preference DOMAINS are reconciled natively (not the three-way file
		// classify), but status must STILL report them — otherwise a scope with only
		// terminal domains in it (and no dotfiles) wrongly prints "no managed config".
		// Report each in-scope terminal domain's managed/applied state read-only.
		if it.kind == kindPreference {
			reported++
			if reportTerminalStatus(out, colour, ctx.RepoPath, it) {
				drifted++
			}
			continue
		}
		// Converged FileDomain items (fn-5). Agents targets carry the domain's
		// repo-authoritative guidance (capture never ingests them in v1), so their
		// drift lines point at the repo copy, not `ferry capture`; dotfiles and
		// config-file terminals share the three-way status rendering with capture
		// guidance.
		if it.kind != kindFile {
			continue
		}
		reported++

		if it.fileDomain == "agents" {
			if reportAgentsStatus(out, colour, it.domain, it.state) {
				drifted++
			}
			continue
		}

		// A missing secret means apply would SKIP this target — report it as
		// blocked (held back), NOT as drift, so it is not a false action item.
		if it.skip {
			fmt.Fprintf(out, "  %-22s %s (missing secret: %s)\n", it.domain, colour(colYellow, "blocked"), strings.Join(it.missing, ", "))
			continue
		}

		if reportStatus(out, colour, it.domain, it.state) {
			drifted++
		}
	}

	// Deps drift is an INDEPENDENT, read-only status pass (deps is a side-channel
	// outside the FileDomain/ResourceDomain plan, so it is not in buildPlan). It
	// surfaces Homebrew Brewfile drift and npm-globals drift when their domains are
	// managed, WITHOUT installing anything or rewriting a manifest.
	dr, dd := reportDepsStatus(ctx, out, colour)
	reported += dr
	drifted += dd

	if reported == 0 {
		fmt.Fprintln(out, "no managed config in scope; nothing to report")
		return nil
	}
	// One-line summary footer so the report is scannable at a glance.
	if drifted == 0 {
		fmt.Fprintf(out, "\n%s — %d in sync, no drift detected\n", colour(colGreen, "clean"), reported)
	} else {
		fmt.Fprintf(out, "\n%s — %d drifted, %d clean of %d in scope\n",
			colour(colYellow, "drift"), drifted, reported-drifted, reported)
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
func reportTerminalStatus(out io.Writer, colour func(*color.Color, string) string, repo string, it planItem) (isDrift bool) {
	if !it.prefApplied {
		fmt.Fprintf(out, "  %-22s %s (run `ferry apply`)\n", it.domain, colour(colYellow, "not yet applied"))
		return true
	}
	if platform.IsDarwin() && terminalLiveDiffers(repo, it.domain) {
		fmt.Fprintf(out, "  %-22s %s (live differs from repo; run `ferry capture` to record it)\n", it.domain, colour(colYellow, "drifted"))
		return true
	}
	fmt.Fprintf(out, "  %-22s %s (applied)\n", it.domain, colour(colGreen, "managed"))
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
		d = terminal.NewITerm2(nil, terminal.ExecRunner{}, terminal.ExecProcessController{})
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
		// iTerm2 ONLY: compare LIKE-FOR-LIKE by reducing BOTH sides to the allowlisted
		// global keys. `defaults export` carries volatile machine state (window
		// geometry, NoSync* flags, the retired custom-prefs pointer) that the repo plist
		// never contains; filtering both sides compares the actual carried settings and
		// never mis-reports drift from a volatile key (see terminal.FilterAllowlist).
		liveBlob = terminal.FilterAllowlist(liveBlob)
		repoBytes = terminal.FilterAllowlist(repoBytes)
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

// reportAgentsStatus prints one status line for an agents-domain target and
// reports whether it counts as drift. Same three-way states as reportStatus,
// but the guidance is the domain's: agents targets are repo-authoritative in
// v1 (capture skips them), so a live edit is resolved by updating the repo
// copy (or overriding with `ferry apply --force`), never by capture.
func reportAgentsStatus(out io.Writer, colour func(*color.Color, string) string, name string, state dotfile.State) (isDrift bool) {
	switch state {
	case dotfile.StateClean:
		fmt.Fprintf(out, "  %-22s %s\n", name, colour(colGreen, "clean"))
		return false
	case dotfile.StateLocallyDrifted:
		fmt.Fprintf(out, "  %-22s %s (edited live; repo-authoritative — update the repo copy, or `ferry apply --force`)\n", name, colour(colYellow, "drifted"))
		return true
	case dotfile.StateConflict:
		fmt.Fprintf(out, "  %-22s %s (edited live AND in the repo; update the repo copy, or `ferry apply --force`)\n", name, colour(colRed, "conflict"))
		return true
	case dotfile.StateRepoAhead:
		fmt.Fprintf(out, "  %-22s %s (repo changed; run `ferry apply` to deploy it)\n", name, colour(colYellow, "repo-ahead"))
		return true
	case dotfile.StateMissing:
		fmt.Fprintf(out, "  %-22s %s (not yet deployed; run `ferry apply`)\n", name, colour(colYellow, "missing"))
		return true
	default:
		fmt.Fprintf(out, "  %-22s %s\n", name, state)
		return true
	}
}

// reportDepsStatus prints the read-only deps drift lines and returns how many
// deps domains it reported and how many drifted. It reuses the SAME dump/read
// machinery apply/capture use, but read-only: Homebrew drift goes through
// `brew bundle dump --file=-` (stdout, writes no file) and npm-globals drift
// through `npm ls -g` — neither installs a package nor rewrites a manifest. Each
// managed deps domain is gated on the SAME Scope.IsManaged predicate as capture,
// keeping the managed surface consistent across status/capture (`apply --deps`
// stays the explicit install override). A hard error or an absent tool is a
// clean, non-drift note — status never fails on the read-only path.
func reportDepsStatus(ctx *cmdContext, out io.Writer, colour func(*color.Color, string) string) (reported, drifted int) {
	depsDir := filepath.Join(ctx.RepoPath, "deps")

	if ctx.Scope.IsManaged("brew") {
		reported++
		drift, ok, err := deps.BrewDrift(depsDir, deps.ExecRunner{})
		switch {
		case err != nil:
			fmt.Fprintf(out, "  %-22s %s (%v)\n", "brew", colour(colYellow, "skipped"), err)
		case !ok:
			fmt.Fprintf(out, "  %-22s %s (no Homebrew on this machine)\n", "brew", colour(colGreen, "n/a"))
		case drift.Empty():
			fmt.Fprintf(out, "  %-22s %s\n", "brew", colour(colGreen, "clean"))
		default:
			drifted++
			fmt.Fprintf(out, "  %-22s %s (%d to capture, %d to install; `ferry capture` / `ferry apply --deps`)\n",
				"brew", colour(colYellow, "drifted"), len(drift.Added), len(drift.Removed))
		}
	}

	if ctx.Scope.IsManaged("npm-globals") {
		reported++
		switch {
		case !platform.HasNpm():
			fmt.Fprintf(out, "  %-22s %s (npm not installed)\n", "npm-globals", colour(colGreen, "n/a"))
		default:
			drift, err := deps.NpmGlobalsDrift(depsDir, deps.ExecRunner{})
			switch {
			case err != nil:
				fmt.Fprintf(out, "  %-22s %s (%v)\n", "npm-globals", colour(colYellow, "skipped"), err)
			case drift.Empty():
				fmt.Fprintf(out, "  %-22s %s\n", "npm-globals", colour(colGreen, "clean"))
			default:
				drifted++
				fmt.Fprintf(out, "  %-22s %s (%d to capture, %d to install; `ferry capture` / `ferry apply --deps`)\n",
					"npm-globals", colour(colYellow, "drifted"), len(drift.Added), len(drift.Removed))
			}
		}
	}

	return reported, drifted
}

// reportStatus prints one git-status-like line for a target and reports whether
// it counts as drift (a non-clean state the user should act on). Names the target
// and its state: clean / locally-drifted (capture candidate) / repo-ahead /
// conflict / missing.
func reportStatus(out io.Writer, colour func(*color.Color, string) string, name string, state dotfile.State) (isDrift bool) {
	switch state {
	case dotfile.StateClean:
		fmt.Fprintf(out, "  %-22s %s\n", name, colour(colGreen, "clean"))
		return false
	case dotfile.StateLocallyDrifted:
		fmt.Fprintf(out, "  %-22s %s (locally modified; run `ferry capture` to record it)\n", name, colour(colYellow, "drifted"))
		return true
	case dotfile.StateConflict:
		fmt.Fprintf(out, "  %-22s %s (modified locally AND in the repo; `ferry capture` or `ferry apply --force`)\n", name, colour(colRed, "conflict"))
		return true
	case dotfile.StateRepoAhead:
		fmt.Fprintf(out, "  %-22s %s (repo changed; run `ferry apply` to deploy it)\n", name, colour(colYellow, "repo-ahead"))
		return true
	case dotfile.StateMissing:
		fmt.Fprintf(out, "  %-22s %s (not yet deployed; run `ferry apply`)\n", name, colour(colYellow, "missing"))
		return true
	default:
		fmt.Fprintf(out, "  %-22s %s\n", name, state)
		return true
	}
}
