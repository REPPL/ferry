package domains

// FREEZE PROOF (fn-5, plan §3.1 / §5 STOP condition).
//
// gitIncludeDomain is a TOY FileDomain modelling the git `[include]` shape: a
// shared ~/.gitconfig whose LAST directive is `[include] path = ~/.gitconfig.local`
// (git's native, last-wins overlay — the equivalent of ferry's `.local` sidecar),
// with the machine identity keys forced LOCAL-only so a WholeFile deploy can never
// carry one machine's commit identity onto another.
//
// It exists only to prove that the FROZEN FileDomain interface can express a
// non-zsh domain WITHOUT leaking identity — the plan's freeze gate before any
// port. It is never registered and never wired to cmd/; if the interface could
// NOT express this (e.g. Plan had no way to emit the native include directive, or
// FileItem could not carry the filtered shared content), that is the plan STOP
// condition "the fn-5 interface cannot express the git FileDomain without leaking
// -> re-cut before any port."

import (
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/dotfile"
)

// gitIdentityKeys are the git config keys that must NEVER be shared across
// machines (plan §3.1): sharing one machine's identity would silently corrupt
// another's commit authorship / signing. They live only in ~/.gitconfig.local.
var gitIdentityKeys = []string{
	"user.email",
	"user.name",
	"user.signingkey",
	"gpg.program",
	"credential.helper",
}

// gitIncludeDomain is the toy non-zsh FileDomain.
type gitIncludeDomain struct{}

func (gitIncludeDomain) Name() string { return "git" }

// Overlay: git carries its own native `[include]` mechanism, so the per-machine
// overlay is a WHOLE-FILE replace of a separate ~/.gitconfig.local that git
// itself pulls in — NOT ferry's injected shell-style sidecar. Every key resolves
// to OverlayWholeFileReplace.
func (gitIncludeDomain) Overlay(key string) dotfile.OverlayMode {
	return dotfile.OverlayWholeFileReplace
}

// Captures: git is a capture candidate (an edited ~/.gitconfig can be pulled
// back), like dotfiles and agents.
func (gitIncludeDomain) Captures() bool { return true }

// Plan emits ONE FileItem for the shared ~/.gitconfig. Its content is the shared
// config with every identity key dropped (forced local-only) and a trailing
// native `[include]` directive so git last-wins-merges ~/.gitconfig.local.
func (gitIncludeDomain) Plan(in PlanInput) ([]FileItem, []string, error) {
	// A representative shared config that ALSO contains identity keys, to prove
	// the domain drops them rather than never having had them.
	sharedSource := strings.Join([]string{
		"[core]",
		"\teditor = vim",
		"[init]",
		"\tdefaultBranch = main",
		"[alias]",
		"\tst = status",
		// Identity keys that MUST be forced local-only — present in the source,
		// dropped from the shared output.
		"[user]",
		"\temail = alice@example.com",
		"\tname = Alice Example",
		"\tsigningkey = ABCD1234",
		"[gpg]",
		"\tprogram = gpg2",
		"[credential]",
		"\thelper = osxkeychain",
		"",
	}, "\n")

	shared := dropIdentityKeys(sharedSource)
	shared = appendGitInclude(shared, "~/.gitconfig.local")

	target, err := dotfile.TargetFor(in.RepoRoot, in.Home, ".gitconfig")
	if err != nil {
		// A per-target refusal is a warning + skip, never a plan abort.
		return nil, []string{"git: " + err.Error()}, nil
	}

	return []FileItem{{
		Key:     "gitconfig",
		Label:   "git:gitconfig",
		Target:  target,
		Content: []byte(shared),
	}}, nil, nil
}

// dropIdentityKeys removes any line assigning one of the identity keys, keyed by
// the current INI section header. It is deliberately simple — a proof, not the
// production extractor.
func dropIdentityKeys(config string) string {
	section := ""
	var kept []string
	for _, line := range strings.Split(config, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.Trim(trimmed, "[]"))
			// Drop a bare section header (e.g. [gpg]) whose only keys are identity.
			if sectionIsIdentityOnly(section) {
				continue
			}
			kept = append(kept, line)
			continue
		}
		if eq := strings.IndexByte(trimmed, '='); eq >= 0 && section != "" {
			key := section + "." + strings.TrimSpace(trimmed[:eq])
			if isIdentityKey(key) {
				continue
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// sectionIsIdentityOnly reports whether every identity key we drop under this
// section would empty it — so the section header itself is dropped too, leaving
// no orphan `[user]`/`[gpg]`/`[credential]` block in the shared output.
func sectionIsIdentityOnly(section string) bool {
	switch section {
	case "user", "gpg", "credential":
		return true
	}
	return false
}

func isIdentityKey(key string) bool {
	for _, id := range gitIdentityKeys {
		if key == id {
			return true
		}
	}
	return false
}

// appendGitInclude appends the native git `[include]` directive as the LAST
// block, so git applies it last (last-wins), giving the machine-local file the
// final say — the native equivalent of ferry's overlay.
func appendGitInclude(config, path string) string {
	if !strings.HasSuffix(config, "\n") {
		config += "\n"
	}
	return config + "[include]\n\tpath = " + path + "\n"
}

// TestGitIncludeDomain_FreezeProof is the freeze gate: it proves the frozen
// FileDomain interface expresses the git `[include]` shape without leaking any
// identity key into the shared content.
func TestGitIncludeDomain_FreezeProof(t *testing.T) {
	// The toy domain must satisfy the frozen interface at compile time.
	var d FileDomain = gitIncludeDomain{}

	if d.Name() != "git" {
		t.Fatalf("Name() = %q, want %q", d.Name(), "git")
	}

	// Overlay: git uses its own native include, NOT ferry's injected sidecar.
	if got := d.Overlay("gitconfig"); got != dotfile.OverlayWholeFileReplace {
		t.Errorf("Overlay(gitconfig) = %q, want OverlayWholeFileReplace (git uses a native [include], not ferry's sidecar)", got)
	}

	// Captures: git is a capture candidate.
	if !d.Captures() {
		t.Errorf("Captures() = false, want true (an edited ~/.gitconfig can be captured back)")
	}

	items, warnings, err := d.Plan(PlanInput{
		RepoRoot: t.TempDir(),
		Home:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Plan returned err: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("Plan returned warnings: %v", warnings)
	}
	if len(items) != 1 {
		t.Fatalf("Plan returned %d items, want exactly 1 (the shared ~/.gitconfig)", len(items))
	}

	item := items[0]
	content := string(item.Content)

	// (1) The shared FileItem carries the native `[include]` directive naming the
	// per-machine file — the git overlay mechanism.
	if !strings.Contains(content, "[include]") {
		t.Errorf("shared ~/.gitconfig content has no [include] section — git overlay directive missing:\n%s", content)
	}
	if !strings.Contains(content, "path = ~/.gitconfig.local") {
		t.Errorf("shared ~/.gitconfig content does not include ~/.gitconfig.local — the per-machine overlay is not wired:\n%s", content)
	}

	// (2) The FREEZE PROOF: NONE of the identity keys leak into the shared content.
	for _, key := range gitIdentityKeys {
		bare := key[strings.IndexByte(key, '.')+1:] // e.g. "email"
		if strings.Contains(content, bare+" =") || strings.Contains(content, bare+"=") {
			t.Errorf("IDENTITY LEAK: shared ~/.gitconfig content contains identity key %q — it must be forced LOCAL-only (plan §3.1):\n%s", key, content)
		}
	}
	// The dropped section headers must not survive as orphan blocks either.
	for _, section := range []string{"[user]", "[gpg]", "[credential]"} {
		if strings.Contains(content, section) {
			t.Errorf("IDENTITY LEAK: shared ~/.gitconfig content contains an orphan %s block — identity section must be dropped whole:\n%s", section, content)
		}
	}

	// The `[include]` directive must be LAST so git applies the machine-local
	// file last (last-wins overlay).
	trimmed := strings.TrimRight(content, "\n")
	lastBlock := trimmed[strings.LastIndex(trimmed, "["):]
	if !strings.HasPrefix(lastBlock, "[include]") {
		t.Errorf("[include] is not the last block — git would not last-wins the overlay:\n%s", content)
	}
}
