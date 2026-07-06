package evals

// v0.2.1 data-loss SAFETY evals (empty-seed -> apply wipe).
//
// These drive the REAL binary through the harness and are RED-until-impl: with
// FERRY_BIN unset, s.Ferry* calls requireBin -> t.Skip, so the whole file skips
// cleanly (compiles, no failures) until the fix lands. Each eval asserts a
// user-observable outcome (file bytes/mode/mtime, exit code, stderr wording),
// never a source proxy.
//
// Coverage:
//   - AC-init-adopts-existing-zshrc: fresh init WITH a substantial ~/.zshrc adopts
//     it (repo source carries the content) AND the following apply is an exit-0
//     no-op (the confirmed-incident scenario).
//   - AC-init-no-empty-deployable: fresh init with NO ~/.zshrc does not stage a
//     deployable empty source that a later apply zeroes an existing file with.
//   - AC-apply-refuses-empty-over-substantial: apply refuses to zero a substantial
//     live file with an empty/near-empty repo source (general — zsh AND generic),
//     naming the file and the hazard; live file byte/mode/mtime unchanged.
//   - AC-apply-force-allows-with-warning: --force proceeds (live == repo source)
//     but warns, naming the file; then restore reverts.
//   - AC-restore-still-works: reversibility intact across the adopt, refuse, and
//     force paths.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// substantialZshrc is a realistic ~/.zshrc: 40+ lines of aliases/exports/functions.
// Entirely fake — no secrets, no real paths, no PII.
const substantialZshrc = `# ~/.zshrc — shell configuration
export EDITOR=vim
export PAGER=less
export LANG=en_US.UTF-8
export HISTSIZE=10000
export SAVEHIST=10000
export HISTFILE=~/.zsh_history

# PATH additions
export PATH="$HOME/.local/bin:$PATH"
export PATH="$HOME/bin:$PATH"

# aliases
alias ll='ls -lah'
alias la='ls -A'
alias l='ls -CF'
alias gs='git status'
alias gd='git diff'
alias gc='git commit'
alias gp='git push'
alias gl='git log --oneline --graph'
alias ..='cd ..'
alias ...='cd ../..'
alias grep='grep --color=auto'
alias df='df -h'
alias du='du -h'

# options
setopt AUTO_CD
setopt HIST_IGNORE_DUPS
setopt SHARE_HISTORY
setopt INTERACTIVE_COMMENTS

# prompt
autoload -Uz vcs_info
PROMPT='%n@%m %1~ %# '

# functions
mkcd() { mkdir -p "$1" && cd "$1"; }
extract() { case "$1" in *.tar.gz) tar xzf "$1";; *) echo "unknown";; esac; }

# completion
autoload -Uz compinit && compinit
`

// TestInitAdoptsExistingZshrc covers AC-init-adopts-existing-zshrc: a fresh
// `ferry init` on a machine that ALREADY has a substantial ~/.zshrc must ADOPT
// that content into the repo source, so the immediately-following `ferry apply`
// is a NO-OP — never a wipe. This is the exact confirmed-incident flow: without
// the fix, init seeds an EMPTY dotfiles/zshrc and apply zeroes ~/.zshrc.
func TestInitAdoptsExistingZshrc_AC_init_adopts_existing_zshrc(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)

	// A substantial, realistic pre-existing ~/.zshrc (the live file at risk).
	zshrc := s.WriteHomeFile(t, ".zshrc", substantialZshrc, 0o644)
	snap := s.SnapshotFile(t, zshrc)

	// Bare fresh init (no clone URL): the Fresh path that seeds a new repo.
	if _, errOut, code := s.FerryWithInput("", "init"); code != 0 {
		t.Fatalf("AC-init-adopts-existing-zshrc: fresh `ferry init` exited %d\n%s", code, errOut)
	}

	// ADOPTION must be provable, not best-effort: init must have written config.toml
	// with a repo path, and the repo's managed zsh source must exist, be non-empty,
	// and carry the adopted content. A repo that cannot be located, or an EMPTY zsh
	// source, is the data-loss bug and must FAIL here (not silently pass).
	repo := recordedRepoPath(t, s)
	if repo == "" {
		t.Fatalf("AC-init-adopts-existing-zshrc: fresh init recorded no repo path in config.toml — cannot verify adoption")
	}
	src, ok := findRepoZshSource(repo)
	if !ok {
		t.Fatalf("AC-init-adopts-existing-zshrc: no managed zsh source under %q after init — nothing was adopted", repo)
	}
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("AC-init-adopts-existing-zshrc: read repo zsh source %q: %v", src, err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		t.Errorf("AC-init-adopts-existing-zshrc: repo zsh source %q is EMPTY after init — the empty-seed data-loss bug is present", src)
	}
	// ADOPTION means the repo source carries the EXISTING ~/.zshrc content, not just
	// one line. Assert the FULL seeded body is present (allowing ferry to append its
	// own deterministic include/sidecar directive — the AC permits that composition,
	// so we require the seeded content as a substring, not byte-exact equality).
	if !strings.Contains(string(body), strings.TrimRight(substantialZshrc, "\n")) {
		missing := firstMissingLine(string(body), substantialZshrc)
		t.Errorf("AC-init-adopts-existing-zshrc: repo zsh source %q did not adopt the FULL existing ~/.zshrc content (first missing line: %q)", src, missing)
	}

	// The immediate apply must be an EXIT-0 NO-OP: adoption means repo == live, so
	// apply writes nothing. Byte-, mode-, and mtime-unchanged (tripwire).
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Errorf("AC-init-adopts-existing-zshrc: apply after adopting init exited %d (adoption should make it a clean no-op)\n%s", code, errOut)
	}
	snap.AssertUnchanged(t)
}

// TestInitNoEmptyDeployable covers AC-init-no-empty-deployable: a fresh `ferry
// init` on a machine with NO ~/.zshrc must not stage a state where a later `apply`
// writes an empty file over existing content. We run the init-with-no-zshrc path,
// then INDEPENDENTLY plant a substantial ~/.zshrc and run apply — asserting the
// init-produced repo state does not zero it. (If init seeded an empty deployable
// source, apply would wipe the planted file; the fix must adopt/refuse/inert it.)
func TestInitNoEmptyDeployable_AC_init_no_empty_deployable(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)

	// Deliberately NO ~/.zshrc exists at init time.
	if _, err := os.Stat(s.HomePath(".zshrc")); err == nil {
		t.Fatalf("precondition: ~/.zshrc should be absent before init")
	}

	if _, errOut, code := s.FerryWithInput("", "init"); code != 0 {
		t.Fatalf("AC-init-no-empty-deployable: fresh init (no ~/.zshrc) exited %d\n%s", code, errOut)
	}

	// Inspect the init-produced repo: if it seeded a managed zsh source at all, that
	// source must NOT be an empty/near-empty deployable payload — an empty seed is
	// exactly what deploys destructively over a real file. Absence of a source (or a
	// clearly-inert non-empty placeholder) is acceptable.
	repo := recordedRepoPath(t, s)
	if repo != "" {
		if src, ok := findRepoZshSource(repo); ok {
			body, _ := os.ReadFile(src)
			if len(strings.TrimSpace(string(body))) == 0 {
				t.Errorf("AC-init-no-empty-deployable: init seeded an EMPTY managed zsh source %q — a later apply would zero any real ~/.zshrc", src)
			}
		}
	}

	// Decisive behavioral check: now a substantial ~/.zshrc appears (e.g. the user
	// creates one before their first apply). A default apply against the
	// init-produced repo state must NOT wipe it.
	target := s.WriteHomeFile(t, ".zshrc", substantialZshrc, 0o644)
	snap := s.SnapshotFile(t, target)
	s.Ferry("apply") // exit code is not the gate; the tripwire is.
	snap.AssertUnchanged(t)
}

// TestApplyRefusesEmptyOverSubstantial covers AC-apply-refuses-empty-over-substantial:
// for ANY managed dotfile, a default `apply` (no --force) must REFUSE to overwrite a
// substantial live file with an EMPTY/near-empty repo source, leaving the live file
// byte-for-byte untouched. Proven for zsh AND a generic (non-zsh) dotfile so the
// guard is shown to be general, not zshrc-special-cased. Also cross-checks
// AC-restore-still-works: a refused apply leaves the file already at its baseline.
func TestApplyRefusesEmptyOverSubstantial_AC_apply_refuses_empty_over_substantial(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string // subtest label
		dotfile  string // manifest/home name, e.g. ".zshrc"
		bare     string // bare name used in the repo dotfiles/ path, e.g. "zshrc"
		emptySrc string // the near-empty repo content to seed
		live     string // substantial live content to protect
	}{
		{
			name:     "zsh",
			dotfile:  ".zshrc",
			bare:     "zshrc",
			emptySrc: "", // the exact incident: a truly empty seed
			live:     substantialZshrc,
		},
		{
			name:     "generic-gitconfig",
			dotfile:  ".gitconfig",
			bare:     "gitconfig",
			emptySrc: "\n   \n# only a comment\n", // whitespace/comment-only = near-empty
			live:     "[user]\n\tname = Real User\n\temail = user@example.com\n[core]\n\teditor = vim\n[alias]\n\tst = status\n\tco = checkout\n",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			s.SeedSharedManifest(t, "[manage]\ndotfiles = [\""+tc.dotfile+"\"]\n")

			// Seed the near-empty repo source in the canonical + dotted layouts so the
			// test is not brittle to the repo convention.
			s.WriteRepoFile(t, filepath.Join("dotfiles", tc.bare), tc.emptySrc)
			s.WriteRepoFile(t, filepath.Join("dotfiles", tc.dotfile), tc.emptySrc)

			// The substantial live file that must NOT be zeroed.
			target := s.WriteHomeFile(t, tc.dotfile, tc.live, 0o644)
			snap := s.SnapshotFile(t, target)

			// Confirm the guided walkthrough (this adoption of a pre-existing file is
			// risky) so the flow reaches the empty-over-substantial guard, which must
			// then refuse the destructive empty overwrite with its specific message.
			out, errOut, code := s.ApplyConfirmed()
			combined := out + errOut

			// REFUSAL must be OBSERVABLE and SPECIFIC. The message must name the file
			// AND explain the empty-over-substantial hazard — BOTH sides of it: that the
			// repo source is empty/blank AND that this would destroy/replace real
			// content. A bare "skip", a lone non-zero exit with no explanation, or a
			// message that names only one side is NOT user-honest and does not pass.
			namesFile := containsAnyFold(combined, tc.dotfile, tc.bare)
			namesEmptySide := containsAnyFold(combined, "empty", "blank")
			namesDestructiveSide := containsAnyFold(combined, "erase", "wipe", "zero", "delete", "destroy", "substantial", "overwrit", "clobber", "existing content")
			hazardFullyNamed := namesFile && namesEmptySide && namesDestructiveSide
			if !hazardFullyNamed {
				t.Errorf("AC-apply-refuses-empty-over-substantial[%s]: refusal message did not name the file AND both sides of the empty-over-substantial hazard (empty source + destructive overwrite)\nexit=%d\n%s", tc.name, code, combined)
			}

			// The load-bearing safety assertion: the live file is byte-, mode-, and
			// mtime-identical (a same-bytes rewrite is also a failure).
			snap.AssertUnchanged(t)

			// Regression guard (AC-restore-still-works, refuse path): nothing was
			// written, so the file already matches its pre-ferry baseline and a
			// restore must exit cleanly and leave it intact.
			if _, rErr, rCode := s.Ferry("restore"); rCode != 0 {
				t.Errorf("AC-restore-still-works[refuse/%s]: restore exited %d after a refused apply (should revert cleanly)\n%s", tc.name, rCode, rErr)
			}
			snap.AssertUnchanged(t)
		})
	}
}

// TestApplyForceAllowsWithWarning covers AC-apply-force-allows-with-warning: with
// --force, apply may proceed with the empty-over-substantial overwrite, but MUST
// warn (naming the file / the destructive replacement). A --force path that
// silently zeroes the file with no warning must not pass. Reversibility is checked
// inline (cross-checks AC-restore-still-works, force path).
func TestApplyForceAllowsWithWarning_AC_apply_force_allows_with_warning(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)

	// Empty repo source over a substantial live ~/.zshrc.
	emptySrc := ""
	s.WriteRepoFile(t, filepath.Join("dotfiles", "zshrc"), emptySrc)
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), emptySrc)
	target := s.WriteHomeFile(t, ".zshrc", substantialZshrc, 0o644)
	preSnap := s.SnapshotFile(t, target)

	out, errOut, code := s.Ferry("apply", "--force")
	combined := out + errOut
	if code != 0 {
		t.Fatalf("AC-apply-force-allows-with-warning: apply --force exited %d (force should proceed)\n%s", code, errOut)
	}

	// The overwrite proceeded with DOCUMENTED --force semantics: the live file now
	// equals the (empty) repo source that was deployed. Asserting BYTE-EXACT equality
	// — not a trimmed compare, not merely "the old content is gone" — pins the real
	// outcome (a whitespace-only file would NOT equal an exactly-empty source).
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target after --force: %v", err)
	}
	if string(got) != emptySrc {
		t.Errorf("AC-apply-force-allows-with-warning: --force did not deploy the empty repo source byte-for-byte; live file is %d bytes (%q…)", len(got), truncate(string(got), 60))
	}

	// The WARNING is required and must be SPECIFIC — mirroring the refusal shape: it
	// must NAME the file AND flag BOTH sides of the empty-over-substantial hazard (the
	// source is empty/blank AND a substantial/real file is being replaced/destroyed).
	// A generic "overwriting .zshrc" that omits the empty-file danger, or a message
	// that flags only one side, is NOT enough; silently zeroing the file is a hard fail.
	warnNamesFile := containsAnyFold(combined, ".zshrc", "zshrc")
	warnEmptySide := containsAnyFold(combined, "empty", "blank")
	warnDestructiveSide := containsAnyFold(combined, "erase", "wipe", "zero", "delete", "destroy", "substantial", "overwrit", "clobber", "existing content")
	if !(warnNamesFile && warnEmptySide && warnDestructiveSide) {
		t.Errorf("AC-apply-force-allows-with-warning: --force warning did not name the file AND flag both sides of the empty-over-substantial hazard (empty source + destructive replacement)\n%s", combined)
	}

	// Reversibility (AC-restore-still-works, force path): the pre-force content was
	// backed up, so restore returns it byte-, mode-, and mtime-identically.
	if _, errOut, code := s.Ferry("restore"); code != 0 {
		t.Fatalf("AC-apply-force-allows-with-warning: restore exited %d\n%s", code, errOut)
	}
	preSnap.AssertUnchanged(t)
}

// TestRestoreStillWorksAfterAdoptInit covers AC-restore-still-works for the
// adopting-init path: after a fresh init that adopts a substantial ~/.zshrc and a
// no-op apply, the file already matches its pre-ferry baseline, and a `restore`
// exits cleanly without disturbing it. Guards the fix against a new leak on the
// non-force paths. (The refuse and force restore paths are covered inline in the
// two tests above.)
func TestRestoreStillWorksAfterAdoptInit_AC_restore_still_works(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)

	zshrc := s.WriteHomeFile(t, ".zshrc", substantialZshrc, 0o644)
	snap := s.SnapshotFile(t, zshrc)

	if _, errOut, code := s.FerryWithInput("", "init"); code != 0 {
		t.Fatalf("AC-restore-still-works: fresh init exited %d\n%s", code, errOut)
	}
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Errorf("AC-restore-still-works: apply after adopting init exited %d (should be a clean no-op)\n%s", code, errOut)
	}
	// Adopt path: apply was a no-op, live == pre-ferry baseline.
	snap.AssertUnchanged(t)

	// restore must exit cleanly and not disturb the baseline it already matches.
	if _, errOut, code := s.Ferry("restore"); code != 0 {
		t.Errorf("AC-restore-still-works: restore exited %d on an adopted baseline (should revert cleanly)\n%s", code, errOut)
	}
	snap.AssertUnchanged(t)
}

// recordedRepoPath reads config.toml (if init wrote one) and returns the recorded
// repo path, or "" when it cannot be determined.
func recordedRepoPath(t *testing.T, s *Sandbox) string {
	t.Helper()
	data, err := os.ReadFile(s.ConfigTOMLPath())
	if err != nil {
		return ""
	}
	return extractRepoPath(string(data))
}

// findRepoZshSource returns the repo-side managed zsh source, probing the layouts
// ferry uses (dotfiles/zshrc, dotfiles/.zshrc). Returns ok=false if none exists.
func findRepoZshSource(repo string) (string, bool) {
	for _, cand := range []string{
		filepath.Join(repo, "dotfiles", "zshrc"),
		filepath.Join(repo, "dotfiles", ".zshrc"),
	} {
		if info, err := os.Stat(cand); err == nil && info.Mode().IsRegular() {
			return cand, true
		}
	}
	return "", false
}

// truncate shortens s for error messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// firstMissingLine returns the first non-blank line of want that does not appear in
// got — used to report precisely which adopted line is absent from a repo source.
func firstMissingLine(got, want string) string {
	for _, line := range strings.Split(want, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(got, line) {
			return line
		}
	}
	return "(all lines present)"
}
