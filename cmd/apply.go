package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/agents"
	"github.com/REPPL/ferry/internal/backup"
	"github.com/REPPL/ferry/internal/deps"
	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/paths"
	"github.com/REPPL/ferry/internal/platform"
	"github.com/REPPL/ferry/internal/secret"
	"github.com/REPPL/ferry/internal/termcfg"
	"github.com/REPPL/ferry/internal/terminal"
)

func init() {
	// --force is an apply-only override (overwrite uncaptured local edits on a
	// conflict). Registered here so commands.go stays owned by the skeleton wave;
	// --deps and --dry-run are already declared there.
	applyCmd.Flags().Bool("force", false, "overwrite uncaptured local edits on conflict")
	// --skip-wizard is the expert opt-out from the guided walkthrough: safe changes
	// still auto-apply, but risky changes are NOT prompted — they FAIL CLOSED
	// (listed, refused, non-zero exit) exactly as in a non-interactive run. It
	// suppresses the UI; it never makes a risky change happen unattended.
	applyCmd.Flags().Bool("skip-wizard", false, "skip the guided walkthrough (safe changes auto-apply; risky changes are refused, not prompted)")
}

// kind classifies a planned item so diff/apply can describe it precisely.
type planKind int

const (
	kindDotfile    planKind = iota // a file-domain target reconciled by copy
	kindOverlay                    // a .local overlay materialised to its home path
	kindPreference                 // a native macOS preference (plist/defaults) domain
	kindAgents                     // an agents-domain target (derived content, repo-authoritative)
	kindTerminal                   // a config-file terminal target (carried like a dotfile)
)

// planItem is one unit of work apply would perform. It carries the fully
// resolved, secret-rendered content so diff and apply share identical planning.
type planItem struct {
	kind    planKind
	domain  string         // human label (e.g. "zsh", "iterm2", ".zshrc")
	target  dotfile.Target // dotfile/overlay targets only
	content []byte         // effective content to materialise (post-render)
	action  string         // computed action for reporting (created/updated/noop/conflict/skipped/preview)
	skip    bool           // true when a missing secret forces a skip
	missing []string       // refs that were missing (when skip)
	note    string         // free-form note (de-scope warnings, preference TODO)

	// risky/riskReason are the guided-apply risk gate's per-item verdict, computed
	// during planning by assessRisk. A risky change halts for confirmation in the
	// interactive walkthrough and FAILS CLOSED (never applied) in a non-interactive
	// run or under --skip-wizard. riskReason is the human sentence shown to the
	// user (empty when not risky). Meaningful for the guided kinds
	// (kindDotfile/kindOverlay/kindAgents); always false for kindPreference (terminal
	// domains auto-apply through their own reversible backup path, outside the
	// dotfiles/agents grouping the walkthrough covers).
	risky      bool
	riskReason string

	// secretRouted is true when this target's deployed content was rendered from
	// the secret store (its effective source carried a {{ferry.secret ...}}
	// placeholder that was substituted). It rides onto dotfile.Result.SecretRouted
	// so CommitLastApplied records ONLY the content hash for such a target, never
	// the rendered bytes — keeping plaintext secrets out of the last-applied state
	// file. Meaningful for kindDotfile/kindOverlay; always false for the agents and
	// preference kinds (agents content is not secret-rendered; preference domains
	// stage their rendered bytes separately, never via the last-applied baseline).
	secretRouted bool

	// state is the three-way classification dotfile.Classify computed for this
	// target against its EFFECTIVE (composed + secret-rendered) content during
	// planning — the same resolution apply acts on. It is the SINGLE source of
	// truth that diff/status print and apply deploys: printPlan renders it as the
	// real per-target outcome (clean / would-create / would-update / conflict),
	// and runStatus reuses it so status never diverges from apply. Empty for
	// kindPreference (which carries its own prefDomain.Plan()) and for skipped
	// items (skip/missing describe a blocked target instead).
	state dotfile.State

	// execBit records, for a kindAgents item, whether the repo source carried
	// an executable bit — a first-ever write of a hook script materialises
	// 0755 so it stays runnable (agents.ApplyItem reads it). Meaningless for
	// other kinds.
	execBit bool

	// prefDomain is the constructed native macOS preference domain for a
	// kindPreference item (iTerm2 / Apple Terminal). diff renders its Plan();
	// apply Registers + BackupResources + Applies it. nil for non-preference kinds.
	prefDomain *terminal.PreferenceDomain

	// stagePlistPath / stageContent carry the iTerm2 RENDERED STAGING write. When
	// stagePlistPath is non-empty (iterm2 only), mutate writes stageContent — the
	// repo plist with its {{ferry.secret ...}} placeholders already substituted —
	// into the ferry-owned staging folder (com.googlecode.iterm2.plist) via the
	// backup engine BEFORE terminal.Apply points PrefsCustomFolder at that folder.
	// So iTerm2 loads the rendered plist, never the raw repo one. Empty on the
	// read-only preview path and for non-iterm2 kinds.
	stagePlistPath string
	stageContent   []byte

	// prefApplied is the observable "already applied" signal for a kindPreference
	// item: true when an immutable baseline exists for this domain's resource path
	// (HasBaseline(ResourcePath(prefID))), i.e. ferry applied it on this machine
	// before. It is the write-free, read-only proxy for "the terminal domain is
	// already managed", so a clean machine's diff/dry-run shows "managed (re-apply
	// on demand)" instead of an unconditional pending "would apply". Computed from
	// the live engine on the mutating path, and from a NON-MUTATING baseline stat
	// (baselineHasBeenApplied) on the read-only preview path — never by constructing
	// an engine in a read-only command (that would create the state dir).
	// Meaningless for non-preference kinds.
	prefApplied bool
}

// runApply is the idempotent, transactional, non-interactive reconcile.
func runApply(c *cobra.Command, _ []string) error {
	depsFlag, _ := c.Flags().GetBool("deps")
	dryRun, _ := c.Flags().GetBool("dry-run")
	force, _ := c.Flags().GetBool("force")
	skipWizard, _ := c.Flags().GetBool("skip-wizard")

	ctx, err := loadContext()
	if err != nil {
		return err
	}

	out := c.OutOrStdout()
	in := bufio.NewReader(c.InOrStdin())

	// --dry-run is a pure preview: take NO lock, write NOTHING. The plan is
	// read-only here, so it is safe to compute it without the lock — it never
	// drives a mutation on this path.
	if dryRun {
		// Read-only preview: buildPlan creates no ferry state (no engine, no
		// state-dir mkdir). Terminal items probe the already-applied baseline via a
		// non-mutating stat, so a managed domain shows "managed (re-apply on demand)"
		// without ever writing.
		plan, warnings, err := buildPlan(ctx)
		if err != nil {
			return err
		}
		for _, w := range warnings {
			fmt.Fprintln(out, w)
		}
		printPlan(out, plan)
		fmt.Fprintln(out, "dry-run: no changes written")
		return nil
	}

	// Mutating apply: acquire the lock and roll back any incomplete prior run
	// FIRST, then compute the plan UNDER the lock so it cannot be stale, then
	// mutate + commit + persist last-applied. applyPlan owns that whole ordered
	// transaction (lock -> rollback -> buildPlan -> Begin -> mutate -> Commit ->
	// CommitLastApplied -> Unlock).
	if err := applyPlan(ctx, force, guidedOpts{skipWizard: skipWizard}, in, out); err != nil {
		return err
	}

	// Dependencies are a SEPARATE, explicitly gated step. Default apply (no
	// --deps) NEVER touches a package manager.
	if depsFlag {
		if err := applyDeps(ctx, out); err != nil {
			return err
		}
	}

	return nil
}

// buildPlan computes, without writing anything, what apply would do for every
// in-scope domain. It also returns de-scope warnings for the DOTFILE domain
// (dotfiles previously applied but now out of scope). It is read-only — it opens
// the last-applied store read-only so a pure preview (diff / apply --dry-run)
// never creates ferry state. Terminal de-scope warnings are computed separately
// on the mutating path (they need the backup engine's baseline).
//
// This is the READ-ONLY entry point (diff / status / apply --dry-run / init
// preview). It stays write-free: it NEVER creates ferry's state dir OR any of its
// subdirs. The terminal already-applied check is a NON-MUTATING stat of the
// immutable baseline metadata (baselineHasBeenApplied → backup.HasBaselineReadOnly)
// rather than a constructed engine: backup.New()/NewAt() eagerly mkdir+chmod
// baseline/, blobs/, journal/, snapshots/, so building an engine merely to read a
// baseline would CREATE those subdirs on a machine that has only the state ROOT
// (or nothing) — violating the read-only contract. Stat-only sidesteps that. On a
// fresh machine (no baseline metadata) the terminal item falls back to the
// always-surface preview line, which is correct (nothing has been applied yet).
// The mutating apply path calls buildPlanWithEngine with its live engine.
func buildPlan(ctx *cmdContext) (items []planItem, warnings []string, err error) {
	return buildPlanWithEngine(ctx, nil)
}

// baselineHasBeenApplied reports whether ferry recorded an immutable baseline for
// the terminal preference domain — i.e. it was applied on this machine — using a
// pure stat of the baseline metadata, with NO engine construction and therefore NO
// state-dir creation. It is the read-only preview's "already applied?" probe.
// Resolving the state dir is itself read-only; an unresolved root or absent
// baseline reads as not-applied.
func baselineHasBeenApplied(prefID string) bool {
	stateRoot, err := paths.StateDir()
	if err != nil {
		return false
	}
	return backup.HasBaselineReadOnly(stateRoot, backup.ResourcePath(prefID))
}

// buildPlanWithEngine is buildPlan with an OPTIONAL backup engine. When eng is
// non-nil (the mutating apply path) it is used READ-ONLY to classify terminal
// preference domains as already-applied (HasBaseline(ResourcePath(prefID))) so a
// clean terminal domain is shown as "managed (re-apply on demand)" rather than a
// false pending change. With eng==nil (the read-only preview path) the same
// already-applied signal is computed via a NON-MUTATING baseline stat
// (baselineHasBeenApplied), so no engine is ever built and no state dir/subdir is
// created.
func buildPlanWithEngine(ctx *cmdContext, eng *backup.Engine) (items []planItem, warnings []string, err error) {
	secretStore, err := secret.Open()
	if err != nil {
		return nil, nil, fmt.Errorf("open secret store: %w", err)
	}
	lastApplied, err := dotfile.OpenStoreReadOnly()
	if err != nil {
		return nil, nil, fmt.Errorf("open last-applied store: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}

	if ctx.Scope.IsManaged("dotfiles") {
		ditems, dwarn, derr := planDotfiles(ctx, home, secretStore, lastApplied)
		if derr != nil {
			return nil, nil, derr
		}
		items = append(items, ditems...)
		warnings = append(warnings, dwarn...)
	}

	// Agents domain: the harness registry × instruction sources × optional
	// devtree × asset trees, expanded to 1:1 (content, target) items by
	// planAgents and reconciled through the same three-way machinery as
	// dotfiles. Gated behind `[manage] agents = true` (default off). When the
	// domain is de-scoped, previously applied targets collapse into one
	// de-scope warning (files left untouched).
	if ctx.Scope.IsManaged("agents") {
		aitems, awarn, aerr := planAgents(ctx, home, lastApplied)
		if aerr != nil {
			return nil, nil, aerr
		}
		items = append(items, aitems...)
		warnings = append(warnings, awarn...)
	} else {
		warnings = append(warnings, descopeAgentsWarnings(lastApplied, nil, false)...)
	}

	// Config-file terminal domain: the built-in terminal registry × the repo's
	// terminals/ configs, expanded to 1:1 (content, target) items by
	// planTerminals and reconciled through the same three-way machinery as
	// dotfiles (they ARE carried like dotfiles: capturable, .local overlay wins).
	// Gated behind `[manage] terminals = true` (default off). When de-scoped,
	// previously applied targets collapse into one de-scope warning (files left
	// untouched). Distinct from the macOS `terminal`/`iterm2` preference domains
	// below.
	if ctx.Scope.IsManaged("terminals") {
		titems, twarn, terr := planTerminals(ctx, home, lastApplied)
		if terr != nil {
			return nil, nil, terr
		}
		items = append(items, titems...)
		warnings = append(warnings, twarn...)
	} else {
		warnings = append(warnings, descopeTerminalConfigWarnings(lastApplied, nil, false)...)
	}

	// Terminal preference domains: represent in-scope iterm2 / Apple Terminal as
	// native macOS PREFERENCE domains (not file copies). Each carries the
	// constructed terminal.PreferenceDomain so diff renders its real Plan() and
	// (on darwin) apply Registers + BackupResources + Applies it. Out-of-scope
	// domains are never built, so they never surface in the plan (the AC tripwire).
	//
	// On a NON-darwin host the domain is still included in the plan, but as the
	// terminal.Plan()'s SKIPPED ("macOS only") entry — so `ferry diff` on Linux
	// HONESTLY shows "iterm2: skipped (macOS only)" rather than silently dropping
	// an in-scope terminal domain. Constructing the domain is shell-free
	// (NewITerm2/NewAppleTerminal only set fields/closures; defaults runs only on
	// Apply/Backup), and Plan() is darwin-guarded, so this builds clean on Linux
	// and never shells out. The already-applied baseline probe is darwin-only (the
	// engine baseline is a macOS apply artifact); on Linux the entry is a pure
	// no-op skip that apply never mutates (terminal.Apply no-ops via ErrNotDarwin).
	for _, domain := range []string{"iterm2", "terminal"} {
		if !ctx.Scope.IsManaged(domain) {
			continue
		}
		d, stage, derr := buildTerminalDomain(ctx.RepoPath, domain, secretStore)
		if derr != nil {
			// The repo-side prefs folder / export blob is a symlink, escapes the repo,
			// or resolves under ~/.ssh / a system location. REFUSE the domain rather than
			// hand a poisoned folder to `defaults write PrefsCustomFolder` — skip it with
			// a clear notice instead of persisting a symlinked prefs folder.
			warnings = append(warnings, refusalWarning(domain, derr))
			continue
		}
		// Secret render-or-SKIP parity with dotfiles: the plist/export bytes this
		// domain would deploy carry unrendered {{ferry.secret ...}} placeholders whose
		// secret is MISSING from the store. SKIP the whole terminal domain (live config
		// left intact) rather than point iTerm2 at — or `defaults import` — an
		// unrendered placeholder, which would expose it to the terminal preference
		// mechanism. With the secret PRESENT the bytes are rendered and applied.
		if len(stage.Missing) > 0 {
			warnings = append(warnings, fmt.Sprintf("%-22s skipped (missing secret: %s)", domain, strings.Join(stage.Missing, ", ")))
			continue
		}
		// Observable "already applied": a recorded immutable baseline for this
		// domain's resource path means ferry applied it on this machine before, so
		// it is not a pending change. The mutating path (eng != nil) reads it from
		// the live engine; the read-only preview path (eng == nil) reads it via a
		// NON-MUTATING baseline stat (baselineHasBeenApplied) that never builds an
		// engine and so never creates the state dir/subdirs. Both observe the same
		// immutable baseline, so diff/dry-run and apply agree. Only meaningful on
		// darwin — a non-darwin host has no terminal baseline to record.
		applied := false
		if isDarwin() {
			if prefID, ok := terminalPrefDomainID(domain); ok {
				if eng != nil {
					applied = eng.HasBaseline(backup.ResourcePath(prefID))
				} else {
					applied = baselineHasBeenApplied(prefID)
				}
			}
		}
		items = append(items, planItem{
			kind:           kindPreference,
			domain:         domain,
			action:         "preview",
			prefDomain:     d,
			prefApplied:    applied,
			stagePlistPath: stage.plistPath,
			stageContent:   stage.content,
		})
	}

	// Dotfile de-scope warnings: a dotfile ferry previously applied (it has a
	// last-applied record) but the manifest no longer declares. Leave its file
	// untouched; warn how to revert. Terminal de-scope warnings are computed on
	// the mutating path via the engine baseline (terminal resources live in the
	// backup engine, not this dotfile store).
	warnings = append(warnings, descopeDotfileWarnings(ctx, lastApplied)...)

	return items, warnings, nil
}

// planDotfiles builds the per-target plan for the dotfiles domain, honouring the
// PER-DOMAIN overlay strategy (PLAN.md "Per-domain overlay strategy"):
//   - zsh (include-style): build the target with IncludeSidecarTarget, always
//     deploy the SHARED file (with `source ~/.zshrc.local` appended last so the
//     per-machine sidecar wins), and materialise the sidecar (~/.zshrc.local).
//   - generic dotfiles (whole-file replace, e.g. .gitconfig): deploy the LOCAL
//     copy (local/<domain>/<bare>) INSTEAD OF shared when one exists (local wins),
//     else the shared content — the chosen bytes are composed into the effective
//     content and deployed via the shared apply core (dotfile.ApplyContentDeferred).
//
// TargetFor / IncludeSidecarTarget are the ~/.ssh + path-traversal SECURITY
// BOUNDARY: on ErrForbiddenSSHPath / ErrPathEscapesHome (or any error) the dotfile
// is SKIPPED with a clear refusal warning and never read/written — so a manifest
// declaring `.ssh/config` is refused before any ~/.ssh access.
//
// Secret placeholders are rendered (render-or-SKIP) for BOTH the shared content
// AND a local whole-file overlay before deploy: a missing {{ferry.secret ...}} in
// either skips that target rather than writing an unrendered placeholder.
func planDotfiles(ctx *cmdContext, home string, secretStore *secret.Store, lastApplied *dotfile.Store) ([]planItem, []string, error) {
	var items []planItem
	var warnings []string

	for _, name := range ctx.Scope.DeclaredDotfiles() {
		bare := strings.TrimPrefix(name, ".")
		zsh := isZsh(bare)

		// SECURITY BOUNDARY: build the validated target. zsh is include-style; every
		// other dotfile is whole-file-replace. An ~/.ssh / traversal name is refused
		// here and the dotfile is skipped before any read or write.
		var t dotfile.Target
		var terr error
		if zsh {
			t, terr = dotfile.IncludeSidecarTarget(ctx.RepoPath, home, name)
		} else {
			t, terr = dotfile.TargetFor(ctx.RepoPath, home, name)
		}
		if terr != nil {
			warnings = append(warnings, refusalWarning(name, terr))
			continue
		}

		// Resolve the repo source robustly: the canonical layout is
		// dotfiles/<bare>, but a repo may also store dotfiles/.<bare> or a
		// top-level .<bare>. Point Target.Repo at whichever exists.
		src, ok := resolveDotfileSource(ctx.RepoPath, name)
		if !ok {
			// Declared but no source on disk yet — nothing to materialise.
			continue
		}
		// SECURITY: os.ReadFile (below) follows symlinks, so a repo source that is a
		// symlink to ~/.ssh/config would be READ. Refuse a symlinked/escaping repo
		// source before any read; skip the dotfile with a clear refusal warning.
		if src, terr = safeRepoPath(ctx.RepoPath, src); terr != nil {
			warnings = append(warnings, refusalWarning(name, terr))
			continue
		}
		t.Repo = src

		overlaySrc, hasOverlay := resolveOverlaySource(ctx.RepoPath, bare)
		if hasOverlay {
			if overlaySrc, terr = safeRepoPath(ctx.RepoPath, overlaySrc); terr != nil {
				warnings = append(warnings, refusalWarning(name, terr))
				continue
			}
		}

		if zsh {
			// Include-style (zsh): deploy SHARED with `source ~/.<bare>.local`
			// appended last, then materialise the sidecar separately.
			raw, err := os.ReadFile(src)
			if err != nil {
				return nil, nil, err
			}
			content := raw
			if hasOverlay {
				content = appendSourceDirective(raw, "."+bare+".local")
			}

			rendered, missing, skip, err := renderSecrets(secretStore, content)
			if err != nil {
				return nil, nil, err
			}
			if skip {
				items = append(items, planItem{
					kind: kindDotfile, domain: "." + bare, target: t,
					action: "skipped", skip: true, missing: missing,
				})
				continue
			}
			secretRouted := isSecretRouted(content)
			state, risky, reason, err := classifyItem(t, rendered, secretRouted, lastApplied)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, planItem{
				kind: kindDotfile, domain: "." + bare, target: t, content: rendered,
				state: state, secretRouted: secretRouted, risky: risky, riskReason: reason,
			})

			if hasOverlay {
				overlayContent, err := os.ReadFile(overlaySrc)
				if err != nil {
					return nil, nil, err
				}
				ot := dotfile.Target{
					Name: bare + ".local",
					Repo: overlaySrc,
					Home: filepath.Join(home, "."+bare+".local"),
				}
				// The sidecar overlay can carry {{ferry.secret ...}} placeholders just
				// like a whole-file source: render-or-SKIP it too, so a missing secret
				// in the zsh local overlay skips materializing the sidecar rather than
				// writing an unrendered placeholder to ~/.<bare>.local.
				orendered, omissing, oskip, err := renderSecrets(secretStore, overlayContent)
				if err != nil {
					return nil, nil, err
				}
				if oskip {
					items = append(items, planItem{
						kind: kindOverlay, domain: "." + bare + ".local", target: ot,
						action: "skipped", skip: true, missing: omissing,
					})
					continue
				}
				oSecretRouted := isSecretRouted(overlayContent)
				ostate, orisky, oreason, err := classifyItem(ot, orendered, oSecretRouted, lastApplied)
				if err != nil {
					return nil, nil, err
				}
				items = append(items, planItem{
					kind: kindOverlay, domain: "." + bare + ".local",
					target: ot, content: orendered, state: ostate,
					secretRouted: oSecretRouted, risky: orisky, riskReason: oreason,
				})
			}
			continue
		}

		// Whole-file replace (generic dotfile): the LOCAL copy
		// local/<domain>/<bare>, when present, replaces the shared source (local
		// wins); else the shared source deploys. Render secrets on whichever source
		// is effective so a missing secret in the LOCAL overlay skips too — never a
		// literal placeholder written.
		effectiveSrc := effectiveSource(ctx.RepoPath, src, name)
		// The effective source may be the local overlay (a different repo path than
		// src); guard it too before the symlink-following read.
		effectiveSrc, terr = safeRepoPath(ctx.RepoPath, effectiveSrc)
		if terr != nil {
			warnings = append(warnings, refusalWarning(name, terr))
			continue
		}
		raw, err := os.ReadFile(effectiveSrc)
		if err != nil {
			return nil, nil, err
		}
		rendered, missing, skip, err := renderSecrets(secretStore, raw)
		if err != nil {
			return nil, nil, err
		}
		if skip {
			items = append(items, planItem{
				kind: kindDotfile, domain: "." + bare, target: t,
				action: "skipped", skip: true, missing: missing,
			})
			continue
		}
		secretRouted := isSecretRouted(raw)
		state, risky, reason, err := classifyItem(t, rendered, secretRouted, lastApplied)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, planItem{
			kind: kindDotfile, domain: "." + bare, target: t, content: rendered,
			state: state, secretRouted: secretRouted, risky: risky, riskReason: reason,
		})
	}

	return items, warnings, nil
}

// classifyItem computes the three-way dotfile.Classify state for a target against
// its EFFECTIVE (composed + secret-rendered) content — the exact bytes apply would
// deploy — PLUS the guided-apply risk verdict. It hashes the effective content IN
// MEMORY via dotfile.ClassifyContent: NO temp file is staged and NO secret-rendered
// byte is ever written to disk, so the diff/status/dry-run preview path is fully
// write-free while still observing the identical state apply's deploy path sees
// (zsh source-last, whole-file local-wins, rendered secrets all agree). It then
// runs the risk gate (assessRisk) over the resulting Status so planning computes
// both state and risk in a single live-file read. secretRouted flags a target
// whose effective bytes were rendered from the secret store (always risky). The
// store is read-only and no ferry state is created — safe on the diff/status path.
func classifyItem(t dotfile.Target, content []byte, secretRouted bool, store *dotfile.Store) (dotfile.State, bool, string, error) {
	st, err := dotfile.ClassifyContent(t, content, store)
	if err != nil {
		return "", false, "", err
	}
	risky, reason := assessRisk(t, st, secretRouted, store)
	return st.State, risky, reason, nil
}

// effectiveZshShared returns the EFFECTIVE shared ~/.<bare> content apply and
// status compute for an include-style zsh dotfile: the raw shared repo bytes
// with ferry's managed `source ~/.<bare>.local` directive appended LAST when a
// local overlay exists (so the sidecar wins). This is the SAME composition
// planDotfiles/status use to classify and deploy the shared zsh file. Capture
// reuses it so the live ~/.<bare> is diffed against the effective content, not
// the raw repo source — otherwise ferry's OWN injected include line would
// surface as a spurious "user hunk" and could be captured into shared (or, on a
// local route, into a self-sourcing sidecar). raw is the shared repo source
// bytes; hasOverlay is whether local/zsh/<bare>.local exists (resolveOverlaySource).
func effectiveZshShared(raw []byte, bare string, hasOverlay bool) []byte {
	if !hasOverlay {
		return raw
	}
	return appendSourceDirective(raw, "."+bare+".local")
}

// effectiveSource resolves the source apply would actually DEPLOY for a
// whole-file-replace dotfile, so status can classify against the same bytes.
// Mirrors planDotfiles' local-wins decision: when a per-machine local copy
// exists at LocalOverlayPath(repo, domain, name) it replaces the shared source
// (local wins); otherwise the shared source deploys. The domain segment for a
// generic dotfile is its bare name (zsh maps to "zsh", but zsh is include-style
// and not routed through this whole-file path). sharedSrc is the resolved shared
// repo source (resolveDotfileSource); the returned path is the bytes apply
// deploys. This is the SHARED apply/status resolution: status calls it so a
// machine where a local overlay was applied reads CLEAN, not perpetual repo-ahead.
func effectiveSource(repo, sharedSrc, name string) string {
	bare := strings.TrimPrefix(name, ".")
	localSrc := dotfile.LocalOverlayPath(repo, bare, name)
	if regularRepoFile(repo, localSrc) {
		return localSrc
	}
	return sharedSrc
}

// refusalWarning renders a clear, user-facing refusal for a dotfile TargetFor
// rejected. The ~/.ssh refusal is called out explicitly (the absolute hands-off
// contract); a traversal/escape is an invalid managed path. Any other error is
// surfaced too — the dotfile is always skipped, never read or written.
func refusalWarning(name string, err error) string {
	switch {
	case errors.Is(err, dotfile.ErrForbiddenSSHPath):
		return fmt.Sprintf("refusing %s: ferry never manages paths under ~/.ssh", name)
	case errors.Is(err, dotfile.ErrPathEscapesHome):
		return fmt.Sprintf("refusing %s: invalid managed path (escapes $HOME)", name)
	default:
		return fmt.Sprintf("refusing %s: %v", name, err)
	}
}

// applyPlan runs the whole mutating transaction in the order the engine requires:
// Lock -> RollbackIncomplete -> buildPlan (UNDER the lock, so it cannot be stale)
// -> Begin -> per-target write (deferred last-applied) -> Commit ->
// CommitLastApplied -> Unlock. The plan that drives mutation is computed AFTER the
// lock + rollback so a concurrent apply or a just-rolled-back run can't leave it
// stale. last-applied is persisted ONLY after the journal commit succeeds, so a
// crash/commit-error can never leave last-applied ahead of a rolled-back file.
// A conflict on any target is reported and skipped (force overrides); other
// targets still apply.
//
// The guided walkthrough (decideGuided) runs UNDER the lock, on the freshly
// recomputed plan: it partitions pending changes into safe (auto-applied) and
// risky (halt for confirmation), FAILS CLOSED on unconfirmed risky changes, and
// hands mutate only the approved subset. So the risk gate can never let a risky
// change apply unattended, and the bytes written are never stale.
func applyPlan(ctx *cmdContext, force bool, gopts guidedOpts, in *bufio.Reader, out io.Writer) (retErr error) {
	// Obtain the transactional engine (built lazily; this is the first mutating
	// use, so it creates ferry's state dir). Read-only diff/dry-run never reach here.
	eng, err := ctx.Engine()
	if err != nil {
		return err
	}

	lock, err := eng.Lock()
	if err != nil {
		var held *backup.ErrLockHeld
		if errors.As(err, &held) {
			return fmt.Errorf("another ferry apply is in progress (pid %d); try again later", held.OwnerPID)
		}
		return fmt.Errorf("acquire apply lock: %w", err)
	}
	// Always attempt to release the lock on every path. A FAILED unlock must not
	// masquerade as success: if the command is otherwise succeeding, surface the
	// Unlock error (a stale lock would block the next apply) with a clear pointer
	// to how to clear it. If we are already returning an error, keep that primary
	// error but still warn that the lock may be stale.
	defer func() {
		if uErr := lock.Unlock(); uErr != nil {
			if retErr == nil {
				retErr = fmt.Errorf("release apply lock: %w (the lock may be stale; remove it before the next apply)", uErr)
			} else {
				fmt.Fprintf(out, "warning: failed to release apply lock: %v; the lock may be stale and block the next apply\n", uErr)
			}
		}
	}()

	// Register the terminal preference domains on the engine BEFORE rolling back
	// any incomplete prior run. Resource-journal rollback requires the resource to
	// be REGISTERED (internal/backup keys the Restore hook on the registered domain;
	// an unregistered resource entry errors "no resource registered for domain"). A
	// prior apply that crashed during/after a terminal BackupResource leaves an
	// incomplete resource:// journal entry; without this up-front registration
	// RollbackIncomplete below could not roll it back and the next apply would wedge.
	//
	// Register BOTH iTerm2 + Apple Terminal, darwin-guarded, REGARDLESS of current
	// scope — registration is cheap and idempotent, and a domain that was applied
	// then later de-scoped must still be rollback-able. This mirrors restore.go's
	// registerTerminalDomains; it is idempotent with the per-domain eng.Register in
	// the kindPreference case below (registering twice is harmless).
	if err := registerTerminalDomains(ctx); err != nil {
		return err
	}

	// Roll back any incomplete prior run before computing or starting a fresh one.
	if _, err := eng.RollbackIncomplete(); err != nil {
		return fmt.Errorf("roll back incomplete run: %w", err)
	}

	// Compute the mutating plan UNDER the lock + after rollback so it cannot be
	// stale. buildPlan yields the dotfile de-scope warnings; terminal de-scope is
	// engine-baseline based (terminal resources live in the engine, not the
	// dotfile store). Pass the engine so terminal items can classify their
	// already-applied state from the immutable baseline.
	plan, warnings, err := buildPlanWithEngine(ctx, eng)
	if err != nil {
		return err
	}
	warnings = append(warnings, descopeTerminalWarnings(ctx, eng)...)
	for _, w := range warnings {
		fmt.Fprintln(out, w)
	}

	// GUIDED WALKTHROUGH + RISK GATE. decideGuided partitions the plan into what
	// should actually be written this run: safe changes auto-apply, risky changes
	// are confirmed (interactive) or refused (non-interactive / --skip-wizard). It
	// may persist skip-always exclusions to the .local layer. It never mutates the
	// live machine — it only decides and reports.
	dec, err := decideGuided(ctx, plan, force, gopts, in, out)
	if err != nil {
		return err
	}
	if dec.nothingToDo {
		// A clean, in-sync apply prints ONE line and does not walk (no mutation,
		// no journal run) — the plan's "quiet when safe" contract at its quietest.
		fmt.Fprintf(out, "in sync: %d target(s) already match the repo; nothing to apply\n", dec.cleanCount)
		return nil
	}
	if len(dec.toApply) == 0 {
		// Nothing approved to write. Either everything risky was refused (fail
		// closed, non-zero) or the user skipped it all this run (exit 0).
		if len(dec.refused) > 0 {
			return riskyRefusedError(out, dec.refused)
		}
		fmt.Fprintln(out, "nothing applied: every pending change was skipped this run.")
		return nil
	}

	run, err := eng.Begin()
	if err != nil {
		return fmt.Errorf("begin apply run: %w", err)
	}

	// Bind the OPEN run into closures so mutate never has to name the engine's
	// unexported run type (cmd cannot reference it). Every write/resource-backup and
	// the commit funnel through this one journal entry.
	b := backuperFunc(func(target string, content []byte, perm os.FileMode) error {
		return eng.BackupAndWrite(run, target, content, perm)
	})
	backupResource := func(domain string) error { return eng.BackupResource(run, domain) }
	commitRun := func() error { return run.Commit() }

	// From here on the journal run is OPEN (started, not committed). Any ordinary
	// in-process error before run.Commit() leaves the machine partially mutated, so
	// we must roll THIS run back INLINE before returning — not wait for the next
	// apply's RollbackIncomplete (that net is only for a real crash, where no
	// in-process handler can run). mutate performs the whole per-target apply and
	// returns its error; on a non-nil error we roll the in-progress run back here.
	if err := mutate(eng, b, backupResource, commitRun, dec.toApply, force, out); err != nil {
		// Roll back the current run's recorded changes immediately so a failed apply
		// leaves the machine in its pre-apply state (files restored, terminal
		// resources re-imported/deleted to their captured baseline) rather than half
		// applied. The run is uncommitted (no COMPLETE marker) and the start-of-apply
		// RollbackIncomplete already cleared any prior incomplete run, so the only
		// incomplete run now is this one — RollbackIncomplete reverts exactly it.
		if _, rbErr := eng.RollbackIncomplete(); rbErr != nil {
			// The genuinely-bad case: the apply failed AND we could not undo it, so the
			// machine may be left partially mutated. Surface BOTH errors loudly.
			return fmt.Errorf("apply failed (%v); inline rollback also failed (machine may be partially applied): %w", err, rbErr)
		}
		return err
	}

	// The safe/confirmed subset committed cleanly. If risky changes were refused
	// (non-interactive / --skip-wizard), report them and exit NON-ZERO — the safe
	// work is kept, but the run did not fully succeed and the user must review.
	if len(dec.refused) > 0 {
		return riskyRefusedError(out, dec.refused)
	}

	return nil
}

// mutate runs the per-target apply for an OPEN journal run, bound via closures so
// it never names the engine's unexported run type: b writes one target into the
// run, backupResource exports a preference domain into the run, and commitRun
// finalises the journal. It materialises every in-scope target (preference domains
// and dotfiles/overlays), commits the journal, and persists last-applied AFTER the
// commit. It returns the first in-process error; the caller rolls the open run back
// inline on any such error, so mutate itself just returns — it never leaves the run
// committed on failure. The happy path commits normally and is idempotent.
func mutate(eng *backup.Engine, b dotfile.Backuper, backupResource func(domain string) error, commitRun func() error, plan []planItem, force bool, out io.Writer) error {
	lastApplied, err := dotfile.OpenStore()
	if err != nil {
		return fmt.Errorf("open last-applied store: %w", err)
	}

	var conflicts []string
	// Collect deferred last-applied results: persisted ONLY after run.Commit().
	var deferred []dotfile.Result
	// Agents targets touched by this plan, for the cumulative restore record
	// (key -> absolute home path), persisted post-commit.
	agentsTargets := map[string]string{}
	for i := range plan {
		it := &plan[i]
		switch it.kind {
		case kindPreference:
			d := it.prefDomain
			if d == nil {
				continue
			}
			// A macOS-only domain on a non-darwin host is a pure no-op skip: do NOT
			// Register/BackupResource/Apply it (those would shell out to `defaults`),
			// just report it and move on. The plan/diff still surfaces it as
			// "skipped (macOS only)"; apply mutates nothing on Linux.
			if d.Plan().Skipped {
				fmt.Fprintf(out, "  %-22s skipped (macOS only)\n", it.domain)
				continue
			}
			// iTerm2 RENDERED STAGING: materialise the secret-rendered repo plist into
			// the ferry-owned staging folder (com.googlecode.iterm2.plist) BEFORE Apply
			// points PrefsCustomFolder at it, so iTerm2 loads the rendered plist and
			// never the raw {{ferry.secret}} one. The write goes through the backup
			// engine (b) — tracked in the journal + reversible on rollback, just like a
			// dotfile. The folder is created 0700 (StateDir convention) and the plist
			// 0600 (it carries substituted secret values). A refused/missing leaf or a
			// missing secret already SKIPPED this domain at build time, so stagePlistPath
			// is set only when there is a rendered plist to write.
			if it.stagePlistPath != "" {
				// Symlink-harden the staging dir under StateDir before writing: a
				// rendered plist must not be written through a symlinked store
				// component. Lexical; never touches ~/.ssh.
				if err := paths.HardenStoreDir(filepath.Dir(it.stagePlistPath)); err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Dir(it.stagePlistPath), 0o700); err != nil {
					return fmt.Errorf("stage %s rendered plist dir: %w", it.domain, err)
				}
				if err := b.BackupAndWrite(it.stagePlistPath, it.stageContent, 0o600); err != nil {
					return fmt.Errorf("stage %s rendered plist: %w", it.domain, err)
				}
			}
			// Register so restore (and incomplete-run rollback) can find this
			// resource, then export its CURRENT state into baseline-if-first +
			// this run's journal FIRST (export-before-mutate). The captured blob
			// is what an apply failure rolls back to via d.Restore.
			eng.Register(d)
			if err := backupResource(d.Domain()); err != nil {
				return fmt.Errorf("back up %s preference domain: %w", it.domain, err)
			}
			// Secure the inline-rollback state BEFORE mutating, and FAIL CLOSED: we
			// never run terminal.Apply (the mutation) without a valid pre-mutation
			// snapshot in hand to roll back to. An ABSENT domain (the fresh-machine
			// case — e.g. iTerm2 never configured) is NOT a failure: capturedAbsent
			// is true and the inline rollback DELETES the domain to return to that
			// pre-ferry state. A clean ErrNotDarwin means this host has nothing to
			// mutate (defensive; this branch only builds on darwin) — skip without
			// error. Any OTHER capture error aborts the apply for this domain WITHOUT
			// mutating, so a partial mutation can never be left un-revertible.
			capturedBlob, capturedAbsent, blobErr := d.Backup()
			if blobErr != nil {
				if errors.Is(blobErr, terminal.ErrNotDarwin) {
					fmt.Fprintf(out, "  %-22s skipped (macOS only)\n", it.domain)
					continue
				}
				return fmt.Errorf("capture %s preference domain before mutating: %w", it.domain, blobErr)
			}
			res := terminal.Apply(d)
			if res.Skipped && errors.Is(res.Err, terminal.ErrNotDarwin) {
				// Clean skip on a non-darwin host (defensive; this branch only
				// builds on darwin). Nothing mutated; nothing to roll back.
				fmt.Fprintf(out, "  %-22s skipped (macOS only)\n", it.domain)
				continue
			}
			if res.Err != nil {
				// A partial preference mutation must be reverted IMMEDIATELY: re-import
				// the captured pre-mutation export via the resource's own Restore hook
				// so the domain is left as it was, BEFORE we surface the error. We hold
				// a valid blob (capture is fail-closed above), so rollback always runs.
				// The caller then rolls the WHOLE open run back inline (reverting every
				// other target this run touched too); RollbackIncomplete remains a third
				// line of defence for a real crash.
				if rbErr := d.Restore(capturedBlob, capturedAbsent); rbErr != nil {
					return fmt.Errorf("apply %s preference domain failed (%v); inline rollback also failed: %w", it.domain, res.Err, rbErr)
				}
				return fmt.Errorf("apply %s preference domain: %w", it.domain, res.Err)
			}
			fmt.Fprintf(out, "  %-22s preference domain applied\n", it.domain)
			if res.Note != "" {
				fmt.Fprintf(out, "  %-22s note: %s\n", "", res.Note)
			}
			continue
		case kindAgents:
			// Agents targets deploy their DERIVED in-memory content through the
			// same Backuper/journal as dotfiles (agents.ApplyItem), with the
			// domain's repo-authoritative wording on refusals: a live edit is
			// never captured back in v1, so the fix is the repo copy (or --force).
			//
			// Every planned agents target is also collected for the persisted
			// target record (agents-targets.json, unioned post-commit): scoped
			// restore reverts from that record, so a later de-scope — or a
			// deleted repo — can never hide a previously applied target.
			agentsTargets[it.target.Name] = it.target.Home
			res, err := agents.ApplyItem(agents.Item{
				Key:     it.target.Name,
				Label:   it.domain,
				Target:  it.target,
				Content: it.content,
				Exec:    it.execBit,
			}, lastApplied, b, force)
			if err != nil {
				var conflict *dotfile.ConflictError
				if errors.As(err, &conflict) {
					conflicts = append(conflicts, it.domain)
					fmt.Fprintf(out, "  %-22s CONFLICT: edited live AND in the repo; not overwritten (agents targets are repo-authoritative — update the repo copy, or `ferry apply --force`)\n", it.domain)
					continue
				}
				// The empty-over-substantial data-loss guard aborts the run, same
				// as a dotfile target (the run rolls back inline).
				return err
			}
			if res.ForcedEmptyOverSubstantial {
				fmt.Fprintf(out, "warning: --force replaced %s (a substantial existing file) with an empty/blank repo source — real config content was overwritten (backed up; run `ferry restore` to recover)\n", res.ForcedPath)
			}
			if res.Action == dotfile.ActionSkipped {
				fmt.Fprintf(out, "  %-22s skipped (edited live; agents targets are repo-authoritative — update the repo copy, or `ferry apply --force`)\n", it.domain)
				continue
			}
			deferred = append(deferred, res)
			it.action = string(res.Action)
			fmt.Fprintf(out, "  %-22s %s\n", it.domain, res.Action)
			continue
		case kindTerminal:
			// Config-file terminal targets deploy their overlay-or-shared Content
			// through the same Backuper/journal as dotfiles (termcfg.ApplyItem).
			// Unlike dotfiles, though, capture has NO config-file terminal pass
			// (cmd/capture.go handles only declared dotfiles and the iterm2/terminal
			// PREFERENCE domains), so a drifted/conflicting terminal target is NOT a
			// capture candidate: the guidance points at updating the repo source or
			// `ferry apply --force`, never `ferry capture`.
			res, err := termcfg.ApplyItem(termcfg.Item{
				Key:     it.target.Name,
				Label:   it.domain,
				Target:  it.target,
				Content: it.content,
				Exec:    it.execBit,
			}, lastApplied, b, force)
			if err != nil {
				var conflict *dotfile.ConflictError
				if errors.As(err, &conflict) {
					conflicts = append(conflicts, it.domain)
					fmt.Fprintf(out, "  %-22s CONFLICT: local edits AND repo change; not overwritten (update the repo source to match, or `ferry apply --force`)\n", it.domain)
					continue
				}
				// The empty-over-substantial data-loss guard aborts the run, same
				// as a dotfile target (the run rolls back inline).
				return err
			}
			if res.ForcedEmptyOverSubstantial {
				fmt.Fprintf(out, "warning: --force replaced %s (a substantial existing file) with an empty/blank repo source — real config content was overwritten (backed up; run `ferry restore` to recover)\n", res.ForcedPath)
			}
			if res.Action == dotfile.ActionSkipped {
				fmt.Fprintf(out, "  %-22s skipped (local edits; update the repo source to match, or `ferry apply --force`)\n", it.domain)
				continue
			}
			deferred = append(deferred, res)
			it.action = string(res.Action)
			fmt.Fprintf(out, "  %-22s %s\n", it.domain, res.Action)
			continue
		case kindDotfile, kindOverlay:
			if it.skip {
				fmt.Fprintf(out, "  %-22s skipped (missing secret: %s)\n", it.domain, strings.Join(it.missing, ", "))
				continue
			}
			res, err := applyTarget(it, lastApplied, b, force)
			if err != nil {
				var conflict *dotfile.ConflictError
				if errors.As(err, &conflict) {
					conflicts = append(conflicts, it.domain)
					fmt.Fprintf(out, "  %-22s CONFLICT: uncaptured local edits; not overwritten (run `ferry capture`, or `ferry apply --force`)\n", it.domain)
					continue
				}
				// The empty-over-substantial data-loss guard: a default apply refuses
				// to zero a substantial live file with an empty/near-empty repo source.
				// This is a hard abort (not a skip) so the run rolls back and exits
				// non-zero; the error names the file and both sides of the hazard.
				return err
			}
			// --force pushed an empty/near-empty repo source OVER a substantial live
			// file. The overwrite proceeded (documented force semantics), but WARN,
			// naming the file and both sides of the hazard — silently zeroing a real
			// config must never be quiet.
			if res.ForcedEmptyOverSubstantial {
				fmt.Fprintf(out, "warning: --force replaced %s (a substantial existing file) with an empty/blank repo source — real config content was overwritten (backed up; run `ferry restore` to recover)\n", res.ForcedPath)
			}
			// A secret-routed target's deployed bytes hold substituted secret values;
			// flag the result so CommitLastApplied records only its hash, never the
			// plaintext bytes (secret-at-rest boundary).
			res.SecretRouted = it.secretRouted
			deferred = append(deferred, res)
			it.action = string(res.Action)
			fmt.Fprintf(out, "  %-22s %s\n", it.domain, res.Action)
		}
	}

	if err := commitRun(); err != nil {
		return fmt.Errorf("commit apply run: %w", err)
	}
	// Persist deferred last-applied ONLY after the journal commit succeeds, so a
	// crash/commit-error between the file write and here can never leave
	// last-applied ahead of a rolled-back file (Codex#3). CommitLastApplied
	// ignores results with no PendingHash (noop/skipped), so passing all is safe.
	if err := dotfile.CommitLastApplied(deferred, lastApplied); err != nil {
		return fmt.Errorf("commit last-applied: %w", err)
	}
	// Union this plan's agents targets into the persisted record (cumulative —
	// entries are never removed) so `ferry restore agents` can resolve the
	// full applied set WITHOUT the config repo, including later-de-scoped
	// targets. Post-commit like last-applied: a rolled-back run records nothing.
	if len(agentsTargets) > 0 {
		stateDir, err := paths.StateDir()
		if err != nil {
			return fmt.Errorf("record agents targets: %w", err)
		}
		if err := agents.RecordTargets(stateDir, agentsTargets); err != nil {
			return fmt.Errorf("record agents targets: %w", err)
		}
	}
	if len(conflicts) > 0 {
		fmt.Fprintf(out, "%d conflict(s) left unchanged: %s\n", len(conflicts), strings.Join(conflicts, ", "))
	}
	return nil
}

// applyTarget materialises one dotfile/overlay through the dotfile domain using
// the DEFERRED apply: it writes the file (backed up first, atomically) but does
// NOT persist last-applied — the recorded hash rides on Result.PendingHash for the
// caller to commit via CommitLastApplied AFTER the journal commit. Every target —
// whole-file-replace (generic, e.g. .gitconfig) and include-style (zsh sidecar)
// alike — deploys the effective bytes IN MEMORY via the shared apply core the
// agents domain uses, so no temp file is ever staged and a crash cannot leave the
// ALREADY-RENDERED plaintext of a secret-routed dotfile behind in $TMPDIR.
// it.content is the effective source: the local-wins decision (whole-file overlay)
// and zsh source-last composition are already baked in during planning, and any
// {{ferry.secret}} tokens are already rendered, so behaviour is preserved without
// re-selecting a source at write time.
func applyTarget(it *planItem, store *dotfile.Store, b dotfile.Backuper, force bool) (dotfile.Result, error) {
	t := it.target

	// DEFERRED semantics (last-applied via CommitLastApplied post-commit) keep
	// last-applied from advancing ahead of a rolled-back file. DefaultPerm governs
	// only a first-ever write — an existing home destination's mode is preserved by
	// the shared core.
	res, err := dotfile.ApplyContentDeferred(t, it.content, dotfile.DefaultPerm(), store, b, force)
	if err != nil {
		return dotfile.Result{}, err
	}
	return res, nil
}

// applyDeps runs the gated dependency install and persists the recorded
// installed set so `restore --packages` can later uninstall only what ferry
// installed. ErrNoPackageManager is reported, never a bootstrap trigger.
func applyDeps(ctx *cmdContext, out io.Writer) error {
	depsDir := filepath.Join(ctx.RepoPath, "deps")
	result, err := deps.Install(depsDir, deps.ExecRunner{})
	if err != nil {
		if errors.Is(err, deps.ErrNoPackageManager) {
			fmt.Fprintln(out, "deps: no package manager present; skipping dependency install")
			return nil
		}
		return fmt.Errorf("install dependencies: %w", err)
	}
	installed := result.RecordedInstalledSet()
	if err := persistInstalledSet(installed); err != nil {
		return fmt.Errorf("record installed packages: %w", err)
	}
	fmt.Fprintf(out, "deps: installed %d package(s)\n", len(installed))
	return nil
}

// printPlan renders the planned actions (dry-run / diff). For dotfile/overlay
// targets it prints the REAL three-way classification computed during planning
// (it.state) — the same resolution apply acts on — rather than a blanket "would
// deploy": a clean target is shown clean, a conflict as conflict, a missing
// secret as blocked. So a clean machine's diff shows "no changes" and a
// conflicted/repo-ahead target is previewed faithfully. It is print-only and
// writes nothing.
func printPlan(out io.Writer, plan []planItem) {
	if len(plan) == 0 {
		fmt.Fprintln(out, "nothing to apply (no in-scope domains)")
		return
	}

	// Count the targets diff would actually change so a clean machine reports
	// "no changes" instead of an empty "would apply" header.
	pending := 0
	for _, it := range plan {
		if planItemPending(it) {
			pending++
		}
	}
	if pending == 0 {
		fmt.Fprintln(out, "no changes: every in-scope target is already in sync")
		return
	}

	colour := stateColourer(out)
	var create, update, conflict int

	fmt.Fprintln(out, "ferry would apply:")
	for _, it := range plan {
		switch it.kind {
		case kindPreference:
			// Render the REAL native preference-domain plan (from d.Plan()): the
			// line carries the macOS preference DOMAIN ID (e.g.
			// com.googlecode.iterm2) plus the native-mechanism summary, tagged as a
			// "preference domain" — the AC-terminal-config diff marker, never a
			// hardcoded or file-copy string. We compose from the PlanEntry fields
			// rather than its String() so the rendered line carries no "file copy"
			// phrase a file-copy tripwire could match.
			if it.prefDomain != nil {
				pe := it.prefDomain.Plan()
				switch {
				case pe.Skipped:
					fmt.Fprintf(out, "  %-22s [preference domain] %s — skipped (macOS only)\n", it.domain, pe.Domain)
				case it.prefApplied:
					// Already applied on this machine (engine baseline recorded): not a
					// pending change. Still surfaced as a preference domain (keeps the
					// domain id + native marker the AC tripwire reads), but tagged
					// "managed (re-apply on demand)" rather than a false "would apply".
					fmt.Fprintf(out, "  %-22s [preference domain] %s — managed (re-apply on demand)\n", it.domain, pe.Domain)
				default:
					update++
					fmt.Fprintf(out, "  %-22s [preference domain] %s — %s\n", it.domain, pe.Domain, pe.Summary)
				}
			}
		case kindAgents:
			// Agents targets share the dotfile states but carry the domain's
			// repo-authoritative guidance: a live edit is fixed in the repo copy
			// (capture never ingests these in v1), so the pointers differ.
			switch it.state {
			case dotfile.StateClean:
				fmt.Fprintf(out, "  %-22s %s (already in sync)\n", it.domain, colour(colGreen, "clean"))
			case dotfile.StateMissing:
				create++
				fmt.Fprintf(out, "  %-22s %s\n", it.domain, colour(colYellow, "would create"))
			case dotfile.StateRepoAhead:
				update++
				fmt.Fprintf(out, "  %-22s %s\n", it.domain, colour(colYellow, "would update"))
			case dotfile.StateLocallyDrifted:
				fmt.Fprintf(out, "  %-22s %s (edited live; repo-authoritative — update the repo copy, or `ferry apply --force`)\n", it.domain, colour(colYellow, "would skip"))
			case dotfile.StateConflict:
				conflict++
				fmt.Fprintf(out, "  %-22s %s (edited live AND in the repo; update the repo copy, or `ferry apply --force`)\n", it.domain, colour(colRed, "conflict"))
			default:
				fmt.Fprintf(out, "  %-22s %s\n", it.domain, it.state)
			}
		case kindTerminal:
			// Config-file terminal targets share the dotfile states but, unlike
			// dotfiles, capture has NO config-file terminal pass, so a drift/conflict
			// is NOT a capture candidate: the guidance points at updating the repo
			// source or `ferry apply --force`, never `ferry capture`.
			switch it.state {
			case dotfile.StateClean:
				fmt.Fprintf(out, "  %-22s %s (already in sync)\n", it.domain, colour(colGreen, "clean"))
			case dotfile.StateMissing:
				create++
				fmt.Fprintf(out, "  %-22s %s\n", it.domain, colour(colYellow, "would create"))
			case dotfile.StateRepoAhead:
				update++
				fmt.Fprintf(out, "  %-22s %s\n", it.domain, colour(colYellow, "would update"))
			case dotfile.StateLocallyDrifted:
				fmt.Fprintf(out, "  %-22s %s (local edits; update the repo source to match, or `ferry apply --force`)\n", it.domain, colour(colYellow, "would skip"))
			case dotfile.StateConflict:
				conflict++
				fmt.Fprintf(out, "  %-22s %s (modified locally AND in the repo; update the repo source, or `ferry apply --force`)\n", it.domain, colour(colRed, "conflict"))
			default:
				fmt.Fprintf(out, "  %-22s %s\n", it.domain, it.state)
			}
		default:
			if it.skip {
				fmt.Fprintf(out, "  %-22s %s (missing secret: %s)\n", it.domain, colour(colYellow, "blocked"), strings.Join(it.missing, ", "))
				continue
			}
			switch it.state {
			case dotfile.StateClean:
				fmt.Fprintf(out, "  %-22s %s (already in sync)\n", it.domain, colour(colGreen, "clean"))
			case dotfile.StateMissing:
				create++
				fmt.Fprintf(out, "  %-22s %s\n", it.domain, colour(colYellow, "would create"))
			case dotfile.StateRepoAhead:
				update++
				fmt.Fprintf(out, "  %-22s %s\n", it.domain, colour(colYellow, "would update"))
			case dotfile.StateLocallyDrifted:
				fmt.Fprintf(out, "  %-22s %s (uncaptured local edits; run `ferry capture`)\n", it.domain, colour(colYellow, "would skip"))
			case dotfile.StateConflict:
				conflict++
				fmt.Fprintf(out, "  %-22s %s (modified locally AND in the repo; `ferry capture` or `ferry apply --force`)\n", it.domain, colour(colRed, "conflict"))
			default:
				fmt.Fprintf(out, "  %-22s %s\n", it.domain, it.state)
			}
		}
	}

	// One-line summary footer, only counting the actionable states.
	if summary := planSummary(create, update, conflict); summary != "" {
		fmt.Fprintf(out, "\n%s\n", summary)
	}
}

// planSummary renders a compact "N would create, M would update, K conflict"
// footer from the counted states, omitting zero categories. Returns "" when there
// is nothing actionable to summarise (all-clean plans are handled earlier).
func planSummary(create, update, conflict int) string {
	var parts []string
	if create > 0 {
		parts = append(parts, fmt.Sprintf("%d would create", create))
	}
	if update > 0 {
		parts = append(parts, fmt.Sprintf("%d would update", update))
	}
	if conflict > 0 {
		parts = append(parts, fmt.Sprintf("%d conflict", conflict))
	}
	return strings.Join(parts, ", ")
}

// planItemPending reports whether a plan item represents a change diff/apply
// would make — i.e. anything other than an already-clean target. A blocked
// (missing-secret) item counts as pending so the user is told it is held back;
// preference domains are always surfaced (their native Plan() describes the
// mechanism). Only a StateClean dotfile/overlay is "nothing to do".
func planItemPending(it planItem) bool {
	switch it.kind {
	case kindDotfile, kindOverlay, kindAgents, kindTerminal:
		if it.skip {
			return true
		}
		return it.state != dotfile.StateClean
	case kindPreference:
		// A macOS-only domain on a non-darwin host is a no-op skip, never a pending
		// change: apply mutates nothing on Linux (terminal.Apply no-ops via
		// ErrNotDarwin), so diff must not count it as work to do — it is surfaced as a
		// "skipped (macOS only)" line, not a pending change.
		if it.prefDomain != nil && it.prefDomain.Plan().Skipped {
			return false
		}
		// A terminal domain whose immutable baseline is already recorded was applied
		// before; re-applying is idempotent, so a clean machine should NOT show it as
		// a pending change. prefApplied is set from a non-mutating baseline stat even
		// on the read-only preview path, so a managed domain is non-pending there too;
		// a never-applied domain stays surfaced (the native Plan() describes the
		// mechanism).
		return !it.prefApplied
	default:
		return true
	}
}

// descopeDotfileWarnings returns warnings for DOTFILES ferry previously applied
// (they have a last-applied record) but which the manifest no longer declares.
// Files are left untouched (de-scope = warn, never auto-remove); the warning
// states they are no longer managed and how to revert via `ferry restore <name>`.
//
// It enumerates the dotfiles ferry has applied on this machine — exactly the keys
// of the last-applied store, via the READ-ONLY Store.RecordedNames() — and warns
// for each one the manifest no longer reports in scope. This is a write-free
// warning pass: the store is opened read-only by the caller, and enumeration
// never persists.
//
// KEY NORMALIZATION: the store records each target by its BARE name (Target.Name,
// e.g. "zshrc"), whereas DeclaredDotfiles() returns the manifest's "."-prefixed
// form (e.g. ".zshrc"). Both sides are trimmed to bare before comparison so a
// still-in-scope dotfile is never spuriously reported as de-scoped.
func descopeDotfileWarnings(ctx *cmdContext, store *dotfile.Store) []string {
	// Bare-name set of dotfiles still in scope. When the dotfiles domain is wholly
	// unmanaged (no declarations) this is empty, so EVERY recorded name warns.
	inScope := map[string]bool{}
	if ctx.Scope.IsManaged("dotfiles") {
		for _, name := range ctx.Scope.DeclaredDotfiles() {
			inScope[strings.TrimPrefix(name, ".")] = true
		}
	}

	var out []string
	for _, name := range store.RecordedNames() {
		// Skip the IMPLICIT zsh SIDECAR key only. The zsh include-style strategy
		// records its materialised sidecar (~/.zshrc.local) under a "<zshbare>.local"
		// key in the SAME last-applied store as real dotfiles, but a sidecar is NOT a
		// standalone declared dotfile — the in-scope set only carries the base name
		// ("zshrc"). Treating it as a de-scoped dotfile would falsely warn that a
		// validly-managed ~/.zshrc.local "is no longer managed". isSidecarKey matches
		// ONLY that precise shape (a zsh include-style base + ".local"), so a REAL
		// declared dotfile that merely ends in ".local" (e.g. ".env.local") is NOT
		// suppressed and still warns correctly when de-scoped.
		if isSidecarKey(name) {
			continue
		}
		// Agents-domain records are namespaced under "agents/" and report their
		// OWN de-scope (descopeAgentsWarnings) — they are never declared dotfiles,
		// so warning here would be a false positive on every apply.
		if strings.HasPrefix(name, agentsKeyPrefix) {
			continue
		}
		// Config-file terminal records are namespaced under "terminals/" and
		// report their OWN de-scope (descopeTerminalConfigWarnings) — they are
		// never declared dotfiles, so warning here would falsely report every
		// still-managed terminal target as de-scoped on every apply.
		if strings.HasPrefix(name, termcfg.KeyPrefix) {
			continue
		}
		if inScope[strings.TrimPrefix(name, ".")] {
			continue
		}
		out = append(out, fmt.Sprintf(
			"warning: %s is no longer managed; existing file left as-is (now unmanaged). To fully revert: ferry restore %s",
			name, name))
	}
	sort.Strings(out)
	return out
}

// isSidecarKey reports whether a recorded last-applied name is the IMPLICIT zsh
// SIDECAR overlay ferry materialises for an include-style domain (e.g.
// "zshrc.local" / ".zshrc.local" — the ~/.zshrc.local file planDotfiles deploys
// when local/zsh/<bare>.local exists), rather than a user-declared dotfile that
// merely happens to end in ".local".
//
// PRECISION matters: the sidecar is recorded under "<bare>.local" where <bare> is
// an include-style zsh domain (zshrc/zshenv/zprofile) — it is NOT in
// DeclaredDotfiles, it is a side effect of applying the zsh domain. A user dotfile
// like ".env.local" or ".foo.local" IS a declared dotfile and must STILL warn when
// de-scoped, so a blanket ".local" suffix is too broad. We identify the sidecar
// precisely by its recorded shape: bare base (sans the ".local" overlay suffix) is
// a recognised zsh include-style domain. So ".env.local" (base "env", not zsh)
// warns; the zsh "~/.zshrc.local" sidecar (base "zshrc") is suppressed.
func isSidecarKey(name string) bool {
	bare := strings.TrimPrefix(name, ".")
	base := strings.TrimSuffix(bare, ".local")
	return base != bare && isZsh(base)
}

// descopeTerminalWarnings warns about terminal preference DOMAINS ferry
// previously applied but the manifest no longer manages. Terminal resources live
// in the backup engine's immutable baseline (NOT the dotfile last-applied store),
// so the previously-applied signal is engine.HasBaseline(ResourcePath(domain)).
// Files/preferences are left in place (now unmanaged); the warning states how to
// fully revert via `ferry restore <domain>`.
func descopeTerminalWarnings(ctx *cmdContext, eng *backup.Engine) []string {
	var out []string
	for _, domain := range []string{"iterm2", "terminal"} {
		if ctx.Scope.IsManaged(domain) {
			continue
		}
		// apply's BackupResource records the NATIVE preference domain id
		// (com.googlecode.iterm2 / com.apple.Terminal), NOT the user-facing scope
		// name, so the baseline lookup must use the mapped id — otherwise a
		// de-scoped terminal domain that WAS applied is never detected/warned.
		prefID, ok := terminalPrefDomainID(domain)
		if !ok {
			continue
		}
		if eng.HasBaseline(backup.ResourcePath(prefID)) {
			out = append(out, fmt.Sprintf(
				"warning: %s is no longer managed; existing preferences left as-is (now unmanaged). To fully revert: ferry restore %s",
				domain, domain))
		}
	}
	sort.Strings(out)
	return out
}

// backuperFunc adapts the transactional engine (whose BackupAndWrite takes a
// live, unexported *run) to the dotfile.Backuper interface (which writes one
// target). The run is captured by the closure that builds it, so cmd never names
// the engine's opaque run type while every write still lands in one journal entry.
type backuperFunc func(target string, content []byte, perm os.FileMode) error

func (f backuperFunc) BackupAndWrite(target string, content []byte, perm os.FileMode) error {
	return f(target, content, perm)
}

// regularRepoFile reports whether cand is an existing REGULAR repo file. It is
// GUARD-FIRST: it routes cand through safeRepoPath(repoRoot, cand) BEFORE any
// filesystem syscall, and only Lstats the SAFE path safeRepoPath returns. This
// closes the symlinked-PARENT bypass: a bare os.Lstat(<repo>/dotfiles/x) FOLLOWS a
// symlinked `dotfiles` parent (e.g. `<repo>/dotfiles -> ~/.ssh`) into ~/.ssh before
// any guard runs. safeRepoPath walks from repoRoot component-by-component, never
// traversing a symlinked parent, so when it refuses we return false WITHOUT ever
// stat-ing the candidate. On the safe path, os.Lstat (does NOT follow symlinks)
// means a symlinked LEAF Lstats as a symlink (not a regular file) and is likewise
// "not a usable source". The downstream read/write still re-routes through
// safeRepoPath. INVARIANT: no os.Lstat/os.Stat of a repo candidate happens before
// its FULL path is verified symlink-free and in-repo.
func regularRepoFile(repoRoot, cand string) bool {
	safe, err := safeRepoPath(repoRoot, cand)
	if err != nil {
		return false
	}
	fi, err := os.Lstat(safe)
	return err == nil && fi.Mode().IsRegular()
}

// resolveDotfileSource finds the repo-side source for a declared dotfile name,
// probing the candidate layouts: dotfiles/<bare> (canonical), dotfiles/.<bare>,
// and a top-level .<bare>. Returns the first that exists.
func resolveDotfileSource(repo, name string) (string, bool) {
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

// resolveOverlaySource finds the repo-side .local overlay for a dotfile, under
// local/<domain>/<bare>.local. zsh maps to the "zsh" domain dir.
func resolveOverlaySource(repo, bare string) (string, bool) {
	domain := bare
	if isZsh(bare) {
		domain = "zsh"
	}
	cand := filepath.Join(repo, "local", domain, bare+".local")
	if regularRepoFile(repo, cand) {
		return cand, true
	}
	return "", false
}

func isZsh(bare string) bool { return bare == "zshrc" || bare == "zshenv" || bare == "zprofile" }

// appendSourceDirective appends a real `source ~/<file>` line so the overlay is
// sourced LAST (after the shared file's own content) and therefore wins. It is
// idempotent: if the directive is already present (uncommented), raw is returned
// unchanged so re-apply produces byte-identical content.
func appendSourceDirective(raw []byte, file string) []byte {
	directive := "[ -f ~/" + file + " ] && source ~/" + file
	if sourceDirectivePresent(raw, file) {
		return raw
	}
	body := raw
	if len(body) > 0 && body[len(body)-1] != '\n' {
		body = append(append([]byte{}, body...), '\n')
	}
	return append(body, []byte("\n# ferry: per-machine overlay, sourced last so it wins\n"+directive+"\n")...)
}

// stripSourceDirective removes ferry's managed include boilerplate for file (the
// `# ferry: per-machine overlay …` comment and the `[ -f ~/<file> ] && source
// ~/<file>` line appendSourceDirective injects) from content, so capture's
// EFFECTIVE-content diff can route the user's real edits to shared WITHOUT
// committing ferry's own generated include line. It is the inverse of
// appendSourceDirective and is conservative: it drops only the exact managed
// marker comment and an uncommented source/`.`-include line naming file; any
// user-authored source line for a different target is left untouched. Used on
// the shared-capture write path for zsh so shared never gains ferry boilerplate.
func stripSourceDirective(content []byte, file string) []byte {
	const marker = "# ferry: per-machine overlay, sourced last so it wins"
	lines := strings.Split(string(content), "\n")
	kept := make([]string, 0, len(lines))
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == marker {
			continue
		}
		// Drop an uncommented include line (`source …file…` / `. …file…`) naming
		// this overlay file — ferry's injected directive. A commented or unrelated
		// line is kept.
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, file) {
			lc := strings.ToLower(trimmed)
			if strings.Contains(lc, "source ") || strings.Contains(trimmed, ". ") {
				continue
			}
		}
		kept = append(kept, raw)
	}
	// Collapse a blank line left where the injected block stood: appendSourceDirective
	// prefixes its block with a single blank line, so trimming one trailing blank run
	// to a single newline restores the raw shared file's tail shape.
	out := strings.Join(kept, "\n")
	out = strings.TrimRight(out, "\n")
	if len(content) > 0 && content[len(content)-1] == '\n' {
		out += "\n"
	}
	return []byte(out)
}

// sourceDirectivePresent reports whether content already has an uncommented
// `source <…file…>` / `. <…file…>` directive for file.
func sourceDirectivePresent(content []byte, file string) bool {
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code := line
		if h := strings.IndexByte(code, '#'); h >= 0 {
			code = strings.TrimSpace(code[:h])
		}
		if !strings.Contains(code, file) {
			continue
		}
		lc := strings.ToLower(code)
		if strings.Contains(lc, "source ") || strings.Contains(code, ". ") {
			return true
		}
	}
	return false
}

// isSecretRouted reports whether content references the secret store — it carries
// at least one {{ferry.secret ...}} placeholder that apply substitutes with a real
// value. A secret-routed target's deployed bytes hold plaintext secrets, so its
// last-deployed baseline records ONLY the content hash, never the bytes (gated via
// dotfile.Result.SecretRouted in CommitLastApplied). It is judged on the PRE-render
// content — the source referenced the store regardless of the substituted value.
func isSecretRouted(content []byte) bool {
	return len(secret.DetectPlaceholders(string(content))) > 0
}

// renderSecrets runs the placeholder renderer; a missing referenced secret means
// skip (Skip=true, no rendered output). Content without placeholders renders to
// itself.
func renderSecrets(store *secret.Store, content []byte) (rendered []byte, missing []string, skip bool, err error) {
	res, err := store.RenderPlaceholders(string(content))
	if err != nil {
		return nil, nil, false, err
	}
	if res.Skip {
		return nil, res.Missing, true, nil
	}
	return []byte(res.Rendered), nil, false, nil
}

// persistInstalledSet records the packages ferry has installed under ferry's
// state dir, so `restore --packages` can later uninstall ONLY these. It is
// CUMULATIVE: the existing recorded set is read and UNIONed with this run's newly
// installed packages (deduped, sorted), then written back. So an idempotent later
// `apply --deps` that installs nothing new does NOT erase earlier records —
// restore --packages still uninstalls everything ferry ever installed. Format:
// newline-delimited package names in ~/.local/state/ferry/deps-installed.txt.
func persistInstalledSet(installed []string) error {
	stateDir, err := paths.StateDir()
	if err != nil {
		return err
	}
	// Symlink-harden before writing: a state dir symlinked into ~/.ssh or a system
	// path must never be written through. Lexical; never touches ~/.ssh.
	if err := paths.HardenStoreDir(stateDir); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(stateDir, "deps-installed.txt")

	// Read the existing recorded set and union this run's packages into it.
	set := map[string]struct{}{}
	if prior, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(prior), "\n") {
			if p := strings.TrimSpace(line); p != "" {
				set[p] = struct{}{}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, p := range installed {
		if p = strings.TrimSpace(p); p != "" {
			set[p] = struct{}{}
		}
	}

	union := make([]string, 0, len(set))
	for p := range set {
		union = append(union, p)
	}
	sort.Strings(union)

	data := []byte(strings.Join(union, "\n"))
	if len(union) > 0 {
		data = append(data, '\n')
	}
	// Atomic temp+rename: this file is the CUMULATIVE record restore --packages
	// reads to know exactly what ferry installed. A crash mid-write with a plain
	// os.WriteFile could truncate it, silently dropping earlier packages from the
	// uninstall set. AtomicWrite leaves the prior record intact on crash and swaps
	// in the full new content only on success.
	return backup.AtomicWrite(path, data, 0o600)
}

func isDarwin() bool { return platform.IsDarwin() }

// buildTerminalDomain constructs the native macOS preference domain for a
// terminal scope name, applying the documented LOCAL-WINS overlay rule: a
// per-machine local capture (local/<domain>/<id>.plist) takes WHOLE-DOMAIN
// precedence over the shared repo copy, exactly like a plist-domain dotfile
// ("machine-divergent settings go local wholesale"). Capture can route a terminal
// domain to local/<domain>/<id>.plist; without this, apply only ever read the
// shared repo path, so a local-routed terminal capture was DEAD (never
// materialised). Both branches now prefer the local overlay when present:
//
//   - iTerm2: when local/iterm2/<id>.plist exists, point PrefsCustomFolder at the
//     LOCAL overlay folder (local/iterm2/) so iTerm2 loads the per-machine plist;
//     otherwise the shared repo iterm2/ folder. iTerm2 reads
//     com.googlecode.iterm2.plist from whichever folder it is pointed at, and
//     capture writes the local plist under exactly that name.
//   - Apple Terminal: import the LOCAL export blob (local/terminal/<id>.plist) when
//     present, else the shared committed blob, else nil (manage backup/restore only,
//     Apply no-ops the import).
//
// Both share the production ExecRunner (which shells `defaults` via PATH). Callers
// gate on platform.IsDarwin(); the terminal package itself stays darwin-guarded.
//
// Secret render-or-SKIP parity with dotfiles: the bytes the domain would deploy are
// passed through the secret renderer BEFORE the domain is constructed:
//   - Apple Terminal: the export blob's {{ferry.secret ...}} placeholders are
//     rendered; the RENDERED bytes are what `defaults import` imports. A MISSING
//     secret returns its refs in `missing` and a nil domain — the caller skips the
//     import entirely, leaving live Apple Terminal config intact.
//   - iTerm2: iTerm2 loads a FOLDER (PrefsCustomFolder). Rather than point it at the
//     RAW repo folder (whose com.googlecode.iterm2.plist may contain unrendered
//     {{ferry.secret ...}} placeholders), ferry RENDERS the chosen repo plist and
//     STAGES the rendered copy into a ferry-owned staging folder, then points
//     PrefsCustomFolder at THAT folder — so iTerm2 always loads the rendered plist,
//     never the raw placeholder one. The leaf plist is safeRepoPath-validated: a
//     REFUSED leaf (symlink/escape/under ~/.ssh) returns err so the caller SKIPS the
//     domain (the refusal is honoured for the very file iTerm2 would load, not
//     swallowed). A MISSING referenced secret returns its refs in `missing` and a nil
//     domain — caller skips, live iTerm2 config intact. The returned iterm2Stage
//     carries the rendered bytes + staging plist path the mutating path materialises
//     via the backup engine before Apply.
//
// Returns (domain, stage, missing, err): a non-empty `missing` means SKIP this domain
// with a reported notice; err is the symlink/escape REFUSAL (also a skip). stage is
// meaningful only for iterm2 (the rendered plist to materialise).
func buildTerminalDomain(repo, scopeName string, store *secret.Store) (*terminal.PreferenceDomain, iterm2Stage, error) {
	switch scopeName {
	case "iterm2":
		folder, err := iterm2PrefsFolder(repo)
		if err != nil {
			return nil, iterm2Stage{}, err
		}
		stage, missing, err := iterm2RenderStage(repo, folder, store)
		if err != nil {
			// REFUSED leaf plist (symlink/escape/under ~/.ssh): skip the iterm2 domain.
			// Returning err here means iTerm2 is NEVER pointed at the staging folder for
			// a poisoned leaf — the refusal is honoured, not swallowed.
			return nil, iterm2Stage{}, err
		}
		if len(missing) > 0 {
			return nil, iterm2Stage{Missing: missing}, nil
		}
		return terminal.NewITerm2(stage.folder, terminal.ExecRunner{}), stage, nil
	default: // "terminal" / Apple Terminal
		blob, err := appleTerminalExportBlob(repo)
		if err != nil {
			// PRESENT-but-REFUSED local overlay (symlink/escape/under ~/.ssh): skip the
			// Apple Terminal domain with the refusal rather than importing shared.
			return nil, iterm2Stage{}, err
		}
		rendered, missing, skip, err := renderSecrets(store, blob)
		if err != nil {
			return nil, iterm2Stage{}, err
		}
		if skip {
			return nil, iterm2Stage{Missing: missing}, nil
		}
		return terminal.NewAppleTerminal(rendered, terminal.ExecRunner{}), iterm2Stage{}, nil
	}
}

// iterm2Stage carries the RENDERED STAGING result for the iTerm2 domain: the
// ferry-owned staging folder PrefsCustomFolder points at, the absolute staging
// plist path the mutating apply writes, and the rendered bytes to write there.
// Missing carries the unresolved secret refs when rendering must SKIP the domain.
type iterm2Stage struct {
	folder    string   // staging folder -> PrefsCustomFolder
	plistPath string   // <folder>/com.googlecode.iterm2.plist (absolute)
	content   []byte   // rendered plist bytes to materialise
	Missing   []string // unresolved secret refs (skip the domain when non-empty)
}

// iterm2RenderStage validates + renders the iTerm2 leaf plist the chosen prefs
// folder would load (com.googlecode.iterm2.plist) and computes the ferry-owned
// RENDERED STAGING destination PrefsCustomFolder will point at instead of the raw
// repo folder.
//
//   - The leaf plist is safeRepoPath-validated. A REFUSED leaf (symlink / repo
//     escape / resolving under ~/.ssh or a system path) returns the refusal ERROR so
//     the caller SKIPS the whole iterm2 domain — iTerm2 is never pointed anywhere for
//     a poisoned leaf (fixes "refused plist swallowed": the refusal governs the very
//     file iTerm2 loads, not just the containing folder).
//   - The plist is read and secret-rendered. A MISSING referenced secret returns its
//     refs in `missing` (the caller skips; live iTerm2 config left intact).
//   - On success (all referenced secrets present, or no placeholders) the rendered
//     bytes + staging destination are returned. The mutating path writes them into
//     the staging folder via the backup engine (tracked + reversible) before pointing
//     PrefsCustomFolder there. We ALWAYS stage (even a placeholder-free plist that
//     renders to itself) so the path is uniform and the leaf is always validated +
//     rendered.
//
// The staging folder is <StateDir>/rendered/iterm2 (created 0700 by the mutating
// path; the plist written 0600). A missing leaf plist (fresh repo folder, nothing to
// load) yields empty content and no staging — there is nothing for iTerm2 to render.
func iterm2RenderStage(repo, folder string, store *secret.Store) (iterm2Stage, []string, error) {
	plist := filepath.Join(folder, terminal.ITerm2Domain+".plist")
	safe, err := safeRepoPath(repo, plist)
	if err != nil {
		// REFUSED leaf: surface the error so the caller skips the domain.
		return iterm2Stage{}, nil, err
	}
	data, err := os.ReadFile(safe)
	if err != nil {
		if os.IsNotExist(err) {
			// GENUINELY ABSENT leaf plist (fresh folder) — nothing for iTerm2 to load,
			// nothing to stage. Point at the (empty) staging folder so the path is still
			// uniform and never the raw repo folder; no plist is written.
			stateDir, serr := paths.StateDir()
			if serr != nil {
				return iterm2Stage{}, nil, serr
			}
			return iterm2Stage{folder: filepath.Join(stateDir, "rendered", "iterm2")}, nil, nil
		}
		// PRESENT-but-UNREADABLE leaf (permission / I/O / any non-NotExist error):
		// FAIL-CLOSED. A present plist that cannot be read must NEVER be downgraded to
		// "absent" and then mutate live state by pointing PrefsCustomFolder at an empty
		// staging folder. Surface the error so the caller SKIPS the iterm2 domain with a
		// warning, leaving live iTerm2 state intact.
		return iterm2Stage{}, nil, fmt.Errorf("iterm2: read leaf plist %s: %w", plist, err)
	}
	rendered, missing, skip, err := renderSecrets(store, data)
	if err != nil {
		return iterm2Stage{}, nil, err
	}
	if skip {
		return iterm2Stage{}, missing, nil
	}
	stateDir, err := paths.StateDir()
	if err != nil {
		return iterm2Stage{}, nil, err
	}
	stageFolder := filepath.Join(stateDir, "rendered", "iterm2")
	return iterm2Stage{
		folder:    stageFolder,
		plistPath: filepath.Join(stageFolder, terminal.ITerm2Domain+".plist"),
		content:   rendered,
	}, nil, nil
}

// iterm2PrefsFolder picks the prefs folder iTerm2 is pointed at: the per-machine
// LOCAL overlay folder local/iterm2/ when a local plist exists there (local wins
// whole-domain), else the shared repo iterm2/ folder. iTerm2 loads
// com.googlecode.iterm2.plist from PrefsCustomFolder, and capture's local route
// writes local/iterm2/<id>.plist under that same id, so pointing iTerm2 at the
// local folder deploys the captured per-machine plist.
//
// The CHOSEN folder (local OR shared) is safeRepoPath-validated BEFORE it is
// returned to be handed to `defaults write ... PrefsCustomFolder`. A repo-side
// `iterm2 -> ~/.ssh` (or a folder escaping the repo / resolving under a system
// location) must NEVER be persisted as iTerm2's prefs folder — that would bypass
// the repo-side symlink policy for a managed terminal domain.
//
// Local overlay PRESENCE is decided GUARD-FIRST: safeRepoPath(repo, localPlist)
// runs BEFORE any os.Lstat of the leaf, then presence is Lstat'd on the SAFE path
// safeRepoPath returns. This closes a symlinked-PARENT bypass: a raw
// os.Lstat(<repo>/local/iterm2/<id>.plist) does NOT follow the final leaf but DOES
// traverse a symlinked PARENT (e.g. `local/iterm2 -> ~/.ssh`), touching outside the
// repo BEFORE the guard runs. safeRepoPath walks component-by-component from the
// repo root and never traverses a symlinked parent, so a poisoned parent (or a
// symlinked/escaping leaf) is REFUSED before any leaf stat. A PRESENT-but-REFUSED
// local overlay returns the refusal error so the caller SKIPS the iterm2 domain —
// it must NOT silently fall back to the shared settings (that would let a malicious
// local overlay mask its own refusal). Only an ABSENT local leaf (the safe path
// Lstats as not-exist) legitimately falls back to shared, whose own leaf/folder is
// then likewise validated; on refusal of the shared folder the error is returned so
// the domain is skipped rather than writing a poisoned PrefsCustomFolder.
func iterm2PrefsFolder(repo string) (string, error) {
	localFolder := filepath.Join(repo, "local", "iterm2")
	localPlist := filepath.Join(localFolder, terminal.ITerm2Domain+".plist")
	// GUARD FIRST: validate the leaf (component-by-component from the repo root,
	// never traversing a symlinked parent) BEFORE we stat it. A refusal here —
	// symlinked parent, symlinked/escaping leaf, or a target under ~/.ssh — SKIPS the
	// domain; we must NOT fall back to shared behind a present, poisoned overlay.
	safeLocalPlist, err := safeRepoPath(repo, localPlist)
	if err != nil {
		return "", err
	}
	// Presence is decided on the SAFE path WITHOUT following the final link, so a
	// symlinked leaf (already cleared by safeRepoPath as in-repo) still counts as
	// PRESENT and governs the domain.
	if li, lerr := os.Lstat(safeLocalPlist); lerr == nil {
		// Local overlay leaf EXISTS. It governs the domain. Validate the chosen prefs
		// folder too; a refusal SKIPS the domain rather than falling back to shared.
		if !li.Mode().IsRegular() {
			return "", fmt.Errorf("iterm2: local overlay leaf %s is not a regular file", localPlist)
		}
		if _, err := safeRepoPath(repo, localFolder); err != nil {
			return "", err
		}
		return localFolder, nil
	} else if !os.IsNotExist(lerr) {
		// A present-but-unstatable safe leaf is not a clean absence; skip the domain
		// rather than silently reaching for shared.
		return "", fmt.Errorf("iterm2: stat local overlay %s: %w", localPlist, lerr)
	}
	// ABSENT local leaf: legitimately fall back to shared. VALIDATE before use — a
	// symlinked/escaping <repo>/iterm2 is refused so `defaults write
	// PrefsCustomFolder` never persists it.
	sharedFolder := filepath.Join(repo, "iterm2")
	if _, err := safeRepoPath(repo, sharedFolder); err != nil {
		return "", err
	}
	return sharedFolder, nil
}

// appleTerminalExportBlob reads the `defaults export com.apple.Terminal` blob apply
// should import, applying local-wins: the per-machine LOCAL overlay
// (local/terminal/<id>.plist) takes precedence over the shared committed copy when
// present, so a local-routed Apple Terminal capture is actually re-imported on this
// machine. Returns (nil, nil) when neither exists (Apply then manages backup/restore
// only). Probes the conventional committed locations under each tree.
//
// Local-overlay probing is GUARD-FIRST: safeRepoPath(repo, cand) runs BEFORE any
// os.Lstat of the candidate, then presence is Lstat'd on the SAFE path. A raw
// os.Lstat of a local candidate does NOT follow the final leaf but DOES traverse a
// symlinked PARENT (e.g. `local/terminal -> <outside-repo>`), touching outside the
// repo BEFORE the guard runs; the guard-first walk never traverses a symlinked
// parent. A LOCAL candidate that is PRESENT (the safe path Lstats) but REFUSED by
// safeRepoPath (symlinked parent/leaf, escape, or under ~/.ssh) returns a non-nil
// ERROR so buildTerminalDomain SKIPS the Apple Terminal domain with a refusal
// notice, rather than masking the poisoned overlay by importing the shared blob.
// Only an ABSENT local candidate falls through to the shared committed export,
// whose own candidate is then validated before the symlink-following read.
func appleTerminalExportBlob(repo string) ([]byte, error) {
	localCands := []string{
		filepath.Join(repo, "local", "terminal", terminal.AppleTerminalDomain+".plist"),
		filepath.Join(repo, "local", "terminal", terminal.AppleTerminalDomain),
	}
	for _, cand := range localCands {
		// GUARD FIRST: a refusal (symlinked parent/leaf, escape, under ~/.ssh) SKIPS
		// the domain — do not fall back to shared behind a poisoned local overlay.
		safe, err := safeRepoPath(repo, cand)
		if err != nil {
			return nil, err
		}
		// Probe presence on the SAFE path WITHOUT following the final link, so a
		// symlinked leaf (already cleared as in-repo) still counts as PRESENT.
		if _, lerr := os.Lstat(safe); lerr != nil {
			if os.IsNotExist(lerr) {
				continue // ABSENT local candidate: fall through to shared.
			}
			return nil, fmt.Errorf("terminal: stat local overlay %s: %w", cand, lerr)
		}
		data, rerr := os.ReadFile(safe)
		if rerr == nil {
			return data, nil
		}
		// PRESENT-but-UNREADABLE local overlay (permission / I/O / any error): the
		// Lstat above already proved the candidate EXISTS, so this is never genuine
		// absence. FAIL-CLOSED — return the error so buildTerminalDomain SKIPS the
		// Apple Terminal domain with a warning rather than importing a partial/empty
		// config or silently reaching for shared behind a present overlay. A present-
		// but-unreadable overlay must never be downgraded to "absent".
		return nil, fmt.Errorf("terminal: read local overlay %s: %w", cand, rerr)
	}
	// No local overlay present — use the shared committed export.
	for _, cand := range []string{
		filepath.Join(repo, "terminal", terminal.AppleTerminalDomain+".plist"),
		filepath.Join(repo, "terminal", terminal.AppleTerminalDomain),
	} {
		// Refuse a symlinked/escaping plist before the symlink-following read.
		safe, err := safeRepoPath(repo, cand)
		if err != nil {
			continue
		}
		data, rerr := os.ReadFile(safe)
		if rerr == nil {
			return data, nil
		}
		if os.IsNotExist(rerr) {
			// GENUINELY ABSENT candidate: try the next committed name, then absence.
			continue
		}
		// PRESENT-but-UNREADABLE shared export (permission / I/O / any non-NotExist
		// error): FAIL-CLOSED. A present export that cannot be read must NEVER be
		// downgraded to "absent" and then skip Apple Terminal as if unconfigured —
		// surface the error so the domain is SKIPPED with a warning, live state intact.
		return nil, fmt.Errorf("terminal: read committed export %s: %w", cand, rerr)
	}
	return nil, nil
}
