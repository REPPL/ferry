package agents

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/REPPL/ferry/internal/config"
)

// captureByKey indexes capture targets by store key.
func captureByKey(ts []CaptureTarget) map[string]CaptureTarget {
	m := map[string]CaptureTarget{}
	for _, t := range ts {
		m[t.Key] = t
	}
	return m
}

// TestCaptureTargets_KindsAndRoutes proves CaptureTargets classifies each
// deployed target and resolves its shared/local routes exactly as the plan
// deploys: assets are 1:1 (shared repo file + local overlay path), general/
// coding are single-source instruction targets, and a combined target is marked
// non-decomposable (no shared source to write).
func TestCaptureTargets_KindsAndRoutes(t *testing.T) {
	repo := writeSST(t, map[string]string{
		"general.md":           "GENERAL\n",
		"coding.md":            "CODING\n",
		"skills/demo/SKILL.md": "skill\n",
		"hooks/guard.sh":       "#!/bin/sh\n",
	}, "hooks/guard.sh")
	home := t.TempDir()

	targets, warnings, err := CaptureTargets(PlanInput{RepoRoot: repo, Home: home, Config: config.AgentsConfig{}})
	if err != nil {
		t.Fatalf("CaptureTargets: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	byKey := captureByKey(targets)

	// claude -> general.md verbatim (SourceGeneral).
	claude, ok := byKey["agents/claude"]
	if !ok {
		t.Fatal("missing agents/claude target")
	}
	if claude.Kind != CaptureGeneral {
		t.Errorf("claude kind = %v, want CaptureGeneral", claude.Kind)
	}
	if claude.RepoDest != filepath.Join(repo, "agents", "general.md") {
		t.Errorf("claude RepoDest = %q, want agents/general.md", claude.RepoDest)
	}
	if string(claude.Content) != "GENERAL\n" {
		t.Errorf("claude Content = %q, want GENERAL", claude.Content)
	}

	// codex -> combined, no single source.
	codex := byKey["agents/codex"]
	if codex.Kind != CaptureCombined {
		t.Errorf("codex kind = %v, want CaptureCombined", codex.Kind)
	}
	if codex.RepoDest != "" {
		t.Errorf("codex RepoDest = %q, want empty (combined is not decomposable)", codex.RepoDest)
	}

	// hook asset -> shared repo file + local overlay path, exec preserved.
	hook := byKey["agents/hooks/guard.sh"]
	if hook.Kind != CaptureAsset {
		t.Errorf("hook kind = %v, want CaptureAsset", hook.Kind)
	}
	if hook.RepoDest != filepath.Join(repo, "agents", "hooks", "guard.sh") {
		t.Errorf("hook RepoDest = %q", hook.RepoDest)
	}
	if hook.LocalDest != filepath.Join(repo, "local", "agents", "hooks", "guard.sh") {
		t.Errorf("hook LocalDest = %q, want local/agents/hooks/guard.sh", hook.LocalDest)
	}
	if !hook.Exec {
		t.Error("hook Exec = false, want true")
	}
}

// TestCaptureTargets_LocalOverlayWins proves an asset whose shared source is
// shadowed by a local/agents overlay reports the OVERLAY bytes as the deployed
// content — the same local-wins resolution apply uses — so drift is classified
// against what is actually on the machine.
func TestCaptureTargets_LocalOverlayWins(t *testing.T) {
	repo := writeSST(t, map[string]string{
		"skills/demo/SKILL.md": "SHARED\n",
	})
	// A per-machine overlay shadows the shared source.
	overlay := filepath.Join(repo, "local", "agents", "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(overlay), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("LOCAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	targets, _, err := CaptureTargets(PlanInput{RepoRoot: repo, Home: home, Config: config.AgentsConfig{}})
	if err != nil {
		t.Fatalf("CaptureTargets: %v", err)
	}
	skill := captureByKey(targets)["agents/skills/demo/SKILL.md"]
	if string(skill.Content) != "LOCAL\n" {
		t.Errorf("Content = %q, want LOCAL (local overlay wins)", skill.Content)
	}
}

// TestPlan_AssetLocalOverlayWins proves apply's planner deploys the per-machine
// local/agents overlay in place of the shared asset source when one exists —
// the wiring that makes a capture-back LOCAL route actually take effect on the
// next apply (otherwise the local route would be dead scaffolding).
func TestPlan_AssetLocalOverlayWins(t *testing.T) {
	repo := writeSST(t, map[string]string{
		"skills/demo/SKILL.md": "SHARED\n",
	})
	overlay := filepath.Join(repo, "local", "agents", "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(overlay), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("LOCAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	items, _, err := Plan(PlanInput{RepoRoot: repo, Home: home, Config: config.AgentsConfig{}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	skill := itemByKey(items)["agents/skills/demo/SKILL.md"]
	if string(skill.Content) != "LOCAL\n" {
		t.Errorf("Plan deployed %q, want LOCAL (local overlay must win)", skill.Content)
	}
}

// TestAdoptCandidates_RegistryDriven proves adoption finds a new agent-shaped
// file — one sitting under a resolved asset mapping's $HOME target dir that
// ferry never deployed — and routes it to the mapping's repo source, while a
// file the domain DOES deploy is not offered.
func TestAdoptCandidates_RegistryDriven(t *testing.T) {
	repo := writeSST(t, map[string]string{
		"general.md":     "G\n",
		"coding.md":      "C\n",
		"skills/kept.md": "kept\n",
	})
	home := t.TempDir()

	// Deploy the known skill so it is "managed" (present in the plan target set).
	kept := filepath.Join(home, ".claude", "skills", "kept.md")
	if err := os.MkdirAll(filepath.Dir(kept), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kept, []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A brand-new agent-shaped file ferry never deployed.
	newSkill := filepath.Join(home, ".claude", "skills", "brand-new.md")
	if err := os.WriteFile(newSkill, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cands, _, err := AdoptCandidates(PlanInput{RepoRoot: repo, Home: home, Config: config.AgentsConfig{}})
	if err != nil {
		t.Fatalf("AdoptCandidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(cands), cands)
	}
	if cands[0].Home != filepath.Clean(newSkill) {
		t.Errorf("candidate Home = %q, want %q", cands[0].Home, newSkill)
	}
	if cands[0].RepoDest != filepath.Join(repo, "agents", "skills", "brand-new.md") {
		t.Errorf("candidate RepoDest = %q, want agents/skills/brand-new.md", cands[0].RepoDest)
	}
}
