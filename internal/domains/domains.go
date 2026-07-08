// Package domains is the converged domain registry for ferry (fn-5).
//
// Historically ferry's extension mechanisms were parallel, hand-written
// dispatch arms in cmd/: the isZsh() oracle routed dotfiles onto the
// include-sidecar vs whole-file strategy, a hardcoded {"iterm2","terminal"}
// literal enumerated the native preference domains, and dotfiles/agents/
// terminals each got their own planKind and mutate() arm. fn-5 converges those
// arms onto ONE registry exposing two interfaces:
//
//   - FileDomain — a domain whose managed state is a set of regular files under
//     $HOME, each reconciled by content -> target -> dotfile.ApplyContentDeferred
//     (dotfiles, zsh, agents, termcfg, and the post-fn-5 config plugins: git,
//     Emacs, iTerm2 Dynamic Profiles, tmux, npm, key-bindings).
//   - ResourceDomain — a domain whose managed state is an opaque backup-engine
//     resource (a macOS `defaults` preference domain), reconciled by
//     defaults export/import (native terminals, and later the iTerm2 global
//     plist).
//
// This file is the FREEZE GATE: it defines the interfaces and their carrier
// types ONLY. It deliberately does NOT construct a DefaultRegistry — the
// concrete domain adapters (the zsh FileDomain, the dotfile/agents/termcfg
// adapters, the PreferenceDomain wrapper) land in the porting step. The freeze
// exists so the interface shape can be proven to express a non-zsh FileDomain
// (the git `[include]` shape) WITHOUT leaking identity keys before any dispatch
// code in cmd/ is touched. See gitfixture_test.go for that proof.
package domains

import (
	"github.com/REPPL/ferry/internal/backup"
	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/secret"
	"github.com/REPPL/ferry/internal/terminal"
)

// FileDomain is a domain whose managed state is a set of regular files under
// $HOME, each reconciled by content -> target -> dotfile.ApplyContentDeferred.
// It is the converged replacement for today's dotfile/agents/termcfg planning
// arms, and the extension seam every post-fn-5 config plugin registers on.
type FileDomain interface {
	// Name is the [manage] scope key (e.g. "dotfiles", "agents", "terminals",
	// "git", "emacs", "tmux", "npm", "keybindings"). The registry driver gates
	// each domain on Scope.IsManaged(Name()) before it calls Plan.
	Name() string

	// Plan expands the domain into strictly 1:1 file items. Each item's Target
	// is built via dotfile.TargetFor (flat) or dotfile.NestedTarget (nested);
	// any {{ferry.secret ...}} placeholders are pre-rendered into Content. A
	// per-target refusal (an ~/.ssh / $HOME-escape / symlink / missing-secret)
	// becomes a warning and skips that item — it never aborts the whole plan.
	Plan(in PlanInput) (items []FileItem, warnings []string, err error)

	// Overlay reports, per bare/leaf key, whether this domain composes its
	// per-machine `.local` overlay as an include-style sidecar or a whole-file
	// replace. This is the DATA-DRIVEN replacement for the isZsh() oracle: the
	// zsh domain returns OverlayIncludeSidecar for zshrc/zshenv/zprofile; most
	// domains return the constant OverlayWholeFileReplace. The two-strip
	// contract keys its trigger off this value, not off a hardcoded name.
	Overlay(key string) dotfile.OverlayMode

	// Captures reports whether capture offers this domain's targets back as
	// candidates. termcfg returns false (repo-authoritative — no capture pass);
	// dotfiles and agents return true. This lets the registry drive the capture
	// passes without a per-domain hand-coded gate, preserving termcfg's
	// deliberate capture asymmetry.
	Captures() bool
}

// FileItem is the unit every FileDomain plan produces — the converged shape of
// today's dotfile planItem, agents.Item, and termcfg.Item. The registry driver
// carries one FileItem straight into dotfile.ApplyContentDeferred.
type FileItem struct {
	// Key is the domain-namespaced last-applied store key (e.g. "agents/claude",
	// "terminals/alacritty/foo.toml", or a dotfile's bare name).
	Key string
	// Label is the human-facing name reports print (e.g. "agents:claude").
	Label string
	// Target is the validated $HOME destination (built via dotfile.TargetFor or
	// dotfile.NestedTarget, so ~/.ssh and $HOME-escapes are impossible).
	Target dotfile.Target
	// Content is the effective bytes to materialise, with any
	// {{ferry.secret ...}} placeholders already rendered.
	Content []byte
	// Exec preserves the repo source's executable bit on a first-ever write.
	Exec bool
	// SecretRouted marks a plaintext-credential target: ApplyContentDeferred
	// materialises it 0600 and the apply command records only its hash — never
	// the bytes — in the last-applied snapshot.
	SecretRouted bool
}

// PlanInput carries what a FileDomain needs to plan. It is intentionally the
// minimal-but-sufficient union of what planDotfiles, agents.Plan, and
// termcfg.Plan consume today:
//
//   - RepoRoot + Home build every Target (dotfile.TargetFor / NestedTarget).
//   - Scope supplies the dotfiles domain its declared list
//     (Scope.DeclaredDotfiles); the registry driver has already gated the
//     domain on Scope.IsManaged(Name()) before calling Plan.
//   - Secrets renders {{ferry.secret ...}} placeholders into FileItem.Content
//     (a missing ref skips the item, never a literal placeholder deployed).
//
// The config-repo symlink-refusing read validator (safeRepoPath) is NOT a
// PlanInput field: each per-domain planner applies its own guard internally, so
// exposing one here would be a dead field no Plan implementation reads.
//
// A domain's own configuration (which agents/terminals are declared) is held by
// the domain adapter itself — assembled by the porting step's DefaultRegistry —
// so it is not a PlanInput field.
type PlanInput struct {
	RepoRoot string
	Home     string
	Scope    config.Scope
	Secrets  *secret.Store
}

// ResourceDomain is a domain whose managed state is an opaque backup-engine
// resource — a macOS `defaults` preference domain, reconciled by
// defaults export/import rather than a file copy. It is the converged
// replacement for the hardcoded {"iterm2","terminal"} preference-domain arm.
type ResourceDomain interface {
	// backup.Resource is Domain() / Backup() / Restore() — the engine drives
	// pre-mutation capture and rollback through it.
	backup.Resource
	// Name is the [manage] scope key (e.g. "iterm2", "terminal") — distinct from
	// backup.Resource.Domain(), which is the `defaults` identifier
	// (e.g. "com.googlecode.iterm2").
	Name() string
	// Plan emits the preference-domain PlanEntry the diff/status renderer and the
	// AC-terminal-config tripwire key on (Kind == "preference-domain").
	Plan() terminal.PlanEntry
}

// Registry is the single converged domain registry fn-5 drives dispatch from:
// an ordered set of FileDomains (planned + reconciled through
// dotfile.ApplyContentDeferred) and ResourceDomains (reconciled through the
// backup engine's Resource hook). The porting step adds a DefaultRegistry
// constructor that assembles the built-in set; this freeze defines the carrier
// only.
type Registry struct {
	FileDomains     []FileDomain
	ResourceDomains []ResourceDomain
}
