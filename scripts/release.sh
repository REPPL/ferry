#!/usr/bin/env bash
# ferry release driver — the one blessed path from a clean main to a pushed tag.
#
#   scripts/release.sh vX.Y.Z            gate, rehearse, then tag and push
#   scripts/release.sh vX.Y.Z --yes      skip the pre-tag confirmation prompt
#   scripts/release.sh vX.Y.Z --dry-run  run every gate and rehearsal, print what
#                                        the tag/push and NEXT.md reset WOULD do,
#                                        and change nothing (no tag, no push, and
#                                        the real .abcd/.work.local/NEXT.md is never touched)
#
# A ferry release is triggered by pushing a vX.Y.Z tag: the release workflow
# re-verifies the pushed commit, then builds, checksums, attests, and publishes.
# This script is the local half — it fails closed at every gate BEFORE creating
# the tag, so a release only ever starts from a clean, documented, verified tree.
# The tag push (step 6) is the single irreversible act; everything before it is
# reversible and everything after it (the NEXT.md reset, step 7) is bookkeeping.
#
# --dry-run and --yes compose: `--dry-run --yes` rehearses the whole flow with no
# prompt and no side effects.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: release.sh vX.Y.Z [--dry-run] [--yes]

  vX.Y.Z      the release version (semver, v-prefixed)
  --dry-run   run gates 1-5, print what tag/push and the NEXT.md reset would do;
              create no tag or push and never touch .abcd/.work.local/NEXT.md (the rehearsal
              still writes the gitignored bin/ artefacts, as a real run would)
  --yes       do not prompt before the (irreversible) tag + push
EOF
}

# ---------------------------------------------------------------------------
# Functions (also sourced by tests; see the executed-directly guard at the end).
# ---------------------------------------------------------------------------

die() { echo "release: $*" >&2; exit 1; }

# render_next_md <src> <version> — print a reset .abcd/.work.local/NEXT.md to stdout.
#
# Everything above the carry markers is replaced with a fresh header naming
# <version> as the latest release; the carry region and everything below it is
# preserved byte-for-byte (from the `next:carry:start` marker to end of file).
# Refuses (non-zero) if either carry marker is absent — never guesses a split.
render_next_md() {
  local src="$1" version="$2"
  if ! grep -qF 'next:carry:start' "$src"; then
    echo "release: $src is missing the '<!-- next:carry:start ... -->' marker; refusing to regenerate it" >&2
    return 1
  fi
  if ! grep -qF 'next:carry:end' "$src"; then
    echo "release: $src is missing the '<!-- next:carry:end -->' marker; refusing to regenerate it" >&2
    return 1
  fi
  local start_line
  start_line="$(grep -n -F -m1 'next:carry:start' "$src" | cut -d: -f1)"
  # Fresh, ephemeral top: a new header, a Current-state line naming this release,
  # and an emptied In-progress section.
  printf '# NEXT\n\nUpdated: %s\n\n## Current state\n\n%s is the latest release; main clean.\n\n## In progress\n\n_(none)_\n\n' \
    "$(date +%F)" "$version"
  # Durable tail: the carry region and everything below, verbatim. tail -n +N
  # copies bytes from line N to EOF without adding or dropping a trailing newline.
  tail -n +"$start_line" "$src"
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

main() {
  local DRY_RUN=0 ASSUME_YES=0 VERSION=""

  while [ $# -gt 0 ]; do
    case "$1" in
      --dry-run) DRY_RUN=1; shift ;;
      --yes)     ASSUME_YES=1; shift ;;
      -h|--help) usage; exit 0 ;;
      -*)        echo "release: unknown option: $1" >&2; usage; exit 2 ;;
      *)
        if [ -n "$VERSION" ]; then
          echo "release: unexpected extra argument: $1" >&2; usage; exit 2
        fi
        VERSION="$1"; shift ;;
    esac
  done

  if [ -z "$VERSION" ]; then
    echo "release: a version argument is required" >&2; usage; exit 2
  fi
  if ! printf '%s' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
    die "'$VERSION' is not a vMAJOR.MINOR.PATCH version (e.g. v0.5.0)"
  fi
  local ver_no_v="${VERSION#v}"

  # -------------------------------------------------------------------------
  # Gate 1 — preconditions.
  # -------------------------------------------------------------------------
  local ROOT
  ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || die "not inside a git repository"
  [ "$PWD" -ef "$ROOT" ] || die "run from the repo root ($ROOT), not $PWD"

  local branch
  branch="$(git rev-parse --abbrev-ref HEAD)"
  [ "$branch" = "main" ] || die "current branch is '$branch'; releases are cut from main"

  [ -z "$(git status --porcelain)" ] || die "working tree is not clean; commit or stash first"

  echo "release: fetching origin to compare main..."
  git fetch --quiet origin || die "git fetch origin failed"
  local local_main remote_main
  local_main="$(git rev-parse --verify --quiet refs/heads/main || true)"
  remote_main="$(git rev-parse --verify --quiet refs/remotes/origin/main || true)"
  [ -n "$remote_main" ] || die "origin/main not found; cannot verify main is up to date"
  [ "$local_main" = "$remote_main" ] || \
    die "local main ($local_main) != origin/main ($remote_main); pull/push to sync first"

  # -------------------------------------------------------------------------
  # Gate 2 — CHANGELOG: the version must be promoted out of [Unreleased].
  # -------------------------------------------------------------------------
  local dot_re="${ver_no_v//./\\.}"
  if ! grep -Eq "^## \[${dot_re}\] - " "$ROOT/CHANGELOG.md"; then
    die "CHANGELOG.md has no '## [${ver_no_v}] - <date>' section; promote it out of [Unreleased] first"
  fi
  echo "release: gate CHANGELOG — '## [${ver_no_v}]' present."

  # -------------------------------------------------------------------------
  # Gate 3 — docs currency (deterministic lint).
  # -------------------------------------------------------------------------
  command -v docs-currency-lint >/dev/null 2>&1 || \
    die "docs-currency-lint is not on PATH; install it before releasing"
  docs-currency-lint || die "docs-currency-lint reported findings; fix them before releasing"
  echo "release: gate docs-currency-lint — clean."

  # -------------------------------------------------------------------------
  # Gate 4 — the release plan (if any) is marked shipped.
  # -------------------------------------------------------------------------
  "$ROOT/scripts/check-plan-shipped.sh" "$VERSION" || die "plan-shipped check failed for $VERSION"

  # -------------------------------------------------------------------------
  # Step 5 — local rehearsal (build, version stamp, checksums, prune dry-run).
  # -------------------------------------------------------------------------
  echo "release: rehearsal — make build VERSION=$VERSION"
  make -C "$ROOT" build VERSION="$VERSION"
  local bin
  bin="$ROOT/bin/ferry-$(go env GOOS)-$(go env GOARCH)"
  [ -x "$bin" ] || die "expected built binary $bin is missing"
  local reported
  reported="$("$bin" version)"
  if ! printf '%s' "$reported" | grep -qF "$VERSION"; then
    die "built binary reports '$reported', not '$VERSION'"
  fi
  echo "release: rehearsal — $bin reports: $reported"

  scripts/gen-checksums.sh

  echo "release: rehearsal — prune plan (dry-run):"
  scripts/prune-releases.sh --current "$VERSION" --dry-run

  # -------------------------------------------------------------------------
  # Step 6 — the single irreversible act: tag and push.
  # -------------------------------------------------------------------------
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "release: [dry-run] WOULD run: git tag $VERSION && git push origin $VERSION"
  else
    if [ "$ASSUME_YES" -ne 1 ]; then
      printf 'release: tag %s at %s and push to origin? [y/N] ' "$VERSION" "$local_main"
      local reply=""
      read -r reply </dev/tty || reply=""
      case "$reply" in
        y|Y|yes|YES) ;;
        *) die "aborted before tagging (no tag created)";;
      esac
    fi
    git tag "$VERSION"
    git push origin "$VERSION"
    echo "release: pushed tag $VERSION — the release workflow now takes over."
  fi

  # -------------------------------------------------------------------------
  # Step 7 — post-tag artefact reset of .abcd/.work.local/NEXT.md.
  # -------------------------------------------------------------------------
  local next="$ROOT/.abcd/.work.local/NEXT.md"
  if [ ! -f "$next" ]; then
    echo "release: no .abcd/.work.local/NEXT.md — skipping the NEXT.md reset."
    return 0
  fi

  if [ "$DRY_RUN" -eq 1 ]; then
    echo "release: [dry-run] WOULD archive .abcd/.work.local/NEXT.md to .abcd/.work.local/history/NEXT-$VERSION-<timestamp>.md"
    echo "release: [dry-run] WOULD reset .abcd/.work.local/NEXT.md; diff (current -> regenerated) below:"
    local tmp
    tmp="$(mktemp)"
    # render reads the real NEXT.md read-only and writes to a temp copy; the real
    # file is never touched. A non-zero render (missing markers) fails the run.
    render_next_md "$next" "$VERSION" > "$tmp"
    diff -u "$next" "$tmp" || true
    rm -f "$tmp"
    return 0
  fi

  local histdir="$ROOT/.abcd/.work.local/history"
  mkdir -p "$histdir"
  local archive
  archive="$histdir/NEXT-$VERSION-$(date +%Y%m%d-%H%M%S).md"
  cp "$next" "$archive"
  echo "release: archived NEXT.md to ${archive#"$ROOT"/}"

  local tmp
  tmp="$(mktemp)"
  render_next_md "$next" "$VERSION" > "$tmp"
  mv "$tmp" "$next"
  echo "release: reset .abcd/.work.local/NEXT.md (carry region preserved)."
}

# Run main only when executed directly; when sourced (tests), just define the
# functions above so render_next_md can be exercised in isolation.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
