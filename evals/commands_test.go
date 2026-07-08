package evals

// Command-surface ACs: every documented subcommand exists, --help mentions the
// documented behaviour, and documented flags parse. Source is never inspected;
// these drive the real binary's --help and exit codes.

import (
	"strings"
	"testing"
)

// documentedCommands is the exact set from the README "Commands" table
// (AC-cmd-set-complete).
var documentedCommands = []string{
	"init", "apply", "capture", "status", "doctor", "diff", "restore", "bundle",
}

// TestTopLevelHelpListsAllCommands covers AC-cmd-set-complete: `ferry --help`
// advertises all seven documented subcommands AS LISTED COMMANDS (not incidental
// prose substrings like "init" inside a sentence).
func TestTopLevelHelpListsAllCommands_AC_cmd_set_complete(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	out, errOut, code := s.Ferry("--help")
	if code != 0 {
		t.Fatalf("ferry --help exited %d; stderr:\n%s", code, errOut)
	}
	help := out + errOut
	for _, cmd := range documentedCommands {
		if !commandListedInHelp(help, cmd) {
			t.Errorf("AC-cmd-set-complete: top-level help does not LIST command %q as a subcommand (incidental prose does not count)\n%s", cmd, help)
		}
	}
}

// commandListedInHelp reports whether cmd appears as a LISTED subcommand in help
// output — i.e. as a whole-word token at the START of a (possibly indented) line,
// which is how cobra and conventional CLIs render a command list:
//
//	Available Commands:
//	  apply   Reconcile this machine to the repo
//
// This avoids matching incidental prose ("ferry init writes …") elsewhere.
func commandListedInHelp(help, cmd string) bool {
	for _, raw := range strings.Split(help, "\n") {
		line := strings.TrimLeft(raw, " \t")
		// The command must be the first whitespace-delimited token on the line,
		// followed by whitespace/end (so "applyx" or "apply-foo" don't match).
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if strings.EqualFold(fields[0], cmd) {
			// Require a description column OR a bare listing line — either way the
			// command is the leading token of a list entry, not mid-sentence.
			return true
		}
	}
	return false
}

// TestEachCommandHelpExits0 covers the GATING half of AC-cmd-init/apply/capture/
// status/doctor/diff/restore: each documented command EXISTS AND RUNS — its
// `<cmd> --help` resolves and exits 0, AND it appears in top-level `ferry --help`.
// (Round-3: help-text *wording* is non-gating; "exists and runs" is the gate.)
func TestEachCommandHelpExits0_AC_cmd_existence(t *testing.T) {
	t.Parallel()
	for _, cmd := range documentedCommands {
		cmd := cmd
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			// Gating: subcommand --help resolves and exits 0.
			_, errOut, code := s.Ferry(cmd, "--help")
			if code != 0 {
				t.Errorf("AC-cmd-%s: `ferry %s --help` exited %d (subcommand missing?); stderr:\n%s",
					cmd, cmd, code, errOut)
			}
			// Gating: the command is advertised AS A LISTED COMMAND in top-level help.
			topOut, topErr, topCode := s.Ferry("--help")
			if topCode != 0 {
				t.Fatalf("AC-cmd-%s: `ferry --help` exited %d; stderr:\n%s", cmd, topCode, topErr)
			}
			if !commandListedInHelp(topOut+topErr, cmd) {
				t.Errorf("AC-cmd-%s: command %q is not LISTED in `ferry --help` (incidental prose does not count)", cmd, cmd)
			}
		})
	}
}

// helpMention maps each command to phrases its --help must mention, per the AC
// "How to verify" notes. We use OR-groups (containsAnyFold) so we assert the
// documented *concept* without over-fitting exact wording.
type helpExpect struct {
	cmd    string
	ac     string
	groups [][]string // each inner slice is an OR-group; all groups must match
}

var helpExpectations = []helpExpect{
	// AC-cmd-init: locate/clone repo into ferry's own space, write config.
	{"init", "AC-cmd-init", [][]string{
		{"clone", "repo", "config repo"},
		{"config", "ferry's config"},
	}},
	// AC-cmd-apply: reconcile machine to repo, deploy dotfiles/terminal settings.
	{"apply", "AC-cmd-apply", [][]string{
		{"reconcile", "deploy"},
		{"dotfile", "dotfiles"},
	}},
	// AC-cmd-capture: pull local changes, interactive per-change approval, shared/local.
	{"capture", "AC-cmd-capture", [][]string{
		{"approve", "review", "interactive"},
		{"shared"},
		{"local"},
	}},
	// AC-cmd-status: report config drift.
	{"status", "AC-cmd-status", [][]string{
		{"drift", "changed", "what changed"},
	}},
	// AC-cmd-doctor: report machine/tool health.
	{"doctor", "AC-cmd-doctor", [][]string{
		{"health", "report"},
	}},
	// AC-cmd-diff: preview what apply would change.
	{"diff", "AC-cmd-diff", [][]string{
		{"preview", "would change", "what will change"},
	}},
	// AC-cmd-restore: reverse ferry's changes from an automatic backup.
	{"restore", "AC-cmd-restore", [][]string{
		{"restore", "reverse", "pre-ferry", "undo"},
		{"backup"},
	}},
}

// TestCommandHelpMentionsDocumentedBehaviour is NON-GATING (round-3 overreach
// fix, ACCEPTANCE.md "Help-text gating demoted to non-gating"). The docs promise
// BEHAVIOR, not help-text content, and the behavior is gated by the behavioral
// ACs. Missing wording is logged as a soft signal, NEVER a failure.
func TestCommandHelpMentionsDocumentedBehaviour(t *testing.T) {
	t.Parallel()
	for _, exp := range helpExpectations {
		exp := exp
		t.Run(exp.cmd, func(t *testing.T) {
			t.Parallel()
			s := NewSandbox(t)
			out, errOut, code := s.Ferry(exp.cmd, "--help")
			if code != 0 {
				t.Fatalf("%s: `ferry %s --help` exited %d; stderr:\n%s", exp.ac, exp.cmd, code, errOut)
			}
			help := out + errOut
			for _, group := range exp.groups {
				if !containsAnyFold(help, group...) {
					// NON-GATING: soft signal only — do not fail the gate on wording.
					t.Logf("%s (non-gating help-text wording): `ferry %s --help` does not mention any of %v",
						exp.ac, exp.cmd, group)
				}
			}
		})
	}
}

// TestDiffNoWrite covers the GATING BEHAVIORAL half of AC-cmd-diff: "running
// `diff` itself never writes managed files" (ACCEPTANCE.md AC-cmd-diff). The
// command-existence half is covered by TestEachCommandHelpExits0; this gates the
// no-write behavior UNDER AC-cmd-diff (the preview-predicts-reality half is
// AC-diff-preview-only in status_diff_test.go).
func TestDiffNoWrite_AC_cmd_diff(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	// A managed dotfile that `apply` WOULD deploy — its target is the tripwire.
	s.WriteRepoFile(t, ".zshrc", "# would be deployed\n")
	s.WriteRepoFile(t, "dotfiles/.zshrc", "# would be deployed\n")

	target := s.HomePath(".zshrc")
	tw := s.SnapshotFile(t, target) // absent

	out, errOut, code := s.Ferry("diff")
	if code != 0 {
		t.Fatalf("AC-cmd-diff: `ferry diff` exited %d; stderr:\n%s", code, errOut)
	}
	_ = out
	// GATING: diff must NOT have written the managed target (still absent, no change).
	tw.AssertUnchanged(t)
}

// TestApplyDepsFlagParses covers AC-cmd-apply-deps-flag. GATING: `--deps` is a
// real recognised flag (resolves) and an unknown flag is rejected. The behavior
// behind --deps is gated by AC-deps-install-attempted / AC-deps-not-during-default-apply.
// Help-text documentation of --deps is NON-GATING (soft log only).
func TestApplyDepsFlagParses_AC_cmd_apply_deps_flag(t *testing.T) {
	t.Parallel()

	// GATING: a bogus flag must be rejected AS AN UNKNOWN/UNRECOGNIZED FLAG — not
	// merely fail later for unrelated reasons. Require BOTH non-zero AND an
	// unknown-flag signal that NAMES the offending flag, so a CLI that silently
	// ignores unknown flags (and fails downstream) does not pass.
	bogusFlag := "--ferry-bogus-flag-xyz"
	s2 := NewSandbox(t)
	bogusOut, bogusErr, badCode := s2.Ferry("apply", bogusFlag)
	bogusCombined := bogusOut + bogusErr
	if badCode == 0 {
		t.Errorf("AC-cmd-apply-deps-flag: `ferry apply %s` unexpectedly exited 0 (unknown flag not rejected)", bogusFlag)
	}
	if !looksLikeUnknownFlag(bogusCombined) {
		t.Errorf("AC-cmd-apply-deps-flag: `ferry apply %s` did not fail AS AN UNKNOWN FLAG (no unknown/unrecognized-flag signal)\n%s", bogusFlag, bogusCombined)
	}
	// The error should name the offending flag (the flag string, sans dashes).
	if !containsAnyFold(bogusCombined, bogusFlag, "ferry-bogus-flag-xyz") {
		t.Errorf("AC-cmd-apply-deps-flag: unknown-flag error does not name the offending flag %q\n%s", bogusFlag, bogusCombined)
	}

	// GATING: `--deps` is ACCEPTED/resolves — it is NOT rejected as an unknown flag.
	// (A real `apply --deps` may still exit non-zero for runtime reasons like no PM;
	// the gate is that the FLAG PARSES, i.e. no unknown/unrecognized-flag error and
	// no usage-error exit that the bogus flag produces.)
	s3 := NewSandbox(t)
	s3.SeedSharedManifest(t, baseManifest)
	s3.WriteRepoFile(t, ".zshrc", "# managed\n")
	depsOut, depsErr, _ := s3.FerryEnv([]string{"PATH=" + t.TempDir()}, "apply", "--deps")
	depsCombined := depsOut + depsErr
	// The bogus flag produces an unknown-flag signal; --deps must NOT.
	if looksLikeUnknownFlag(depsCombined) {
		t.Errorf("AC-cmd-apply-deps-flag: `ferry apply --deps` was rejected as an unknown/unrecognized flag (must resolve)\n%s", depsCombined)
	}

	// NON-GATING: help-text documentation of --deps.
	s := NewSandbox(t)
	out, errOut, code := s.Ferry("apply", "--help")
	if code != 0 {
		t.Fatalf("AC-cmd-apply-deps-flag: `ferry apply --help` exited %d; stderr:\n%s", code, errOut)
	}
	help := out + errOut
	if _, ok := containsAllFold(help, "--deps"); !ok {
		t.Logf("AC-cmd-apply-deps-flag (non-gating help-text wording): `ferry apply --help` does not document --deps")
	}
	if !containsAnyFold(help, "dependenc", "deps") {
		t.Logf("AC-cmd-apply-deps-flag (non-gating help-text wording): --deps not associated with dependency installation in help")
	}
}

// TestApplyDryRunFlagDocumented covers AC-cmd-apply-dryrun-flag — NON-GATING /
// OPTIONAL. The mandatory preview path is `ferry diff` (AC-cmd-diff /
// AC-diff-preview-only); `apply --dry-run` is not doc-named.
//
// NOTE (doc ambiguity, ACCEPTANCE.md §ambiguities): the docs give a dedicated
// `ferry diff` command and never spell out `apply --dry-run` in prose. If
// --dry-run is NOT exposed, we SKIP (gate falls back to AC-cmd-diff). If it IS
// exposed, we additionally do a non-gating no-write check here (the behavioural
// no-write half is not in apply_test.go — it lives inline below when the flag exists).
func TestApplyDryRunFlagDocumented_AC_cmd_apply_dryrun_flag(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	out, errOut, code := s.Ferry("apply", "--help")
	if code != 0 {
		t.Fatalf("`ferry apply --help` exited %d; stderr:\n%s", code, errOut)
	}
	help := out + errOut
	if _, ok := containsAllFold(help, "--dry-run"); !ok {
		t.Skipf("AC-cmd-apply-dryrun-flag: `apply --dry-run` not exposed; only `ferry diff` is doc-mandatory (see ACCEPTANCE.md ambiguities). Covered by AC-cmd-diff.")
	}

	// Flag IS exposed: non-gating no-write check — `apply --dry-run` must report a
	// would-be change without writing the managed target.
	s2 := NewSandbox(t)
	s2.SeedSharedManifest(t, baseManifest)
	s2.WriteRepoFile(t, ".zshrc", "# would be deployed\n")
	s2.WriteRepoFile(t, "dotfiles/.zshrc", "# would be deployed\n")
	tw := s2.SnapshotFile(t, s2.HomePath(".zshrc")) // absent
	if _, _, code := s2.Ferry("apply", "--dry-run"); code != 0 {
		t.Logf("AC-cmd-apply-dryrun-flag (non-gating): `apply --dry-run` exited %d", code)
	}
	tw.AssertUnchanged(t) // dry-run must not write the managed target
}

// looksLikeUnknownFlag reports whether output contains a typical "flag not
// recognised" parse error (the signal an unknown flag produces but an accepted
// flag does not).
func looksLikeUnknownFlag(out string) bool {
	return containsAnyFold(out,
		"unknown flag", "unknown option", "unrecognized flag", "unrecognised flag",
		"unrecognized option", "flag provided but not defined", "invalid option",
		"unknown shorthand")
}
