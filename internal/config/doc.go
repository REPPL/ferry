// Package config loads ferry's three TOML configuration files and resolves the
// effective scope that governs every domain operation.
//
// Three files, distinct roles, no overlap:
//
//   - ~/.config/ferry/config.toml (per-machine, NOT in the repo; resolved via
//     internal/paths.ConfigFile): this machine's identity (hostname) plus the
//     path to its repo clone. Answers "where am I, which clone is mine."
//     Load/Save here; init writes it in a later wave.
//   - <repo>/ferry.toml (committed): the SHARED baseline scope — which domains
//     are managed by default on every machine.
//   - <repo>/ferry.local.toml (gitignored): per-machine scope OVERRIDES, merged
//     ON TOP of ferry.toml (local wins).
//
// EffectiveScope = ferry.toml (+) ferry.local.toml. The SAME effective Scope
// governs BOTH directions: apply (repo -> machine) and capture (machine ->
// repo) ask the one Scope "is this domain managed?" so a domain disabled on
// this machine is neither applied nor captured here.
//
// All parsing validates at the boundary: a malformed or unreadable TOML file
// yields a clear error, never a panic. Internal callers are trusted.
//
// This package is pure logic with no cobra/command dependency; Wave 2 commands
// compose it.
package config
