package evals

// v0.3.0 capture reverse-render evals (PLAN "Capture reverse-render") —
// eval-first, RED until the implementing wave lands.
//
// Covered here:
//
//	AC-capture-roundtrip     TestWizardCapture_AC_capture_roundtrip (edit → capture exit 0,
//	                         not gate-blocked, placeholders preserved, post-capture Clean,
//	                         repo edit ⇒ non-empty RepoAhead report naming the target,
//	                         never conflict) +
//	                         TestWizardCapture_AC_capture_roundtrip_adjacent_edit
//	                         (r4-M1 span granularity: an edit ADJACENT to the stored
//	                         span in the same block still reverse-renders) +
//	                         TestWizardCapture_AC_capture_roundtrip_sidecar
//	                         (Codex M2: the ~/.zshrc.local SIDECAR leg — a local-routed
//	                         secret block puts the placeholder in the sidecar seed;
//	                         capture of a sidecar edit is not blocked, placeholder kept)
//	AC-capture-new-secret    TestWizardCapture_AC_capture_new_secret (r6-M1: exit 0 +
//	                         the consent prompt names the gate condition; store the
//	                         new span only, no placeholder inside a stored value;
//	                         DECLINE arm: consent-gated — nothing stored, seed and
//	                         live file untouched) +
//	                         TestWizardCapture_AC_capture_missing_ref (r8-M2/r9-M1:
//	                         verbatim fallback, no content loss, no whole-file escape,
//	                         READ-ONLY block report + no store write) +
//	                         TestWizardCapture_AC_capture_multiline_pem (r8-C1: one
//	                         placeholder line survives, no key material leaks) +
//	                         TestWizardCapture_AC_capture_multi_placeholder_line
//	                         (r9-M2: two placeholders on one source line render,
//	                         round-trip unchanged, re-emit exactly one source line)
//
// Setup flow shared by most tests: wizard-init (answers file) with a store-routed
// secret, then apply — after which the live file is byte-identical to the adopted
// original (placeholders render back), and any later edit exercises the
// reverse-render path. Successful captures additionally assert exit code 0.
//
// FIXTURE GATE NOTE: secret values here are high-entropy by construction (see the
// note in wizard_test.go) — internal/secret's secretAssignment rule does NOT match
// `export GITHUB_TOKEN=...`, so the VALUE must trip the high-entropy-token rule.
//
// TODO(contract): the capture accept/route keystrokes are the suite's existing
// convention (y = accept, s = route shared, l = route local, trailing y =
// confirm); the store-route key on a gated block is NOT doc-pinned yet — the
// scripted input is generous and the load-bearing gates are the observable
// end-states.

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// wcRoundtripZshrc: path block / alias block / token block. The token block
// carries a comment and a non-secret neighbor line so the r4-M1 adjacent-edit arm
// can edit INSIDE the secret's block without touching the stored span.
// Secret block index: 2.
const wcRoundtripZshrc = `# --- ferry-eval: path block ---
export PATH="$HOME/bin:$PATH"

# --- ferry-eval: alias block ---
alias gs='git status'
alias ll='ls -la'

# --- ferry-eval: token block ---
# service credentials for CI
export GITHUB_TOKEN=` + wizSecretToken + `
export GITHUB_API_HOST=api.github.com
`

// wcRoundtripAnswers routes ONLY the secret block to the store; everything else
// stays shared verbatim (keep-as-is).
const wcRoundtripAnswers = `mode = "keep-as-is"
confirm = true

[secret-routes]
"2" = "store"
`

// wcNewSecretValue is the SECOND synthetic secret introduced after the wizard
// run — high-entropy by construction (37 chars, ~5.0 bits/char, base64-shaped)
// so it trips the real gate as a NEW secret.
const wcNewSecretValue = "FERRYNEW2m5K8n1B4v7C0x3Z6QwErTyUiOpAs"

// Distinctive fake PEM body lines (base64ish filler; obviously not a real key).
const wcPEMBody1 = "c3ludGhldGljRmFrZUtleUJvZHlMaW5lMDFaWlpaWlpaWlpaWlpaWlpaWlpa"
const wcPEMBody2 = "c3ludGhldGljRmFrZUtleUJvZHlMaW5lMDJaWlpaWlpaWlpaWlpaWlpaWlpa"

// wcPEMZshrc: a fake multi-line PEM block (secret block index 1) between two
// benign blocks. The PEM BEGIN header trips the scanner's pem-private-key rule.
const wcPEMZshrc = `# --- ferry-eval: path block ---
export PATH="$HOME/bin:$PATH"

# --- ferry-eval: deploy key (fake) ---
-----BEGIN OPENSSH PRIVATE KEY-----
` + wcPEMBody1 + `
` + wcPEMBody2 + `
-----END OPENSSH PRIVATE KEY-----

# --- ferry-eval: alias block ---
alias gs='git status'
`

const wcPEMAnswers = `mode = "keep-as-is"
confirm = true

[secret-routes]
"1" = "store"
`

// wcSidecarBlockLocal is a machine block that CARRIES the secret; routed LOCAL,
// its seed (placeholder included) lives in local/zsh/zshrc.local — the fixture
// for the sidecar capture leg (Codex M2).
const wcSidecarBlockLocal = "# --- ferry-eval: machine token block ---\n" +
	"export GITHUB_TOKEN=" + wizSecretToken + "\n" +
	"export MACHINE_LABEL=ferry-eval-mac\n"

// wcSidecarZshrc: two shared blocks + the local-routed secret-bearing block
// (block index 2). Reuses the byte-pinned shared blocks from wizard_test.go.
const wcSidecarZshrc = wizBlockPath + wizBlockAlias + wcSidecarBlockLocal

// wcSidecarAnswers routes the machine block LOCAL and its secret line to the
// store. INTERPRETATION (Codex-validation point): the [secret-routes] entry
// governs the secret LINE (store), the [routes] entry governs the block's
// REMAINDER (local) — so the placeholder lands in the SIDECAR seed.
const wcSidecarAnswers = `mode = "per-block"
confirm = true

[routes]
"0" = "shared"
"1" = "shared"
"2" = "local"

[secret-routes]
"2" = "store"
`

// wcSetup wizard-inits the given zshrc with the given answers (store-routing its
// secret), applies, and asserts the round-trip precondition: the live file is
// byte-identical to the adopted original (the seed's placeholders render back).
// Sandbox.Repo is repointed at the wizard-created repo so the repo-scanning
// helpers (AssertNoSecretInRepo, findFileContaining) target the right tree.
func wcSetup(t *testing.T, zshrc, answers string) *Sandbox {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: wizard init needs git")
	}
	s := NewSandbox(t)
	s.WriteHomeFile(t, ".zshrc", zshrc, 0o644)
	af := wizWriteAnswers(t, answers)
	if _, errOut, code := s.Ferry("init", "--wizard=answers:"+af); code != 0 {
		t.Fatalf("wcSetup: wizard init exited %d\n%s", code, errOut)
	}
	s.Repo = managedRepoPath(s)
	// The first apply adopts the pre-existing ~/.zshrc and deploys a secret-routed
	// seed — both risky — so confirm the guided walkthrough.
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("wcSetup: apply exited %d\n%s", code, errOut)
	}
	live, err := os.ReadFile(s.HomePath(".zshrc"))
	if err != nil {
		t.Fatalf("wcSetup: read live .zshrc: %v", err)
	}
	if string(live) != zshrc {
		t.Fatalf("wcSetup precondition (AC-secret-store-route render-back): live .zshrc after first apply is not byte-identical to the adopted original.\ngot:\n%q\nwant:\n%q", live, zshrc)
	}
	return s
}

// wcSharedSeed reads the shared zshrc seed from the wizard-created repo.
func wcSharedSeed(t *testing.T, s *Sandbox) string {
	t.Helper()
	return wizReadSharedSeed(t, s.Repo)
}

// wcAppendFile appends text to an existing file.
func wcAppendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("wcAppendFile %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("wcAppendFile %s: %v", path, err)
	}
}

// wcAppendLive appends text to the live ~/.zshrc.
func wcAppendLive(t *testing.T, s *Sandbox, text string) {
	t.Helper()
	wcAppendFile(t, s.HomePath(".zshrc"), text)
}

// wcStoreState snapshots the secret store's file set and contents
// (rel path -> content hash) for exact store-unchanged assertions.
func wcStoreState(t *testing.T, s *Sandbox) map[string]string {
	t.Helper()
	state := map[string]string{}
	root := wizStoreDir(s)
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		state[rel] = hashFile(t, path)
		return nil
	})
	return state
}

// wcReplaceLive rewrites the live ~/.zshrc with old replaced by new (must occur).
func wcReplaceLive(t *testing.T, s *Sandbox, old, new string) {
	t.Helper()
	p := s.HomePath(".zshrc")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("wcReplaceLive: %v", err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("wcReplaceLive: live .zshrc does not contain %q", old)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(data), old, new, 1)), 0o644); err != nil {
		t.Fatalf("wcReplaceLive: %v", err)
	}
}

// -----------------------------------------------------------------------------
// AC-capture-roundtrip
// -----------------------------------------------------------------------------

// TestWizardCapture_AC_capture_roundtrip covers AC-capture-roundtrip: "wizard
// store-route + apply + a later live edit: capture is NOT gate-blocked …; the
// shared/local sources keep their placeholders (never the value) … After the
// capture completes, `ferry status` reports Clean for the target, and a
// subsequent repo-side edit classifies RepoAhead — never conflict (r4-M2)."
func TestWizardCapture_AC_capture_roundtrip(t *testing.T) {
	t.Parallel()
	s := wcSetup(t, wcRoundtripZshrc, wcRoundtripAnswers)

	// A later live edit FAR from the secret: a new alias paragraph at the end.
	marker := "alias gd='git diff'"
	wcAppendLive(t, s, "\n# --- ferry-eval: captured alias ---\n"+marker+"\n")

	out, errOut, captureCode := s.FerryWithInput("y\ns\ny\n", "capture")
	combined := out + errOut

	// A successful capture exits 0.
	if captureCode != 0 {
		t.Errorf("AC-capture-roundtrip: successful capture exited %d\n%s", captureCode, combined)
	}
	// NOT gate-blocked: the edit reached the shared repo source.
	seed := wcSharedSeed(t, s)
	if !strings.Contains(seed, marker) {
		t.Fatalf("AC-capture-roundtrip: the live edit did not reach the repo source — capture was blocked or dropped it (reverse-render missing?)\ncapture output:\n%s\nseed:\n%s", combined, seed)
	}
	// The placeholder is STILL in the repo source; the value NEVER lands in the repo.
	if !strings.Contains(seed, wizGithubTokenPlaceholder) {
		t.Errorf("AC-capture-roundtrip: repo source lost its placeholder after capture\n%s", seed)
	}
	s.AssertNoSecretInRepo(t, wizSecretToken)

	// Post-capture state consistency (r4-M2): status reports Clean for zshrc.
	stOut, stErr, stCode := s.Ferry("status")
	stCombined := stOut + stErr
	if stCode != 0 {
		t.Errorf("AC-capture-roundtrip: post-capture `status` exited %d\n%s", stCode, stCombined)
	}
	if !containsAnyFold(stCombined, "no drift", "clean", "up to date", "up-to-date", "in sync", "no changes", "nothing to") {
		t.Errorf("AC-capture-roundtrip: post-capture `status` gave no positive Clean signal (stale last-applied?)\n%s", stCombined)
	}

	// A subsequent REPO-side edit classifies RepoAhead — never conflict, never
	// silence: the report must be non-empty and must name the zshrc target.
	wcAppendFile(t, wizSharedSeedPath(s.Repo), "\n# repo-side edit\n")

	aheadOut, aheadErr, _ := s.Ferry("status")
	diffOut, diffErr, _ := s.Ferry("diff")
	ahead := aheadOut + aheadErr + diffOut + diffErr
	if strings.TrimSpace(ahead) == "" {
		t.Errorf("AC-capture-roundtrip: repo-side edit produced an EMPTY status/diff report (want a RepoAhead drift report)")
	}
	if containsAnyFold(ahead, "conflict") {
		t.Errorf("AC-capture-roundtrip: a repo-side edit after capture was classified as CONFLICT (r4-M2: last-applied must reflect rendered-effective bytes)\n%s", ahead)
	}
	if !containsAnyFold(ahead, "zshrc") {
		t.Errorf("AC-capture-roundtrip: the RepoAhead drift report does not name the zshrc target\n%s", ahead)
	}
}

// TestWizardCapture_AC_capture_roundtrip_adjacent_edit covers the r4-M1 arm of
// AC-capture-roundtrip: "an edit in the same block ADJACENT to a stored secret
// span still reverse-renders (r4-M1 span granularity)" — the stored value is the
// secret SPAN, never the whole block, so a neighbor-line edit cannot break the
// exact match.
func TestWizardCapture_AC_capture_roundtrip_adjacent_edit(t *testing.T) {
	t.Parallel()
	s := wcSetup(t, wcRoundtripZshrc, wcRoundtripAnswers)

	// Edit the non-secret NEIGHBOR line inside the token block.
	wcReplaceLive(t, s,
		"export GITHUB_API_HOST=api.github.com",
		"export GITHUB_API_HOST=api.example.com")

	out, errOut, captureCode := s.FerryWithInput("y\ns\ny\n", "capture")
	if captureCode != 0 {
		t.Errorf("AC-capture-roundtrip[adjacent]: successful capture exited %d\n%s", captureCode, out+errOut)
	}

	seed := wcSharedSeed(t, s)
	if !strings.Contains(seed, "export GITHUB_API_HOST=api.example.com") {
		t.Errorf("AC-capture-roundtrip[adjacent]: the adjacent edit did not reach the repo source (whole-block value matching? must be span-grained)\ncapture output:\n%s\nseed:\n%s", out+errOut, seed)
	}
	if !strings.Contains(seed, wizGithubTokenPlaceholder) {
		t.Errorf("AC-capture-roundtrip[adjacent]: placeholder lost from the repo source\n%s", seed)
	}
	s.AssertNoSecretInRepo(t, wizSecretToken)
}

// TestWizardCapture_AC_capture_roundtrip_sidecar covers the SIDECAR leg of
// AC-capture-roundtrip (Codex M2: "capture is NOT gate-blocked on EITHER leg —
// shared dotfile AND sidecar"): a secret-bearing block routed LOCAL puts the
// placeholder in local/zsh/zshrc.local; after apply + a live ~/.zshrc.local edit,
// capture must exit 0, land the edit in the LOCAL seed, and keep the placeholder
// there — the value never enters the repo.
func TestWizardCapture_AC_capture_roundtrip_sidecar(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: wizard init needs git")
	}
	s := NewSandbox(t)
	s.WriteHomeFile(t, ".zshrc", wcSidecarZshrc, 0o644)
	af := wizWriteAnswers(t, wcSidecarAnswers)
	if _, errOut, code := s.Ferry("init", "--wizard=answers:"+af); code != 0 {
		t.Fatalf("sidecar setup: init exited %d\n%s", code, errOut)
	}
	s.Repo = managedRepoPath(s)
	// Adopting the pre-existing ~/.zshrc is risky; confirm the walkthrough.
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("sidecar setup: apply exited %d\n%s", code, errOut)
	}

	// Preconditions: the LOCAL seed carries the placeholder; the live sidecar
	// carries the rendered secret.
	localSeedPath := wizLocalSeedPath(s.Repo)
	localSeed, err := os.ReadFile(localSeedPath)
	if err != nil {
		t.Fatalf("sidecar setup: local seed missing at %s: %v", localSeedPath, err)
	}
	if !strings.Contains(string(localSeed), wizGithubTokenPlaceholder) {
		t.Fatalf("sidecar setup: local seed lacks the placeholder (the local-routed secret block must carry it in the SIDECAR seed)\n%s", localSeed)
	}
	liveSidecar, err := os.ReadFile(s.HomePath(".zshrc.local"))
	if err != nil {
		t.Fatalf("sidecar setup: ~/.zshrc.local not materialised: %v", err)
	}
	if !strings.Contains(string(liveSidecar), wizSecretTokenLine) {
		t.Fatalf("sidecar setup: ~/.zshrc.local does not carry the rendered secret\n%s", liveSidecar)
	}

	// A later edit to the LIVE sidecar.
	marker := "alias mlocal='true'"
	wcAppendFile(t, s.HomePath(".zshrc.local"), "\n# --- ferry-eval: sidecar edit ---\n"+marker+"\n")

	out, errOut, captureCode := s.FerryWithInput("y\nl\ny\n", "capture")
	combined := out + errOut
	if captureCode != 0 {
		t.Errorf("AC-capture-roundtrip[sidecar]: successful sidecar capture exited %d\n%s", captureCode, combined)
	}

	// NOT gate-blocked on the sidecar leg: the edit reached the local seed.
	localAfter, err := os.ReadFile(localSeedPath)
	if err != nil {
		t.Fatalf("read local seed after capture: %v", err)
	}
	if !strings.Contains(string(localAfter), marker) {
		t.Fatalf("AC-capture-roundtrip[sidecar]: the sidecar edit did not reach local/zsh/zshrc.local — the SIDECAR leg gate-blocked or dropped it (reverse-render must cover both legs, M2)\ncapture output:\n%s\nlocal seed:\n%s", combined, localAfter)
	}
	// The placeholder is STILL in the local seed; the value never enters the repo.
	if !strings.Contains(string(localAfter), wizGithubTokenPlaceholder) {
		t.Errorf("AC-capture-roundtrip[sidecar]: local seed lost its placeholder after capture\n%s", localAfter)
	}
	s.AssertNoSecretInRepo(t, wizSecretToken)
}

// -----------------------------------------------------------------------------
// AC-capture-new-secret
// -----------------------------------------------------------------------------

// TestWizardCapture_AC_capture_new_secret covers AC-capture-new-secret (r6-M1):
// "a NEW secret added after a wizard store-route + apply: capture gates it; taking
// the store route stores ONLY the new span (no placeholder ever inside a stored
// value), patches just that span in the repo source, preserves the curated
// remainder, and the next apply renders BOTH placeholders correctly (no literal
// `{{ferry.secret …}}` in ~/.zshrc)." The store route is CONSENT-GATED: the
// accepted arm asserts exit 0 + a prompt that names the gate condition; the
// declined arm proves the prompt is real — reject ⇒ nothing stored, seed and
// live file untouched.
func TestWizardCapture_AC_capture_new_secret(t *testing.T) {
	t.Parallel()

	t.Run("store_route_accepted", func(t *testing.T) {
		t.Parallel()
		s := wcSetup(t, wcRoundtripZshrc, wcRoundtripAnswers)

		newSecretLine := "export NEW_TOKEN=" + wcNewSecretValue
		wcAppendLive(t, s, "\n# --- ferry-eval: new token ---\n"+newSecretLine+"\n")

		// Scripted answers using the EXISTING capture key surface (the plan pins
		// "capture's blocked-path prompts keep the existing capture UX"):
		// [y]/[n] hunks (capture.go:269), secret-store is [x] / reject [r]
		// (captureBlocked, capture.go:465), route [s]hared/[l]ocal (capture.go:948).
		// Generous ordering: hunk-accept, store-route, shared-route, confirms.
		// End-state assertions below are the real gate (Codex eval-r6 #1: 's' at
		// the blocked prompt would REJECT and false-fail a correct implementation).
		out, errOut, captureCode := s.FerryWithInput("y\nx\ns\ny\ny\n", "capture")
		combined := out + errOut

		// The consent flow COMPLETES (exit 0) and the prompt was real: the output
		// names the secret/gate condition before offering the store choice.
		if captureCode != 0 {
			t.Errorf("AC-capture-new-secret: consented store-route capture exited %d\n%s", captureCode, combined)
		}
		if !(containsAnyFold(combined, "secret", "private key", "credential", "token", "sensitive") &&
			containsAnyFold(combined, "block", "gate", "store", "route")) {
			t.Errorf("AC-capture-new-secret: the output does not name the secret/gate condition for the consent choice\n%s", combined)
		}

		// The stored value for the NEW ref contains NO placeholder (r6-M1: never store
		// reverse-rendered bytes) — and no store file ever contains one.
		if p := findFileContaining(t, wizStoreDir(s), "{{ferry.secret"); p != "" {
			t.Errorf("AC-capture-new-secret: store file %s contains a placeholder INSIDE a stored value (r6-M1 violation — apply would leave it literal)", p)
		}
		if findFileContaining(t, wizStoreDir(s), wcNewSecretValue) == "" {
			t.Fatalf("AC-capture-new-secret: the new secret value was not stored (store route not taken?)\ncapture output:\n%s", combined)
		}

		// The repo source keeps ALL curated lines; ONLY the new token line became a
		// placeholder; the source was NOT reduced to a whole-file escape.
		seed := wcSharedSeed(t, s)
		for _, curated := range []string{`export PATH="$HOME/bin:$PATH"`, "alias gs='git status'", "alias ll='ls -la'"} {
			if !strings.Contains(seed, curated) {
				t.Errorf("AC-capture-new-secret: curated line %q lost from the repo source (whole-file escape?)\n%s", curated, seed)
			}
		}
		if !strings.Contains(seed, wizGithubTokenPlaceholder) {
			t.Errorf("AC-capture-new-secret: the ORIGINAL placeholder was lost\n%s", seed)
		}
		if n := strings.Count(seed, "{{ferry.secret"); n != 2 {
			t.Errorf("AC-capture-new-secret: repo source has %d placeholders, want exactly 2 (original + new span)\n%s", n, seed)
		}
		if strings.Contains(seed, wcNewSecretValue) {
			t.Errorf("AC-capture-new-secret: the new secret VALUE is in the repo source\n%s", seed)
		}
		s.AssertNoSecretInRepo(t, wcNewSecretValue)
		s.AssertNoSecretInRepo(t, wizSecretToken)

		// Next apply renders BOTH placeholders: no literal `{{ferry.secret` in the
		// live file, and both values present. Secret-routed deploys are risky, so
		// confirm the walkthrough.
		if _, applyErr, code := s.ApplyConfirmed(); code != 0 {
			t.Fatalf("AC-capture-new-secret: apply exited %d\n%s", code, applyErr)
		}
		live, err := os.ReadFile(s.HomePath(".zshrc"))
		if err != nil {
			t.Fatalf("read live .zshrc: %v", err)
		}
		if strings.Contains(string(live), "{{ferry.secret") {
			t.Errorf("AC-capture-new-secret: literal placeholder left in ~/.zshrc after apply (nested-placeholder render bug)\n%s", live)
		}
		if !strings.Contains(string(live), newSecretLine) || !strings.Contains(string(live), wizSecretTokenLine) {
			t.Errorf("AC-capture-new-secret: apply did not render both secrets back\n%s", live)
		}
	})

	t.Run("store_route_declined", func(t *testing.T) {
		t.Parallel()
		s := wcSetup(t, wcRoundtripZshrc, wcRoundtripAnswers)
		seedBefore := wcSharedSeed(t, s)
		storeBefore := wcStoreState(t, s)

		wcAppendLive(t, s, "\n# --- ferry-eval: new token ---\nexport NEW_TOKEN="+wcNewSecretValue+"\n")
		// Snapshot the live file AFTER our edit: capture must not touch it.
		liveSnap := s.SnapshotFile(t, s.HomePath(".zshrc"))

		// DECLINE the prompt — [r]eject at the blocked-secret prompt (its default;
		// capture.go:465) and [n] at any hunk prompt; EOF afterwards keeps
		// everything unapproved. The store route is consent-gated, never automatic.
		out, errOut, _ := s.FerryWithInput("n\nr\nn\nr\n", "capture")

		// Nothing new stored: the store file SET AND CONTENTS are unchanged.
		if !maps.Equal(storeBefore, wcStoreState(t, s)) {
			t.Errorf("AC-capture-new-secret[declined]: the store changed after a DECLINED prompt (consent gate not real)\nbefore: %v\nafter:  %v\ncapture output:\n%s", storeBefore, wcStoreState(t, s), out+errOut)
		}
		if findFileContaining(t, wizStoreDir(s), wcNewSecretValue) != "" {
			t.Errorf("AC-capture-new-secret[declined]: the declined value was stored anyway")
		}
		// Repo seed unchanged; live file untouched; value nowhere in the repo.
		if got := wcSharedSeed(t, s); got != seedBefore {
			t.Errorf("AC-capture-new-secret[declined]: the repo seed changed after a DECLINED capture.\nbefore:\n%q\nafter:\n%q", seedBefore, got)
		}
		liveSnap.AssertUnchanged(t)
		s.AssertNoSecretInRepo(t, wcNewSecretValue)
	})
}

// TestWizardCapture_AC_capture_missing_ref covers the missing-ref arm of
// AC-capture-new-secret (r8-M2/r9-M1): "with a placeholder-bearing repo and an
// empty local store, capture behaves exactly as today (no error, no content loss,
// no value written) and reports the missing refs; if the gate blocks in that
// fallback, the whole-file store escape is NOT offered."
func TestWizardCapture_AC_capture_missing_ref(t *testing.T) {
	t.Parallel()
	s := wcSetup(t, wcRoundtripZshrc, wcRoundtripAnswers)

	// DELETE the store: the placeholder's ref no longer resolves (the new-machine
	// shape: placeholder-bearing repo, unpopulated local store).
	if err := os.RemoveAll(wizStoreDir(s)); err != nil {
		t.Fatalf("remove store: %v", err)
	}
	seedBefore := wcSharedSeed(t, s)

	wcAppendLive(t, s, "\n# --- ferry-eval: captured alias ---\nalias gd='git diff'\n")
	out, errOut, code := s.FerryWithInput("y\ns\ny\n", "capture")
	combined := out + errOut

	// Not a crash: no panic, and a controlled exit (0 or 1).
	if strings.Contains(combined, "panic") || strings.Contains(combined, "goroutine") {
		t.Fatalf("AC-capture-new-secret[missing-ref]: capture CRASHED\n%s", combined)
	}
	if code != 0 && code != 1 {
		t.Errorf("AC-capture-new-secret[missing-ref]: capture exited %d (want a controlled 0/1, never a crash)\n%s", code, combined)
	}
	// The missing ref is reported.
	if !containsAnyFold(combined, "missing", "unresolved", "zsh.github_token", "not populated") {
		t.Errorf("AC-capture-new-secret[missing-ref]: output does not mention the missing ref\n%s", combined)
	}

	// No repo content lost: the curated source keeps its placeholder and curated
	// lines (the edit may or may not have been captured under the fallback, but
	// nothing may be destroyed).
	seedAfter := wcSharedSeed(t, s)
	if !strings.Contains(seedAfter, wizGithubTokenPlaceholder) {
		t.Errorf("AC-capture-new-secret[missing-ref]: placeholder lost from the repo source\nbefore:\n%s\nafter:\n%s", seedBefore, seedAfter)
	}
	for _, curated := range []string{`export PATH="$HOME/bin:$PATH"`, "alias gs='git status'"} {
		if !strings.Contains(seedAfter, curated) {
			t.Errorf("AC-capture-new-secret[missing-ref]: curated line %q lost (content loss in the fallback)\n%s", curated, seedAfter)
		}
	}
	// The whole-file store escape did NOT occur (r9-M1): the source is not a
	// single placeholder line.
	if lines := wizNonBlankLines(seedAfter); len(lines) <= 1 {
		t.Errorf("AC-capture-new-secret[missing-ref]: repo source reduced to %d line(s) — the destructive whole-file store escape ran\n%s", len(lines), seedAfter)
	}
	// r9-M1 READ-ONLY BLOCK REPORT: the fallback gates the rendered secret in the
	// live file and "reports the block read-only, with guidance" — the store-route
	// escape is NOT offered, so NO store write occurs: the store file set is
	// unchanged (still empty after the deletion above).
	if entries := dirEntrySet(wizStoreDir(s)); len(entries) != 0 {
		t.Errorf("AC-capture-new-secret[missing-ref]: the store file set changed (%v) — a store write occurred despite the READ-ONLY block contract (r9-M1: no store escape in the missing-ref fallback)", entries)
	}
	// And the output is a block REPORT naming the gate/block condition — not a
	// silent no-op and not an interactive store-route escape.
	if !containsAnyFold(combined, "block", "gate", "secret", "read-only", "read only") {
		t.Errorf("AC-capture-new-secret[missing-ref]: no read-only block report in the output (the gate fires on the rendered secret; the block must be REPORTED, with guidance)\n%s", combined)
	}
	// No value written anywhere in the repo.
	s.AssertNoSecretInRepo(t, wizSecretToken)
}

// TestWizardCapture_AC_capture_multiline_pem covers the multi-line arm of
// AC-capture-new-secret (r8-C1): "with a stored PEM span and an unrelated live
// edit, the reassembled repo source keeps the single placeholder line (no key
// material, no corrupted neighbors)" — the segment map keeps a multi-line
// rendered value atomic so the splice cannot leak key lines into the repo.
func TestWizardCapture_AC_capture_multiline_pem(t *testing.T) {
	t.Parallel()
	s := wcSetup(t, wcPEMZshrc, wcPEMAnswers)

	// Sanity: the wizard seed carries ONE placeholder line for the whole PEM run.
	seed := wcSharedSeed(t, s)
	if n := strings.Count(seed, "{{ferry.secret"); n != 1 {
		t.Fatalf("setup: expected exactly 1 placeholder in the PEM seed, got %d\n%s", n, seed)
	}

	// An unrelated live edit (different block).
	wcReplaceLive(t, s, "alias gs='git status'", "alias gs='git status -sb'")
	out, errOut, captureCode := s.FerryWithInput("y\ns\ny\n", "capture")
	if captureCode != 0 {
		t.Errorf("AC-capture-new-secret[pem]: successful capture exited %d\n%s", captureCode, out+errOut)
	}

	seed = wcSharedSeed(t, s)
	// Exactly ONE placeholder line survives for the PEM.
	placeholderLines := 0
	for _, l := range strings.Split(seed, "\n") {
		if strings.Contains(l, "{{ferry.secret") {
			placeholderLines++
		}
	}
	if placeholderLines != 1 {
		t.Errorf("AC-capture-new-secret[pem]: repo source has %d placeholder lines, want exactly 1 (multi-line splice corruption)\ncapture output:\n%s\nseed:\n%s", placeholderLines, out+errOut, seed)
	}
	// The edit landed; the neighbors are uncorrupted; no raw key lines in the repo.
	if !strings.Contains(seed, "alias gs='git status -sb'") {
		t.Errorf("AC-capture-new-secret[pem]: the unrelated edit did not reach the repo source\n%s", seed)
	}
	if !strings.Contains(seed, `export PATH="$HOME/bin:$PATH"`) {
		t.Errorf("AC-capture-new-secret[pem]: neighbor content corrupted/lost\n%s", seed)
	}
	if strings.Contains(seed, "BEGIN OPENSSH PRIVATE KEY") {
		t.Errorf("AC-capture-new-secret[pem]: raw PEM header leaked into the repo source\n%s", seed)
	}
	s.AssertNoSecretInRepo(t, wcPEMBody1)
	s.AssertNoSecretInRepo(t, wcPEMBody2)
}

// TestWizardCapture_AC_capture_multi_placeholder_line covers the r9-M2 arm of
// AC-capture-new-secret: "two placeholders on one source line render, round-trip
// unchanged, and re-emit exactly one source line" — RenderWithMap coalesces
// segments PER SOURCE LINE, so the whole rendered range maps back to exactly one
// placeholder-bearing source line.
//
// The wizard's span extraction is not guaranteed to produce two placeholders on
// one line, so the repo + store state is constructed DIRECTLY (documented store
// shape: ~/.config/ferry/secrets-local/<domain>.toml, 0600). These stored values
// deliberately need no gate detection — they are pre-stored and rendered, and
// reverse-render must keep them out of the gate as unchanged segments.
func TestWizardCapture_AC_capture_multi_placeholder_line(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: capture needs git")
	}
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.SeedSharedManifest(t, baseManifest)

	valueA := "aaasynthetic1111111111aaa"
	valueB := "bbbsynthetic2222222222bbb"
	multiLine := `export A={{ferry.secret "zsh.token_a"}} B={{ferry.secret "zsh.token_b"}}`
	src := "# --- ferry-eval: multi-placeholder line ---\n" +
		multiLine + "\n" +
		"\n" +
		"# --- ferry-eval: alias block ---\n" +
		"alias gs='git status'\n"
	// Seed both candidate shared-source layouts (suite convention).
	s.WriteRepoFile(t, ".zshrc", src)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), src)
	gitCommitAll(t, s.Repo, "baseline")

	// Populate the store directly at the documented path/shape.
	s.WriteHomeFile(t, filepath.Join(".config", "ferry", "secrets-local", "zsh.toml"),
		"token_a = \""+valueA+"\"\ntoken_b = \""+valueB+"\"\n", 0o600)

	// Apply renders BOTH placeholders on the one line (secret-routed = risky).
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("setup apply exited %d\n%s", code, errOut)
	}
	live, err := os.ReadFile(s.HomePath(".zshrc"))
	if err != nil {
		t.Fatalf("read live .zshrc: %v", err)
	}
	renderedLine := "export A=" + valueA + " B=" + valueB
	if !strings.Contains(string(live), renderedLine) {
		t.Fatalf("setup: apply did not render both placeholders on one line.\nwant line %q in:\n%s", renderedLine, live)
	}

	// Edit ANOTHER line, capture shared.
	wcReplaceLive(t, s, "alias gs='git status'", "alias gs='git status -sb'")
	out, errOut, captureCode := s.FerryWithInput("y\ns\ny\n", "capture")
	if captureCode != 0 {
		t.Errorf("AC-capture-new-secret[multi-placeholder]: successful capture exited %d\n%s", captureCode, out+errOut)
	}

	// The two-placeholder line survives the round-trip as EXACTLY one source line
	// carrying both placeholders; the edit landed; no value entered the repo.
	seedPath := wizSharedSeedPath(s.Repo)
	if seedPath == "" {
		t.Fatalf("no shared source found in repo after capture")
	}
	data, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read repo source: %v", err)
	}
	seed := string(data)
	var placeholderLines []string
	for _, l := range strings.Split(seed, "\n") {
		if strings.Contains(l, "{{ferry.secret") {
			placeholderLines = append(placeholderLines, l)
		}
	}
	if len(placeholderLines) != 1 || placeholderLines[0] != multiLine {
		t.Errorf("AC-capture-new-secret[multi-placeholder]: the two-placeholder source line did not round-trip to exactly one unchanged line.\ngot lines: %q\nwant one line: %q\ncapture output:\n%s\nseed:\n%s",
			placeholderLines, multiLine, out+errOut, seed)
	}
	if !strings.Contains(seed, "alias gs='git status -sb'") {
		t.Errorf("AC-capture-new-secret[multi-placeholder]: the edit did not reach the repo source\n%s", seed)
	}
	s.AssertNoSecretInRepo(t, valueA)
	s.AssertNoSecretInRepo(t, valueB)
}
