package evals

// W2 (capture-back for the agents domain) ACs, driven through the real binary.
// Live edits to deployed agent files flow back into the config repo through the
// SAME approve + route flow as dotfiles; a true divergence is refused with a
// diff; new agent-shaped files are offered for adoption.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const agentsCaptureManifest = `[manage]
agents = true
dotfiles = []
brew = false
iterm2 = false
fonts = false

[agents]
harnesses = ["claude"]
assets = ["skills"]
`

// newAgentsCaptureSandbox seeds an agents-enabled repo (one harness, one asset
// mapping), applies it so the targets are deployed and baselined, and returns
// the sandbox. The general.md source feeds ~/.claude/CLAUDE.md; the skill asset
// deploys to ~/.claude/skills/demo/SKILL.md.
func newAgentsCaptureSandbox(t *testing.T) *Sandbox {
	t.Helper()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, agentsCaptureManifest)
	s.WriteRepoFile(t, filepath.Join("agents", "general.md"), "GENERAL RULES\n")
	s.WriteRepoFile(t, filepath.Join("agents", "coding.md"), "CODING RULES\n")
	s.WriteRepoFile(t, filepath.Join("agents", "skills", "demo", "SKILL.md"), "SKILL BODY\n")
	gitCommitAll(t, s.Repo, "agents baseline")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("newAgentsCaptureSandbox: apply exited %d; stderr:\n%s", code, errOut)
	}
	// Sanity: the targets deployed.
	if _, err := os.Stat(s.HomePath(".claude", "CLAUDE.md")); err != nil {
		t.Fatalf("newAgentsCaptureSandbox: CLAUDE.md not deployed: %v", err)
	}
	if _, err := os.Stat(s.HomePath(".claude", "skills", "demo", "SKILL.md")); err != nil {
		t.Fatalf("newAgentsCaptureSandbox: SKILL.md not deployed: %v", err)
	}
	return s
}

// TestAgentsCaptureBackAsset covers the core W2 promise: a live edit to a
// deployed agent asset flows back into its config-repo source through capture's
// approve + shared route — and the deployed file itself is left untouched.
func TestAgentsCaptureBackAsset(t *testing.T) {
	t.Parallel()
	s := newAgentsCaptureSandbox(t)

	skillHome := s.HomePath(".claude", "skills", "demo", "SKILL.md")
	marker := "LIVE_SKILL_EDIT"
	if err := os.WriteFile(skillHome, []byte("SKILL BODY\n"+marker+"\n"), 0o644); err != nil {
		t.Fatalf("live skill edit: %v", err)
	}

	// Accept the hunk, route shared.
	out, errOut, code := s.FerryWithInput("y\ns\n", "capture")
	if code != 0 {
		t.Fatalf("capture exited %d; stderr:\n%s\nstdout:\n%s", code, errOut, out)
	}

	// The edit reached the config-repo asset source.
	repoSkill := s.RepoPath("agents", "skills", "demo", "SKILL.md")
	data, err := os.ReadFile(repoSkill)
	if err != nil {
		t.Fatalf("read repo skill: %v", err)
	}
	if !strings.Contains(string(data), marker) {
		t.Errorf("capture-back: repo asset source did not receive the live edit\n%s", data)
	}
	// It did NOT leak into the local overlay (shared route must be distinct).
	if dirTreeContains(t, s.RepoPath("local"), marker) {
		t.Errorf("capture-back: shared-routed change also landed under local/ (routes must be distinct)")
	}
}

// TestAgentsCaptureBackInstruction covers a live edit to a deployed harness
// instruction file (CLAUDE.md, sourced from general.md) flowing back to
// general.md via the shared route.
func TestAgentsCaptureBackInstruction(t *testing.T) {
	t.Parallel()
	s := newAgentsCaptureSandbox(t)

	claudeHome := s.HomePath(".claude", "CLAUDE.md")
	marker := "LIVE_INSTRUCTION_EDIT"
	if err := os.WriteFile(claudeHome, []byte("GENERAL RULES\n"+marker+"\n"), 0o644); err != nil {
		t.Fatalf("live CLAUDE.md edit: %v", err)
	}

	out, errOut, code := s.FerryWithInput("y\ns\n", "capture")
	if code != 0 {
		t.Fatalf("capture exited %d; stderr:\n%s\nstdout:\n%s", code, errOut, out)
	}

	general, err := os.ReadFile(s.RepoPath("agents", "general.md"))
	if err != nil {
		t.Fatalf("read general.md: %v", err)
	}
	if !strings.Contains(string(general), marker) {
		t.Errorf("capture-back: general.md did not receive the CLAUDE.md edit\n%s", general)
	}
}

// TestAgentsCaptureDivergenceRefused covers the safety-critical rule: when the
// deployed file AND its config-repo source have BOTH changed since ferry last
// deployed it, capture REFUSES — it never overwrites the repo source with the
// live content and never auto-merges. We prove the repo source is left as the
// user's own edit, and the refusal is reported.
func TestAgentsCaptureDivergenceRefused(t *testing.T) {
	t.Parallel()
	s := newAgentsCaptureSandbox(t)

	// (1) Edit the deployed file.
	skillHome := s.HomePath(".claude", "skills", "demo", "SKILL.md")
	liveMarker := "LIVE_ONLY_EDIT"
	if err := os.WriteFile(skillHome, []byte("SKILL BODY\n"+liveMarker+"\n"), 0o644); err != nil {
		t.Fatalf("live edit: %v", err)
	}
	// (2) Independently edit the repo source too — both now differ from baseline.
	repoSkill := s.RepoPath("agents", "skills", "demo", "SKILL.md")
	repoMarker := "REPO_ONLY_EDIT"
	if err := os.WriteFile(repoSkill, []byte("SKILL BODY\n"+repoMarker+"\n"), 0o644); err != nil {
		t.Fatalf("repo edit: %v", err)
	}

	out, errOut, code := s.FerryWithInput("y\ns\ny\ns\n", "capture")
	if code != 0 {
		t.Fatalf("capture exited %d; stderr:\n%s", code, errOut)
	}
	combined := out + errOut

	// The repo source must still be the USER's own edit — never overwritten by the
	// live content, never merged.
	data, err := os.ReadFile(repoSkill)
	if err != nil {
		t.Fatalf("read repo skill: %v", err)
	}
	if strings.Contains(string(data), liveMarker) {
		t.Errorf("divergence: capture wrote the live content over the diverged repo source (must refuse, not merge)\n%s", data)
	}
	if !strings.Contains(string(data), repoMarker) {
		t.Errorf("divergence: the repo source's own edit was lost\n%s", data)
	}
	// The refusal must be reported and show a diff.
	if !containsAnyFold(combined, "diverge", "both", "refus") {
		t.Errorf("divergence: capture did not report the refusal\n%s", combined)
	}
}

// TestAgentsCaptureAdoptNew covers adopt-new: a brand-new agent-shaped file
// under a tracked mapping's $HOME target dir (~/.claude/skills) that ferry never
// deployed is offered and, on accept, brought into the repo asset source.
func TestAgentsCaptureAdoptNew(t *testing.T) {
	t.Parallel()
	s := newAgentsCaptureSandbox(t)

	// A new skill file ferry never deployed.
	newHome := s.HomePath(".claude", "skills", "adopted", "NEW.md")
	if err := os.MkdirAll(filepath.Dir(newHome), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	marker := "ADOPT_ME"
	if err := os.WriteFile(newHome, []byte(marker+"\n"), 0o644); err != nil {
		t.Fatalf("write new skill: %v", err)
	}

	// Adopt yes, route shared.
	out, errOut, code := s.FerryWithInput("y\ns\n", "capture")
	if code != 0 {
		t.Fatalf("capture exited %d; stderr:\n%s\nstdout:\n%s", code, errOut, out)
	}

	repoNew := s.RepoPath("agents", "skills", "adopted", "NEW.md")
	data, err := os.ReadFile(repoNew)
	if err != nil {
		t.Fatalf("adopt-new: repo source not created: %v", err)
	}
	if !strings.Contains(string(data), marker) {
		t.Errorf("adopt-new: adopted file content wrong\n%s", data)
	}
}

// TestAgentsCaptureLocalRouteWins proves the LOCAL route is genuinely wired: a
// captured local overlay (local/agents/...) is deployed by a later apply in
// place of the shared source (local wins) — so the local route is not dead
// scaffolding.
func TestAgentsCaptureLocalRouteWins(t *testing.T) {
	t.Parallel()
	s := newAgentsCaptureSandbox(t)

	skillHome := s.HomePath(".claude", "skills", "demo", "SKILL.md")
	localMarker := "LOCAL_OVERLAY_BODY"
	if err := os.WriteFile(skillHome, []byte(localMarker+"\n"), 0o644); err != nil {
		t.Fatalf("live edit: %v", err)
	}

	// Accept the hunk, route LOCAL.
	if _, errOut, code := s.FerryWithInput("y\nl\n", "capture"); code != 0 {
		t.Fatalf("capture exited %d; stderr:\n%s", code, errOut)
	}

	// The overlay landed under local/agents/ (gitignored per-machine layer).
	localOverlay := s.RepoPath("local", "agents", "skills", "demo", "SKILL.md")
	if _, err := os.Stat(localOverlay); err != nil {
		t.Fatalf("local route: overlay not written at local/agents/...: %v", err)
	}

	// Differential proof of local-wins: change the SHARED source and remove the
	// live file, then re-apply — apply must redeploy the LOCAL overlay, not shared.
	if err := os.WriteFile(s.RepoPath("agents", "skills", "demo", "SKILL.md"), []byte("SHARED_CHANGED\n"), 0o644); err != nil {
		t.Fatalf("shared edit: %v", err)
	}
	if err := os.Remove(skillHome); err != nil {
		t.Fatalf("remove live: %v", err)
	}
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("re-apply exited %d; stderr:\n%s", code, errOut)
	}
	got, err := os.ReadFile(skillHome)
	if err != nil {
		t.Fatalf("read redeployed skill: %v", err)
	}
	if !strings.Contains(string(got), localMarker) {
		t.Errorf("local-wins: re-apply deployed %q, want the local overlay %q", got, localMarker)
	}
	if strings.Contains(string(got), "SHARED_CHANGED") {
		t.Errorf("local-wins: re-apply deployed the SHARED source, not the local overlay\n%s", got)
	}
}
