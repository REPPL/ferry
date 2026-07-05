// Package agents implements the agents domain: it carries a single source of
// truth of agent instructions (agents/general.md + agents/coding.md in the
// config repo, plus skills/agents/hooks assets and scaffold templates) to the
// per-harness global instruction files each coding CLI reads.
//
// The domain adapts the instruction pipeline to ferry's model — regular-file
// COPIES reconciled by hash, never symlinks under $HOME. A harness registry
// (data, not code) maps each harness to its home-relative target and the
// instruction source it receives; `combined` content is DERIVED
// deterministically in memory at apply time and never committed. The planner
// expands (sources × harness registry × optional devtree × asset trees) into
// 1:1 (content, target) pairs; the write path reuses the dotfile domain's
// three-way classification, Backuper-mediated atomic writes, and deferred
// last-applied recording, so apply stays idempotent and `ferry restore`
// reverses every deploy from the same file baseline as any other domain.
//
// The package also carries the two repo-onboarding operations: Scaffold (stamp
// the standard AGENTS.md router, .work/ skeleton, and in-repo bridge symlinks
// into a project repo — in-repo symlinks are the project's own and are never
// deployed to $HOME) and the adopt helpers (import an existing symlink-based
// instruction directory into the config repo and retire its $HOME bridge
// symlinks in favour of ferry-managed copies).
package agents
