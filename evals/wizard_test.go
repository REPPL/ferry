package evals

// v0.3.0 wizard evals — eval-first, RED until the implementing wave lands.
//
// These tests drive `ferry init`'s wizard DATA-MODEL path via the documented
// `ferry init --wizard-answers <file>` flag (PLAN "Answers file — the concrete
// data-model driver"): a TOML answers file bypasses ONLY the TUI; every invariant
// (pure-until-confirm, gates, backup, scaffold, masking) runs identically and no
// tty is required. The non-interactive fallback (no answers file, non-tty) is
// covered separately. As everywhere in this suite, requireBin gates execution:
// with FERRY_BIN unset every test SKIPS at its first binary invocation.
//
// AC ACCOUNTING — every AC in .work/PLAN-v0.3.0-plugin-wizard.md "Acceptance
// criteria" and where it is pinned:
//
//	AC-wizard-adopt-existing        TestWizard_AC_wizard_adopt_existing (BYTE-EXACT seeds at exactly dotfiles/zshrc + local/zsh/zshrc.local)
//	AC-wizard-scaffold              TestWizard_AC_wizard_scaffold (route-everything-local + drop-everything arms; apply survival; preview == seeded bytes)
//	AC-wizard-keep-as-is-secrets    TestWizard_AC_wizard_keep_as_is_secrets (store route + secret-free byte-identity); drop route in TestWizard_AC_secret_route_drop_and_enforcement
//	AC-wizard-symlink-decline       TestWizard_AC_wizard_symlink_decline (symlink AND unreadable-regular arms; start-fresh refused over both, r5-M1)
//	AC-wizard-from-scratch          TestWizard_AC_wizard_from_scratch
//	AC-wizard-fresh-over-existing   TestWizard_AC_wizard_fresh_over_existing (incl. preview shows original + starter sides)
//	AC-repair-home-path             TestWizard_AC_repair_home_path
//	AC-secret-store-route           TestWizard_AC_secret_store_route_and_combined_secret_repair + TestWizard_AC_secret_route_drop_and_enforcement (drop arm; shared/local answers-layer refusal; missing-[secret-routes] refusal — no silent default route)
//	AC-combined-secret-repair       TestWizard_AC_secret_store_route_and_combined_secret_repair
//	AC-repair-machine-local         TestWizard_AC_repair_machine_local
//	AC-repair-dedupe                TestWizard_AC_repair_dedupe (BYTE-EXACT survivor bytes; emptied-not-deleted)
//	AC-repair-noninteractive-refused TestWizard_AC_repair_noninteractive_refused (--yes, --no-wizard, AND plain non-tty arms)
//	AC-wizard-pure-until-confirm    TestWizard_AC_wizard_pure_until_confirm
//	AC-wizard-backup                TestWizard_AC_wizard_backup (original-bytes/0600/regular arm; symlink + `-2` collision arms UNIT-PHASE, see below)
//	AC-preview-masking              TestWizard_AC_preview_masking (store arm: placeholder shown; drop arm: DROP-LINE MASKING pin —
//	                                the removed line is shown masked by its placeholder, value on no stream, store empty)
//	AC-noninteractive-fallback      TestInitFallback_AC_noninteractive_fallback (non-tty + --yes + --no-wizard triggers over the
//	                                ENRICHED fixture: TWO secrets incl. a PEM span, machine line stays SHARED, /Users/testuser NOT
//	                                auto-repaired; first-apply byte-identity; refs stderr-only; recursive HOME-delta confinement)
//	AC-github-seedplan              TestWizard_AC_github_seedplan (fallback_auto_extract + wizard_routed arms; BYTE-EXACT SeedPlan
//	                                identity asserted on the LOCAL seed AND on the PUSHED tree of a real local bare remote;
//	                                FLAG PRECEDENCE pin: `--github --wizard-answers <f> --yes` runs the wizard-routed path
//	                                fully non-interactively) and
//	                                TestGitHubSecretExtracted_AC_github_secret_extracted (evals/github_test.go — the
//	                                v0.3.0 recut of the retired v0.2.x AC-github-secret-blocked abort contract)
//	AC-init-rerun-guard             TestWizard_AC_init_rerun_guard (re-run init: no wizard re-entry, seeds/.bak untouched; single sidecar directive after re-apply)
//	AC-capture-roundtrip            evals/wizard_capture_test.go (round-trip + adjacent-edit + repo-ahead + SIDECAR-leg arms)
//	AC-capture-new-secret           evals/wizard_capture_test.go (new-secret, multi-line PEM, missing-ref, multi-placeholder arms)
//	Test-strategy rider             TestReadOnly_NoStateDirCreate (status/diff never create ~/.local/state/ferry)
//
// UNIT-PHASE (not eval-testable against the built binary; unit tests in
// internal/plugin + internal/plugin/zsh land with the implementation):
//
//	AC-plugin-registry        needs direct plugin.Registry API access (no CLI surface)
//	AC-zsh-parse-lossless     Parse→Reassemble byte-identity over fixtures + a native go fuzz target
//	AC-zsh-classify           BlockKind classification table over the parser
//	AC-route-enforcement      needs a SYNTHETIC plugin emitting Routes:[Shared] for a SecretLine
//	                          (the answers-layer face — a user ATTEMPTING shared/local for a
//	                          secret block is refused — IS eval-pinned in
//	                          TestWizard_AC_secret_route_drop_and_enforcement)
//	AC-gate-defense-in-depth  needs an INJECTED plugin bug leaking a secret into the shared text
//	AC-wizard-backup (symlink-candidate AND regular-file `-2` collision arms) —
//	    the .bak name embeds a wall-clock timestamp
//	    (`~/.zshrc.ferry-YYYYMMDD-HHMMSS.bak`), so neither a symlink nor a regular
//	    file can be deterministically pre-planted at the exact candidate name from
//	    outside the process; the Lstat-refuse-symlink + O_EXCL + `-2`…`-9` suffix
//	    contract (F2-7/F3-4) is unit-tested on the backup helper.
//	AC-github-seedplan / AC-github-secret-extracted (defense-in-depth arm) — "a
//	    SeedPlan bug leaking a raw secret still blocks the push" needs an INJECTED
//	    bug; unit-tested on the pre-commit gate.
//
// FIXTURE GATE NOTE (re-audited against internal/secret/scan.go): the synthetic
// secret VALUES are high-entropy BY CONSTRUCTION (len >= 24, base64-shaped,
// letters+digits, Shannon entropy >= 4.0) because the scanner's secretAssignment
// rule only fires when the credential word itself sits at line start or right
// after `export ` — `export GITHUB_TOKEN=...` does NOT match it (the var name
// starts with GITHUB, not `token`). The VALUE therefore has to trip the
// high-entropy-token rule for these fixtures to exercise ferry's REAL gate.
//
// NEWLY PINNED PLAN CONTRACTS exercised here (plan "Pinned contracts"):
// FLAG PRECEDENCE — --wizard-answers outranks the wizard-skip meaning of --yes;
// --yes retains its create-confirm/apply-confirm assent meaning, so
// `--github --wizard-answers f --yes` succeeds non-interactively while
// `--github` without --yes still refuses. NOTE: the closing apply confirmation
// exists only WITH --apply (cmd/init.go:25) — plain `init --yes` is INIT-ONLY
// and must not mutate user dotfiles. DROP-LINE MASKING — a dropped secret
// still has its SecretExtraction placeholder; the preview shows the removed
// line masked by that placeholder (placeholder in preview, value nowhere,
// nothing stored).
//
// Carried invariants: AC-no-ssh / AC-no-secret-shared are pinned by the existing
// suite (SSHTripwire / AssertNoSecretInRepo in capture_test.go, safety_test.go,
// github_test.go) plus every AssertNoSecretInRepo call in this file. AC-no-wipe is
// pinned by the existing apply-guard evals plus the scaffold apply-survival arm
// here. AC-parse-no-pii is a fixture rule (synthetic /Users/testuser only, no real
// usernames) obeyed throughout this file and asserted again at the parser unit level.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// -----------------------------------------------------------------------------
// Fixtures — inline zshrc bodies with distinct, greppable blocks separated by
// blank lines. All paths synthetic (/Users/testuser); all secrets synthetic.
// -----------------------------------------------------------------------------

// wizSecretToken is a SYNTHETIC secret value engineered to trip ferry's real gate
// via the high-entropy-token rule (internal/secret/scan.go): 36 chars, base64url
// alphabet only, letters+digits, Shannon entropy ~4.93 bits/char (threshold 4.0).
// See the FIXTURE GATE NOTE in the header: the GITHUB_TOKEN var name alone does
// NOT trigger the secretAssignment rule, so the value carries the detection.
const wizSecretToken = "FERRYEVALq7W3e9R1t8Y2u0I6o4P5aSdKjZx"

// wizSecretTokenLine is the exact live line the wizard must extract.
const wizSecretTokenLine = "export GITHUB_TOKEN=" + wizSecretToken

// wizGithubTokenPlaceholder is the deterministic placeholder the plan pins for a
// GITHUB_TOKEN extraction in the zsh domain (ref naming: sanitized lowercase var
// name, domain-prefixed — the plan's own canonical example).
const wizGithubTokenPlaceholder = `{{ferry.secret "zsh.github_token"}}`

// wizScaffoldSourceLine is the BARE (non-marker-shaped) sidecar source line the
// minimum shared scaffold must carry (PLAN "Minimum shared scaffold", F3-1).
const wizScaffoldSourceLine = `[ -f ~/.zshrc.local ] && source ~/.zshrc.local`

// wizOverlayMarker is the ferry overlay marker comment that must NOT precede the
// scaffold's source line (marker shape gets stripped and the apply guard refuses).
const wizOverlayMarker = "# ferry: per-machine overlay"

// The standard 3-block adopt fixture, defined block-by-block so byte-exact seed
// expectations derive precisely. SEED ASSEMBLY (pinned): blocks partition the
// input; each block's Raw OWNS its trailing separator (the blank line after it);
// a routed seed is the byte-concat of the selected blocks' Raw.
const (
	wizBlockPath  = "# --- ferry-eval: path block ---\nexport PATH=\"$HOME/bin:$PATH\"\n\n"
	wizBlockAlias = "# --- ferry-eval: alias block ---\nalias gs='git status'\nalias ll='ls -la'\n\n"
	wizBlockBrew  = "# --- ferry-eval: brew block ---\neval \"$(/opt/homebrew/bin/brew shellenv)\"\n"
)

// wizThreeBlockZshrc: PATH export block / alias block / machine-specific brew
// block (block indices 0,1,2).
const wizThreeBlockZshrc = wizBlockPath + wizBlockAlias + wizBlockBrew

// wizPlainZshrc: substantial, secret-free, machine-neutral — for keep-as-is
// byte-identity, backup, and fresh-over-existing fixtures.
const wizPlainZshrc = `# --- ferry-eval: path block ---
export PATH="$HOME/bin:$PATH"

# --- ferry-eval: alias block ---
alias gs='git status'
alias ll='ls -la'
`

// wizSecretKeepZshrc: two benign blocks plus a token block (secret block index 2).
const wizSecretKeepZshrc = `# --- ferry-eval: path block ---
export PATH="$HOME/bin:$PATH"

# --- ferry-eval: alias block ---
alias gs='git status'

# --- ferry-eval: token block ---
export GITHUB_TOKEN=` + wizSecretToken + `
`

// wizHomePathZshrc: one repairable hardcoded-home line (synthetic /Users/testuser).
const wizHomePathZshrc = `# --- ferry-eval: path block ---
export PATH="$HOME/bin:$PATH"

# --- ferry-eval: project alias ---
alias proj="cd /Users/testuser/projects"
`

// wizCombinedZshrc: a single block carrying BOTH a secret and a repairable
// hardcoded-home line (block index 1) — the r3-M2 single-writer case.
const wizCombinedZshrc = `# --- ferry-eval: path block ---
export PATH="$HOME/bin:$PATH"

# --- ferry-eval: combined block ---
export GITHUB_TOKEN=` + wizSecretToken + `
alias proj="cd /Users/testuser/projects"
`

// Dedupe fixture blocks: duplicate PATH export + dead source line, each its OWN
// block (no header comment) so "emptied, not deleted" (Raw -> "", separators
// included) yields an unambiguous byte-exact expectation.
const (
	wizDedupeAliasBlock = "# --- ferry-eval: alias block ---\nalias gs='git status'\n\n"
	wizDedupeDupBlock   = "export PATH=\"$HOME/bin:$PATH\"\n\n"
	wizDedupeDeadBlock  = "source ~/nonexistent.zsh\n"
)

// wizDedupeZshrc: path block / alias block / duplicate PATH block / dead source.
const wizDedupeZshrc = wizBlockPath + wizDedupeAliasBlock + wizDedupeDupBlock + wizDedupeDeadBlock

// wizPEMBody is the distinctive body line of the enriched fallback fixture's
// fake PEM key (obviously-fake base64ish filler; the PEM BEGIN header carries
// the gate detection, pem-private-key rule).
const wizPEMBody = "c3ludGhldGljRmFrZUtleUJvZHlMaW5lMDNaWlpaWlpaWlpaWlpaWlpaWlpa"

// wizPEMSpan is the exact contiguous BEGIN..END secret span — the unit the plan
// pins for non-assignment extraction (span-grained, never whole-block).
const wizPEMSpan = "-----BEGIN OPENSSH PRIVATE KEY-----\n" + wizPEMBody + "\n-----END OPENSSH PRIVATE KEY-----"

// wizFallbackZshrc is the ENRICHED non-interactive fallback fixture: TWO secrets
// (the assignment token AND a fake PEM block), a machine-specific brew line, and
// a hardcoded-home line. The fallback must extract BOTH secrets, DECLINE the
// machine-local suggestion (the brew line stays SHARED, no sidecar seed), and
// DECLINE repairs (the /Users/testuser line is byte-preserved).
const wizFallbackZshrc = `# --- ferry-eval: path block ---
export PATH="$HOME/bin:$PATH"

# --- ferry-eval: token block ---
export GITHUB_TOKEN=` + wizSecretToken + `

# --- ferry-eval: deploy key (fake) ---
` + wizPEMSpan + `

# --- ferry-eval: brew block ---
eval "$(/opt/homebrew/bin/brew shellenv)"

# --- ferry-eval: project alias ---
alias proj="cd /Users/testuser/projects"
`

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// wizRequireGit skips when git is unavailable (fresh init needs git).
func wizRequireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: wizard init needs git to establish a repo")
	}
}

// wizWriteAnswers writes a --wizard-answers TOML file OUTSIDE the sandbox HOME
// (so HOME-content assertions stay clean) and returns its path.
func wizWriteAnswers(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "answers.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("wizWriteAnswers: %v", err)
	}
	return p
}

// wizSharedSeedPath returns the shared zshrc seed path inside the wizard-created
// repo, tolerating the candidate layouts the suite already accepts. The plan
// pins dotfiles/zshrc — AC-wizard-adopt-existing asserts that exact path; this
// tolerant reader exists for tests whose subject is CONTENT, not layout.
func wizSharedSeedPath(repoDir string) string {
	for _, rel := range []string{
		filepath.Join("dotfiles", "zshrc"),
		filepath.Join("dotfiles", ".zshrc"),
		".zshrc",
	} {
		p := filepath.Join(repoDir, rel)
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// wizReadSharedSeed reads the shared seed, failing the test when none exists.
func wizReadSharedSeed(t *testing.T, repoDir string) string {
	t.Helper()
	p := wizSharedSeedPath(repoDir)
	if p == "" {
		t.Fatalf("no shared zshrc seed found under %s (looked for dotfiles/zshrc, dotfiles/.zshrc, .zshrc)", repoDir)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read shared seed %s: %v", p, err)
	}
	return string(data)
}

// wizLocalSeedPath is the plan-pinned per-machine sidecar seed location.
func wizLocalSeedPath(repoDir string) string {
	return filepath.Join(repoDir, "local", "zsh", "zshrc.local")
}

// wizStoreDir is the documented secret-store location (~/.config/ferry/secrets-local).
func wizStoreDir(s *Sandbox) string {
	return s.HomePath(".config", "ferry", "secrets-local")
}

// wizBaks globs the visible wizard backups (~/.zshrc.ferry-<ts>.bak[-N]).
func wizBaks(t *testing.T, s *Sandbox) []string {
	t.Helper()
	matches, err := filepath.Glob(s.HomePath(".zshrc.ferry-*.bak*"))
	if err != nil {
		t.Fatalf("glob .bak: %v", err)
	}
	return matches
}

// wizNonBlankLines returns the trimmed non-blank lines of a file body.
func wizNonBlankLines(body string) []string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

// wizAssertNoSecretInRepoDir runs the full working-tree + git-history secret scan
// against an arbitrary repo dir (the wizard creates its repo at ferry's neutral
// default, not at Sandbox.Repo).
func wizAssertNoSecretInRepoDir(t *testing.T, s *Sandbox, repoDir, secret string) {
	t.Helper()
	scan := &Sandbox{t: s.t, Home: s.Home, Repo: repoDir, BinDir: s.BinDir}
	scan.AssertNoSecretInRepo(t, secret)
}

// wizManifest reads the repo's ferry.toml (empty string when absent).
func wizManifest(repoDir string) string {
	data, err := os.ReadFile(filepath.Join(repoDir, "ferry.toml"))
	if err != nil {
		return ""
	}
	return string(data)
}

// wizAssertNothingSeeded asserts the abort/decline contract "nothing was changed":
// no config.toml, no repo dir, no secret store entries, no .bak — the ferry config
// dir must be exactly as empty as NewSandbox left it.
func wizAssertNothingSeeded(t *testing.T, s *Sandbox) {
	t.Helper()
	if _, err := os.Stat(s.ConfigTOMLPath()); err == nil {
		t.Errorf("nothing-seeded violated: config.toml exists at %s", s.ConfigTOMLPath())
	}
	if entries := dirEntrySet(s.HomePath(".config", "ferry")); len(entries) != 0 {
		t.Errorf("nothing-seeded violated: ~/.config/ferry is not empty: %v", entries)
	}
	if baks := wizBaks(t, s); len(baks) != 0 {
		t.Errorf("nothing-seeded violated: a .bak was created: %v", baks)
	}
}

// wizConfigFerryPrefix is the ferry-owned HOME area every CONFIRMED wizard/init
// run may write (repo, secret store, config.toml).
var wizConfigFerryPrefix = filepath.Join(".config", "ferry")

// wizHomeState recursively snapshots every file AND directory under HOME:
// rel path -> "hash|mode|size" for regular files, "symlink-><target>" for
// symlinks, "dir" for directories — so a NEW (even empty) directory outside the
// allowed areas, e.g. ~/.cache/ferry, is caught by the confinement diff. mtime
// is deliberately excluded — the SnapshotFile tripwires cover same-bytes
// rewrites where one path is pinned; this walk answers "did anything under HOME
// change AT ALL, anywhere".
func wizHomeState(t *testing.T, s *Sandbox) map[string]string {
	t.Helper()
	state := map[string]string{}
	err := filepath.Walk(s.Home, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// A file git creates and then removes mid-walk — e.g. a
			// .git/objects/pack/tmp_pack_* temporary during a pack/gc — can
			// vanish between the walk enumerating it and the lstat, which is a
			// transient, not a test failure. Skip a since-deleted entry rather
			// than aborting the whole snapshot.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, rerr := filepath.Rel(s.Home, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil // HOME itself
		}
		if info.IsDir() {
			state[rel] = "dir"
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(path)
			state[rel] = "symlink->" + target
			return nil
		}
		state[rel] = fmt.Sprintf("%s|%o|%d", hashFile(t, path), info.Mode().Perm(), info.Size())
		return nil
	})
	if err != nil {
		t.Fatalf("wizHomeState: walk: %v", err)
	}
	return state
}

// wizAssertHomeDeltaConfined diffs two recursive HOME snapshots and fails on ANY
// created, deleted, or modified file outside the allowed rel-path prefixes plus
// — when allowBak — the visible ~/.zshrc.ferry-<ts>.bak. This closes the shallow
// top-level-entries gap: a write under the sandbox's pre-created
// ~/.local/state/ferry or ~/.local/bin is caught here.
func wizAssertHomeDeltaConfined(t *testing.T, before, after map[string]string, allowedPrefixes []string, allowBak bool) {
	t.Helper()
	allowed := func(rel string) bool {
		for _, p := range allowedPrefixes {
			if rel == p || strings.HasPrefix(rel, p+string(os.PathSeparator)) {
				return true
			}
		}
		// The .bak allowance is a TOP-LEVEL regular file only (Codex eval-r6 #2:
		// a backup-shaped DIRECTORY with nested files must not slip through).
		if allowBak && strings.HasPrefix(rel, ".zshrc.ferry-") && strings.Contains(rel, ".bak") &&
			!strings.Contains(rel, string(os.PathSeparator)) && after[rel] != "dir" {
			return true
		}
		return false
	}
	for rel, sig := range after {
		prev, existed := before[rel]
		if !existed {
			if !allowed(rel) {
				t.Errorf("HOME-delta tripwire: file CREATED outside the allowed areas: ~/%s", rel)
			}
			continue
		}
		if prev != sig && !allowed(rel) {
			t.Errorf("HOME-delta tripwire: file MODIFIED outside the allowed areas: ~/%s (%s -> %s)", rel, prev, sig)
		}
	}
	for rel := range before {
		if _, still := after[rel]; !still && !allowed(rel) {
			t.Errorf("HOME-delta tripwire: file DELETED outside the allowed areas: ~/%s", rel)
		}
	}
}

// wizPushedSharedSeed reads the shared zshrc seed out of the PUSHED tree of a
// bare remote (branch-agnostic: the newest commit across all refs). Errors and
// returns "" when nothing was pushed at all — a push EVENT without transferred
// content fails here.
func wizPushedSharedSeed(t *testing.T, bare string) string {
	t.Helper()
	rev := exec.Command("git", "-C", bare, "rev-list", "--all", "-1")
	rev.Env = gitEnv()
	shaOut, err := rev.CombinedOutput()
	sha := strings.TrimSpace(string(shaOut))
	if err != nil || sha == "" {
		t.Errorf("pushed-content check: bare remote %s has no commits — nothing was actually pushed\n%s", bare, shaOut)
		return ""
	}
	for _, rel := range []string{"dotfiles/zshrc", "dotfiles/.zshrc", ".zshrc"} {
		show := exec.Command("git", "-C", bare, "show", sha+":"+rel)
		show.Env = gitEnv()
		if out, err := show.Output(); err == nil {
			return string(out)
		}
	}
	t.Errorf("pushed-content check: no shared zshrc seed in the pushed tree of %s (commit %s)", bare, sha)
	return ""
}

// -----------------------------------------------------------------------------
// AC-wizard-adopt-existing
// -----------------------------------------------------------------------------

// TestWizard_AC_wizard_adopt_existing covers AC-wizard-adopt-existing: "(data-model
// path) blocks routed shared/local/drop produce exactly the expected
// `dotfiles/zshrc` + `local/zsh/zshrc.local` seeds; manifest matches initFresh's;
// no USER file changed — writes confined to `~/.config/ferry/…` + the .bak."
//
// SEED ASSEMBLY (pinned): a routed seed is the byte-concat of the selected
// blocks' Raw, each Raw owning its trailing separator — asserted BYTE-EXACT here,
// at the plan-pinned repo paths (no layout alternates).
//
// INTERPRETATION (Codex-validation point): blocks are the blank-line-separated
// paragraphs of the fixture, 0-indexed in file order (comment header + its lines
// form one block, owning the blank line that follows it).
func TestWizard_AC_wizard_adopt_existing(t *testing.T) {
	t.Parallel()
	wizRequireGit(t)
	s := NewSandbox(t)
	s.WriteHomeFile(t, ".zshrc", wizThreeBlockZshrc, 0o644)
	liveBefore := s.SnapshotFile(t, s.HomePath(".zshrc"))
	localBefore := s.SnapshotFile(t, s.HomePath(".zshrc.local"))
	homeBefore := wizHomeState(t, s)

	answers := wizWriteAnswers(t, `mode = "per-block"
confirm = true

[routes]
"0" = "shared"
"1" = "local"
"2" = "drop"
`)
	if _, errOut, code := s.Ferry("init", "--wizard-answers", answers); code != 0 {
		t.Fatalf("AC-wizard-adopt-existing: init --wizard-answers exited %d\n%s", code, errOut)
	}
	repoDir := managedRepoPath(s)

	// Shared seed == the shared-routed block's exact bytes, at EXACTLY
	// <repo>/dotfiles/zshrc (the plan-pinned layout).
	sharedPath := filepath.Join(repoDir, "dotfiles", "zshrc")
	sharedData, err := os.ReadFile(sharedPath)
	if err != nil {
		t.Fatalf("AC-wizard-adopt-existing: shared seed missing at exactly %s: %v", sharedPath, err)
	}
	if string(sharedData) != wizBlockPath {
		t.Errorf("AC-wizard-adopt-existing: shared seed is not BYTE-EXACT block 0 (Raw incl. trailing separator).\ngot:\n%q\nwant:\n%q", sharedData, wizBlockPath)
	}

	// Local seed == the local-routed block's exact bytes, at the plan-pinned
	// local/zsh/zshrc.local.
	localData, err := os.ReadFile(wizLocalSeedPath(repoDir))
	if err != nil {
		t.Fatalf("AC-wizard-adopt-existing: local seed missing at %s: %v", wizLocalSeedPath(repoDir), err)
	}
	if string(localData) != wizBlockAlias {
		t.Errorf("AC-wizard-adopt-existing: local seed is not BYTE-EXACT block 1 (Raw incl. trailing separator).\ngot:\n%q\nwant:\n%q", localData, wizBlockAlias)
	}

	// Dropped block appears NOWHERE in the repo.
	if p := findFileContaining(t, repoDir, "brew shellenv"); p != "" {
		t.Errorf("AC-wizard-adopt-existing: DROPPED block found in repo file %s", p)
	}

	// Manifest matches initFresh's: ferry.toml declares .zshrc.
	if m := wizManifest(repoDir); !strings.Contains(m, ".zshrc") {
		t.Errorf("AC-wizard-adopt-existing: repo ferry.toml does not declare .zshrc\n%s", m)
	}

	// No USER file changed: live ~/.zshrc and ~/.zshrc.local untouched (init does
	// not apply); RECURSIVE HOME diff — the only allowed deltas anywhere under
	// HOME are ~/.config/ferry/** plus the single visible .bak (a write under the
	// pre-created ~/.local/state/ferry or ~/.local/bin fails here).
	liveBefore.AssertUnchanged(t)
	localBefore.AssertUnchanged(t)
	wizAssertHomeDeltaConfined(t, homeBefore, wizHomeState(t, s), []string{wizConfigFerryPrefix}, true)
	if baks := wizBaks(t, s); len(baks) != 1 {
		t.Errorf("AC-wizard-adopt-existing: want exactly one ~/.zshrc.ferry-<ts>.bak, got %v", baks)
	}
}

// -----------------------------------------------------------------------------
// AC-wizard-scaffold
// -----------------------------------------------------------------------------

// TestWizard_AC_wizard_scaffold covers AC-wizard-scaffold: "both starvation shapes
// yield the minimum shared scaffold as the shared seed and the preview text equals
// the seeded bytes; the scaffold survives `stripFerryOverlayDirective`
// non-near-empty (no marker-shape source line) and first apply succeeds over a
// substantial live zshrc (no EmptyOverSubstantialError). Arms differ (F3-7):
// route-everything-Local ⇒ scaffold + local sidecar deployed + source directive
// present; drop-everything ⇒ scaffold only, NO local seed file."
func TestWizard_AC_wizard_scaffold(t *testing.T) {
	t.Parallel()

	assertScaffold := func(t *testing.T, seed, stdout string) {
		t.Helper()
		if !strings.Contains(seed, wizScaffoldSourceLine) {
			t.Errorf("AC-wizard-scaffold: shared seed lacks the BARE scaffold source line %q\n%s", wizScaffoldSourceLine, seed)
		}
		if strings.Contains(seed, wizOverlayMarker) {
			t.Errorf("AC-wizard-scaffold: scaffold source line is MARKER-SHAPED (%q present) — the stripper would empty it and the apply guard would refuse\n%s", wizOverlayMarker, seed)
		}
		// None of the routed-away user content may sit in the shared seed.
		for _, gone := range []string{"alias gs", "brew shellenv", `export PATH="$HOME/bin:$PATH"`} {
			if strings.Contains(seed, gone) {
				t.Errorf("AC-wizard-scaffold: shared seed still contains routed-away content %q", gone)
			}
		}
		// "the preview text equals the seeded bytes": the FULL seeded shared
		// content must appear verbatim in the stdout preview.
		if !strings.Contains(stdout, seed) {
			t.Errorf("AC-wizard-scaffold: the stdout preview does not contain the seeded shared bytes VERBATIM (preview must equal the seed)\nseed:\n%q\nstdout:\n%s", seed, stdout)
		}
	}

	t.Run("route_everything_local", func(t *testing.T) {
		t.Parallel()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizThreeBlockZshrc, 0o644)
		answers := wizWriteAnswers(t, `mode = "per-block"
confirm = true

[routes]
"0" = "local"
"1" = "local"
"2" = "local"
`)
		out, errOut, code := s.Ferry("init", "--wizard-answers", answers)
		if code != 0 {
			t.Fatalf("AC-wizard-scaffold[local]: init exited %d\n%s", code, errOut)
		}
		repoDir := managedRepoPath(s)
		seed := wizReadSharedSeed(t, repoDir)
		assertScaffold(t, seed, out)
		// Local sidecar seed carries ALL routed content.
		localData, err := os.ReadFile(wizLocalSeedPath(repoDir))
		if err != nil {
			t.Fatalf("AC-wizard-scaffold[local]: local sidecar seed missing: %v", err)
		}
		for _, want := range []string{`export PATH="$HOME/bin:$PATH"`, "alias gs='git status'", "brew shellenv"} {
			if !strings.Contains(string(localData), want) {
				t.Errorf("AC-wizard-scaffold[local]: local seed missing routed content %q", want)
			}
		}

		// First apply over the substantial live zshrc must SUCCEED (no
		// EmptyOverSubstantial refusal) and deploy scaffold + sidecar + directive.
		// Adopting the pre-existing file is risky, so confirm the walkthrough.
		if _, errOut, code := s.ApplyConfirmed(); code != 0 {
			t.Fatalf("AC-wizard-scaffold[local]: first apply exited %d (EmptyOverSubstantial refusal? the scaffold must survive the guard)\n%s", code, errOut)
		}
		live, err := os.ReadFile(s.HomePath(".zshrc"))
		if err != nil {
			t.Fatalf("read live .zshrc: %v", err)
		}
		if n := strings.Count(string(live), "source ~/.zshrc.local"); n != 1 {
			t.Errorf("AC-wizard-scaffold[local]: live .zshrc has %d `source ~/.zshrc.local` occurrences, want exactly 1\n%s", n, live)
		}
		if strings.Contains(string(live), "alias gs") {
			t.Errorf("AC-wizard-scaffold[local]: live .zshrc still carries locally-routed content (should live in ~/.zshrc.local)\n%s", live)
		}
		sidecar, err := os.ReadFile(s.HomePath(".zshrc.local"))
		if err != nil {
			t.Fatalf("AC-wizard-scaffold[local]: ~/.zshrc.local not materialised: %v", err)
		}
		if !strings.Contains(string(sidecar), "alias gs='git status'") {
			t.Errorf("AC-wizard-scaffold[local]: ~/.zshrc.local missing routed content\n%s", sidecar)
		}
	})

	t.Run("drop_everything", func(t *testing.T) {
		t.Parallel()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizThreeBlockZshrc, 0o644)
		answers := wizWriteAnswers(t, `mode = "per-block"
confirm = true

[routes]
"0" = "drop"
"1" = "drop"
"2" = "drop"
`)
		out, errOut, code := s.Ferry("init", "--wizard-answers", answers)
		if code != 0 {
			t.Fatalf("AC-wizard-scaffold[drop]: init exited %d\n%s", code, errOut)
		}
		repoDir := managedRepoPath(s)
		assertScaffold(t, wizReadSharedSeed(t, repoDir), out)
		// F3-7: NO local seed file in the drop arm — never an empty sidecar.
		if _, err := os.Lstat(wizLocalSeedPath(repoDir)); err == nil {
			t.Errorf("AC-wizard-scaffold[drop]: a local sidecar seed exists at %s (must not; nothing routed local)", wizLocalSeedPath(repoDir))
		}
		// Dropped content is not anywhere in the repo.
		if p := findFileContaining(t, repoDir, "alias gs"); p != "" {
			t.Errorf("AC-wizard-scaffold[drop]: dropped content found in repo file %s", p)
		}

		// Apply over the substantial live zshrc succeeds; live becomes the scaffold;
		// no ~/.zshrc.local appears (the scaffold's guarded source line stays inert).
		// Adopting the pre-existing file is risky, so confirm the walkthrough.
		if _, errOut, code := s.ApplyConfirmed(); code != 0 {
			t.Fatalf("AC-wizard-scaffold[drop]: first apply exited %d (EmptyOverSubstantial refusal?)\n%s", code, errOut)
		}
		live, err := os.ReadFile(s.HomePath(".zshrc"))
		if err != nil {
			t.Fatalf("read live .zshrc: %v", err)
		}
		if !strings.Contains(string(live), wizScaffoldSourceLine) {
			t.Errorf("AC-wizard-scaffold[drop]: live .zshrc is not the scaffold\n%s", live)
		}
		// "no directive appended" (plan, F3-7/Codex eval-r6 #3): the scaffold's own
		// guarded source line must be the ONLY one — a duplicate appended
		// `source ~/.zshrc.local` directive means appendSourceDirective ran.
		if n := strings.Count(string(live), "source ~/.zshrc.local"); n != 1 {
			t.Errorf("AC-wizard-scaffold[drop]: want exactly 1 `source ~/.zshrc.local` occurrence in live .zshrc, got %d (directive appended despite no local seed?)\n%s", n, live)
		}
		if strings.Contains(string(live), "alias gs") || strings.Contains(string(live), "brew shellenv") {
			t.Errorf("AC-wizard-scaffold[drop]: dropped content survived into the live .zshrc\n%s", live)
		}
		if _, err := os.Lstat(s.HomePath(".zshrc.local")); err == nil {
			t.Errorf("AC-wizard-scaffold[drop]: ~/.zshrc.local was materialised despite nothing routed local")
		}
	})
}

// -----------------------------------------------------------------------------
// AC-wizard-keep-as-is-secrets
// -----------------------------------------------------------------------------

// TestWizard_AC_wizard_keep_as_is_secrets covers AC-wizard-keep-as-is-secrets:
// "keep-as-is with a secret-bearing zshrc routes the secret blocks (store/drop)
// and seeds everything else verbatim; the seeded repo never contains the secret
// bytes; keep-as-is with NO secrets is byte-identical to v0.2.1 adopt."
// The drop route is pinned in TestWizard_AC_secret_route_drop_and_enforcement.
func TestWizard_AC_wizard_keep_as_is_secrets(t *testing.T) {
	t.Parallel()

	t.Run("secret_routed_to_store", func(t *testing.T) {
		t.Parallel()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizSecretKeepZshrc, 0o644)
		answers := wizWriteAnswers(t, `mode = "keep-as-is"
confirm = true

[secret-routes]
"2" = "store"
`)
		if _, errOut, code := s.Ferry("init", "--wizard-answers", answers); code != 0 {
			t.Fatalf("AC-wizard-keep-as-is-secrets: init exited %d\n%s", code, errOut)
		}
		repoDir := managedRepoPath(s)
		seed := wizReadSharedSeed(t, repoDir)

		// Everything else verbatim + RHS swapped for the deterministic placeholder:
		// the seed is EXACTLY the live file with the value replaced by the placeholder.
		want := strings.Replace(wizSecretKeepZshrc, wizSecretToken, wizGithubTokenPlaceholder, 1)
		if seed != want {
			t.Errorf("AC-wizard-keep-as-is-secrets: shared seed is not the verbatim file with the RHS placeholder swap.\ngot:\n%s\nwant:\n%s", seed, want)
		}
		if !strings.Contains(seed, wizGithubTokenPlaceholder) {
			t.Errorf("AC-wizard-keep-as-is-secrets: seed lacks placeholder %s", wizGithubTokenPlaceholder)
		}
		wizAssertNoSecretInRepoDir(t, s, repoDir, wizSecretToken)

		// The store under ~/.config/ferry/secrets-local holds the value, mode 0600.
		storeFile := findFileContaining(t, wizStoreDir(s), wizSecretToken)
		if storeFile == "" {
			t.Fatalf("AC-wizard-keep-as-is-secrets: secret value not found in any file under %s", wizStoreDir(s))
		}
		if info, err := os.Stat(storeFile); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("AC-wizard-keep-as-is-secrets: store file %s mode = %v, want 0600 (err %v)", storeFile, info.Mode().Perm(), err)
		}
	})

	t.Run("secret_free_byte_identical", func(t *testing.T) {
		t.Parallel()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizPlainZshrc, 0o644)
		answers := wizWriteAnswers(t, `mode = "keep-as-is"
confirm = true
`)
		if _, errOut, code := s.Ferry("init", "--wizard-answers", answers); code != 0 {
			t.Fatalf("AC-wizard-keep-as-is-secrets[secret-free]: init exited %d\n%s", code, errOut)
		}
		seed := wizReadSharedSeed(t, managedRepoPath(s))
		if seed != wizPlainZshrc {
			t.Errorf("AC-wizard-keep-as-is-secrets[secret-free]: seed is not BYTE-IDENTICAL to the live zshrc (v0.2.1 adopt parity).\ngot:\n%q\nwant:\n%q", seed, wizPlainZshrc)
		}
	})
}

// -----------------------------------------------------------------------------
// AC-secret-store-route — drop arm + forced-choice enforcement (answers layer)
// -----------------------------------------------------------------------------

// TestWizard_AC_secret_route_drop_and_enforcement covers the remaining faces of
// AC-secret-store-route — "a secret-shaped line offers ONLY SecretStore/Drop —
// never Shared, never Local" — and AC-wizard-keep-as-is-secrets' drop route, as
// observable at the answers layer:
//
//   - DROP: the secret line lands in NEITHER the seed NOR the store; the original
//     (secret included) survives only in the .bak ("dropped lines remain in your
//     backup").
//   - ENFORCEMENT: an answers file that tries to route the secret block Shared or
//     Local is refused (non-zero, nothing seeded) — whether the wizard rejects the
//     illegal route or errors on the missing forced [secret-routes] entry, the
//     forced store/drop choice must be real, not advisory.
func TestWizard_AC_secret_route_drop_and_enforcement(t *testing.T) {
	t.Parallel()

	t.Run("drop_removes_line_bak_keeps_it", func(t *testing.T) {
		t.Parallel()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizSecretKeepZshrc, 0o644)
		answers := wizWriteAnswers(t, `mode = "keep-as-is"
confirm = true

[secret-routes]
"2" = "drop"
`)
		if _, errOut, code := s.Ferry("init", "--wizard-answers", answers); code != 0 {
			t.Fatalf("AC-secret-store-route[drop]: init exited %d\n%s", code, errOut)
		}
		repoDir := managedRepoPath(s)
		seed := wizReadSharedSeed(t, repoDir)

		// The dropped secret line is GONE from the deployed config: no value, no
		// var, no placeholder.
		if strings.Contains(seed, wizSecretToken) || strings.Contains(seed, "GITHUB_TOKEN") || strings.Contains(seed, "{{ferry.secret") {
			t.Errorf("AC-secret-store-route[drop]: the dropped secret line (or a placeholder for it) is still in the seed\n%s", seed)
		}
		// The rest of the file is still seeded.
		for _, want := range []string{`export PATH="$HOME/bin:$PATH"`, "alias gs='git status'"} {
			if !strings.Contains(seed, want) {
				t.Errorf("AC-secret-store-route[drop]: non-secret content %q lost from the seed\n%s", want, seed)
			}
		}
		wizAssertNoSecretInRepoDir(t, s, repoDir, wizSecretToken)

		// DROP is not STORE: the value must NOT be in the secret store either.
		if p := findFileContaining(t, wizStoreDir(s), wizSecretToken); p != "" {
			t.Errorf("AC-secret-store-route[drop]: a DROPPED secret was written to the store at %s", p)
		}

		// "dropped lines remain in your backup": the .bak holds the ORIGINAL bytes.
		baks := wizBaks(t, s)
		if len(baks) != 1 {
			t.Fatalf("AC-secret-store-route[drop]: want exactly one .bak, got %v", baks)
		}
		bakData, err := os.ReadFile(baks[0])
		if err != nil {
			t.Fatalf("read .bak: %v", err)
		}
		if string(bakData) != wizSecretKeepZshrc {
			t.Errorf("AC-secret-store-route[drop]: .bak does not preserve the ORIGINAL (dropped line included).\ngot:\n%q\nwant:\n%q", bakData, wizSecretKeepZshrc)
		}
	})

	refuse := func(t *testing.T, route string) {
		t.Helper()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizSecretKeepZshrc, 0o644)
		liveSnap := s.SnapshotFile(t, s.HomePath(".zshrc"))
		answers := wizWriteAnswers(t, `mode = "per-block"
confirm = true

[routes]
"0" = "shared"
"1" = "shared"
"2" = "`+route+`"
`)
		out, errOut, code := s.Ferry("init", "--wizard-answers", answers)
		combined := out + errOut
		if code == 0 {
			t.Errorf("AC-secret-store-route[%s-refused]: routing a SECRET block %q was ACCEPTED (exit 0) — SecretLine routes are only {store, drop}\n%s", route, route, combined)
		}
		if !containsAnyFold(combined, "secret") {
			t.Errorf("AC-secret-store-route[%s-refused]: the refusal does not explain the secret routing conflict\n%s", route, combined)
		}
		wizAssertNothingSeeded(t, s)
		liveSnap.AssertUnchanged(t)
	}
	t.Run("shared_route_refused", func(t *testing.T) { t.Parallel(); refuse(t, "shared") })
	t.Run("local_route_refused", func(t *testing.T) { t.Parallel(); refuse(t, "local") })

	// The plan's answers-file contract: "a missing [secret-routes] entry for a
	// detected secret finding is an error, mirroring the TUI's forced choice" —
	// an eval must never silently get a default route.
	t.Run("missing_secret_routes_refused", func(t *testing.T) {
		t.Parallel()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizSecretKeepZshrc, 0o644)
		liveSnap := s.SnapshotFile(t, s.HomePath(".zshrc"))
		// keep-as-is over a SECRET-BEARING zshrc with NO [secret-routes] table.
		answers := wizWriteAnswers(t, `mode = "keep-as-is"
confirm = true
`)
		out, errOut, code := s.Ferry("init", "--wizard-answers", answers)
		combined := out + errOut
		if code == 0 {
			t.Errorf("AC-secret-store-route[missing-routes]: a detected secret with NO [secret-routes] entry was accepted (exit 0) — the forced choice must be an error, never a silent default\n%s", combined)
		}
		if !containsAnyFold(combined, "secret") || !containsAnyFold(combined, "route", "secret-routes", "unrouted") {
			t.Errorf("AC-secret-store-route[missing-routes]: the error does not name the UNROUTED secret finding\n%s", combined)
		}
		wizAssertNothingSeeded(t, s)
		liveSnap.AssertUnchanged(t)
	})
}

// -----------------------------------------------------------------------------
// AC-wizard-symlink-decline
// -----------------------------------------------------------------------------

// TestWizard_AC_wizard_symlink_decline covers AC-wizard-symlink-decline: "a
// symlinked OR unreadable-regular ~/.zshrc: wizard offers only
// continue-without-managing / abort; no seed is written; init completes with
// .zshrc declared-no-source (v0.2.1 parity); from-scratch is NOT offered (r5-M1)."
//
// INTERPRETATION (Codex-validation point): with a Symlink/Unreadable detection
// the wizard short-circuits BEFORE consuming a mode answer, so init must complete
// (exit 0) with or without an answers file present. For a start-fresh answers
// file the exit code is unpinned (refuse-or-complete are both defensible) — the
// load-bearing gate is that NO starter is seeded and NO .bak is written.
func TestWizard_AC_wizard_symlink_decline(t *testing.T) {
	t.Parallel()

	const unmanagedContent = "# real target content\nalias gs='git status'\n"

	// setupShape stages ~/.zshrc in the given shape and returns a verify func
	// proving the file (and any symlink target) was left untouched.
	setupShape := func(t *testing.T, s *Sandbox, shape string) func(t *testing.T) {
		t.Helper()
		switch shape {
		case "symlink":
			target := s.WriteHomeFile(t, "zshrc-real-target", unmanagedContent, 0o644)
			if err := os.Symlink(target, s.HomePath(".zshrc")); err != nil {
				t.Fatalf("symlink ~/.zshrc: %v", err)
			}
			targetSnap := s.SnapshotFile(t, target)
			return func(t *testing.T) {
				t.Helper()
				targetSnap.AssertUnchanged(t)
				if info, err := os.Lstat(s.HomePath(".zshrc")); err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Errorf("~/.zshrc is no longer a symlink (err %v)", err)
				}
			}
		case "unreadable":
			// r5-M1: a REGULAR file whose read fails must be treated like a
			// symlink (continue-without-managing), never like "absent".
			if os.Geteuid() == 0 {
				t.Skip("running as root: a 0000-mode file is still readable; the Unreadable shape cannot be staged")
			}
			p := s.WriteHomeFile(t, ".zshrc", unmanagedContent, 0o644)
			if err := os.Chmod(p, 0o000); err != nil {
				t.Fatalf("chmod 000 ~/.zshrc: %v", err)
			}
			return func(t *testing.T) {
				t.Helper()
				info, err := os.Lstat(p)
				if err != nil {
					t.Errorf("lstat unreadable ~/.zshrc: %v", err)
					return
				}
				if info.Mode().Perm() != 0 {
					t.Errorf("ferry changed the mode of the unreadable ~/.zshrc (now %o)", info.Mode().Perm())
				}
				// Restore readability to prove the BYTES were untouched.
				if err := os.Chmod(p, 0o644); err != nil {
					t.Fatalf("chmod ~/.zshrc back to 644: %v", err)
				}
				data, err := os.ReadFile(p)
				if err != nil || string(data) != unmanagedContent {
					t.Errorf("unreadable ~/.zshrc content changed (err %v):\n%s", err, data)
				}
			}
		default:
			t.Fatalf("unknown shape %q", shape)
			return nil
		}
	}

	decline := func(t *testing.T, shape string, withAnswers bool) {
		t.Helper()
		wizRequireGit(t)
		s := NewSandbox(t)
		verify := setupShape(t, s, shape)

		args := []string{"init"}
		if withAnswers {
			args = append(args, "--wizard-answers", wizWriteAnswers(t, `mode = "keep-as-is"
confirm = true
`))
		}
		if _, errOut, code := s.Ferry(args...); code != 0 {
			t.Fatalf("AC-wizard-symlink-decline[%s]: init exited %d (must complete, continuing without managing .zshrc)\n%s", shape, code, errOut)
		}
		repoDir := managedRepoPath(s)

		// NO seed written for zshrc anywhere in the repo.
		if p := wizSharedSeedPath(repoDir); p != "" {
			t.Errorf("AC-wizard-symlink-decline[%s]: a zshrc seed was written at %s (must not manage a %s rc)", shape, p, shape)
		}
		// .zshrc still DECLARED (v0.2.1 parity: declared, no source).
		if m := wizManifest(repoDir); !strings.Contains(m, ".zshrc") {
			t.Errorf("AC-wizard-symlink-decline[%s]: ferry.toml does not declare .zshrc\n%s", shape, m)
		}
		// No .bak (nothing adopted, nothing to back up); the rc untouched.
		if baks := wizBaks(t, s); len(baks) != 0 {
			t.Errorf("AC-wizard-symlink-decline[%s]: a .bak was created for an unmanaged rc: %v", shape, baks)
		}
		verify(t)
	}

	t.Run("symlink_with_answers_file", func(t *testing.T) { t.Parallel(); decline(t, "symlink", true) })
	t.Run("symlink_without_answers_file", func(t *testing.T) { t.Parallel(); decline(t, "symlink", false) })
	t.Run("unreadable_regular", func(t *testing.T) { t.Parallel(); decline(t, "unreadable", false) })

	// r5-M1: from-scratch is NOT offered over a Symlink/Unreadable rc — a
	// start-fresh answers file must never produce a starter seed or a .bak,
	// whether init refuses the answers or completes declared-no-source.
	startFreshRefused := func(t *testing.T, shape string) {
		t.Helper()
		wizRequireGit(t)
		s := NewSandbox(t)
		verify := setupShape(t, s, shape)
		answers := wizWriteAnswers(t, `mode = "start-fresh"
confirm = true

[starter]
prompt = "minimal"
`)
		_, _, _ = s.Ferry("init", "--wizard-answers", answers) // exit code unpinned
		if p := wizSharedSeedPath(managedRepoPath(s)); p != "" {
			t.Errorf("AC-wizard-symlink-decline[%s/start-fresh]: a STARTER was seeded at %s over an unmanageable rc (r5-M1: from-scratch must not be offered)", shape, p)
		}
		if baks := wizBaks(t, s); len(baks) != 0 {
			t.Errorf("AC-wizard-symlink-decline[%s/start-fresh]: a .bak was created: %v", shape, baks)
		}
		verify(t)
	}
	t.Run("start_fresh_refused_symlink", func(t *testing.T) { t.Parallel(); startFreshRefused(t, "symlink") })
	t.Run("start_fresh_refused_unreadable", func(t *testing.T) { t.Parallel(); startFreshRefused(t, "unreadable") })
}

// -----------------------------------------------------------------------------
// AC-wizard-from-scratch
// -----------------------------------------------------------------------------

// TestWizard_AC_wizard_from_scratch covers AC-wizard-from-scratch: "no ~/.zshrc +
// starter answers → a valid portable starter seeded (sources ~/.zshrc.local last;
// $HOME not /Users; commented via Description text)."
func TestWizard_AC_wizard_from_scratch(t *testing.T) {
	t.Parallel()
	wizRequireGit(t)
	s := NewSandbox(t)
	// Deliberately NO ~/.zshrc.
	answers := wizWriteAnswers(t, `mode = "start-fresh"
confirm = true

[starter]
prompt = "minimal"
`)
	if _, errOut, code := s.Ferry("init", "--wizard-answers", answers); code != 0 {
		t.Fatalf("AC-wizard-from-scratch: init exited %d\n%s", code, errOut)
	}
	seed := wizReadSharedSeed(t, managedRepoPath(s))

	if !strings.Contains(seed, "$HOME") {
		t.Errorf("AC-wizard-from-scratch: starter does not use $HOME\n%s", seed)
	}
	if strings.Contains(seed, "/Users/") {
		t.Errorf("AC-wizard-from-scratch: starter contains a hardcoded /Users/ path (must be portable)\n%s", seed)
	}
	// Commented via Description text: the starter carries comment lines.
	hasComment := false
	for _, l := range wizNonBlankLines(seed) {
		if strings.HasPrefix(l, "#") {
			hasComment = true
			break
		}
	}
	if !hasComment {
		t.Errorf("AC-wizard-from-scratch: starter has no comment lines (Description text must be woven in)\n%s", seed)
	}
	// Sources ~/.zshrc.local LAST: the final source-bearing line references it.
	var lastSource string
	for _, l := range wizNonBlankLines(seed) {
		if strings.HasPrefix(l, "#") {
			continue
		}
		if strings.Contains(l, "source ") || strings.Contains(l, ". ~/") {
			lastSource = l
		}
	}
	if lastSource == "" || !strings.Contains(lastSource, ".zshrc.local") {
		t.Errorf("AC-wizard-from-scratch: the LAST source line is %q, want it to source ~/.zshrc.local\n%s", lastSource, seed)
	}
}

// -----------------------------------------------------------------------------
// AC-wizard-fresh-over-existing
// -----------------------------------------------------------------------------

// TestWizard_AC_wizard_fresh_over_existing covers AC-wizard-fresh-over-existing:
// "start-fresh chosen OVER an existing substantial zshrc: preview shows the full
// replacement diff; .bak contains the original bytes; first apply replaces
// ~/.zshrc with the starter (first-touch adoption); `ferry restore` returns the
// original."
func TestWizard_AC_wizard_fresh_over_existing(t *testing.T) {
	t.Parallel()
	wizRequireGit(t)
	s := NewSandbox(t)
	s.WriteHomeFile(t, ".zshrc", wizPlainZshrc, 0o644)
	answers := wizWriteAnswers(t, `mode = "start-fresh"
confirm = true

[starter]
prompt = "minimal"
`)
	out, errOut, code := s.Ferry("init", "--wizard-answers", answers)
	if code != 0 {
		t.Fatalf("AC-wizard-fresh-over-existing: init exited %d\n%s", code, errOut)
	}
	repoDir := managedRepoPath(s)
	starter := wizReadSharedSeed(t, repoDir)

	// "preview shows the full replacement diff": the stdout preview must show
	// BOTH sides — a distinctive line of the ORIGINAL rc and the starter content.
	if !strings.Contains(out, "alias gs='git status'") {
		t.Errorf("AC-wizard-fresh-over-existing: preview does not show the ORIGINAL side of the replacement diff (want a distinctive original line)\n%s", out)
	}
	var starterLine string
	for _, l := range wizNonBlankLines(starter) {
		if !strings.HasPrefix(l, "#") {
			starterLine = l
			break
		}
	}
	if starterLine == "" || !strings.Contains(out, starterLine) {
		t.Errorf("AC-wizard-fresh-over-existing: preview does not show the STARTER side of the replacement (want line %q)\n%s", starterLine, out)
	}

	// The visible .bak holds the ORIGINAL bytes, mode 0600.
	baks := wizBaks(t, s)
	if len(baks) != 1 {
		t.Fatalf("AC-wizard-fresh-over-existing: want exactly one ~/.zshrc.ferry-<ts>.bak, got %v", baks)
	}
	bakData, err := os.ReadFile(baks[0])
	if err != nil {
		t.Fatalf("read .bak: %v", err)
	}
	if string(bakData) != wizPlainZshrc {
		t.Errorf("AC-wizard-fresh-over-existing: .bak does not hold the ORIGINAL bytes.\ngot:\n%q\nwant:\n%q", bakData, wizPlainZshrc)
	}
	if info, err := os.Stat(baks[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("AC-wizard-fresh-over-existing: .bak mode = %v, want 0600 (err %v)", info.Mode().Perm(), err)
	}

	// First apply replaces the live file with the starter (risky overwrite of a
	// pre-existing file); confirm the walkthrough.
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("AC-wizard-fresh-over-existing: apply exited %d\n%s", code, errOut)
	}
	live, err := os.ReadFile(s.HomePath(".zshrc"))
	if err != nil {
		t.Fatalf("read live .zshrc: %v", err)
	}
	if string(live) != starter {
		t.Errorf("AC-wizard-fresh-over-existing: after apply live .zshrc != seeded starter.\ngot:\n%q\nwant:\n%q", live, starter)
	}

	// `ferry restore` brings the original back (the engine baseline, not the .bak).
	if _, errOut, code := s.Ferry("restore"); code != 0 {
		t.Fatalf("AC-wizard-fresh-over-existing: restore exited %d\n%s", code, errOut)
	}
	restored, err := os.ReadFile(s.HomePath(".zshrc"))
	if err != nil {
		t.Fatalf("read restored .zshrc: %v", err)
	}
	if string(restored) != wizPlainZshrc {
		t.Errorf("AC-wizard-fresh-over-existing: restore did not return the original.\ngot:\n%q\nwant:\n%q", restored, wizPlainZshrc)
	}
}

// -----------------------------------------------------------------------------
// AC-repair-home-path
// -----------------------------------------------------------------------------

// TestWizard_AC_repair_home_path covers AC-repair-home-path (opt-in): "/Users/<name>
// line → $HOME offered; accept ⇒ replaced; decline ⇒ byte-identical."
//
// INTERPRETATION (Codex-validation point): `--repair` composes with
// `--wizard-answers` (the answers file replaces only the TUI, so the r2-m4 tty
// requirement is satisfied by the data-model path); [repairs] indexes the
// REPAIRABLE findings presented at step 4, 0-based in Analyze order.
func TestWizard_AC_repair_home_path(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, decision string) string {
		t.Helper()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizHomePathZshrc, 0o644)
		answers := wizWriteAnswers(t, `mode = "keep-as-is"
confirm = true

[repairs]
"0" = "`+decision+`"
`)
		if _, errOut, code := s.Ferry("init", "--repair", "--wizard-answers", answers); code != 0 {
			t.Fatalf("AC-repair-home-path[%s]: init --repair exited %d\n%s", decision, code, errOut)
		}
		return wizReadSharedSeed(t, managedRepoPath(s))
	}

	t.Run("accept", func(t *testing.T) {
		t.Parallel()
		seed := run(t, "accept")
		if strings.Contains(seed, "/Users/testuser") {
			t.Errorf("AC-repair-home-path[accept]: seed still contains the hardcoded home path\n%s", seed)
		}
		if !strings.Contains(seed, `alias proj="cd $HOME/projects"`) {
			t.Errorf("AC-repair-home-path[accept]: seed lacks the $HOME-repaired line\n%s", seed)
		}
	})

	t.Run("decline", func(t *testing.T) {
		t.Parallel()
		seed := run(t, "decline")
		if seed != wizHomePathZshrc {
			t.Errorf("AC-repair-home-path[decline]: declined repair must leave the seed BYTE-IDENTICAL.\ngot:\n%q\nwant:\n%q", seed, wizHomePathZshrc)
		}
	})
}

// -----------------------------------------------------------------------------
// AC-secret-store-route + AC-combined-secret-repair
// -----------------------------------------------------------------------------

// TestWizard_AC_secret_store_route_and_combined_secret_repair covers
// AC-secret-store-route ("accept ⇒ Store.Put with the deterministic ref naming +
// placeholder in the shared text; the repo … never contains the secret bytes;
// apply renders via the existing RenderPlaceholders") AND
// AC-combined-secret-repair ("a single block containing BOTH a secret and a
// repairable line: accepting both yields a final block with the placeholder AND
// the $HOME repair (single ApplyRepairs pass, r3-M2); neither edit is lost").
func TestWizard_AC_secret_store_route_and_combined_secret_repair(t *testing.T) {
	t.Parallel()
	wizRequireGit(t)
	s := NewSandbox(t)
	s.WriteHomeFile(t, ".zshrc", wizCombinedZshrc, 0o644)
	answers := wizWriteAnswers(t, `mode = "keep-as-is"
confirm = true

[secret-routes]
"1" = "store"

[repairs]
"0" = "accept"
`)
	if _, errOut, code := s.Ferry("init", "--repair", "--wizard-answers", answers); code != 0 {
		t.Fatalf("AC-combined-secret-repair: init --repair exited %d\n%s", code, errOut)
	}
	repoDir := managedRepoPath(s)
	seed := wizReadSharedSeed(t, repoDir)

	// Both edits present in the final block — neither lost (r3-M2 single writer).
	if !strings.Contains(seed, "export GITHUB_TOKEN="+wizGithubTokenPlaceholder) {
		t.Errorf("AC-secret-store-route: seed lacks the placeholder-swapped token line\n%s", seed)
	}
	if !strings.Contains(seed, `alias proj="cd $HOME/projects"`) {
		t.Errorf("AC-combined-secret-repair: seed lacks the $HOME repair (repair lost to the secret substitution?)\n%s", seed)
	}
	if strings.Contains(seed, "/Users/testuser") {
		t.Errorf("AC-combined-secret-repair: seed still contains the hardcoded home path\n%s", seed)
	}
	wizAssertNoSecretInRepoDir(t, s, repoDir, wizSecretToken)

	// The store holds the value.
	if findFileContaining(t, wizStoreDir(s), wizSecretToken) == "" {
		t.Errorf("AC-secret-store-route: value not found under %s", wizStoreDir(s))
	}

	// Apply renders the placeholder back: the live secret line equals the ORIGINAL
	// adopted bytes; the repaired line carries $HOME; no placeholder literal leaks.
	// Adopting the pre-existing file and deploying a secret-routed seed are risky;
	// confirm the walkthrough.
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("AC-secret-store-route: apply exited %d\n%s", code, errOut)
	}
	live, err := os.ReadFile(s.HomePath(".zshrc"))
	if err != nil {
		t.Fatalf("read live .zshrc: %v", err)
	}
	if !strings.Contains(string(live), wizSecretTokenLine) {
		t.Errorf("AC-secret-store-route: live .zshrc lacks the rendered-back original secret line\n%s", live)
	}
	if strings.Contains(string(live), "{{ferry.secret") {
		t.Errorf("AC-secret-store-route: live .zshrc contains a literal placeholder (render failed)\n%s", live)
	}
	if !strings.Contains(string(live), `alias proj="cd $HOME/projects"`) {
		t.Errorf("AC-combined-secret-repair: live .zshrc lacks the repaired $HOME line\n%s", live)
	}
}

// -----------------------------------------------------------------------------
// AC-repair-machine-local
// -----------------------------------------------------------------------------

// TestWizard_AC_repair_machine_local covers AC-repair-machine-local:
// "machine-specific line routes to the local sidecar seed." Driven through the
// per-block data model (Local is the wizard's SUGGESTED route for a
// machine-specific block; the answers file makes the choice explicit).
func TestWizard_AC_repair_machine_local(t *testing.T) {
	t.Parallel()
	wizRequireGit(t)
	s := NewSandbox(t)
	s.WriteHomeFile(t, ".zshrc", wizThreeBlockZshrc, 0o644)
	answers := wizWriteAnswers(t, `mode = "per-block"
confirm = true

[routes]
"0" = "shared"
"1" = "shared"
"2" = "local"
`)
	if _, errOut, code := s.Ferry("init", "--wizard-answers", answers); code != 0 {
		t.Fatalf("AC-repair-machine-local: init exited %d\n%s", code, errOut)
	}
	repoDir := managedRepoPath(s)

	localData, err := os.ReadFile(wizLocalSeedPath(repoDir))
	if err != nil {
		t.Fatalf("AC-repair-machine-local: local seed missing at %s: %v", wizLocalSeedPath(repoDir), err)
	}
	if !strings.Contains(string(localData), "brew shellenv") {
		t.Errorf("AC-repair-machine-local: machine-specific brew line not in local/zsh/zshrc.local\n%s", localData)
	}
	seed := wizReadSharedSeed(t, repoDir)
	if strings.Contains(seed, "brew shellenv") {
		t.Errorf("AC-repair-machine-local: machine-specific brew line leaked into the SHARED seed\n%s", seed)
	}
	for _, want := range []string{`export PATH="$HOME/bin:$PATH"`, "alias gs='git status'"} {
		if !strings.Contains(seed, want) {
			t.Errorf("AC-repair-machine-local: shared seed missing shared-routed content %q\n%s", want, seed)
		}
	}
}

// -----------------------------------------------------------------------------
// AC-repair-dedupe
// -----------------------------------------------------------------------------

// TestWizard_AC_repair_dedupe covers AC-repair-dedupe: "duplicate PATH export /
// dead `source` flagged; accept ⇒ cleaned; block count and order unchanged
// (emptied, not deleted)."
//
// BYTE-EXACT expectation: accepted repairs EMPTY the duplicate and dead-source
// blocks (a full-block removal sets Raw to "" — separator included, since Raw
// owns it; ApplyRepairs never adds/removes/reorders blocks), so the cleaned seed
// is exactly the two surviving blocks' bytes. A block-DELETING implementation
// that disturbs neighbors fails this.
//
// INTERPRETATION (Codex-validation point): dedupe keeps the FIRST occurrence;
// [repairs] indices are 0-based over the repairable findings in block order
// (duplicate-PATH first, dead-source second).
func TestWizard_AC_repair_dedupe(t *testing.T) {
	t.Parallel()
	wizRequireGit(t)
	s := NewSandbox(t)
	s.WriteHomeFile(t, ".zshrc", wizDedupeZshrc, 0o644)
	answers := wizWriteAnswers(t, `mode = "keep-as-is"
confirm = true

[repairs]
"0" = "accept"
"1" = "accept"
`)
	if _, errOut, code := s.Ferry("init", "--repair", "--wizard-answers", answers); code != 0 {
		t.Fatalf("AC-repair-dedupe: init --repair exited %d\n%s", code, errOut)
	}
	seed := wizReadSharedSeed(t, managedRepoPath(s))

	// Informative pre-checks (better failure messages than the byte compare).
	if n := strings.Count(seed, `export PATH="$HOME/bin:$PATH"`); n != 1 {
		t.Errorf("AC-repair-dedupe: duplicate PATH export appears %d times, want exactly 1\n%s", n, seed)
	}
	if strings.Contains(seed, "nonexistent.zsh") {
		t.Errorf("AC-repair-dedupe: dead `source ~/nonexistent.zsh` line survived the accepted cleanup\n%s", seed)
	}
	// BYTE-EXACT survivors: original bytes minus exactly the two emptied blocks.
	want := wizBlockPath + wizDedupeAliasBlock
	if seed != want {
		t.Errorf("AC-repair-dedupe: cleaned seed is not BYTE-EXACT (emptied blocks must contribute zero bytes, survivors untouched in order).\ngot:\n%q\nwant:\n%q", seed, want)
	}
}

// -----------------------------------------------------------------------------
// AC-repair-noninteractive-refused
// -----------------------------------------------------------------------------

// TestWizard_AC_repair_noninteractive_refused covers AC-repair-noninteractive-refused:
// "`--repair` without a full tty pair, or combined with `--yes`/`--no-wizard`,
// exits non-zero with a clear message naming the conflict; nothing seeded."
// Three arms: --yes, --no-wizard, and PLAIN `init --repair` under the harness's
// non-tty stdin/stdout (no answers file, so no data-model path either).
func TestWizard_AC_repair_noninteractive_refused(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, conflictFlag string) {
		t.Helper()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizHomePathZshrc, 0o644)
		liveSnap := s.SnapshotFile(t, s.HomePath(".zshrc"))

		args := []string{"init", "--repair"}
		if conflictFlag != "" {
			args = append(args, conflictFlag)
		}
		out, errOut, code := s.Ferry(args...)
		combined := out + errOut
		if code == 0 {
			t.Errorf("AC-repair-noninteractive-refused: `ferry %s` exited 0 (must refuse)\n%s", strings.Join(args, " "), combined)
		}
		if conflictFlag != "" {
			// The error names the conflicting flag.
			if !strings.Contains(combined, conflictFlag) {
				t.Errorf("AC-repair-noninteractive-refused: error does not name the conflicting flag %s\n%s", conflictFlag, combined)
			}
		} else {
			// Plain non-tty --repair: the refusal must explain the tty requirement.
			if !containsAnyFold(combined, "tty", "terminal", "interactive") {
				t.Errorf("AC-repair-noninteractive-refused: non-tty refusal does not explain the interactive/tty requirement\n%s", combined)
			}
		}
		if !containsAnyFold(combined, "--repair", "repair") {
			t.Errorf("AC-repair-noninteractive-refused: error does not name --repair\n%s", combined)
		}
		// NOTHING seeded: no repo dir content, no config.toml, no store, no .bak.
		wizAssertNothingSeeded(t, s)
		liveSnap.AssertUnchanged(t)
	}

	t.Run("with_yes", func(t *testing.T) { t.Parallel(); run(t, "--yes") })
	t.Run("with_no_wizard", func(t *testing.T) { t.Parallel(); run(t, "--no-wizard") })
	t.Run("plain_non_tty", func(t *testing.T) { t.Parallel(); run(t, "") })
}

// -----------------------------------------------------------------------------
// AC-wizard-pure-until-confirm
// -----------------------------------------------------------------------------

// TestWizard_AC_wizard_pure_until_confirm covers AC-wizard-pure-until-confirm:
// "before the step-6 confirm there is NO repo dir, no git init, no store entry, no
// .bak, no $HOME change; decline ⇒ clean exit, message truthful." confirm=false in
// the answers file IS the decline at the preview gate.
func TestWizard_AC_wizard_pure_until_confirm(t *testing.T) {
	t.Parallel()
	wizRequireGit(t)
	s := NewSandbox(t)
	s.WriteHomeFile(t, ".zshrc", wizSecretKeepZshrc, 0o644)
	liveSnap := s.SnapshotFile(t, s.HomePath(".zshrc"))
	homeBefore := wizHomeState(t, s)

	answers := wizWriteAnswers(t, `mode = "keep-as-is"
confirm = false

[secret-routes]
"2" = "store"
`)
	out, errOut, code := s.Ferry("init", "--wizard-answers", answers)
	if code != 0 {
		t.Errorf("AC-wizard-pure-until-confirm: declined wizard exited %d (want clean exit 0)\n%s", code, errOut)
	}
	// The message is truthful by construction: "nothing was changed".
	if !containsAnyFold(out+errOut, "nothing was changed", "nothing changed", "no changes were made", "nothing has been changed", "nothing written") {
		t.Errorf("AC-wizard-pure-until-confirm: no 'nothing was changed'-style message on decline\n%s", out+errOut)
	}

	// NOTHING was written: no repo dir, no config.toml, no store entry, no .bak.
	wizAssertNothingSeeded(t, s)
	// No $HOME change AT ALL: the live file untouched and the RECURSIVE HOME diff
	// empty — no allowance for anything, anywhere (decline writes nothing).
	liveSnap.AssertUnchanged(t)
	wizAssertHomeDeltaConfined(t, homeBefore, wizHomeState(t, s), nil, false)
}

// -----------------------------------------------------------------------------
// AC-wizard-backup
// -----------------------------------------------------------------------------

// TestWizard_AC_wizard_backup covers the EVAL-TESTABLE arm of AC-wizard-backup:
// "on confirm over an existing ~/.zshrc, `~/.zshrc.ferry-<ts>.bak` exists with
// the ORIGINAL bytes, mode 0600, created O_EXCL". The AC's remaining arms — the
// pre-planted-SYMLINK abort and the pre-existing-regular-file `-2` suffix — are
// NOT covered here: they are UNIT-PHASE (accounted in the header), because the
// timestamped candidate name cannot be deterministically pre-planted from
// outside the process.
func TestWizard_AC_wizard_backup(t *testing.T) {
	t.Parallel()

	t.Run("original_bytes_0600_regular_file", func(t *testing.T) {
		t.Parallel()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizPlainZshrc, 0o644)
		answers := wizWriteAnswers(t, `mode = "keep-as-is"
confirm = true
`)
		if _, errOut, code := s.Ferry("init", "--wizard-answers", answers); code != 0 {
			t.Fatalf("AC-wizard-backup: init exited %d\n%s", code, errOut)
		}
		baks := wizBaks(t, s)
		if len(baks) != 1 {
			t.Fatalf("AC-wizard-backup: want exactly one ~/.zshrc.ferry-<ts>.bak, got %v", baks)
		}
		// (a) original bytes, 0600.
		data, err := os.ReadFile(baks[0])
		if err != nil {
			t.Fatalf("read .bak: %v", err)
		}
		if string(data) != wizPlainZshrc {
			t.Errorf("AC-wizard-backup: .bak content is not the ORIGINAL bytes.\ngot:\n%q\nwant:\n%q", data, wizPlainZshrc)
		}
		info, err := os.Lstat(baks[0])
		if err != nil {
			t.Fatalf("lstat .bak: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("AC-wizard-backup: .bak mode = %o, want 0600", info.Mode().Perm())
		}
		// (c) the .bak is a NEW regular file — never a symlink (O_EXCL discipline).
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			t.Errorf("AC-wizard-backup: .bak is not a regular file (mode %v)", info.Mode())
		}
	})

	// (b) UNIT-PHASE: the pre-planted-SYMLINK abort arm AND the pre-existing
	// REGULAR-file `-2` suffix collision arm. The .bak name embeds a wall-clock
	// timestamp (`~/.zshrc.ferry-YYYYMMDD-HHMMSS.bak`), so neither a symlink nor a
	// regular file can be deterministically pre-planted at the exact candidate
	// name ferry will choose (pre-planting at all plausible seconds is a race, not
	// a test). The Lstat-refuse-symlink + O_EXCL + `-2`…`-9` suffix contract
	// (F2-7/F3-4) is unit-tested on the backup helper at implementation time.
	t.Run("symlink_and_collision_candidates", func(t *testing.T) {
		t.Skip("UNIT-PHASE: timestamped candidate name not deterministically pre-plantable; symlink-abort AND -2-suffix arms covered by backup-helper unit tests (F2-7/F3-4)")
	})
}

// -----------------------------------------------------------------------------
// AC-preview-masking
// -----------------------------------------------------------------------------

// TestWizard_AC_preview_masking covers AC-preview-masking: "a routed OR DROPPED
// secret line never prints its value in any wizard preview/diff output (both diff
// sides masked with the placeholder); asserted on the data-model path's rendered
// preview text" — the answers-file run prints the rendered preview to stdout
// before seeding. Two arms, per the DROP-LINE MASKING pin: STORE (placeholder in
// the preview AND in the seed) and DROP (the dropped secret still HAS its
// SecretExtraction placeholder — the preview shows the REMOVED line masked by
// it: placeholder in the preview, value on no stream, NOTHING stored).
func TestWizard_AC_preview_masking(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, route string) (*Sandbox, string) {
		t.Helper()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizSecretKeepZshrc, 0o644)
		answers := wizWriteAnswers(t, `mode = "keep-as-is"
confirm = true

[secret-routes]
"2" = "`+route+`"
`)
		out, errOut, code := s.Ferry("init", "--wizard-answers", answers)
		if code != 0 {
			t.Fatalf("AC-preview-masking[%s]: init exited %d\n%s", route, code, errOut)
		}
		// The secret VALUE must never appear on either stream (scrollback is output).
		if strings.Contains(out, wizSecretToken) {
			t.Errorf("AC-preview-masking[%s]: stdout contains the raw secret value\n%s", route, out)
		}
		if strings.Contains(errOut, wizSecretToken) {
			t.Errorf("AC-preview-masking[%s]: stderr contains the raw secret value\n%s", route, errOut)
		}
		// Both routes mask WITH the placeholder, on the SECRET'S OWN LINE: the
		// preview must contain a line carrying BOTH the var name and the
		// placeholder (the masked original-side diff line for drop; the masked
		// seed line for store) — a generic notice that merely mentions the
		// placeholder somewhere cannot pass.
		maskedLine := false
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "GITHUB_TOKEN") && strings.Contains(l, wizGithubTokenPlaceholder) {
				maskedLine = true
				break
			}
		}
		if !maskedLine {
			t.Errorf("AC-preview-masking[%s]: no stdout line shows the secret's line MASKED (want a line containing both GITHUB_TOKEN and %s)\n%s", route, wizGithubTokenPlaceholder, out)
		}
		return s, out
	}

	t.Run("store_route", func(t *testing.T) {
		t.Parallel()
		s, _ := run(t, "store")
		// Store route: the value went to the store (placeholder-in-seed is pinned
		// by AC-wizard-keep-as-is-secrets).
		if findFileContaining(t, wizStoreDir(s), wizSecretToken) == "" {
			t.Errorf("AC-preview-masking[store]: value not in the store under %s", wizStoreDir(s))
		}
	})

	t.Run("drop_route", func(t *testing.T) {
		t.Parallel()
		// DROP-LINE MASKING (pinned): the preview masks the removed line with the
		// extraction's placeholder (asserted in run) — but NOTHING is stored.
		s, _ := run(t, "drop")
		if p := findFileContaining(t, wizStoreDir(s), wizSecretToken); p != "" {
			t.Errorf("AC-preview-masking[drop]: the DROPPED value was written to the store at %s", p)
		}
		if entries := dirEntrySet(wizStoreDir(s)); len(entries) != 0 {
			t.Errorf("AC-preview-masking[drop]: secrets-local is not empty after a drop-only run: %v", entries)
		}
	})
}

// -----------------------------------------------------------------------------
// AC-noninteractive-fallback
// -----------------------------------------------------------------------------

// TestInitFallback_AC_noninteractive_fallback covers AC-noninteractive-fallback:
// "non-tty / --yes / --no-wizard: no TUI, no prompt, no block on stdin.
// Secret-bearing zshrc: keep-everything-shared seed with EVERY secret span
// extracted to the store (placeholders in the seed, nothing dropped, refs listed
// on stderr, stdout unchanged); first apply leaves the live file byte-identical
// (rendered == adopted bytes). Secret-free zshrc: seed BYTE-IDENTICAL to v0.2.1
// initFresh." The harness is inherently non-tty (piped stdin/stdout) and the
// 30s harness timeout makes a hang a hard failure. All three fallback TRIGGERS
// (plain non-tty, --yes, --no-wizard) must behave identically.
func TestInitFallback_AC_noninteractive_fallback(t *testing.T) {
	t.Parallel()

	runSecretBearing := func(t *testing.T, extraArgs ...string) {
		t.Helper()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizFallbackZshrc, 0o644)
		homeBefore := wizHomeState(t, s)

		out, errOut, code := s.Ferry(append([]string{"init"}, extraArgs...)...)
		label := strings.TrimSpace("init " + strings.Join(extraArgs, " "))
		if code != 0 {
			t.Fatalf("AC-noninteractive-fallback: `ferry %s` exited %d\n%s", label, code, errOut)
		}
		repoDir := managedRepoPath(s)
		seed := wizReadSharedSeed(t, repoDir)

		// BOTH secrets extracted: the assignment token via its deterministic ref,
		// the PEM span via exactly ONE positional-ref placeholder line.
		if !strings.Contains(seed, "export GITHUB_TOKEN="+wizGithubTokenPlaceholder) {
			t.Errorf("AC-noninteractive-fallback[%s]: seed lacks the placeholder-swapped token line\n%s", label, seed)
		}
		if n := strings.Count(seed, "{{ferry.secret"); n != 2 {
			t.Errorf("AC-noninteractive-fallback[%s]: seed has %d placeholders, want exactly 2 (token + PEM span)\n%s", label, n, seed)
		}
		if strings.Contains(seed, "BEGIN OPENSSH PRIVATE KEY") || strings.Contains(seed, wizPEMBody) {
			t.Errorf("AC-noninteractive-fallback[%s]: PEM material leaked into the seed\n%s", label, seed)
		}
		// The MACHINE-SPECIFIC line stays SHARED (suggestions are declined in the
		// fallback) — present in the seed, NO local sidecar seed created.
		if !strings.Contains(seed, "brew shellenv") {
			t.Errorf("AC-noninteractive-fallback[%s]: the machine-specific brew line is missing from the SHARED seed (fallback must decline the Local suggestion)\n%s", label, seed)
		}
		if _, err := os.Lstat(wizLocalSeedPath(repoDir)); err == nil {
			t.Errorf("AC-noninteractive-fallback[%s]: a local sidecar seed was created (fallback must not auto-route Local)", label)
		}
		// The hardcoded-home line is BYTE-PRESERVED (repairs are declined).
		if !strings.Contains(seed, `alias proj="cd /Users/testuser/projects"`) {
			t.Errorf("AC-noninteractive-fallback[%s]: the /Users/testuser line was altered (fallback must decline repairs)\n%s", label, seed)
		}
		// BYTE-EXACT modulo the PEM's positional ref: splice the actual PEM
		// placeholder line into the expected text and compare whole files.
		pemLine := ""
		for _, l := range strings.Split(seed, "\n") {
			if strings.Contains(l, "{{ferry.secret") && !strings.Contains(l, "zsh.github_token") {
				pemLine = l
				break
			}
		}
		if pemLine == "" {
			t.Errorf("AC-noninteractive-fallback[%s]: no PEM placeholder line found in the seed\n%s", label, seed)
		} else {
			want := strings.Replace(wizFallbackZshrc, wizSecretToken, wizGithubTokenPlaceholder, 1)
			want = strings.Replace(want, wizPEMSpan, pemLine, 1)
			if seed != want {
				t.Errorf("AC-noninteractive-fallback[%s]: seed is not the whole-file adopt with exactly the two secret spans swapped.\ngot:\n%q\nwant:\n%q", label, seed, want)
			}
		}
		wizAssertNoSecretInRepoDir(t, s, repoDir, wizSecretToken)
		wizAssertNoSecretInRepoDir(t, s, repoDir, wizPEMBody)

		// Store populated with BOTH values; ref names on STDERR ONLY ("stderr
		// NOTICE lists the extracted ref names …; stdout is unchanged"); neither
		// VALUE on any stream.
		if findFileContaining(t, wizStoreDir(s), wizSecretToken) == "" {
			t.Errorf("AC-noninteractive-fallback[%s]: token value not extracted to the store under %s", label, wizStoreDir(s))
		}
		if findFileContaining(t, wizStoreDir(s), wizPEMBody) == "" {
			t.Errorf("AC-noninteractive-fallback[%s]: PEM span not extracted to the store under %s", label, wizStoreDir(s))
		}
		// BOTH extracted ref names on stderr — the token's deterministic ref AND
		// the PEM's positional ref (plan naming: `secret_<line>` under the zsh
		// domain) — and NEITHER on stdout ("stderr notice, stdout unchanged").
		if !strings.Contains(errOut, "zsh.github_token") {
			t.Errorf("AC-noninteractive-fallback[%s]: stderr does not list the token ref zsh.github_token\n%s", label, errOut)
		}
		if !strings.Contains(errOut, "zsh.secret_") {
			t.Errorf("AC-noninteractive-fallback[%s]: stderr does not list the PEM's positional ref (zsh.secret_<line>)\n%s", label, errOut)
		}
		if strings.Contains(out, "zsh.github_token") || strings.Contains(out, "zsh.secret_") {
			t.Errorf("AC-noninteractive-fallback[%s]: a ref notice leaked to STDOUT (plan: stderr notice, stdout unchanged)\n%s", label, out)
		}
		combined := out + errOut
		if strings.Contains(combined, wizSecretToken) || strings.Contains(combined, wizPEMBody) {
			t.Errorf("AC-noninteractive-fallback[%s]: a raw secret value was printed\nstdout:\n%s\nstderr:\n%s", label, out, errOut)
		}

		// RECURSIVE HOME-delta confinement, IDENTICAL for all three trigger arms:
		// ~/.config/ferry/** + the single .bak, NOTHING else. Plain `init --yes`
		// (no --apply) is INIT-ONLY — --yes confirms the closing apply only WHEN
		// --apply is present (cmd/init.go:25), and the plan pins that user
		// dotfiles change only via the transactional apply. An implementation
		// that auto-applies on plain `init --yes` FAILS here.
		wizAssertHomeDeltaConfined(t, homeBefore, wizHomeState(t, s), []string{wizConfigFerryPrefix}, true)
		// Exactly ONE visible backup — allowBak permits backup-SHAPED paths in
		// the delta, so pin the count (Codex eval-r5: multiple or nested
		// backup-shaped files must not pass as "the single .bak").
		if baks := wizBaks(t, s); len(baks) != 1 {
			t.Errorf("AC-noninteractive-fallback[%s]: want exactly 1 visible .bak, got %d (%v)", label, len(baks), baks)
		}

		// FIRST-APPLY ARM: "first apply leaves the live file byte-identical: the
		// seed's placeholders render back to exactly the adopted bytes" — the
		// multi-line PEM span re-expands too.
		if _, applyErr, applyCode := s.Ferry("apply"); applyCode != 0 {
			t.Fatalf("AC-noninteractive-fallback[%s]: first apply exited %d\n%s", label, applyCode, applyErr)
		}
		live, err := os.ReadFile(s.HomePath(".zshrc"))
		if err != nil {
			t.Fatalf("read live .zshrc: %v", err)
		}
		if string(live) != wizFallbackZshrc {
			t.Errorf("AC-noninteractive-fallback[%s]: first apply did not leave ~/.zshrc BYTE-IDENTICAL to the adopted original (placeholders must render back).\ngot:\n%q\nwant:\n%q", label, live, wizFallbackZshrc)
		}
	}

	t.Run("secret_bearing_non_tty", func(t *testing.T) { t.Parallel(); runSecretBearing(t) })
	t.Run("secret_bearing_yes_flag", func(t *testing.T) { t.Parallel(); runSecretBearing(t, "--yes") })
	t.Run("secret_bearing_no_wizard_flag", func(t *testing.T) { t.Parallel(); runSecretBearing(t, "--no-wizard") })

	t.Run("secret_free_byte_identical", func(t *testing.T) {
		t.Parallel()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizPlainZshrc, 0o644)
		if _, errOut, code := s.Ferry("init"); code != 0 {
			t.Fatalf("AC-noninteractive-fallback[secret-free]: init exited %d\n%s", code, errOut)
		}
		seed := wizReadSharedSeed(t, managedRepoPath(s))
		if seed != wizPlainZshrc {
			t.Errorf("AC-noninteractive-fallback[secret-free]: seed not BYTE-IDENTICAL to the live zshrc (v0.2.1 initFresh parity).\ngot:\n%q\nwant:\n%q", seed, wizPlainZshrc)
		}
	})
}

// -----------------------------------------------------------------------------
// AC-github-seedplan
// -----------------------------------------------------------------------------

// TestWizard_AC_github_seedplan covers AC-github-seedplan: "init --github with a
// wizard-routed secret: the pre-commit gate passes (it scans the SeedPlan's
// placeholder-bearing content, not raw ~/.zshrc); the pushed repo contains
// placeholders only. Fallback mode auto-extracts the same way, so non-interactive
// --github with a secret-bearing zshrc SUCCEEDS with placeholders-only pushed."
// Reuses the suite's gh PATH-shim mock + push-recording git stub; no network.
//
// The defense-in-depth arm ("a SeedPlan bug leaking a raw secret still blocks the
// push") is UNIT-PHASE — it needs an injected bug (see header accounting).
func TestWizard_AC_github_seedplan(t *testing.T) {
	t.Parallel()

	// assertPlaceholdersOnlyPush is the shared postcondition of both arms: push
	// recorded, byte-exact SeedPlan seed committed LOCALLY and PRESENT IN THE
	// PUSHED TREE of the real bare remote, value nowhere (local tree, history,
	// objects, pushed objects, output), store populated. Verifying the bare
	// remote's content defeats a push-empty-then-seed-locally implementation.
	assertPlaceholdersOnlyPush := func(t *testing.T, s *Sandbox, git gitStub, bare, combined string) {
		t.Helper()
		if !git.invokedSubcommand("push") {
			t.Errorf("AC-github-seedplan: no git push recorded (the placeholders-only repo must be pushed)\nrecorded: %v", git.lines())
		}
		// SeedPlan identity: the committed shared seed is BYTE-EXACT the content a
		// plain (non---github) init would seed for the identical fixture — the full
		// original zshrc bytes with exactly the secret span swapped for the
		// placeholder, nothing else changed. Both arms route the one secret to the
		// store (fallback auto-route / wizard keep-as-is + store), so both share
		// this expectation — the SeedPlan is the single source of truth for what
		// gets committed AND pushed.
		wantSeed := strings.Replace(wizSecretKeepZshrc, wizSecretToken, wizGithubTokenPlaceholder, 1)
		repoDir := managedRepoPath(s)
		seed := wizReadSharedSeed(t, repoDir)
		if seed != wantSeed {
			t.Errorf("AC-github-seedplan: committed seed is not the BYTE-EXACT SeedPlan content (original bytes with only the secret span swapped).\ngot:\n%q\nwant:\n%q", seed, wantSeed)
		}
		// PUSHED CONTENT (not just a push event): the bare remote's newest tree
		// carries the byte-exact SeedPlan seed; the value is in NO pushed object.
		if pushed := wizPushedSharedSeed(t, bare); pushed != "" && pushed != wantSeed {
			t.Errorf("AC-github-seedplan: the PUSHED dotfiles/zshrc is not the byte-exact SeedPlan seed.\ngot:\n%q\nwant:\n%q", pushed, wantSeed)
		}
		assertNoSecretInGitObjects(t, bare, wizSecretToken)
		wizAssertNoSecretInRepoDir(t, s, repoDir, wizSecretToken)
		assertNoSecretInGitObjects(t, repoDir, wizSecretToken)
		if strings.Contains(combined, wizSecretToken) {
			t.Errorf("AC-github-seedplan: the raw secret value was printed\n%s", combined)
		}
		if findFileContaining(t, wizStoreDir(s), wizSecretToken) == "" {
			t.Errorf("AC-github-seedplan: extracted value not in the store under %s", wizStoreDir(s))
		}
	}

	t.Run("fallback_auto_extract", func(t *testing.T) {
		t.Parallel()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizSecretKeepZshrc, 0o644)

		gh := newGHMock(t)
		git, bare := newPushToBareGitStub(t)
		sc := ghScenario{
			authOK: true, login: ghOwner, repoViewExists: false,
			jsonIsPrivate: true, jsonNameWithOwner: ghOwner + "/wizrepo",
			jsonURL: "https://github.com/" + ghOwner + "/wizrepo.git",
		}
		out, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)), "init", "--github", "wizrepo", "--yes")
		if code != 0 {
			t.Fatalf("AC-github-seedplan[fallback]: non-interactive `init --github` with a secret-bearing zshrc exited %d (the SeedPlan gate must pass on placeholder content)\n%s", code, out+errOut)
		}
		assertPlaceholdersOnlyPush(t, s, git, bare, out+errOut)
	})

	t.Run("wizard_routed", func(t *testing.T) {
		t.Parallel()
		wizRequireGit(t)
		s := NewSandbox(t)
		s.WriteHomeFile(t, ".zshrc", wizSecretKeepZshrc, 0o644)

		gh := newGHMock(t)
		git, bare := newPushToBareGitStub(t)
		sc := ghScenario{
			authOK: true, login: ghOwner, repoViewExists: false,
			jsonIsPrivate: true, jsonNameWithOwner: ghOwner + "/wizrepo",
			jsonURL: "https://github.com/" + ghOwner + "/wizrepo.git",
		}
		answers := wizWriteAnswers(t, `mode = "keep-as-is"
confirm = true

[secret-routes]
"2" = "store"
`)
		// FLAG PRECEDENCE (pinned): --wizard-answers outranks --yes's wizard-skip
		// meaning — the answers file drives the WIZARD-ROUTED path — while --yes
		// keeps its create-confirm/apply-confirm assent, so the whole run is
		// non-interactive (EMPTY stdin; a `--github` run without --yes still
		// refuses non-interactively, pinned by the existing github evals).
		out, errOut, code := s.FerryEnv(append(sc.env(), combinedPathEnv(gh, git)),
			"init", "--github", "wizrepo", "--wizard-answers", answers, "--yes")
		if code != 0 {
			t.Fatalf("AC-github-seedplan[wizard-routed]: `init --github --wizard-answers --yes` exited %d\n%s", code, out+errOut)
		}
		assertPlaceholdersOnlyPush(t, s, git, bare, out+errOut)
	})
}

// -----------------------------------------------------------------------------
// AC-init-rerun-guard
// -----------------------------------------------------------------------------

// TestWizard_AC_init_rerun_guard covers AC-init-rerun-guard: "re-running init
// respects existing semantics (no duplicate sidecar source line —
// `sourceDirectivePresent`; wizard only on the fresh path)." Two observables:
// (1) a second `ferry init` on the now-CONFIGURED machine does NOT re-enter the
// wizard — no new .bak, seeds and live file untouched (exit code unpinned: no
// doc fixes re-init's exit, so the suite pins the no-mutation observables);
// (2) a second apply does not append a second sidecar source directive.
func TestWizard_AC_init_rerun_guard(t *testing.T) {
	t.Parallel()
	wizRequireGit(t)
	s := NewSandbox(t)
	s.WriteHomeFile(t, ".zshrc", wizThreeBlockZshrc, 0o644)
	answers := wizWriteAnswers(t, `mode = "per-block"
confirm = true

[routes]
"0" = "shared"
"1" = "shared"
"2" = "local"
`)
	if _, errOut, code := s.Ferry("init", "--wizard-answers", answers); code != 0 {
		t.Fatalf("AC-init-rerun-guard: init exited %d\n%s", code, errOut)
	}
	// Adopting the pre-existing ~/.zshrc is risky; confirm the walkthrough.
	if _, errOut, code := s.ApplyConfirmed(); code != 0 {
		t.Fatalf("AC-init-rerun-guard: first apply exited %d\n%s", code, errOut)
	}
	repoDir := managedRepoPath(s)
	sharedPath := wizSharedSeedPath(repoDir)
	if sharedPath == "" {
		t.Fatalf("AC-init-rerun-guard: no shared seed after init")
	}

	// RE-RUN init on the configured machine: wizard only on the FRESH path.
	baksBefore := len(wizBaks(t, s))
	sharedSnap := s.SnapshotFile(t, sharedPath)
	localSnap := s.SnapshotFile(t, wizLocalSeedPath(repoDir))
	liveSnap := s.SnapshotFile(t, s.HomePath(".zshrc"))
	s.Ferry("init") // empty stdin; exit code unpinned — observables below are the contract
	if got := len(wizBaks(t, s)); got != baksBefore {
		t.Errorf("AC-init-rerun-guard: re-running init changed the .bak count (%d -> %d) — the wizard re-entered on a configured machine", baksBefore, got)
	}
	sharedSnap.AssertUnchanged(t)
	localSnap.AssertUnchanged(t)
	liveSnap.AssertUnchanged(t)

	// Second apply: still exactly ONE sidecar source directive in the live file.
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-init-rerun-guard: second apply exited %d\n%s", code, errOut)
	}
	live, err := os.ReadFile(s.HomePath(".zshrc"))
	if err != nil {
		t.Fatalf("read live .zshrc: %v", err)
	}
	if n := strings.Count(string(live), "source ~/.zshrc.local"); n != 1 {
		t.Errorf("AC-init-rerun-guard: live .zshrc carries %d `source ~/.zshrc.local` directives after re-apply, want exactly 1\n%s", n, live)
	}
}

// -----------------------------------------------------------------------------
// Test-strategy rider — read-only commands never create the state dir
// -----------------------------------------------------------------------------

// TestReadOnly_NoStateDirCreate is the PLAN "Test strategy" rider (from
// .work/issues.md 2026-06-30 eval-gap): "read-only commands (`status`, `diff`) do
// NOT create `~/.local/state/ferry` when it is absent" — the read-only-store fix
// landed without a gating eval; this is that tripwire.
func TestReadOnly_NoStateDirCreate(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# baseline\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# baseline\n")
	s.WriteHomeFile(t, ".zshrc", "# baseline\n", 0o644)

	// Remove the state dir the sandbox pre-created; read-only commands must not
	// bring it back.
	if err := os.RemoveAll(s.StateDir()); err != nil {
		t.Fatalf("remove state dir: %v", err)
	}

	s.Ferry("status")
	if _, err := os.Stat(s.StateDir()); err == nil {
		t.Errorf("read-only tripwire: `ferry status` CREATED %s (read-only commands must not write state)", s.StateDir())
	}
	s.Ferry("diff")
	if _, err := os.Stat(s.StateDir()); err == nil {
		t.Errorf("read-only tripwire: `ferry diff` CREATED %s (read-only commands must not write state)", s.StateDir())
	}
}
