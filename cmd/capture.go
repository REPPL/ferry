package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/backup"
	"github.com/REPPL/ferry/internal/deps"
	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/platform"
	"github.com/REPPL/ferry/internal/secret"
	"github.com/REPPL/ferry/internal/terminal"
)

// runCapture is the interactive machine->repo ingest. It only ever considers
// in-scope declared dotfiles (the allowlist), offers ONLY those that actually
// drifted, scans every candidate for secrets BEFORE any write, lets the user
// approve hunk-by-hunk and route the result shared/local/reject, and stops at
// writing repo files — it NEVER commits or pushes (the user does that).
//
// Safety default: with empty stdin / EOF / no approval, capture writes NOTHING.
//
// Stdin protocol (one prompt per line, EOF = the safe default):
//   - per hunk:   y = accept this hunk, n = reject it (anything else / EOF = reject)
//   - per file:   route — s = shared, l = local, r = reject (EOF = reject)
//   - blocked (secret) file: route — r = reject, x = out-of-repo secret store
//     (EOF / anything else = reject; never written to shared or local).
func runCapture(c *cobra.Command, _ []string) error {
	// git preflight: capture writes into a git repo the user then commits/pushes.
	// With git absent, fail clearly with install guidance rather than crashing.
	if err := preflightGit(); err != nil {
		return err
	}

	ctx, err := loadContext()
	if err != nil {
		return err
	}

	out := c.OutOrStdout()
	errOut := c.ErrOrStderr()
	in := bufio.NewReader(c.InOrStdin())

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	lastApplied, err := dotfile.OpenStore()
	if err != nil {
		return fmt.Errorf("open last-applied store: %w", err)
	}
	secretStore, err := secret.Open()
	if err != nil {
		return fmt.Errorf("open secret store: %w", err)
	}

	// Converged registry (fn-5): capture drives its per-domain passes off the
	// registry — the terminal preference pass enumerates ResourceDomains (no more
	// {"iterm2","terminal"} literal), and the dotfile/agents passes run only when
	// their FileDomain Captures(). termcfg's Captures() is false, which is why
	// there is no config-file terminal capture pass (the deliberate asymmetry).
	reg := buildRegistry(ctx)

	captured := 0
	offered := 0

	// Each capture PASS is INDEPENDENT and gated on its OWN scope, NOT on the
	// dotfiles domain: a manifest with `iterm2=true` and NO declared dotfiles must
	// still offer the terminal domain, and a machine whose only change is an
	// installed brew package must still re-dump deps/Brewfile.<goos>. So capture
	// does NOT early-return when dotfiles are unmanaged — it runs the dotfile pass
	// (if any dotfiles are declared), the terminal pass (if a terminal domain is in
	// scope, darwin), and the deps re-dump (if the brew domain is in scope), each on
	// its own. "nothing has drifted" is only reported when ALL passes find nothing.

	// --- Dotfile pass: only the in-scope DECLARED dotfiles are considered. An
	// out-of-scope domain is never offered. ~/.ssh is out of scope by design — never
	// enumerated, read, or special-cased. Skipped wholesale when dotfiles are
	// unmanaged (no declarations), but that NO LONGER short-circuits the terminal /
	// deps passes below.
	var dotfileCandidates []string
	if fileDomainCaptures(reg, "dotfiles") {
		dotfileCandidates = declaredDotfilesIfManaged(ctx)
	}
	for _, name := range dotfileCandidates {
		// SECURITY BOUNDARY: TargetFor refuses ~/.ssh + path-traversal names. A
		// refused dotfile is SKIPPED with a clear message and never read/captured —
		// so a manifest declaring `.ssh/config` never reads or ingests ~/.ssh.
		t, terr := dotfile.TargetFor(ctx.RepoPath, home, name)
		if terr != nil {
			fmt.Fprintln(out, refusalWarning(name, terr))
			continue
		}

		// Shared/whole-file capture for this declared dotfile (the .zshrc ->
		// ~/.zshrc comparison). A clean/missing shared file is simply not offered;
		// it does NOT skip the zsh sidecar pass below.
		wroteShared, offeredShared, err := captureSharedDotfile(captureCtx{
			out:         out,
			errOut:      errOut,
			in:          in,
			repoPath:    ctx.RepoPath,
			name:        name,
			target:      t,
			lastApplied: lastApplied,
			secretStore: secretStore,
		})
		if err != nil {
			return err
		}
		if offeredShared {
			offered++
		}
		if wroteShared {
			captured++
		}

		// zsh sidecar: a zsh (include-style) domain models its per-machine layer as
		// the SEPARATE overlay target ~/.<bare>.local (status/apply treat it as its
		// own overlay). The shared pass above only diffs the shared file (.zshrc ->
		// ~/.zshrc), so an edited ~/.zshrc.local would never be offered — status
		// reports its drift and then dead-ends. Here we ALSO offer the sidecar
		// (live ~/.<bare>.local vs repo local/zsh/<bare>.local) through the same
		// secret-scan + review gate, routed to the local overlay, so the user can
		// capture it back. Gated on a repo overlay existing (apply materialises the
		// sidecar only when local/zsh/<bare>.local is present). Runs INDEPENDENTLY
		// of the shared file's drift state.
		if usesIncludeSidecar(strings.TrimPrefix(name, ".")) {
			wroteSide, offeredSide, err := captureZshSidecar(captureCtx{
				out:         out,
				errOut:      errOut,
				in:          in,
				repoPath:    ctx.RepoPath,
				name:        name,
				target:      t,
				lastApplied: lastApplied,
				secretStore: secretStore,
			}, home)
			if err != nil {
				return err
			}
			if offeredSide {
				offered++
			}
			if wroteSide {
				captured++
			}
		}
	}

	// --- Agents pass: live edits to deployed agent files flow back through the
	// same approve + route (shared vs local) flow as dotfiles. A locally-drifted
	// target is reviewed hunk by hunk and routed; a TRUE DIVERGENCE (the deployed
	// file AND its repo source both moved past the last-deployed baseline) is
	// refused with a diff — never auto-merged. New agent-shaped files under a
	// tracked mapping's target dir are offered for adoption. A SourceCombined
	// render cannot be decomposed, so its drift is reported, not captured.
	if fileDomainCaptures(reg, "agents") && ctx.Scope.IsManaged("agents") {
		wroteAgents, offeredAgents, aerr := captureAgents(ctx, home, in, out, lastApplied)
		if aerr != nil {
			return aerr
		}
		offered += offeredAgents
		captured += wroteAgents
	}

	// Terminal/plist preference domains (A3): the dotfile loop above only handles
	// declared DOTFILES, so a live iTerm2 / Apple Terminal preference change is
	// NEVER captured by it — apply re-exports those as opaque plist DOMAINS, so they
	// must be captured WHOLE-DOMAIN (an opaque plist can't be hunk-merged). On darwin
	// only, for each IN-SCOPE terminal domain whose live state differs from the repo,
	// offer the exported plist as a whole-domain accept/reject and route it
	// shared/local/reject, secret-gated as a whole value. macOS-only: a clean no-op
	// on linux (platform-guarded; internal/terminal builds clean there).
	if platform.IsDarwin() {
		for _, rd := range reg.ResourceDomains {
			domain := rd.Name()
			if !ctx.Scope.IsManaged(domain) {
				continue
			}
			wroteDom, offeredDom, derr := captureTerminalDomain(captureCtx{
				out:         out,
				errOut:      errOut,
				in:          in,
				repoPath:    ctx.RepoPath,
				secretStore: secretStore,
			}, domain)
			if derr != nil {
				return derr
			}
			if offeredDom {
				offered++
			}
			if wroteDom {
				captured++
			}
		}
	}

	// --- Deps re-dump pass: an INDEPENDENT capture step. The deps manifest is
	// re-dumped whenever the brew domain is in scope, REGARDLESS of whether any
	// dotfile/terminal change was accepted above — a machine whose ONLY change is an
	// installed brew package must still update deps/Brewfile.<goos>. (Previously this
	// ran only after a non-deps change was accepted, so a deps-only machine reported
	// "nothing has drifted" and never re-dumped.) reDumpDeps reports the file it
	// wrote and counts as both an offered AND a captured change so the summary below
	// reflects it. A missing/out-of-scope manager is a clean skip (no offer).
	if reDumpDeps(ctx, out) {
		offered++
		captured++
	}

	// --- npm globals re-dump pass: an INDEPENDENT capture step ORTHOGONAL to the
	// brew/apt re-dump above (a machine can carry both). Gated on the npm-globals
	// domain being in scope AND npm being present; a missing npm is a clean skip.
	if reDumpNpmGlobals(ctx, out) {
		offered++
		captured++
	}

	if offered == 0 {
		fmt.Fprintln(out, "capture: nothing has drifted; repo already matches this machine")
		return nil
	}
	if captured == 0 {
		fmt.Fprintln(out, "capture: no changes captured")
		return nil
	}

	fmt.Fprintf(out, "capture: wrote %d change(s) into the repo. Review with `git -C %s status`, then commit/push yourself (ferry never commits or pushes).\n", captured, ctx.RepoPath)
	return nil
}

// declaredDotfilesIfManaged returns the in-scope declared dotfiles when the
// dotfiles domain is managed, else nil. It lets runCapture run the dotfile pass
// without an early-return that would also skip the independent terminal/deps
// passes: a manifest with only terminal/deps domains (no declared dotfiles)
// yields an empty dotfile pass yet still reaches those passes.
func declaredDotfilesIfManaged(ctx *cmdContext) []string {
	if !ctx.Scope.IsManaged("dotfiles") {
		return nil
	}
	return ctx.Scope.DeclaredDotfiles()
}

// preflightGit checks that git is on PATH (capture writes into a git repo the
// user then commits/pushes). When absent it returns an actionable, OS-aware
// install hint rather than letting a later git call crash opaquely.
func preflightGit() error {
	if _, err := exec.LookPath("git"); err == nil {
		return nil
	}
	hint := "install git, then re-run"
	switch runtime.GOOS {
	case "darwin":
		hint = "install git with `xcode-select --install` or `brew install git`, then re-run"
	case "linux":
		hint = "install git with your package manager (e.g. `apt install git`), then re-run"
	}
	return fmt.Errorf("git is required but was not found on PATH: %s (see https://git-scm.com)", hint)
}

// captureCtx bundles the per-candidate inputs for captureOne.
type captureCtx struct {
	out         io.Writer
	errOut      io.Writer
	in          *bufio.Reader
	repoPath    string
	name        string
	target      dotfile.Target
	repoSource  string
	repoBytes   []byte
	liveBytes   []byte
	lastApplied *dotfile.Store
	secretStore *secret.Store

	// placeholderAware: the repo source carries placeholders and every ref
	// resolved — repoBytes is the SOURCE (placeholders intact) and liveBytes is
	// the REVERSE-RENDERED live content (render-and-splice, r7-M1/r8-C1).
	placeholderAware bool
	// missingRefFallback: the source carries placeholders but a ref is missing
	// (r8-M2): capture runs today's raw-source behavior, and a gate block is
	// reported READ-ONLY (no whole-file store escape — r9-M1).
	missingRefFallback bool
}

// captureErrOut returns the ctx's stderr writer, defaulting to the main writer.
func (cc captureCtx) captureErrOut() io.Writer {
	if cc.errOut != nil {
		return cc.errOut
	}
	return cc.out
}

// captureOne reviews and routes a single drifted dotfile. It returns whether it
// wrote anything to the repo. It runs the MANDATORY secret gate before any write
// decision: a high-confidence secret is blocked from BOTH shared and local and
// only reject / out-of-repo secret store are offered.
func captureOne(cc captureCtx) (bool, error) {
	bare := strings.TrimPrefix(cc.name, ".")
	zsh := usesIncludeSidecar(bare)
	extractSpans := captureSpanExtractor(bare)
	fmt.Fprintf(cc.out, "\n=== %s (drifted) ===\n", "."+bare)

	// --- MANDATORY secret gate, BEFORE any write. ---
	// Scan the reviewable content; a high-confidence secret blocks every repo
	// route. For a span-routable candidate (a placeholder-aware source, or tmux
	// with its column-grained recogniser) cc.liveBytes carries the NEW secret
	// only — stored secrets sit behind their intact placeholders and never reach
	// the gate — and its consent choice comes AFTER the hunk review (the
	// span-grained blocked path, r6-M1). A non-span-routable candidate falls back
	// to the whole-file block. In the missing-ref fallback a block is READ-ONLY
	// (r9-M1: no whole-file store escape for a placeholder-bearing source).
	gate := secret.GateText(string(cc.liveBytes))
	var hunkMasks []maskPair
	if gate.BlockedFromRepo {
		if cc.missingRefFallback {
			reportReadOnlyBlock(cc.out, "."+bare)
			return false, nil
		}
		if !spanRoutable(bare, cc.placeholderAware) {
			return captureBlocked(cc, bare)
		}
		// Falling through to the hunk review with a gated NEW secret in the
		// reviewable content: MASK every flagged span value in all hunk output
		// (the wizard preview-masking contract — the raw value must never
		// print, on any stream, before or after the consent prompt).
		hunkMasks = captureSecretMasks(string(cc.liveBytes), bare)
	}

	// --- Hunk-by-hunk review: accept/reject each change independently. ---
	hunks := diffHunks(string(cc.repoBytes), string(cc.liveBytes))
	accepted := make([]bool, len(hunks))
	anyAccepted := false
	for i, h := range hunks {
		fmt.Fprintf(cc.out, "\n--- hunk %d/%d ---\n%s", i+1, len(hunks), maskCaptureText(renderHunk(h), hunkMasks))
		ans := prompt(cc.in, cc.out, "accept this hunk? [y]es / [n]o (default n): ")
		if ans == "y" || ans == "yes" {
			accepted[i] = true
			anyAccepted = true
		}
	}
	if !anyAccepted {
		fmt.Fprintf(cc.out, "  %s: no hunks accepted; nothing written\n", "."+bare)
		return false, nil
	}
	allAccepted := true
	for _, a := range accepted {
		if !a {
			allAccepted = false
			break
		}
	}

	// Compose the captured content = repo content with accepted hunks applied.
	captured := applyHunks(string(cc.repoBytes), hunks, accepted, endsWithNewline(cc.liveBytes))
	spanPatched := false
	var storedRefs []string // refs Put by the span consent (for honest refusal notices)

	// Re-scan the COMPOSED content too: an accepted hunk might carry a secret even
	// if the whole-file scan was driven differently. Never write a secret.
	if secret.IsBlockedFromRepo(captured) {
		if !spanRoutable(bare, cc.placeholderAware) {
			fmt.Fprintf(cc.out, "  %s: accepted change contains secret/credential material — blocked from the repo; handled out-of-band only\n", "."+bare)
			return false, nil
		}
		// Span-routable source + a NEW secret in the accepted change: the
		// span-grained consent path (r6-M1) — store ONLY the new span(s), patch
		// only those spans (a tmux quoted option value is patched column-grained,
		// preserving the `set -g @token '…'` syntax), preserve the curated
		// remainder. Never the whole-file escape (it would overwrite the source).
		patched, refs, ok, cerr := consentSpanStoreRoute(cc.in, cc.out, cc.secretStore, "."+bare, bare, captured, extractSpans)
		if cerr != nil {
			return false, cerr
		}
		if !ok {
			return false, nil
		}
		captured = patched
		storedRefs = refs
		// The route branches below must see the PATCHED composition: the zsh
		// local-route delta is rebuilt from `captured` (see spanPatched), so a
		// store-then-local ([x] then [l]) sequence carries the placeholder into
		// the sidecar instead of refusing on the raw value it just stored.
		spanPatched = true
	}

	// --- Route the accepted result. ---
	route := promptRoute(cc.in, cc.out)
	switch route {
	case secret.RouteShared:
		// For a zsh include-style dotfile the captured content was composed against
		// the EFFECTIVE shared bytes (raw + ferry's injected `source ~/.<bare>.local`
		// line), so strip that managed boilerplate before writing shared — the
		// committed shared source must hold the user's edits, never ferry's generated
		// include line (apply re-appends it on deploy). Non-zsh dotfiles are unchanged.
		sharedOut := []byte(captured)
		if zsh {
			sharedOut = stripSourceDirective(sharedOut, "."+bare+".local", directiveSpecFor(bare))
		}
		// git identity firewall on the shared-capture WRITE: even if the user
		// accepted an identity hunk and routed it [s]hared, strip every identity key
		// + [includeIf …] block so a machine's commit identity can never be written
		// into the shared repo (the STOP condition). A no-op for every other dotfile.
		sharedOut = sharedGitTransform(bare, sharedOut)
		// Write the captured content to every existing shared copy of this dotfile
		// (canonical dotfiles/<bare>, dotted dotfiles/.<bare>, top-level .<bare>) so
		// the committed shared source is consistent regardless of repo layout, plus
		// the canonical path so a fresh repo still gets dotfiles/<bare>.
		for _, dest := range sharedTargets(cc.repoPath, bare) {
			if err := writeRepoFile(cc.repoPath, dest, sharedOut); err != nil {
				return false, err
			}
		}
		fmt.Fprintf(cc.out, "  %s: captured -> shared (%s)\n", "."+bare, relTo(cc.repoPath, cc.repoSource))
		// last-applied for a SHARED capture must record the hash of the EFFECTIVE
		// DEPLOYED content — the same bytes apply writes to ~/.<bare> and status
		// classifies against — NOT the raw repo file. For a zsh include-style
		// dotfile the deployed content is the shared file with ferry's managed
		// `source ~/.<bare>.local` line re-injected last (apply re-appends it on
		// deploy), so the raw stripped repo source does NOT equal the live ~/.<bare>;
		// recording the raw repo hash would leave last-applied behind and a later
		// repo edit would misclassify as a CONFLICT. Stage the effective bytes (the
		// captured composition — it already carries the injected source line) and
		// advance last-applied against THOSE, mirroring how apply records last-applied
		// for zsh (effective, source-last). For a placeholder-bearing source the
		// staged bytes are additionally RENDERED (r4-M2): last-applied reflects the
		// rendered-effective content, so a post-capture status reads Clean and a
		// later repo edit classifies RepoAhead, never conflict. UpdateLastApplied
		// advances only on a full reproduction (live == effective), so a partial
		// capture still leaves the remaining drift reported. Non-zsh dotfiles
		// without placeholders fall through to the raw-repo path below (their
		// deployed content IS the raw repo file).
		if zsh || cc.placeholderAware {
			staged := []byte(captured)
			// Record the hash of the RENDERED-EFFECTIVE bytes — the same bytes apply
			// deploys — whenever the captured composition carries a placeholder:
			// either from a placeholder-aware source (r4-M2) OR from a span-store
			// consent that just patched a NEW secret to a placeholder (spanPatched,
			// e.g. a tmux `set -g @token '…'` value). Without rendering here,
			// last-applied would hold the un-rendered placeholder hash while the
			// deploy writes the rendered value, so status would show spurious drift.
			// renderForLastApplied no-ops on content without placeholders, so the
			// common zsh path (no secrets) is byte-for-byte unchanged.
			if cc.placeholderAware || spanPatched {
				staged = renderForLastApplied(cc.secretStore, staged)
			}
			if err := recordEffectiveLastApplied(cc.target, cc.lastApplied, staged); err != nil {
				return false, err
			}
			return true, nil
		}
	case secret.RouteLocal:
		// Guarantee the local layer is gitignored AT the moment capture creates it,
		// so the overlay never lands as a tracked file (capture, not init, creates
		// local/<domain>/ content here). Idempotent: no duplicate .gitignore lines.
		if err := ensureLocalLayerIgnored(cc.repoPath); err != nil {
			return false, err
		}
		// Per-domain local routing (PLAN "Per-domain overlay strategy" +
		// ACCEPTANCE AC-capture-hunk-by-hunk): hunk-level local routing is valid
		// ONLY for a domain with a real include/append point.
		//   - zsh HAS an include point, so its local layer is the SIDECAR
		//     local/zsh/<bare>.local — apply sources it last so it wins. The
		//     sidecar is a per-machine DELTA, NOT the composed whole file: writing
		//     the composed file would re-run ALL of shared a SECOND time (once as
		//     ~/.zshrc, then again via the sourced ~/.zshrc.local). So the sidecar
		//     holds ONLY the accepted local hunks' added/changed lines (each hunk's
		//     new content / the '+' side), accumulated in order — a standalone shell
		//     fragment that layers the machine-specific lines on top of shared
		//     WITHOUT re-running it. Accepting one hunk and rejecting another
		//     contributes ONLY the accepted hunks' lines; the shared repo file is
		//     NOT touched on a local route.
		//   - generic dotfiles have NO include mechanism, so their only local
		//     strategy is WHOLE-FILE replacement at dotfile.LocalOverlayPath
		//     (local/<domain>/<bare>, the SAME path apply reads). There is no merge
		//     point at which a rejected hunk could be excluded, so the overlay is
		//     all-or-nothing whole-file: we must NEVER write the live file when any
		//     hunk was rejected (that would land rejected content). A partial
		//     accept is therefore refused for generic-local — write the WHOLE live
		//     file only when EVERY hunk was accepted (live == captured), else
		//     nothing.
		if zsh {
			// Sidecar = DELTA only: the added/changed lines of the ACCEPTED hunks,
			// in order. NOT the composed whole file (which would re-run shared via
			// the sourced ~/.zshrc.local). localHunkDelta gathers each accepted
			// hunk's new lines; a rejected hunk contributes nothing.
			//
			// REFUSE delete-only accepted hunks first: the sidecar is ADDITIVE
			// (sourced AFTER shared), so it can only ADD/override lines, never
			// UN-set a line shared already set. An accepted hunk whose sole effect
			// is removing a shared line (oldLines, no newLines) cannot be expressed
			// by the overlay — silently dropping it would report success while
			// capturing nothing. Refuse with a clear message and suggest [s]hared
			// (which edits the shared file) instead. Additive/changed hunks still
			// flow to the sidecar below.
			// After a span-grained store consent the ORIGINAL hunks still carry
			// the raw secret the user just stored — deriving the delta from them
			// would refuse the local capture and leave an ORPHANED store entry.
			// Rebuild the hunks from the PATCHED captured composition instead
			// (repo -> captured is exactly the accepted, placeholder-patched
			// change set); without a patch the original selection stands.
			deltaHunks, deltaAccepted := hunks, accepted
			if spanPatched {
				deltaHunks = diffHunks(string(cc.repoBytes), captured)
				deltaAccepted = make([]bool, len(deltaHunks))
				for i := range deltaAccepted {
					deltaAccepted[i] = true
				}
			}
			if idx, ok := firstDeleteOnlyAccepted(deltaHunks, deltaAccepted); ok {
				fmt.Fprintf(cc.out, "  %s: accepted hunk %d only DELETES shared line(s) and cannot be captured to the zsh `.local` overlay — that overlay is sourced after shared, so it can only ADD/override lines, never remove one shared already set. Re-run and route this change to [s]hared (which edits the shared file) to drop the line, or reject it.\n", "."+bare, idx+1)
				notifyStoredNotWritten(cc.out, "."+bare, storedRefs)
				return false, nil
			}
			delta := localHunkDelta(deltaHunks, deltaAccepted)
			if secret.IsBlockedFromRepo(delta) {
				fmt.Fprintf(cc.out, "  %s: accepted change contains secret/credential material — blocked from the repo; handled out-of-band only\n", "."+bare)
				notifyStoredNotWritten(cc.out, "."+bare, storedRefs)
				return false, nil
			}
			dest := localOverlayPath(cc.repoPath, bare)
			if err := writeRepoFile(cc.repoPath, dest, []byte(delta)); err != nil {
				return false, err
			}
			fmt.Fprintf(cc.out, "  %s: captured -> local (%s, gitignored)\n", "."+bare, relTo(cc.repoPath, dest))
			// A local overlay does not advance last-applied (shared output unchanged).
			return true, nil
		}
		if !allAccepted {
			fmt.Fprintf(cc.out, "  %s: local overlay for this dotfile is WHOLE-FILE (no include point); a partial-hunk local capture is refused so rejected content never lands. Re-run and accept the whole change, or route to shared.\n", "."+bare)
			notifyStoredNotWritten(cc.out, "."+bare, storedRefs)
			return false, nil
		}
		// The whole-file local overlay must carry the SPAN-PATCHED composition
		// when the consent path patched it — writing cc.liveBytes would land the
		// RAW secret the user just stored into the repo worktree (local/ is
		// gitignored but still inside the repo). Without a patch, allAccepted
		// means the composition reproduces the live file, and the live bytes are
		// written byte-verbatim as before.
		localContent := cc.liveBytes
		if spanPatched {
			localContent = []byte(captured)
		}
		// Gate exactly what is written (defense-in-depth; the patched
		// composition was already re-gated by the consent path).
		if secret.IsBlockedFromRepo(string(localContent)) {
			fmt.Fprintf(cc.out, "  %s: accepted change contains secret/credential material — blocked from the repo; handled out-of-band only\n", "."+bare)
			notifyStoredNotWritten(cc.out, "."+bare, storedRefs)
			return false, nil
		}
		dest := dotfile.LocalOverlayPath(cc.repoPath, bare, cc.name)
		if err := writeRepoFile(cc.repoPath, dest, localContent); err != nil {
			return false, err
		}
		fmt.Fprintf(cc.out, "  %s: captured -> local (%s, gitignored)\n", "."+bare, relTo(cc.repoPath, dest))
		// A local overlay does not make the shared managed output reproduce the
		// live file, so do not advance last-applied here.
		return true, nil
	default:
		fmt.Fprintf(cc.out, "  %s: rejected; nothing written\n", "."+bare)
		notifyStoredNotWritten(cc.out, "."+bare, storedRefs)
		return false, nil
	}

	// last-applied update (NON-zsh SHARED route only — a zsh shared capture already
	// recorded the EFFECTIVE deployed hash above and returned). For a generic
	// whole-file dotfile the deployed content IS the raw repo source, so classify
	// against cc.repoSource: UpdateLastApplied advances only when live == repo (full
	// reproduction); on a partial capture it leaves the record so status keeps
	// reporting the remaining drift.
	t := cc.target
	t.Repo = cc.repoSource
	if _, err := dotfile.UpdateLastApplied(t, cc.lastApplied); err != nil {
		return false, err
	}
	return true, nil
}

// recordEffectiveLastApplied advances the last-applied record for target t to the
// hash of the EFFECTIVE DEPLOYED content (the bytes apply materialises to t.Home
// and status classifies against), not the raw repo file. The bytes are hashed
// IN MEMORY (dotfile.UpdateLastAppliedContent) — never staged to a temp file —
// so the record advances ONLY when the live home file already equals effective
// (a full reproduction), and is left put on a partial capture so the remaining
// drift keeps being reported. This mirrors apply's last-applied
// recording for zsh, where the deployed bytes are the shared file with ferry's
// managed `source ~/.<bare>.local` line appended last: after a full shared capture
// that reproduces the live ~/.<bare>, status reads CLEAN and a later repo edit
// classifies as repo-ahead (not a spurious conflict).
func recordEffectiveLastApplied(t dotfile.Target, store *dotfile.Store, effective []byte) error {
	// IN MEMORY, deliberately: the effective bytes may be secret-RENDERED
	// (placeholder-bearing sources), and staging them to a temp file just to
	// hash would leave rendered secrets in $TMPDIR across a crash window.
	// UpdateLastAppliedContent hashes the bytes directly, exactly like the
	// status/diff path's ClassifyContent.
	if _, err := dotfile.UpdateLastAppliedContent(t, effective, store); err != nil {
		return err
	}
	return nil
}

// captureBlocked handles a candidate whose live content holds a high-confidence
// secret. It is NEVER written to shared or local; only reject or the out-of-repo
// secret store are offered. The user-facing message ties the block to SECRET
// material and names the out-of-band path.
func captureBlocked(cc captureCtx, bare string) (bool, error) {
	fmt.Fprintf(cc.out, "  %s: SECRET / credential material detected (e.g. a private key or token).\n", "."+bare)
	fmt.Fprintf(cc.out, "  This change is BLOCKED from the repo entirely (both shared and local). It is never committed.\n")
	fmt.Fprintf(cc.out, "  Only the out-of-band path is offered: [r]eject, or route to the out-of-repo secret store [x].\n")

	ans := prompt(cc.in, cc.out, "route blocked change? [r]eject / secret-store [x] (default r): ")
	if ans != "x" {
		fmt.Fprintf(cc.out, "  %s: rejected; secret kept out of the repo\n", "."+bare)
		return false, nil
	}

	// Out-of-repo secret store: store each detected reference's value out of band
	// and leave a placeholder in the committed file. We key the ref by domain.key
	// = "<bare>.captured" since the dotfile is whole-file opaque here. The secret
	// value never enters the repo; only the placeholder does.
	ref := bare + ".captured"
	if err := cc.secretStore.Put(ref, string(cc.liveBytes)); err != nil {
		return false, fmt.Errorf("write to secret store: %w", err)
	}
	placeholder := secret.Placeholder(ref)
	if err := writeRepoFile(cc.repoPath, cc.repoSource, []byte(placeholder+"\n")); err != nil {
		return false, err
	}
	fmt.Fprintf(cc.out, "  %s: secret stored out-of-band in ~/.config/ferry/secrets-local; a placeholder was written to the repo\n", "."+bare)
	return true, nil
}

// captureSharedDotfile runs the shared/whole-file capture for one declared
// dotfile: resolve the repo source, classify against last-applied, and offer the
// drifted candidate through captureOne. It returns (wrote, offered, err) so the
// caller tallies the same counters. A clean/missing/repo-ahead target (or no repo
// source on disk) is simply not offered — and, crucially, does NOT short-circuit
// the zsh sidecar pass the caller runs next. This is the extracted shared half of
// the loop; behaviour for the shared file is unchanged.
func captureSharedDotfile(cc captureCtx) (wrote bool, offered bool, err error) {
	src, ok := resolveCaptureSource(cc.repoPath, cc.name)
	firstCapture := false
	if !ok {
		// No repo source on disk yet (the Fresh "capture this machine" flow: init
		// declared .zshrc in scope but seeded NO deployable source, so it could
		// never zero a real file). This is the FIRST capture: the repo side is an
		// empty base and the whole live file is the candidate. Target the canonical
		// dotfiles/<bare> path the shared write will create.
		src = filepath.Join(cc.repoPath, dotfile.RepoSubdir, strings.TrimPrefix(cc.name, "."))
		firstCapture = true
	}
	// Guard the repo source against a symlink that escapes the repo / points under
	// ~/.ssh BEFORE reading it: os.ReadFile follows symlinks, so without this a
	// dotfiles/<name> symlinked to ~/.ssh/config would be read.
	src, err = safeRepoPath(cc.repoPath, src)
	if err != nil {
		return false, false, err
	}
	t := cc.target
	t.Repo = src

	var repoBytes []byte
	if !firstCapture {
		repoBytes, err = os.ReadFile(src)
		if err != nil {
			return false, false, err
		}
	}

	// EFFECTIVE shared content: for an include-style zsh dotfile with a local
	// overlay, apply deploys the shared file with ferry's managed `source
	// ~/.<bare>.local` directive appended last. That managed line lives in the live
	// ~/.<bare> but NOT in the raw repo source, so diffing the raw source would
	// present ferry's OWN boilerplate as a user hunk — routing it [s]hared would
	// commit ferry's generated line into the shared repo, and [l]ocal would write a
	// self-sourcing sidecar. Classify and diff against the SAME effective bytes
	// apply/status compute so only GENUINE user edits (beyond the managed include
	// line) are offered. Non-zsh dotfiles use the raw source unchanged.
	bare := strings.TrimPrefix(cc.name, ".")
	if usesIncludeSidecar(bare) && !firstCapture {
		_, hasOverlay := resolveOverlaySource(cc.repoPath, bare)
		repoBytes = effectiveZshShared(repoBytes, bare, hasOverlay)
	}

	// REVERSE-RENDER (placeholder-bearing sources, F3-2): render the effective
	// source in memory and compare/classify against the RENDERED bytes — the
	// same bytes apply deploys — so a store-routed secret never gate-blocks its
	// own round-trip. When a ref is missing (new machine, unpopulated store) the
	// conservative raw-source baseline runs instead (r8-M2), noted on stderr.
	liveBytes, lerr := os.ReadFile(t.Home)
	if lerr != nil {
		liveBytes = nil // absence/unreadability is classified below, not an error here
	}
	compareBytes := repoBytes
	reviewLive := liveBytes
	placeholderAware, missingRef := false, false
	if !firstCapture && liveBytes != nil {
		compareBytes, reviewLive, placeholderAware, missingRef, err = prepareReverseRender(
			cc.secretStore, "shared source for ."+bare, repoBytes, liveBytes, cc.captureErrOut())
		if err != nil {
			return false, false, err
		}
	}

	// Detect drift: a locally-drifted/conflict target is a capture candidate.
	// Classify against the effective (rendered) bytes, not the raw repo source,
	// so neither ferry's managed include line nor a rendered placeholder is
	// seen as drift. The FIRST-capture case (no repo source yet, live file
	// present) is also a candidate: the whole live file is offered against the
	// empty base, so the Fresh "capture this machine" flow can seed the repo
	// from a live dotfile.
	st, err := dotfile.ClassifyContent(t, compareBytes, cc.lastApplied)
	if err != nil {
		return false, false, err
	}
	if !firstCapture {
		if st.State != dotfile.StateLocallyDrifted && st.State != dotfile.StateConflict {
			return false, false, nil
		}
	}
	if !st.LiveExists {
		return false, false, nil
	}
	if liveBytes == nil {
		if liveBytes, err = os.ReadFile(t.Home); err != nil {
			return false, false, err
		}
	}
	if string(compareBytes) == string(liveBytes) {
		return false, false, nil
	}

	wrote, err = captureOne(captureCtx{
		out:                cc.out,
		errOut:             cc.errOut,
		in:                 cc.in,
		repoPath:           cc.repoPath,
		name:               cc.name,
		target:             t,
		repoSource:         src,
		repoBytes:          repoBytes,
		liveBytes:          reviewLive,
		lastApplied:        cc.lastApplied,
		secretStore:        cc.secretStore,
		placeholderAware:   placeholderAware,
		missingRefFallback: missingRef,
	})
	if err != nil {
		return false, true, err
	}
	return wrote, true, nil
}

// captureZshSidecar offers the zsh per-machine SIDECAR (~/.<bare>.local) for
// capture back into the repo overlay (local/zsh/<bare>.local). It is the missing
// half of capture: apply materialises the sidecar from local/zsh/<bare>.local and
// status reports its drift, but the main capture loop only diffs the shared file,
// so an edited ~/.zshrc.local could never be captured. This closes that loop.
//
// The sidecar is a WHOLE-FILE overlay (apply deploys local/zsh/<bare>.local to
// ~/.<bare>.local verbatim — it is NOT appended to shared), so capture is the
// symmetric whole-file ingest: review the live file, secret-scan it (a detected
// secret routes to reject/secret-store, NEVER the repo overlay), and on accept
// write the whole live file to local/zsh/<bare>.local, then advance last-applied
// for the "<bare>.local" target so status reports clean afterwards.
//
// It only offers when (a) a repo overlay already exists at local/zsh/<bare>.local
// (apply only materialises the sidecar then) and (b) the live sidecar drifted from
// it. Returns (wrote, offered, err): offered=true when a drifted candidate was
// presented (so the caller counts it even on a reject).
func captureZshSidecar(cc captureCtx, home string) (wrote bool, offered bool, err error) {
	bare := strings.TrimPrefix(cc.name, ".")
	extractSpans := captureSpanExtractor(bare)

	overlaySrc := localOverlayPath(cc.repoPath, bare) // local/zsh/<bare>.local
	// Guard the overlay path (read below AND written by the sidecar capture routes)
	// against an escaping/~.ssh symlink BEFORE the existence probe — the probe must
	// not os.Stat a symlinked overlay into ~/.ssh. safeRepoPath refuses any symlink
	// component, so the subsequent Lstat-based probe only ever sees a safe path.
	if _, err := safeRepoPath(cc.repoPath, overlaySrc); err != nil {
		return false, false, err
	}
	if !regularRepoFile(cc.repoPath, overlaySrc) {
		// No repo overlay: apply never materialises the sidecar, so there is no
		// managed sidecar output to capture against.
		return false, false, nil
	}
	sidecarHome := filepath.Join(home, "."+bare+".local")

	// The sidecar overlay target mirrors apply/status: Name "<bare>.local" so its
	// last-applied record keys the same slot status classifies.
	ot := dotfile.Target{
		Name: bare + ".local",
		Repo: overlaySrc,
		Home: sidecarHome,
	}

	repoBytes, err := os.ReadFile(overlaySrc)
	if err != nil {
		return false, false, err
	}
	liveBytes, lerr := os.ReadFile(sidecarHome)
	if lerr != nil {
		liveBytes = nil // absence is classified below, not an error here
	}

	// REVERSE-RENDER on the SIDECAR leg too (Codex M2): a placeholder-bearing
	// overlay is compared and reviewed via its rendered bytes, so a local-routed
	// stored secret never gate-blocks the sidecar's own round-trip.
	compareBytes := repoBytes
	reviewLive := liveBytes
	placeholderAware, missingRef := false, false
	if liveBytes != nil {
		compareBytes, reviewLive, placeholderAware, missingRef, err = prepareReverseRender(
			cc.secretStore, "sidecar overlay for ."+bare+".local", repoBytes, liveBytes, cc.captureErrOut())
		if err != nil {
			return false, false, err
		}
	}

	st, err := dotfile.ClassifyContent(ot, compareBytes, cc.lastApplied)
	if err != nil {
		return false, false, err
	}
	if st.State != dotfile.StateLocallyDrifted && st.State != dotfile.StateConflict {
		return false, false, nil
	}
	if !st.LiveExists || liveBytes == nil {
		return false, false, nil
	}
	if string(compareBytes) == string(liveBytes) {
		return false, false, nil
	}

	offered = true
	fmt.Fprintf(cc.out, "\n=== %s (drifted) ===\n", "."+bare+".local")

	// MANDATORY secret gate before any write: a high-confidence secret in the
	// reviewable sidecar content is blocked from the repo overlay entirely.
	// Placeholder-aware capture reviews the REVERSE-RENDERED bytes (stored
	// secrets stay behind their placeholders; only NEW material trips the
	// gate, consented span-grained after the hunk review). The missing-ref
	// fallback reports the block READ-ONLY (r9-M1).
	gate := secret.GateText(string(reviewLive))
	var hunkMasks []maskPair
	if gate.BlockedFromRepo {
		if missingRef {
			reportReadOnlyBlock(cc.out, "."+bare+".local")
			return false, true, nil
		}
		if !spanRoutable(bare, placeholderAware) {
			w, berr := captureBlockedSidecar(cc, bare, overlaySrc, liveBytes)
			return w, true, berr
		}
		// Gated NEW secret on a span-routable sidecar leg: mask every flagged
		// span value in the hunk output (never print the raw value).
		hunkMasks = captureSecretMasks(string(reviewLive), bare+".local")
	}

	// Hunk-by-hunk review of the whole-file sidecar (repo overlay -> live),
	// in SOURCE coordinates for a placeholder-aware capture.
	hunks := diffHunks(string(repoBytes), string(reviewLive))
	accepted := make([]bool, len(hunks))
	anyAccepted := false
	for i, h := range hunks {
		fmt.Fprintf(cc.out, "\n--- hunk %d/%d ---\n%s", i+1, len(hunks), maskCaptureText(renderHunk(h), hunkMasks))
		ans := prompt(cc.in, cc.out, "accept this hunk? [y]es / [n]o (default n): ")
		if ans == "y" || ans == "yes" {
			accepted[i] = true
			anyAccepted = true
		}
	}
	if !anyAccepted {
		fmt.Fprintf(cc.out, "  %s: no hunks accepted; nothing written\n", "."+bare+".local")
		return false, true, nil
	}
	allAccepted := true
	for _, a := range accepted {
		if !a {
			allAccepted = false
			break
		}
	}

	composed := applyHunks(string(repoBytes), hunks, accepted, endsWithNewline(reviewLive))
	var sidecarStoredRefs []string // refs Put by the span consent (for honest refusal notices)
	// Re-scan the composed result: never write secret material to the overlay.
	if secret.IsBlockedFromRepo(composed) {
		if !spanRoutable(bare, placeholderAware) {
			fmt.Fprintf(cc.out, "  %s: accepted change contains secret/credential material — blocked from the repo; handled out-of-band only\n", "."+bare+".local")
			return false, true, nil
		}
		// Span-grained consent for a NEW secret on a span-routable sidecar leg
		// (r6-M1) — the whole-file escape is never taken here.
		patched, refs, ok, cerr := consentSpanStoreRoute(cc.in, cc.out, cc.secretStore, "."+bare+".local", bare+".local", composed, extractSpans)
		if cerr != nil {
			return false, true, cerr
		}
		if !ok {
			return false, true, nil
		}
		composed = patched
		sidecarStoredRefs = refs
	}

	// Route confirmation: the sidecar IS the local overlay, so the only repo route
	// is local (no shared). Offer [l]ocal / [r]eject so the user keeps an explicit
	// opt-in; EOF / anything else rejects (safe default).
	ans := prompt(cc.in, cc.out, "capture this sidecar to the repo overlay? [l]ocal / [r]eject (default r): ")
	if ans != "l" && ans != "local" {
		fmt.Fprintf(cc.out, "  %s: rejected; nothing written\n", "."+bare+".local")
		notifyStoredNotWritten(cc.out, "."+bare+".local", sidecarStoredRefs)
		return false, true, nil
	}

	// Guarantee the local layer is gitignored AT the moment this sidecar overlay is
	// created, exactly like every other local-capture route — local stays out of git.
	// Idempotent and guard-protected; a damaged/absent .gitignore is repaired before
	// the per-machine overlay lands so it can never be committed.
	if err := ensureLocalLayerIgnored(cc.repoPath); err != nil {
		return false, true, err
	}
	if err := writeRepoFile(cc.repoPath, overlaySrc, []byte(composed)); err != nil {
		return false, true, err
	}
	fmt.Fprintf(cc.out, "  %s: captured -> local (%s, gitignored)\n", "."+bare+".local", relTo(cc.repoPath, overlaySrc))

	// Advance last-applied for the sidecar ONLY on a full reproduction (every hunk
	// accepted, so the overlay now equals live). On a partial capture leave it put
	// so status keeps reporting the remaining drift. For a placeholder-bearing
	// overlay the staged bytes are RENDERED first (r4-M2): last-applied then
	// reflects the rendered-effective content — the bytes apply materialises —
	// so a post-capture status reads Clean and a later repo edit classifies
	// RepoAhead, never conflict. recordEffectiveLastApplied/UpdateLastApplied
	// re-check live == staged themselves, so this is also safe if composed differs.
	if allAccepted {
		staged := []byte(composed)
		if placeholderAware {
			staged = renderForLastApplied(cc.secretStore, staged)
		}
		if err := recordEffectiveLastApplied(ot, cc.lastApplied, staged); err != nil {
			return false, true, err
		}
	}
	return true, true, nil
}

// captureBlockedSidecar handles a zsh sidecar whose live content holds a
// high-confidence secret. It is NEVER written to the repo overlay; only reject or
// the out-of-repo secret store are offered, mirroring captureBlocked but keyed to
// the sidecar overlay file rather than the shared source.
func captureBlockedSidecar(cc captureCtx, bare, overlaySrc string, liveBytes []byte) (bool, error) {
	fmt.Fprintf(cc.out, "  %s: SECRET / credential material detected (e.g. a private key or token).\n", "."+bare+".local")
	fmt.Fprintf(cc.out, "  This change is BLOCKED from the repo entirely (the overlay is committed too). It is never committed.\n")
	fmt.Fprintf(cc.out, "  Only the out-of-band path is offered: [r]eject, or route to the out-of-repo secret store [x].\n")

	ans := prompt(cc.in, cc.out, "route blocked change? [r]eject / secret-store [x] (default r): ")
	if ans != "x" {
		fmt.Fprintf(cc.out, "  %s: rejected; secret kept out of the repo\n", "."+bare+".local")
		return false, nil
	}

	ref := bare + ".local.captured"
	if err := cc.secretStore.Put(ref, string(liveBytes)); err != nil {
		return false, fmt.Errorf("write to secret store: %w", err)
	}
	placeholder := secret.Placeholder(ref)
	// The placeholder overlay is still a per-machine local file (local/zsh/<bare>.local);
	// gitignore the local layer before it lands, like every other local-capture route.
	if err := ensureLocalLayerIgnored(cc.repoPath); err != nil {
		return false, err
	}
	if err := writeRepoFile(cc.repoPath, overlaySrc, []byte(placeholder+"\n")); err != nil {
		return false, err
	}
	fmt.Fprintf(cc.out, "  %s: secret stored out-of-band in ~/.config/ferry/secrets-local; a placeholder was written to the repo overlay\n", "."+bare+".local")
	return true, nil
}

// captureTerminalDomain offers ONE in-scope terminal preference DOMAIN (iterm2 /
// Apple Terminal) for whole-domain capture back into the repo. apply re-exports
// these as opaque macOS plist domains (not file copies), so capture is the
// symmetric WHOLE-DOMAIN ingest: an opaque/binary plist cannot be hunk-merged, so
// the whole exported domain is accepted or rejected as one unit. darwin-only — the
// caller guards platform.IsDarwin(); internal/terminal is itself platform-guarded
// and builds clean on linux.
//
// Flow: export the live domain (defaults export) via the same PreferenceDomain
// apply uses; if the domain is absent or its export already equals the repo copy,
// offer nothing (no no-op prompt). Otherwise secret-gate the WHOLE exported value
// (GateValue — plist domains are scanned as whole values per PLAN; a high-confidence
// secret blocks every repo route, reject / out-of-band only), then present a single
// whole-domain accept/reject and route the accepted export shared/local/reject:
//   - [s]hared -> the repo path apply READS for this domain (iterm2/<id>.plist for
//     iTerm2; terminal/<id>.plist for Apple Terminal), so a later apply re-imports it;
//   - [l]ocal  -> local/<domain>/<id>.plist (gitignored), the per-machine wholesale
//     plist (machine-divergent plist settings go local whole-domain);
//   - [r]eject -> nothing written.
//
// Non-tty/empty-stdin safe: every prompt defaults to reject on EOF, so capture
// never hangs and never writes a terminal domain without an explicit answer.
// Returns (wrote, offered, err): offered=true once a drifted domain is presented
// (counted even on a reject), so terminal-only drift still drives the summary.
func captureTerminalDomain(cc captureCtx, domain string) (wrote bool, offered bool, err error) {
	prefID, ok := terminalPrefDomainID(domain)
	if !ok {
		return false, false, nil
	}

	// Export the live domain via the same PreferenceDomain apply uses. The export
	// blob (defaults export <id> -) is XML/diffable; Apple Terminal's import blob is
	// irrelevant to export, so pass nil for it.
	var d *terminal.PreferenceDomain
	switch domain {
	case "iterm2":
		d = terminal.NewITerm2(nil, terminal.ExecRunner{}, terminal.ExecProcessController{})
	default: // "terminal" / Apple Terminal
		d = terminal.NewAppleTerminal(nil, terminal.ExecRunner{})
	}
	liveBlob, absent, berr := d.Backup()
	if berr != nil {
		// A non-darwin host is a clean skip (defensive; the caller already guards
		// darwin). Any other export failure is surfaced.
		if errors.Is(berr, terminal.ErrNotDarwin) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("export %s preference domain: %w", domain, berr)
	}
	if absent {
		// The domain holds nothing pre-ferry (never configured) — no live state to
		// capture; offer nothing.
		return false, false, nil
	}

	// iTerm2 ONLY: reduce the live export to the ALLOWLISTED global keys before it is
	// compared, gated, or written. `defaults export com.googlecode.iterm2` carries
	// volatile machine state — window geometry, NoSync* one-shot flags, the retired
	// custom-prefs-folder pointer — that must NEVER reach the repo; FilterAllowlist
	// keeps ONLY the curated global-behaviour keys (see terminal.FilterAllowlist), so
	// every downstream route (diff, gate, shared/local/secret write) operates on the
	// stable, reviewable subset that is like-for-like with what apply imports.
	if domain == "iterm2" {
		liveBlob = terminal.FilterAllowlist(liveBlob)
	}

	// Repo path apply READS for this domain's plist (align with apply's
	// buildTerminalDomain / terminalExportBlob): iterm2/<id>.plist for iTerm2,
	// terminal/<id>.plist for Apple Terminal.
	repoDest := terminalRepoDest(cc.repoPath, domain, prefID)
	// Guard the terminal plist repo path (read here, written by the shared route /
	// blocked placeholder) against an escaping/~.ssh symlink before reading through it.
	if _, err := safeRepoPath(cc.repoPath, repoDest); err != nil {
		return false, false, err
	}

	// Only offer when the live export actually DIFFERS from the committed repo copy
	// (don't offer a no-op). An absent repo copy is itself a difference (capture
	// would create it).
	repoBytes, _ := os.ReadFile(repoDest)
	if domain == "iterm2" {
		// Compare LIKE-FOR-LIKE: filter the repo side to the same allowlist so a repo
		// plist that happens to carry stale volatile keys never registers as drift.
		repoBytes = terminal.FilterAllowlist(repoBytes)
	}
	if string(repoBytes) == string(liveBlob) {
		return false, false, nil
	}

	offered = true
	fmt.Fprintf(cc.out, "\n=== %s preference domain (%s, drifted) ===\n", domain, prefID)

	// MANDATORY secret gate BEFORE any write: scan the WHOLE exported plist value. A
	// high-confidence secret blocks every repo route (shared AND local); only reject
	// / the out-of-repo secret store are offered.
	gate := secret.GateValue(string(liveBlob))
	if gate.BlockedFromRepo {
		w, gerr := captureBlockedTerminal(cc, domain, prefID, repoDest, liveBlob)
		return w, true, gerr
	}

	// Whole-domain accept/reject: an opaque plist cannot be reviewed hunk-by-hunk.
	ans := prompt(cc.in, cc.out, "capture this whole preference domain? [y]es / [n]o (default n): ")
	if ans != "y" && ans != "yes" {
		fmt.Fprintf(cc.out, "  %s: rejected; nothing written\n", domain)
		return false, true, nil
	}

	// Route the accepted whole domain.
	switch promptRoute(cc.in, cc.out) {
	case secret.RouteShared:
		if err := writeRepoFile(cc.repoPath, repoDest, liveBlob); err != nil {
			return false, true, err
		}
		fmt.Fprintf(cc.out, "  %s: captured -> shared (%s)\n", domain, relTo(cc.repoPath, repoDest))
		return true, true, nil
	case secret.RouteLocal:
		// Guarantee the local layer is gitignored AT creation so the wholesale
		// per-machine plist never lands tracked.
		if err := ensureLocalLayerIgnored(cc.repoPath); err != nil {
			return false, true, err
		}
		dest := terminalLocalDest(cc.repoPath, domain, prefID)
		if err := writeRepoFile(cc.repoPath, dest, liveBlob); err != nil {
			return false, true, err
		}
		fmt.Fprintf(cc.out, "  %s: captured -> local (%s, gitignored)\n", domain, relTo(cc.repoPath, dest))
		return true, true, nil
	default:
		fmt.Fprintf(cc.out, "  %s: rejected; nothing written\n", domain)
		return false, true, nil
	}
}

// captureBlockedTerminal handles a terminal domain whose exported plist holds a
// high-confidence secret. It is NEVER written to shared or local (both live in the
// repo worktree); only reject or the out-of-repo secret store are offered, mirroring
// captureBlocked but keyed to the whole-domain export. The user-facing message ties
// the block to SECRET material and names the out-of-band path.
func captureBlockedTerminal(cc captureCtx, domain, prefID, repoDest string, liveBlob []byte) (bool, error) {
	fmt.Fprintf(cc.out, "  %s: SECRET / credential material detected in the exported preferences (e.g. a token).\n", domain)
	fmt.Fprintf(cc.out, "  This change is BLOCKED from the repo entirely (both shared and local). It is never committed.\n")
	fmt.Fprintf(cc.out, "  Only the out-of-band path is offered: [r]eject, or route to the out-of-repo secret store [x].\n")

	ans := prompt(cc.in, cc.out, "route blocked change? [r]eject / secret-store [x] (default r): ")
	if ans != "x" {
		fmt.Fprintf(cc.out, "  %s: rejected; secret kept out of the repo\n", domain)
		return false, nil
	}

	// Out-of-repo secret store: stash the whole exported value out of band and leave
	// a placeholder at the repo path apply reads. The secret value never enters the
	// repo; only the placeholder does. Keyed by the native domain id.
	ref := prefID + ".captured"
	if err := cc.secretStore.Put(ref, string(liveBlob)); err != nil {
		return false, fmt.Errorf("write to secret store: %w", err)
	}
	placeholder := secret.Placeholder(ref)
	if err := writeRepoFile(cc.repoPath, repoDest, []byte(placeholder+"\n")); err != nil {
		return false, err
	}
	fmt.Fprintf(cc.out, "  %s: secret stored out-of-band in ~/.config/ferry/secrets-local; a placeholder was written to the repo\n", domain)
	return true, nil
}

// terminalRepoDest is the committed repo plist path apply READS for a terminal
// domain — aligned with apply's buildTerminalDomain / terminalExportBlob: both
// iTerm2 and Apple Terminal import the committed <repo>/<domain>/<id>.plist via
// `defaults import` (iTerm2's is the allowlist-filtered global plist). A
// shared-routed capture writes here so a later apply re-deploys exactly these bytes.
func terminalRepoDest(repo, domain, prefID string) string {
	if domain == "iterm2" {
		return filepath.Join(repo, "iterm2", prefID+".plist")
	}
	return filepath.Join(repo, "terminal", prefID+".plist")
}

// terminalLocalDest is the gitignored per-machine wholesale plist destination for a
// local-routed terminal capture: <repo>/local/<domain>/<id>.plist. A machine-
// divergent plist domain goes local WHOLE-DOMAIN (it is opaque; there is no merge
// point), under the same local/<domain>/ tree the dotfile overlays use.
func terminalLocalDest(repo, domain, prefID string) string {
	return filepath.Join(repo, "local", domain, prefID+".plist")
}

// promptRoute asks for the shared/local/reject route for a clean (non-secret)
// accepted change. EOF / anything else defaults to reject (the safe default).
func promptRoute(in *bufio.Reader, out io.Writer) secret.Route {
	ans := prompt(in, out, "route this change? [s]hared / [l]ocal / [r]eject (default r): ")
	switch ans {
	case "s", "shared":
		return secret.RouteShared
	case "l", "local":
		return secret.RouteLocal
	default:
		return secret.RouteReject
	}
}

// prompt PRINTS the label (no trailing newline, so the answer is typed on the
// same line) and reads one trimmed, lower-cased line. On EOF (empty stdin) it
// returns "" so the caller can apply its safe default — capture never hangs and
// never writes without an explicit answer.
//
// The label print is load-bearing: through v0.3.0 this function silently
// DROPPED the label, so every interactive capture question was invisible and a
// real user faced a blank, waiting terminal (first field report, 2026-07-02).
// The evals never caught it because they script stdin and assert end-state;
// TestCaptureOne_PromptLabelsVisible now pins the label's presence in output.
func prompt(in *bufio.Reader, out io.Writer, label string) string {
	fmt.Fprint(out, label)
	line, err := in.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if err != nil && line == "" {
		return ""
	}
	return line
}

// --- hunk diffing (line-based) -------------------------------------------------

// hunk is one contiguous block where live differs from repo. Replacing repo's
// [repoStart,repoEnd) lines with newLines turns repo into live for that region.
type hunk struct {
	repoStart int
	repoEnd   int
	oldLines  []string
	newLines  []string
}

// diffHunks computes line-level change hunks between repo (old) and live (new)
// using a longest-common-subsequence backtrack, then groups adjacent
// non-matching lines into hunks. Equal trailing/leading context separates hunks
// so independent edits (top vs bottom) are reviewable apart.
func diffHunks(oldText, newText string) []hunk {
	o := splitLines(oldText)
	n := splitLines(newText)

	// LCS table.
	lcs := make([][]int, len(o)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(n)+1)
	}
	for i := len(o) - 1; i >= 0; i-- {
		for j := len(n) - 1; j >= 0; j-- {
			if o[i] == n[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var hunks []hunk
	i, j := 0, 0
	cur := hunk{repoStart: -1}
	flush := func() {
		if cur.repoStart >= 0 {
			hunks = append(hunks, cur)
		}
		cur = hunk{repoStart: -1}
	}
	for i < len(o) && j < len(n) {
		if o[i] == n[j] {
			flush()
			i++
			j++
			continue
		}
		if cur.repoStart < 0 {
			cur = hunk{repoStart: i, repoEnd: i}
		}
		if lcs[i+1][j] >= lcs[i][j+1] {
			cur.oldLines = append(cur.oldLines, o[i])
			i++
			cur.repoEnd = i
		} else {
			cur.newLines = append(cur.newLines, n[j])
			j++
		}
	}
	// Tail: remaining old lines deleted, remaining new lines added.
	if i < len(o) || j < len(n) {
		if cur.repoStart < 0 {
			cur = hunk{repoStart: i, repoEnd: i}
		}
		for ; i < len(o); i++ {
			cur.oldLines = append(cur.oldLines, o[i])
			cur.repoEnd = i + 1
		}
		for ; j < len(n); j++ {
			cur.newLines = append(cur.newLines, n[j])
		}
	}
	flush()
	return hunks
}

// applyHunks reconstructs the captured text from repo's lines, replacing each
// ACCEPTED hunk's old region with its new lines and leaving rejected hunks as
// repo's original lines.
//
// trailingNewline carries the NEW (live) side's final-newline shape. The line
// model here terminates every emitted line with '\n', so without this the
// composed result ALWAYS ends in '\n'; when the live file has no trailing newline
// the all-accepted composition would then be live+"\n" != live, and the shared
// route would write that, leaving the target wedged in a permanent StateConflict
// that capture can never clear (the only difference is the trailing byte, which
// yields zero reviewable hunks). Stamping the live side's shape keeps composed ==
// live when every hunk is accepted.
func applyHunks(oldText string, hunks []hunk, accepted []bool, trailingNewline bool) string {
	o := splitLines(oldText)
	var b strings.Builder
	pos := 0
	for idx, h := range hunks {
		for ; pos < h.repoStart; pos++ {
			b.WriteString(o[pos])
			b.WriteByte('\n')
		}
		if accepted[idx] {
			for _, l := range h.newLines {
				b.WriteString(l)
				b.WriteByte('\n')
			}
		} else {
			for _, l := range h.oldLines {
				b.WriteString(l)
				b.WriteByte('\n')
			}
		}
		pos = h.repoEnd
	}
	for ; pos < len(o); pos++ {
		b.WriteString(o[pos])
		b.WriteByte('\n')
	}
	out := b.String()
	if !trailingNewline && strings.HasSuffix(out, "\n") {
		out = out[:len(out)-1]
	}
	return out
}

// endsWithNewline reports whether content's final byte is a newline — the shape
// applyHunks must preserve so a captured composition matches the live file.
func endsWithNewline(content []byte) bool {
	return len(content) > 0 && content[len(content)-1] == '\n'
}

// localHunkDelta builds the zsh `.local` sidecar body: ONLY the added/changed
// lines (newLines) of the ACCEPTED hunks, in order, as a standalone shell
// fragment. It is NOT the composed whole file — apply makes the shared ~/.zshrc
// `source ~/.zshrc.local` LAST, so the sidecar must carry only the per-machine
// delta to layer ON TOP of shared without re-running it. A rejected hunk
// contributes nothing; a hunk that only deletes lines (no newLines) adds
// nothing. The result is a clean per-machine overlay that wins by being sourced
// last (AC-local-wins). Returns "" when no accepted hunk has added content.
func localHunkDelta(hunks []hunk, accepted []bool) string {
	var b strings.Builder
	for idx, h := range hunks {
		if !accepted[idx] {
			continue
		}
		for _, l := range h.newLines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// firstDeleteOnlyAccepted finds the first ACCEPTED hunk that the additive zsh
// `.local` sidecar cannot represent: a delete-only hunk that removes shared
// line(s) and adds none (oldLines non-empty, newLines empty). Such a hunk
// contributes nothing to the sidecar (which only adds lines, sourced after
// shared), so capturing it would silently lose the change. Returns the hunk
// index and true on the first such hunk; (0,false) when every accepted hunk
// has added content (so the delta is meaningful).
func firstDeleteOnlyAccepted(hunks []hunk, accepted []bool) (int, bool) {
	for idx, h := range hunks {
		if !accepted[idx] {
			continue
		}
		if len(h.newLines) == 0 && len(h.oldLines) > 0 {
			return idx, true
		}
	}
	return 0, false
}

// renderHunk renders a hunk as a unified-ish diff for the prompt.
func renderHunk(h hunk) string {
	var b strings.Builder
	for _, l := range h.oldLines {
		b.WriteString("- ")
		b.WriteString(l)
		b.WriteByte('\n')
	}
	for _, l := range h.newLines {
		b.WriteString("+ ")
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

// splitLines splits text into lines, dropping a single trailing empty element so
// a trailing newline does not produce a spurious blank line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// --- path helpers --------------------------------------------------------------

// resolveCaptureSource finds the repo-side shared source for a declared dotfile,
// mirroring apply's resolution (canonical dotfiles/<bare>, then dotfiles/.<bare>,
// then top-level .<bare>). When none exists yet, the canonical dotfiles/<bare> is
// the path a shared capture would create — but capture only offers a candidate
// when a source already exists to diff against.
func resolveCaptureSource(repo, name string) (string, bool) {
	bare := strings.TrimPrefix(name, ".")
	for _, cand := range []string{
		filepath.Join(repo, dotfile.RepoSubdir, bare),
		filepath.Join(repo, dotfile.RepoSubdir, "."+bare),
		filepath.Join(repo, "."+bare),
	} {
		if regularRepoFile(repo, cand) {
			return cand, true
		}
	}
	return "", false
}

// sharedTargets returns the repo-side shared destinations a shared-routed capture
// should write: the canonical dotfiles/<bare> always (so a fresh repo gains it),
// plus any existing alternate layouts (dotfiles/.<bare>, top-level .<bare>) so all
// committed copies of the dotfile stay consistent.
func sharedTargets(repo, bare string) []string {
	canonical := filepath.Join(repo, dotfile.RepoSubdir, bare)
	dests := []string{canonical}
	for _, cand := range []string{
		filepath.Join(repo, dotfile.RepoSubdir, "."+bare),
		filepath.Join(repo, "."+bare),
	} {
		if regularRepoFile(repo, cand) {
			dests = append(dests, cand)
		}
	}
	return dests
}

// localOverlayPath is the gitignored per-machine destination for a local-routed
// capture: <repo>/local/<domain>/<bare>.local. The overlay directory is resolved
// through overlayDomainDir (mirrors apply's resolveOverlaySource) so capture and
// apply always agree on where a sidecar lives.
func localOverlayPath(repo, bare string) string {
	return filepath.Join(repo, "local", overlayDomainDir(bare), bare+".local")
}

// writeRepoFile writes content into the repo worktree, creating parent dirs. This
// is the ONLY write capture makes — it never runs git commit or git push. Before
// writing it routes the destination through safeRepoPath, which REFUSES a symlinked
// repo path (or one whose parent symlink escapes the repo / resolves under ~/.ssh):
// a shared-routed capture must NEVER write THROUGH a symlink and overwrite
// ~/.ssh/config or a system location.
func writeRepoFile(repoRoot, path string, content []byte) error {
	safe, err := safeRepoPath(repoRoot, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(safe), 0o755); err != nil {
		return err
	}
	// Atomic temp+rename: this is the single write path for every capture route,
	// including gitignored local/ overlays that git cannot restore. A crash mid-write
	// with a plain os.WriteFile could truncate the file, destroying content with no
	// version-control fallback. AtomicWrite leaves the prior file intact on crash.
	return backup.AtomicWrite(safe, content, 0o644)
}

// relTo renders a repo-relative path for reporting (best effort).
func relTo(repo, path string) string {
	if rel, err := filepath.Rel(repo, path); err == nil {
		return rel
	}
	return path
}

// reDumpDeps re-dumps ONLY the current platform's manifest, gated on the package
// manager (brew domain) being in scope. It is an INDEPENDENT capture step: it runs
// whenever the brew domain is in scope, NOT only after some other change was
// accepted, so a machine whose only change is an installed package still updates
// deps/Brewfile.<goos>. A missing manager (ErrNoPackageManager) or an out-of-scope
// manager is a clean skip — never a bootstrap, never another OS's file.
//
// Returns true ONLY when it actually (re)wrote a manifest, so the caller can count
// it as an offered+captured change in the summary. An out-of-scope domain or a
// skipped/unsupported dump returns false (nothing was captured).
func reDumpDeps(ctx *cmdContext, out io.Writer) bool {
	if !ctx.Scope.IsManaged("brew") {
		return false
	}
	depsDir := filepath.Join(ctx.RepoPath, "deps")
	// CRITICAL: validate the brew dump target BEFORE invoking deps (which runs
	// `brew bundle dump --force`, a write-through). safeRepoPath REFUSES a symlinked
	// deps/Brewfile.<goos> (or a parent symlink that escapes the repo / resolves
	// under ~/.ssh), so a `deps/Brewfile.darwin -> ~/.ssh/config` can never let brew
	// overwrite ~/.ssh/config. deps/dump.go re-checks (Lstat) so the lib is safe even
	// if a future caller forgets this. Belt-and-suspenders, not redundant.
	// Anchor the target at ctx.RepoPath/deps/Brewfile.<goos>. safeRepoPath
	// absolutizes its candidate via filepath.Abs, which resolves a RELATIVE path
	// against the process CWD — so a repo-relative "deps/Brewfile.<goos>" would be
	// mis-anchored at CWD and falsely rejected when `ferry capture` runs from
	// outside the repo. Pass the absolute repo path so the guard is CWD-independent.
	brewTarget := filepath.Join(ctx.RepoPath, "deps", "Brewfile."+runtime.GOOS)
	if _, err := safeRepoPath(ctx.RepoPath, brewTarget); err != nil {
		fmt.Fprintf(out, "deps: skipped manifest re-dump (%v)\n", err)
		return false
	}
	path, err := deps.ReDumpManifest(depsDir, deps.ExecRunner{})
	if err != nil {
		// No manager / unsupported dump: report briefly, never fail capture.
		fmt.Fprintf(out, "deps: skipped manifest re-dump (%v)\n", err)
		return false
	}
	fmt.Fprintf(out, "deps: re-dumped manifest %s\n", relTo(ctx.RepoPath, path))
	return true
}

// reDumpNpmGlobals re-dumps THIS machine's global npm package NAMES to
// deps/npm-globals.txt, gated on the npm-globals domain being in scope AND npm
// being present. It is an INDEPENDENT capture step that COEXISTS with the brew
// re-dump (a machine can carry both a Brewfile and an npm globals list). A missing
// npm, or a symlinked/escaping target, is a clean skip — never a bootstrap, never
// a write-through. Returns true ONLY when it actually (re)wrote the list, so the
// caller counts it as an offered+captured change.
func reDumpNpmGlobals(ctx *cmdContext, out io.Writer) bool {
	if !ctx.Scope.IsManaged("npm-globals") {
		return false
	}
	if !platform.HasNpm() {
		fmt.Fprintln(out, "npm-globals: skipped (npm not installed)")
		return false
	}
	depsDir := filepath.Join(ctx.RepoPath, "deps")
	// Guard the target BEFORE the dump writes it (belt-and-suspenders: ReDumpNpmGlobals
	// re-checks in the library). safeRepoPath refuses a symlinked deps/npm-globals.txt
	// or a symlinked deps/ parent escaping the repo / resolving under ~/.ssh.
	target := deps.NpmGlobalsFile(depsDir)
	if _, err := safeRepoPath(ctx.RepoPath, target); err != nil {
		fmt.Fprintf(out, "npm-globals: skipped manifest re-dump (%v)\n", err)
		return false
	}
	path, err := deps.ReDumpNpmGlobals(depsDir, deps.ExecRunner{})
	if err != nil {
		fmt.Fprintf(out, "npm-globals: skipped manifest re-dump (%v)\n", err)
		return false
	}
	fmt.Fprintf(out, "npm-globals: re-dumped %s\n", relTo(ctx.RepoPath, path))
	return true
}
