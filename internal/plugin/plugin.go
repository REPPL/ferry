// Package plugin defines ferry's built-in config-plugin interface (v2, per
// .work/PLAN-v0.3.0-plugin-wizard.md). A plugin owns the domain knowledge for
// ONE primary config file (v1 constraint): how to detect it, parse it into
// loss-less blocks, analyze it for secret / machine-specific / repairable
// findings, apply accepted findings, build an opinionated starter, and deploy
// through the EXISTING dotfile machinery. Plugins are compiled in — there is no
// dynamic loading and no third-party code execution.
package plugin

import (
	"fmt"
	"sort"
)

// Plugin is the per-domain config plugin interface.
type Plugin interface {
	// Domain names the plugin's domain, e.g. "zsh". It is the first segment of
	// every secret-store ref the plugin produces.
	Domain() string

	// Detect locates the domain's primary config under home. Lstat discipline:
	// it never resolves symlinks. Present is true only for Reason OK.
	Detect(home string) (Detection, error)

	// Parse segments content into blocks, loss-lessly: Reassemble(Parse(x))
	// MUST equal x byte-for-byte (hard invariant, fuzz-tested). Unknown
	// content classifies as Other.
	Parse(content []byte) ([]Block, error)

	// Analyze returns secret / machine-specific / repairable findings over
	// blocks. Secret findings MUST populate Finding.Secret; key uniqueness
	// across findings is the plugin's contract (deterministic suffixing).
	Analyze(blocks []Block) []Finding

	// ApplyRepairs is the SINGLE WRITER for block text: it applies ALL accepted
	// findings — SecretLine substitutions AND repairs — in one deterministic
	// pass (ascending block, then line order). INVARIANT: it never adds,
	// removes, or reorders blocks; a full-block removal sets Raw to empty.
	ApplyRepairs(blocks []Block, accepted []Finding) ([]Block, error)

	// StarterQuestions returns the generic questions the from-scratch starter
	// path asks; Starter builds the starter bytes from the answers.
	StarterQuestions() []Question
	Starter(a Answers) ([]byte, error)

	// Describe renders a one-line human explanation of a block for the adopt UI.
	Describe(b Block) string

	// Deploy reports which EXISTING dotfile deploy mode the domain uses.
	Deploy() DeploySpec
}

// Reassemble concatenates the blocks' Raw bytes. Because blocks partition their
// input (each Raw owns its separators), Reassemble(Parse(x)) == x.
func Reassemble(blocks []Block) []byte {
	var n int
	for _, b := range blocks {
		n += len(b.Raw)
	}
	out := make([]byte, 0, n)
	for _, b := range blocks {
		out = append(out, b.Raw...)
	}
	return out
}

// Detection is the result of Plugin.Detect.
type Detection struct {
	Path    string
	Present bool // true only for Reason OK
	Reason  DetectReason
}

// DetectReason distinguishes why a config is (not) adoptable. Absent/NearEmpty
// allow the from-scratch starter path; Symlink/Irregular/Unreadable allow ONLY
// continue-without-managing (no adopt, no starter).
type DetectReason int

const (
	OK DetectReason = iota
	Absent
	NearEmpty
	Symlink
	Irregular
	// Unreadable is a REGULAR file whose read fails (mode/ACL). Treated like
	// Symlink/Irregular: the apply tail would fail on it and no honest
	// diff/backup of the original is possible.
	Unreadable
)

func (r DetectReason) String() string {
	switch r {
	case OK:
		return "ok"
	case Absent:
		return "absent"
	case NearEmpty:
		return "near-empty"
	case Symlink:
		return "symlink"
	case Irregular:
		return "irregular"
	case Unreadable:
		return "unreadable"
	}
	return "unknown"
}

// BlockKind classifies a parsed block.
type BlockKind int

const (
	Other BlockKind = iota
	PathExport
	Alias
	Function
	PluginInit
	Prompt
	Source
	Comment
)

func (k BlockKind) String() string {
	switch k {
	case PathExport:
		return "path/export"
	case Alias:
		return "alias"
	case Function:
		return "function"
	case PluginInit:
		return "plugin-init"
	case Prompt:
		return "prompt"
	case Source:
		return "source"
	case Comment:
		return "comment"
	}
	return "other"
}

// Block is one parsed segment. Raw carries the EXACT bytes including trailing
// separators; blocks partition the parsed input.
type Block struct {
	Kind  BlockKind
	Raw   []byte
	Start int // 1-based first line, display only (derived, not authoritative)
}

// FindingKind classifies a Finding.
type FindingKind int

const (
	SecretLine FindingKind = iota
	MachineSpecific
	HardcodedHome
	DuplicatePath
	DeadSource
)

func (k FindingKind) String() string {
	switch k {
	case SecretLine:
		return "secret-line"
	case MachineSpecific:
		return "machine-specific"
	case HardcodedHome:
		return "hardcoded-home"
	case DuplicatePath:
		return "duplicate-path"
	case DeadSource:
		return "dead-source"
	}
	return "unknown"
}

// Finding is one analyzed condition on a block.
type Finding struct {
	Kind       FindingKind
	Block      int    // index into the analyzed []Block
	Detail     string // human explanation — NEVER contains the secret value
	Suggested  string // proposed replacement text (repairs) — empty for pure routing
	Routes     []Route
	Repairable bool
	Secret     *SecretExtraction // non-nil iff Kind == SecretLine
}

// SecretExtraction hands the generic wizard everything needed for the store
// route without the wizard owning any domain parsing. Value is NEVER displayed,
// never logged, never placed in Detail/Suggested; all references are dropped
// after Store.Put.
type SecretExtraction struct {
	Key         string // ref key part; store ref = Domain()+"."+Key (unique per plugin contract)
	Value       string // the raw secret value (span-grained)
	Replacement []byte // block Raw with the value swapped for the placeholder — PREVIEW MASKING ONLY
}

// Route is a wizard routing destination for a block or finding.
type Route int

const (
	Shared Route = iota
	Local
	SecretStore
	Drop
)

func (r Route) String() string {
	switch r {
	case Shared:
		return "shared"
	case Local:
		return "local"
	case SecretStore:
		return "store"
	case Drop:
		return "drop"
	}
	return "unknown"
}

// Answers maps Question.ID -> chosen value.
type Answers map[string]string

// QuestionKind is the input control a Question uses.
type QuestionKind int

const (
	Select QuestionKind = iota
	MultiSelect
	Confirm
	Text
)

// Question is one generic starter question.
type Question struct {
	ID, Prompt, Description string
	Kind                    QuestionKind
	Options                 []string
	Default                 string
}

// OverlayKind maps 1:1 onto the EXISTING dotfile deploy modes.
type OverlayKind int

const (
	Sidecar OverlayKind = iota
	WholeFile
)

// DeploySpec reports the existing dotfile deploy mode the domain uses.
type DeploySpec struct {
	DotfileName string // e.g. ".zshrc"
	Overlay     OverlayKind
}

// Registry maps domain -> Plugin. Duplicate registration panics (programmer
// error at init); an unknown domain errors.
type Registry struct {
	plugins map[string]Plugin
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{plugins: map[string]Plugin{}}
}

// Register adds a plugin; a duplicate domain panics.
func (r *Registry) Register(p Plugin) {
	d := p.Domain()
	if _, dup := r.plugins[d]; dup {
		panic(fmt.Sprintf("plugin: duplicate registration for domain %q", d))
	}
	r.plugins[d] = p
}

// Get resolves a domain to its plugin; an unknown domain errors.
func (r *Registry) Get(domain string) (Plugin, error) {
	p, ok := r.plugins[domain]
	if !ok {
		return nil, fmt.Errorf("plugin: unknown domain %q (registered: %v)", domain, r.Domains())
	}
	return p, nil
}

// Domains lists the registered domains, sorted.
func (r *Registry) Domains() []string {
	out := make([]string, 0, len(r.plugins))
	for d := range r.plugins {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
