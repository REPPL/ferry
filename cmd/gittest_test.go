package cmd

import (
	"os"
	"strings"
)

// gitIsolatedEnv returns the environment for a git subprocess in a cmd-package
// test that cannot read or mutate the developer's real git configuration, or any
// repo outside the test's temp space — even if a call omits `-C`/`cmd.Dir`, or the
// ambient repo's git state is corrupted (e.g. core.bare flipped by a concurrent
// operation). It mirrors the evals harness helper of the same name.
//
// `append(os.Environ(), …)` is unsafe here: an inherited GIT_DIR/GIT_WORK_TREE
// could redirect the command at the host repo, and git would read the host
// ~/.gitconfig — making tests non-deterministic and, under adverse conditions,
// letting a test's identity or fixture commits leak into the working repo. So this
// strips every inherited GIT_* variable, then pins a deterministic identity,
// non-interactive behaviour, and neutralised config discovery (no system config,
// an empty global config). The repo's own LOCAL .git/config is untouched, so tests
// that seed hostile local config (git-hardening tests) still exercise it.
//
// The identity is deliberately distinctive (`ferry-test`) so that if it ever does
// leak, it is instantly recognisable as a test fixture — unlike the bare `t` that
// previously made such a leak hard to trace.
func gitIsolatedEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			continue // drop GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE/GIT_CONFIG*/… host bleed
		}
		env = append(env, kv)
	}
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=ferry-test", "GIT_AUTHOR_EMAIL=ferry-test@localhost",
		"GIT_COMMITTER_NAME=ferry-test", "GIT_COMMITTER_EMAIL=ferry-test@localhost",
	)
	return append(env, extra...)
}
