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
	"github.com/REPPL/ferry/internal/emacs"
	"github.com/REPPL/ferry/internal/iterm2profiles"
	"github.com/REPPL/ferry/internal/keybindings"
	"github.com/REPPL/ferry/internal/paths"
	"github.com/REPPL/ferry/internal/platform"
	"github.com/REPPL/ferry/internal/secret"
	"github.com/REPPL/ferry/internal/termcfg"
	"github.com/REPPL/ferry/internal/terminal"
)

func init() {
	// --force is an apply-only override (overwrite uncaptured local edits on a
	// conflict). Registered here so commands.go stays owned by the skeleton wave;
	// --deps is already declared there.
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
	// kindFile is the converged FileDomain item: any regular-file target under
	// $HOME reconciled by content -> target -> dotfile.ApplyContentDeferred. It
	// replaces the pre-fn-5 kindDotfile/kindOverlay/kindAgents/kindTerminal
	// quartet; the owning domain (planItem.fileDomain) selects the per-domain
	// report wording that used to be keyed on the kind.
	kindFile       planKind = iota // a FileDomain target (dotfile/overlay/agents/terminal config)
	kindPreference                 // a native macOS preference (plist/defaults) domain
)

// planItem is one unit of work apply would perform. It carries the fully
// resolved, secret-rendered content so diff and apply share identical planning.
type planItem struct {
	kind planKind
	// fileDomain is the owning FileDomain's scope name ("dotfiles", "agents",
	// "terminals") for a kindFile item. It selects the per-domain report wording
	// (capture guidance vs repo-authoritative vs repo-source) and the
	// agents-target recording that used to be keyed on the collapsed kinds. Empty
	// for kindPreference.
	fileDomain string
	domain     string         // human label (e.g. "zsh", "iterm2", ".zshrc")
	target     dotfile.Target // dotfile/overlay targets only
	content    []byte         // effective content to materialise (post-render)
	action     string         // computed action for reporting (created/updated/noop/conflict/skipped/preview)
	skip       bool           // true when a missing secret forces a skip
	missing    []string       // refs that were missing (when skip)
	note       string         // free-form note (de-scope warnings, preference TODO)

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

	// prefApplied is the observable "already applied" signal for a kindPreference
	// item: true when an immutable baseline exists for this domain's resource path
	// (HasBaseline(ResourcePath(prefID))), i.e. ferry applied it on this machine
	// before. It is the write-free, read-only proxy for "the terminal domain is
	// already managed", so a clean machine's diff shows "managed (re-apply
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
	force, _ := c.Flags().GetBool("force")
	skipWizard, _ := c.Flags().GetBool("skip-wizard")

	ctx, err := loadContext()
	if err != nil {
		return err
	}

	out := c.OutOrStdout()
	in := bufio.NewReader(c.InOrStdin())

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
// the last-applied store read-only so a pure preview (diff / status)
// never creates ferry state. Terminal de-scope warnings are computed separately
// on the mutating path (they need the backup engine's baseline).
//
// This is the READ-ONLY entry point (diff / status / init
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

	reg := buildRegistry(ctx)

	// FileDomain fan-out (fn-5): plan each in-scope FileDomain through the
	// converged registry, in the registry's LOAD-BEARING order (dotfiles, agents,
	// terminals), so item AND warning ORDERING matches the pre-fn-5 dispatch
	// byte-for-byte. Each managed domain's rich planItems — secrets rendered,
	// three-way state classified, risk assessed — is produced by its per-domain
	// planner via the filePlanner upcast (the frozen domains.FileItem cannot carry
	// state/skip/risk). An out-of-scope domain emits its own de-scope warnings
	// instead; dotfiles DEFER theirs to the END of the plan (after the preference
	// domains) via descopeDotfileWarnings, so descopeUnmanaged returns nil for it.
	// This replaces the hardcoded IsManaged("dotfiles"/"agents"/"terminals")
	// sequence — the agents/terminals per-domain gating notes still hold: each is
	// gated behind its own `[manage]` bool (default off) and collapses de-scoped
	// targets into de-scope warnings.
	for _, fd := range reg.FileDomains {
		fp, ok := fd.(filePlanner)
		if !ok {
			continue
		}
		if ctx.Scope.IsManaged(fd.Name()) {
			fitems, fwarn, ferr := fp.planItems(ctx, home, secretStore, lastApplied)
			if ferr != nil {
				return nil, nil, ferr
			}
			for i := range fitems {
				fitems[i].fileDomain = fd.Name()
			}
			items = append(items, fitems...)
			warnings = append(warnings, fwarn...)
		} else {
			warnings = append(warnings, fp.descopeUnmanaged(ctx, lastApplied)...)
		}
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
	for _, rd := range reg.ResourceDomains {
		domain := rd.Name()
		if !ctx.Scope.IsManaged(domain) {
			continue
		}
		d, missing, derr := buildTerminalDomain(ctx.RepoPath, domain, secretStore)
		if derr != nil {
			// The repo-side export blob is a symlink, escapes the repo, or resolves under
			// ~/.ssh / a system location. REFUSE the domain rather than `defaults import`
			// a poisoned blob — skip it with a clear notice.
			warnings = append(warnings, refusalWarning(domain, derr))
			continue
		}
		// Secret render-or-SKIP parity with dotfiles: the export bytes this domain
		// would deploy carry unrendered {{ferry.secret ...}} placeholders whose secret
		// is MISSING from the store. SKIP the whole terminal domain (live config left
		// intact) rather than `defaults import` an unrendered placeholder. With the
		// secret PRESENT the bytes are rendered and applied.
		if len(missing) > 0 {
			warnings = append(warnings, fmt.Sprintf("%-22s skipped (missing secret: %s)", domain, strings.Join(missing, ", ")))
			continue
		}
		// Observable "already applied": a recorded immutable baseline for this
		// domain's resource path means ferry applied it on this machine before, so
		// it is not a pending change. The mutating path (eng != nil) reads it from
		// the live engine; the read-only preview path (eng == nil) reads it via a
		// NON-MUTATING baseline stat (baselineHasBeenApplied) that never builds an
		// engine and so never creates the state dir/subdirs. Both observe the same
		// immutable baseline, so diff and apply agree. Only meaningful on
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
			kind:        kindPreference,
			domain:      domain,
			action:      "preview",
			prefDomain:  d,
			prefApplied: applied,
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
//   - include-style (zsh/tmux/git): build the target with IncludeSidecarTarget,
//     always deploy the SHARED file (with the format's include directive appended
//     last so the per-machine sidecar wins — shell `source`, tmux `source-file`,
//     git `[include]`), and materialise the sidecar (~/.<bare>.local). For git the
//     shared bytes are additionally identity-firewalled before the include.
//   - generic dotfiles (whole-file replace, e.g. .vimrc): deploy the LOCAL
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
		zsh := usesIncludeSidecar(bare)

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
			// git identity firewall: strip every identity key + [includeIf …] block
			// from the SHARED bytes before ferry's include directive is appended, so
			// a machine's commit identity can never deploy into the shared ~/.gitconfig
			// (a no-op for every other include-style dotfile, and for a clean git
			// source). Identity lives only in the ~/.<bare>.local sidecar below.
			base := sharedGitTransform(bare, raw)
			content := base
			if hasOverlay {
				content = appendSourceDirective(base, "."+bare+".local", directiveSpecFor(bare))
			}
			// Warn when git's credential.helper is `store` (plaintext ~/.git-credentials).
			warnings = append(warnings, gitCredentialHelperWarnings(bare, raw)...)

			rendered, missing, skip, err := renderSecrets(secretStore, content)
			if err != nil {
				return nil, nil, err
			}
			if skip {
				items = append(items, planItem{
					kind: kindFile, domain: "." + bare, target: t,
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
				kind: kindFile, domain: "." + bare, target: t, content: rendered,
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
						kind: kindFile, domain: "." + bare + ".local", target: ot,
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
					kind: kindFile, domain: "." + bare + ".local",
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
				kind: kindFile, domain: "." + bare, target: t,
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
			kind: kindFile, domain: "." + bare, target: t, content: rendered,
			state: state, secretRouted: secretRouted, risky: risky, riskReason: reason,
		})
	}

	return items, warnings, nil
}

// classifyItem computes the three-way dotfile.Classify state for a target against
// its EFFECTIVE (composed + secret-rendered) content — the exact bytes apply would
// deploy — PLUS the guided-apply risk verdict. It hashes the effective content IN
// MEMORY via dotfile.ClassifyContent: NO temp file is staged and NO secret-rendered
// byte is ever written to disk, so the diff/status preview path is fully
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
	// git identity firewall: the effective SHARED bytes have identity stripped
	// (matching the deploy composition in planDotfiles), so capture classifies and
	// diffs against the same firewalled content and never offers an identity line
	// as a shared hunk. A no-op for zsh/tmux and for a clean git source.
	base := sharedGitTransform(bare, raw)
	if !hasOverlay {
		return base
	}
	return appendSourceDirective(base, "."+bare+".local", directiveSpecFor(bare))
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
	// use, so it creates ferry's state dir). Read-only diff/status never reach here.
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
			if res.Skipped && errors.Is(res.Err, terminal.ErrITerm2Running) {
				// iTerm2 is running: importing its global plist now would be silently
				// lost (iTerm2 rewrites the domain on quit; cfprefsd caches it). SKIP
				// the domain, leaving live config intact — nothing was imported, so
				// there is NOTHING to roll back (rolling back would itself `defaults
				// import` into the running iTerm2). Warn loudly with the fix.
				fmt.Fprintf(out, "  %-22s skipped: quit iTerm2, then re-run `ferry apply` (a running iTerm2 would overwrite the imported preferences on quit)\n", it.domain)
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
		case kindFile:
			// Converged FileDomain arm (fn-5): every regular-file target —
			// dotfile/overlay, agents, config-file terminal — deploys its effective
			// in-memory content through the ONE shared apply core
			// (dotfile.ApplyContentDeferred): the three-way decision table,
			// first-touch adoption, conflict refusal, the empty-over-substantial
			// data-loss guard, and the Backuper-mediated atomic write. The owning
			// domain (it.fileDomain) selects the report wording that used to be keyed
			// on the separate kinds; the deploy mechanics are identical.

			// Every planned agents target is collected for the persisted target record
			// (agents-targets.json, unioned post-commit): scoped restore reverts from
			// that record, so a later de-scope — or a deleted repo — can never hide a
			// previously applied target. Dotfiles/terminals restore from the backup
			// baseline instead. Agents items never carry a missing-secret skip.
			if it.fileDomain == "agents" {
				agentsTargets[it.target.Name] = it.target.Home
			}
			// A missing referenced secret SKIPPED rendering during planning (never
			// deploy a literal {{ferry.secret}} to a live config); report it and move
			// on. Agents items are never secret-routed, so skip is ALWAYS false for
			// them and this shared guard is inert — which is why recording the agents
			// target (above) BEFORE this guard is currently safe. If agents ever gains
			// secret routing, a missing secret would skip here AFTER the target was
			// already recorded, so revisit the record-then-skip ordering then.
			if it.skip {
				fmt.Fprintf(out, "  %-22s skipped (missing secret: %s)\n", it.domain, strings.Join(it.missing, ", "))
				continue
			}
			// Fresh-write mode: 0755 for an executable source (agents hooks / terminal
			// helper scripts), else 0644; an existing destination's mode is preserved
			// by the core, and secretRouted clamps the written mode to owner-only and
			// records hash-only last-applied.
			perm := dotfile.DefaultPerm()
			if it.execBit {
				perm = 0o755
			}
			res, err := dotfile.ApplyContentDeferred(it.target, it.content, perm, lastApplied, b, force, it.secretRouted)
			if err != nil {
				var conflict *dotfile.ConflictError
				if errors.As(err, &conflict) {
					conflicts = append(conflicts, it.domain)
					fmt.Fprintf(out, "  %-22s %s\n", it.domain, fileConflictMessage(it.fileDomain))
					continue
				}
				// The empty-over-substantial data-loss guard aborts the run (rolled
				// back inline by the caller); a conflict is reported and skipped above.
				return err
			}
			// --force pushed an empty/near-empty repo source OVER a substantial live
			// file. The overwrite proceeded (documented force semantics), but WARN,
			// naming the file and both sides of the hazard — silently zeroing a real
			// config must never be quiet.
			if res.ForcedEmptyOverSubstantial {
				fmt.Fprintf(out, "warning: --force replaced %s (a substantial existing file) with an empty/blank repo source — real config content was overwritten (backed up; run `ferry restore` to recover)\n", res.ForcedPath)
			}
			// Agents and config-file terminal targets carry the domain's own wording
			// when apply SKIPS a locally-drifted target (repo-authoritative); a dotfile
			// skip has no special wording and falls through to the generic action line.
			if res.Action == dotfile.ActionSkipped {
				if msg := fileSkippedMessage(it.fileDomain); msg != "" {
					fmt.Fprintf(out, "  %-22s %s\n", it.domain, msg)
					continue
				}
			}
			// res.SecretRouted was already stamped by the apply core from the plan's
			// secret-routing flag, so CommitLastApplied records only this target's
			// hash, never the plaintext bytes.
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

// fileConflictMessage renders the per-domain CONFLICT report line body for a
// FileDomain target apply refused to overwrite (edited live AND in the repo).
// The wording is the domain's remedy: dotfiles point at `ferry capture`, while
// agents and config-file terminals are repo-authoritative (no capture pass) so
// they point at updating the repo copy/source. It is exactly the text the
// pre-fn-5 per-kind mutate arms printed, now selected by owning domain.
func fileConflictMessage(fileDomain string) string {
	switch fileDomain {
	case "agents":
		return "CONFLICT: edited live AND in the repo; not overwritten (agents targets are repo-authoritative — update the repo copy, or `ferry apply --force`)"
	case "terminals", "keybindings", "emacs":
		return "CONFLICT: local edits AND repo change; not overwritten (update the repo source to match, or `ferry apply --force`)"
	default:
		return "CONFLICT: uncaptured local edits; not overwritten (run `ferry capture`, or `ferry apply --force`)"
	}
}

// fileSkippedMessage renders the per-domain "skipped (locally drifted)" report
// line body for a FileDomain target apply left for its repo-authoritative
// remedy. Dotfiles have NO special wording — a locally-drifted dotfile is a
// capture candidate that falls through to the generic action line — so this
// returns "" for them.
func fileSkippedMessage(fileDomain string) string {
	switch fileDomain {
	case "agents":
		return "skipped (edited live; agents targets are repo-authoritative — update the repo copy, or `ferry apply --force`)"
	case "terminals", "keybindings", "emacs":
		return "skipped (local edits; update the repo source to match, or `ferry apply --force`)"
	default:
		return ""
	}
}

// applyDeps runs the gated dependency install and persists the recorded
// installed set so `restore --packages` can later uninstall only what ferry
// installed. ErrNoPackageManager is reported, never a bootstrap trigger.
func applyDeps(ctx *cmdContext, out io.Writer) error {
	depsDir := filepath.Join(ctx.RepoPath, "deps")
	result, err := deps.Install(depsDir, deps.ExecRunner{})
	switch {
	case errors.Is(err, deps.ErrNoPackageManager):
		// No OS package manager: report and fall through to npm globals, which are
		// ORTHOGONAL (a machine can have npm but no brew/apt).
		fmt.Fprintln(out, "deps: no package manager present; skipping dependency install")
	case err != nil:
		return fmt.Errorf("install dependencies: %w", err)
	default:
		installed := result.RecordedInstalledSet()
		if err := persistInstalledSet(installed); err != nil {
			return fmt.Errorf("record installed packages: %w", err)
		}
		fmt.Fprintf(out, "deps: installed %d package(s)\n", len(installed))
	}

	// npm globals: a SEPARATE, coexisting manager beside brew/apt (NOT another
	// DetectPackageManager choice — npm is detected independently by presence on
	// PATH, so a brew machine still reconciles npm). Gated on the npm-globals domain
	// being managed; install/reconcile-only (never uninstalls, so nothing is
	// recorded for `restore --packages`, mirroring the Homebrew cleanup-out rule).
	if ctx.Scope.IsManaged("npm-globals") {
		if err := applyNpmGlobals(ctx, out); err != nil {
			return err
		}
	}
	return nil
}

// applyNpmGlobals reconciles this machine's global npm packages to the committed
// deps/npm-globals.txt via `npm i -g`. A missing npm is a clean skip (mirrors the
// no-package-manager report): npm is optional and orthogonal to brew/apt. It never
// uninstalls, so there is nothing to persist for restore.
func applyNpmGlobals(ctx *cmdContext, out io.Writer) error {
	if !platform.HasNpm() {
		fmt.Fprintln(out, "npm-globals: npm not present; skipping global package install")
		return nil
	}
	depsDir := filepath.Join(ctx.RepoPath, "deps")
	names, err := deps.InstallNpmGlobals(depsDir, deps.ExecRunner{})
	if err != nil {
		return fmt.Errorf("install npm globals: %w", err)
	}
	fmt.Fprintf(out, "npm-globals: reconciled %d global package(s)\n", len(names))
	return nil
}

// printPlan renders the planned actions (diff / status). For dotfile/overlay
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
		case kindFile:
			// Converged FileDomain rendering (fn-5): all file targets share the
			// three-way state lines; the owning domain (it.fileDomain) selects the
			// locally-drifted / conflict guidance that used to be keyed on the kind —
			// dotfiles point at `ferry capture`, agents and config-file terminals are
			// repo-authoritative (no capture pass).
			switch it.fileDomain {
			case "agents":
				// Agents targets carry the domain's repo-authoritative guidance: a live
				// edit is fixed in the repo copy (capture never ingests these in v1).
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
			case "terminals", "keybindings", "emacs":
				// A missing referenced secret blocks this terminal target (render-or-SKIP);
				// surface it, exactly like a blocked dotfile, rather than a state line.
				// (keybindings carries no secrets, so it.skip is always false there.)
				if it.skip {
					fmt.Fprintf(out, "  %-22s %s (missing secret: %s)\n", it.domain, colour(colYellow, "blocked"), strings.Join(it.missing, ", "))
					continue
				}
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
	case kindFile:
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
		// Key-bindings records are namespaced under "keybindings/" and report their
		// OWN de-scope (descopeKeybindingsWarnings) — they are never declared
		// dotfiles, so warning here would falsely report a still-managed
		// key-bindings target as de-scoped on every apply.
		if strings.HasPrefix(name, keybindings.KeyPrefix) {
			continue
		}
		// Emacs-domain records are namespaced under "emacs/" and report their OWN
		// de-scope (descopeEmacsConfigWarnings) — they are never declared dotfiles,
		// so warning here would falsely report every still-managed Emacs target as
		// de-scoped on every apply.
		if strings.HasPrefix(name, emacs.KeyPrefix) {
			continue
		}
		// iTerm2 Dynamic Profiles records are namespaced under "iterm2-profiles/" and
		// report their OWN de-scope (descopeIterm2ProfilesWarnings) — they are never
		// declared dotfiles, so warning here would falsely report every still-managed
		// profile target as de-scoped on every apply.
		if strings.HasPrefix(name, iterm2profiles.KeyPrefix) {
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
	return base != bare && usesIncludeSidecar(base)
}

// descopeTerminalWarnings warns about terminal preference DOMAINS ferry
// previously applied but the manifest no longer manages. Terminal resources live
// in the backup engine's immutable baseline (NOT the dotfile last-applied store),
// so the previously-applied signal is engine.HasBaseline(ResourcePath(domain)).
// Files/preferences are left in place (now unmanaged); the warning states how to
// fully revert via `ferry restore <domain>`.
func descopeTerminalWarnings(ctx *cmdContext, eng *backup.Engine) []string {
	var out []string
	reg := buildRegistry(ctx)
	for _, rd := range reg.ResourceDomains {
		domain := rd.Name()
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
// local/<domain>/<bare>.local. The overlay directory is resolved through
// overlayDomainDir (zsh's bares map to local/zsh/, tmux.conf to local/tmux/,
// every other dotfile to its own bare name).
func resolveOverlaySource(repo, bare string) (string, bool) {
	cand := filepath.Join(repo, "local", overlayDomainDir(bare), bare+".local")
	if regularRepoFile(repo, cand) {
		return cand, true
	}
	return "", false
}

// appendSourceDirective appends spec's include line for `file` so the overlay is
// sourced LAST (after the shared file's own content) and therefore wins. spec is
// the per-format directive syntax (shell `source`, tmux `source-file`) selected
// by directiveSpecFor; every other byte of the injected block (the leading blank
// line, the marker comment, the trailing newline) is format-agnostic, so the zsh
// output is byte-identical to what ferry has always injected. It is idempotent:
// if the directive is already present (uncommented), raw is returned unchanged so
// re-apply produces byte-identical content.
func appendSourceDirective(raw []byte, file string, spec directiveSpec) []byte {
	directive := spec.render(file)
	if sourceDirectivePresent(raw, file, spec) {
		return raw
	}
	body := raw
	if len(body) > 0 && body[len(body)-1] != '\n' {
		body = append(append([]byte{}, body...), '\n')
	}
	return append(body, []byte("\n"+spec.marker+"\n"+directive+"\n")...)
}

// stripSourceDirective removes ferry's managed include boilerplate for file (the
// marker comment and the format-specific include line appendSourceDirective
// injects) from content, so capture's EFFECTIVE-content diff can route the user's
// real edits to shared WITHOUT committing ferry's own generated include line. It
// is the FILE-KEYED inverse of appendSourceDirective (spec.namesFile recognises
// the include line naming this exact overlay file) and is conservative: it drops
// only the exact managed marker comment and an uncommented include line naming
// file; any user-authored include line for a different target is left untouched.
// Used on the shared-capture write path so shared never gains ferry boilerplate.
func stripSourceDirective(content []byte, file string, spec directiveSpec) []byte {
	lines := strings.Split(string(content), "\n")
	kept := make([]string, 0, len(lines))
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == spec.marker {
			continue
		}
		// Drop an uncommented include line (this format's `source`/`source-file`/
		// git `path`) naming this overlay file — ferry's injected directive. A
		// commented or unrelated line is kept. For a MULTI-LINE directive (git's
		// `[include]` block) also pop the preceding section-header line (spec.header)
		// so no orphan `[include]` survives; single-line directives leave header ""
		// and this pop never fires.
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && spec.namesFile(trimmed, file) {
			if spec.header != "" && len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == spec.header {
				kept = kept[:len(kept)-1]
			}
			continue
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
// include directive (in spec's format) naming file.
func sourceDirectivePresent(content []byte, file string, spec directiveSpec) bool {
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code := line
		if h := strings.IndexByte(code, '#'); h >= 0 {
			code = strings.TrimSpace(code[:h])
		}
		if spec.namesFile(code, file) {
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

// buildTerminalDomain constructs the native macOS preference domain for a terminal
// scope name via the import-blob model both iTerm2 and Apple Terminal now use
// (v0.7.0 D4 retired iTerm2's custom-prefs-folder mechanism). It reads the committed
// export blob (terminalExportBlob, which applies the LOCAL-WINS overlay rule —
// local/<domain>/<id>.plist takes whole-domain precedence over the shared
// <domain>/<id>.plist), renders its {{ferry.secret ...}} placeholders, and wraps the
// rendered bytes in a PreferenceDomain whose Apply `defaults import`s them:
//
//   - Apple Terminal: NewAppleTerminal imports the rendered blob.
//   - iTerm2: NewITerm2 imports the rendered ALLOWLIST-FILTERED global plist,
//     REFUSING when iTerm2 is running (a running iTerm2 rewrites the domain on quit)
//     and flushing cfprefsd after a successful import.
//
// Both share the production ExecRunner (which shells `defaults` via PATH); iTerm2
// also gets the production ExecProcessController (pgrep/killall). Callers gate on
// platform.IsDarwin(); the terminal package itself stays darwin-guarded.
//
// Secret render-or-SKIP parity with dotfiles: a MISSING referenced secret returns its
// refs in `missing` and a nil domain, so the caller SKIPS the whole domain rather
// than `defaults import` an unrendered {{ferry.secret ...}} placeholder into it.
//
// Returns (domain, missing, err): a non-empty `missing` means SKIP this domain with a
// reported notice; err is the symlink/escape REFUSAL of the repo blob (also a skip).
func buildTerminalDomain(repo, scopeName string, store *secret.Store) (*terminal.PreferenceDomain, []string, error) {
	prefID, ok := terminalPrefDomainID(scopeName)
	if !ok {
		return nil, nil, fmt.Errorf("terminal: unknown preference domain %q", scopeName)
	}
	// Read the committed export blob apply imports (local overlay wins). A
	// PRESENT-but-REFUSED overlay (symlink/escape/under ~/.ssh) returns the refusal
	// so the caller SKIPS the domain rather than importing shared behind it.
	blob, err := terminalExportBlob(repo, scopeName, prefID)
	if err != nil {
		return nil, nil, err
	}
	// Secret render-or-SKIP parity with dotfiles: a MISSING referenced secret returns
	// its refs so the caller skips the whole domain (never `defaults import` an
	// unrendered {{ferry.secret}} placeholder into the live domain).
	rendered, missing, skip, err := renderSecrets(store, blob)
	if err != nil {
		return nil, nil, err
	}
	if skip {
		return nil, missing, nil
	}
	switch scopeName {
	case "iterm2":
		// Import-blob model (v0.7.0 D4): iTerm2 no longer uses a custom-prefs folder.
		// Apply imports the rendered global plist via `defaults import`, REFUSING when
		// iTerm2 is running and flushing cfprefsd afterwards (see terminal.NewITerm2).
		return terminal.NewITerm2(rendered, terminal.ExecRunner{}, terminal.ExecProcessController{}), nil, nil
	default: // "terminal" / Apple Terminal
		return terminal.NewAppleTerminal(rendered, terminal.ExecRunner{}), nil, nil
	}
}

// terminalExportBlob reads the `defaults export <prefID>` blob apply should import
// for a terminal preference domain, applying local-wins: the per-machine LOCAL
// overlay (local/<domain>/<id>.plist) takes precedence over the shared committed
// copy (<domain>/<id>.plist) when present, so a local-routed capture is actually
// re-imported on this machine. Both iTerm2 (D4 import-blob model) and Apple Terminal
// use it. Returns (nil, nil) when neither exists (Apply then manages backup/restore
// only). Probes the conventional committed locations under each tree.
//
// Local-overlay probing is GUARD-FIRST: safeRepoPath(repo, cand) runs BEFORE any
// os.Lstat of the candidate, then presence is Lstat'd on the SAFE path. A raw
// os.Lstat of a local candidate does NOT follow the final leaf but DOES traverse a
// symlinked PARENT (e.g. `local/<domain> -> <outside-repo>`), touching outside the
// repo BEFORE the guard runs; the guard-first walk never traverses a symlinked
// parent. A LOCAL candidate that is PRESENT (the safe path Lstats) but REFUSED by
// safeRepoPath (symlinked parent/leaf, escape, or under ~/.ssh) returns a non-nil
// ERROR so buildTerminalDomain SKIPS the domain with a refusal notice, rather than
// masking the poisoned overlay by importing the shared blob. Only an ABSENT local
// candidate falls through to the shared committed export, whose own candidate is
// then validated before the symlink-following read.
func terminalExportBlob(repo, domain, prefID string) ([]byte, error) {
	localCands := []string{
		filepath.Join(repo, "local", domain, prefID+".plist"),
		filepath.Join(repo, "local", domain, prefID),
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
		filepath.Join(repo, domain, prefID+".plist"),
		filepath.Join(repo, domain, prefID),
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
