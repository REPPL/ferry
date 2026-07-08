package cmd

import (
	"github.com/REPPL/ferry/internal/domains"
	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/secret"
	"github.com/REPPL/ferry/internal/terminal"
)

// buildRegistry assembles ferry's converged domain registry (fn-5): the ordered
// FileDomains (dotfiles, agents, terminals, keybindings, emacs, iterm2-profiles)
// reconciled through dotfile.ApplyContentDeferred, and the ordered ResourceDomains
// (iterm2, apple terminal) reconciled through the backup engine's Resource hook.
// The ORDER is load-bearing: it is exactly the sequence the pre-fn-5 dispatch ran
// the domains in, so plan/status/diff/capture OUTPUT ORDERING is unchanged. New
// FileDomains append AFTER the existing set, so existing ordering is preserved.
// It replaces the hardcoded IsManaged("dotfiles"/"agents"/"terminals") sequence
// and the {"iterm2","terminal"} literal that drove routing before convergence.
//
// The ResourceDomain constructions here are used only for enumeration (Name()) and
// restore registration (the engine replays the captured blob via Restore), so the
// import blob is nil and the runner/process seams are the production ones; apply
// builds a domain with the RENDERED export blob per-run via buildTerminalDomain.
func buildRegistry(ctx *cmdContext) domains.Registry {
	return domains.Registry{
		FileDomains: []domains.FileDomain{
			dotfilesFileDomain{},
			agentsFileDomain{},
			termcfgFileDomain{},
			keybindingsFileDomain{},
			emacsFileDomain{},
			iterm2ProfilesFileDomain{},
		},
		ResourceDomains: []domains.ResourceDomain{
			terminal.NewITerm2(nil, terminal.ExecRunner{}, terminal.ExecProcessController{}),
			terminal.NewAppleTerminal(nil, terminal.ExecRunner{}),
		},
	}
}

// filePlanner is the cmd-side extension of domains.FileDomain: it adds the
// full-fidelity planItems / descopeUnmanaged surface the read/apply plan needs.
// The frozen domains.FileItem deliberately cannot carry the three-way classify
// state, the missing-secret skip, or the guided-apply risk verdict, so the plan
// driver upcasts each registered FileDomain to this interface and produces the
// rich planItems here — the same per-domain planners (planDotfiles / planAgents
// / planTerminals) the direct unit tests still drive, so behaviour is preserved
// byte-for-byte.
type filePlanner interface {
	domains.FileDomain
	// planItems expands the managed domain into the read/apply plan items, with
	// secrets rendered, three-way state classified, and the risk verdict computed
	// — exactly what the pre-fn-5 per-domain planners returned.
	planItems(ctx *cmdContext, home string, secretStore *secret.Store, lastApplied *dotfile.Store) ([]planItem, []string, error)
	// descopeUnmanaged returns the de-scope warnings emitted when this domain is
	// OUT of scope (previously-applied targets left as-is). Dotfiles return nil
	// here: their de-scope is emitted at the END of the plan (after the preference
	// domains) by descopeDotfileWarnings, preserving warning order.
	descopeUnmanaged(ctx *cmdContext, lastApplied *dotfile.Store) []string
}

// fileItemsFromPlanItems is the shared implementation of the frozen
// domains.FileDomain.Plan surface: it runs a domain's rich planItems and
// projects the result down to the frozen FileItem shape (content -> target,
// secrets already rendered), dropping the missing-secret skips per the interface
// contract. Reusing the SAME per-domain planner the driver uses means the frozen
// Plan surface never duplicates a domain's composition logic — the sole reason
// the freeze can prove the interface expresses each domain without a re-cut. It
// opens a throwaway read-only last-applied store; the classification it drives is
// discarded here (only Content/Target/Exec/SecretRouted survive into a FileItem).
func fileItemsFromPlanItems(fp filePlanner, in domains.PlanInput) ([]domains.FileItem, []string, error) {
	lastApplied, err := dotfile.OpenStoreReadOnly()
	if err != nil {
		return nil, nil, err
	}
	ctx := &cmdContext{RepoPath: in.RepoRoot, Scope: in.Scope}
	pits, warnings, err := fp.planItems(ctx, in.Home, in.Secrets, lastApplied)
	if err != nil {
		return nil, nil, err
	}
	var items []domains.FileItem
	for _, it := range pits {
		if it.skip {
			continue // frozen contract: a missing secret drops the item, never a literal placeholder
		}
		items = append(items, domains.FileItem{
			Key:          it.target.Name,
			Label:        it.domain,
			Target:       it.target,
			Content:      it.content,
			Exec:         it.execBit,
			SecretRouted: it.secretRouted,
		})
	}
	return items, warnings, nil
}

// usesIncludeSidecar reports whether the dotfiles domain composes a bare dotfile
// name's per-machine overlay as an include-style SIDECAR (zsh: shared ~/.zshrc
// `source`s ~/.zshrc.local last) rather than a whole-file replace. It is the
// DATA-DRIVEN replacement for the old isZsh() oracle: the decision now comes
// from the dotfiles FileDomain's Overlay(key), the single source of truth the
// two-strip contract keys its trigger off.
func usesIncludeSidecar(bare string) bool {
	return dotfilesFileDomain{}.Overlay(bare) == dotfile.OverlayIncludeSidecar
}

// fileDomainCaptures reports whether the named FileDomain offers its targets back
// to capture (dotfiles/agents true, terminals false). Capture drives its
// per-domain passes off this so termcfg's deliberate no-capture asymmetry is a
// registry fact, not a hand-coded omission.
func fileDomainCaptures(reg domains.Registry, name string) bool {
	for _, fd := range reg.FileDomains {
		if fd.Name() == name {
			return fd.Captures()
		}
	}
	return false
}

// --- dotfiles FileDomain -----------------------------------------------------

// dotfilesFileDomain is the converged dotfiles domain: generic whole-file
// dotfiles PLUS the zsh include-sidecar split. It OWNS the sidecar-vs-whole-file
// overlay decision (Overlay), the single source of truth that replaced isZsh().
type dotfilesFileDomain struct{}

func (dotfilesFileDomain) Name() string { return "dotfiles" }

// Overlay reports the per-machine overlay strategy for a bare dotfile name. The
// include-style names — the zsh family (zshrc/zshenv/zprofile) and tmux
// (tmux.conf) — compose their overlay as a sourced sidecar (their format has a
// real include point: shell `source`, tmux `source-file`); every other dotfile
// is a whole-file replace. This is the authoritative replacement for the isZsh()
// oracle, and the single trigger the two-strip contract keys off (via
// usesIncludeSidecar).
func (dotfilesFileDomain) Overlay(key string) dotfile.OverlayMode {
	switch key {
	case "zshrc", "zshenv", "zprofile", "tmux.conf", "gitconfig":
		return dotfile.OverlayIncludeSidecar
	}
	return dotfile.OverlayWholeFileReplace
}

func (dotfilesFileDomain) Captures() bool { return true }

func (d dotfilesFileDomain) Plan(in domains.PlanInput) ([]domains.FileItem, []string, error) {
	return fileItemsFromPlanItems(d, in)
}

func (dotfilesFileDomain) planItems(ctx *cmdContext, home string, secretStore *secret.Store, lastApplied *dotfile.Store) ([]planItem, []string, error) {
	return planDotfiles(ctx, home, secretStore, lastApplied)
}

func (dotfilesFileDomain) descopeUnmanaged(*cmdContext, *dotfile.Store) []string {
	// Dotfile de-scope is emitted at the END of the plan (after the preference
	// domains) by descopeDotfileWarnings, so nothing is emitted inline here.
	return nil
}

// --- agents FileDomain -------------------------------------------------------

// agentsFileDomain is the converged agents domain (harness instructions + asset
// trees), reconciled like dotfiles and offered back to capture.
type agentsFileDomain struct{}

func (agentsFileDomain) Name() string                       { return "agents" }
func (agentsFileDomain) Overlay(string) dotfile.OverlayMode { return dotfile.OverlayWholeFileReplace }
func (agentsFileDomain) Captures() bool                     { return true }

func (d agentsFileDomain) Plan(in domains.PlanInput) ([]domains.FileItem, []string, error) {
	return fileItemsFromPlanItems(d, in)
}

func (agentsFileDomain) planItems(ctx *cmdContext, home string, _ *secret.Store, lastApplied *dotfile.Store) ([]planItem, []string, error) {
	return planAgents(ctx, home, lastApplied)
}

func (agentsFileDomain) descopeUnmanaged(_ *cmdContext, lastApplied *dotfile.Store) []string {
	return descopeAgentsWarnings(lastApplied, nil, false)
}

// --- config-file terminals FileDomain ----------------------------------------

// termcfgFileDomain is the converged config-file terminal domain (alacritty,
// kitty, wezterm, …), carried like a dotfile but repo-authoritative on capture:
// Captures() is FALSE, preserving the deliberate no-capture asymmetry.
type termcfgFileDomain struct{}

func (termcfgFileDomain) Name() string                       { return "terminals" }
func (termcfgFileDomain) Overlay(string) dotfile.OverlayMode { return dotfile.OverlayWholeFileReplace }
func (termcfgFileDomain) Captures() bool                     { return false }

func (d termcfgFileDomain) Plan(in domains.PlanInput) ([]domains.FileItem, []string, error) {
	return fileItemsFromPlanItems(d, in)
}

func (termcfgFileDomain) planItems(ctx *cmdContext, home string, secretStore *secret.Store, lastApplied *dotfile.Store) ([]planItem, []string, error) {
	return planTerminals(ctx, home, secretStore, lastApplied)
}

func (termcfgFileDomain) descopeUnmanaged(_ *cmdContext, lastApplied *dotfile.Store) []string {
	return descopeTerminalConfigWarnings(lastApplied, nil, false)
}

// --- macOS key-bindings FileDomain -------------------------------------------

// keybindingsFileDomain is the converged macOS Cocoa key-bindings domain: the
// single ~/Library/KeyBindings/DefaultKeyBinding.dict carried like a dotfile but
// repo-authoritative on capture (Captures() is FALSE, like termcfg — you edit
// the dict in the repo and apply deploys it; a live edit shows as drift and apply
// skips it). It has no per-machine .local overlay (keyboard behaviour is
// machine-agnostic, and the old-style dict format has no include/merge layer).
type keybindingsFileDomain struct{}

func (keybindingsFileDomain) Name() string { return "keybindings" }
func (keybindingsFileDomain) Overlay(string) dotfile.OverlayMode {
	return dotfile.OverlayWholeFileReplace
}
func (keybindingsFileDomain) Captures() bool { return false }

func (d keybindingsFileDomain) Plan(in domains.PlanInput) ([]domains.FileItem, []string, error) {
	return fileItemsFromPlanItems(d, in)
}

func (keybindingsFileDomain) planItems(ctx *cmdContext, home string, secretStore *secret.Store, lastApplied *dotfile.Store) ([]planItem, []string, error) {
	return planKeybindings(ctx, home, secretStore, lastApplied)
}

func (keybindingsFileDomain) descopeUnmanaged(_ *cmdContext, lastApplied *dotfile.Store) []string {
	return descopeKeybindingsWarnings(lastApplied, nil, false)
}

// --- Emacs FileDomain --------------------------------------------------------

// emacsFileDomain is the converged Emacs configuration domain: the config repo's
// emacs/ tree fanned out file-by-file under ~/.emacs.d/, carried like a dotfile
// with the per-machine local/emacs/ overlay winning per file, but
// repo-authoritative on capture (Captures() is FALSE, like termcfg — edit the
// repo source and apply deploys it; a live edit shows as drift and apply skips
// it). Volatile, machine-generated paths (elpa/, *.elc, session state, …) are
// pruned by the domain and never deployed.
type emacsFileDomain struct{}

func (emacsFileDomain) Name() string                       { return "emacs" }
func (emacsFileDomain) Overlay(string) dotfile.OverlayMode { return dotfile.OverlayWholeFileReplace }
func (emacsFileDomain) Captures() bool                     { return false }

func (d emacsFileDomain) Plan(in domains.PlanInput) ([]domains.FileItem, []string, error) {
	return fileItemsFromPlanItems(d, in)
}

func (emacsFileDomain) planItems(ctx *cmdContext, home string, secretStore *secret.Store, lastApplied *dotfile.Store) ([]planItem, []string, error) {
	return planEmacs(ctx, home, secretStore, lastApplied)
}

func (emacsFileDomain) descopeUnmanaged(_ *cmdContext, lastApplied *dotfile.Store) []string {
	return descopeEmacsConfigWarnings(lastApplied, nil, false)
}

// --- iTerm2 Dynamic Profiles FileDomain --------------------------------------

// iterm2ProfilesFileDomain is the converged iTerm2 Dynamic Profiles domain: the
// config repo's iterm2/DynamicProfiles/ tree of *.json files fanned out
// file-by-file under ~/Library/Application Support/iTerm2/DynamicProfiles/, carried
// like a dotfile with the per-machine local/iterm2-profiles/ overlay winning per
// file, but repo-authoritative on capture (Captures() is FALSE, like termcfg — edit
// the repo JSON and apply deploys it; a live edit shows as drift and apply skips
// it). Each file is validated (JSON validity everywhere, plutil -convert on macOS —
// not -lint, which rejects valid JSON) before it lands; a profile's frozen Guid is
// byte-preserved (ferry never rewrites
// the JSON). It is distinct from the iTerm2 global-plist ResourceDomain ("iterm2").
type iterm2ProfilesFileDomain struct{}

func (iterm2ProfilesFileDomain) Name() string { return "iterm2-profiles" }
func (iterm2ProfilesFileDomain) Overlay(string) dotfile.OverlayMode {
	return dotfile.OverlayWholeFileReplace
}
func (iterm2ProfilesFileDomain) Captures() bool { return false }

func (d iterm2ProfilesFileDomain) Plan(in domains.PlanInput) ([]domains.FileItem, []string, error) {
	return fileItemsFromPlanItems(d, in)
}

func (iterm2ProfilesFileDomain) planItems(ctx *cmdContext, home string, secretStore *secret.Store, lastApplied *dotfile.Store) ([]planItem, []string, error) {
	return planIterm2Profiles(ctx, home, secretStore, lastApplied)
}

func (iterm2ProfilesFileDomain) descopeUnmanaged(_ *cmdContext, lastApplied *dotfile.Store) []string {
	return descopeIterm2ProfilesWarnings(lastApplied, nil, false)
}
