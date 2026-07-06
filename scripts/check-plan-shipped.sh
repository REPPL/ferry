#!/usr/bin/env bash
# ferry plan-shipped check — assert the release plan for a version is marked shipped.
#
# Each release may carry a design plan under docs/plans/, named
# `YYYY-MM-DD-vX.Y.Z.md`, with a prose frontmatter line of the form
# `Date: ... · Status: <status>`. Once the release is out, that Status must read
# `shipped in vX.Y.Z`. This check enforces the convention:
#
#   scripts/check-plan-shipped.sh vX.Y.Z
#
#   - No plan doc for the version → print an "ok" notice and exit 0 (not every
#     patch release has a plan).
#   - A plan exists but its Status: line does not contain "shipped" (case-
#     insensitive) → print the offending line and exit non-zero.
#   - A plan exists and is marked shipped → print the file(s) and exit 0.
#
# Pure read-only: it never writes. Shared by scripts/release.sh (local gate) and
# the release workflow's verify job (CI backstop).
set -euo pipefail

VERSION="${1:-}"

usage() {
  echo "usage: check-plan-shipped.sh vX.Y.Z" >&2
}

if [ -z "$VERSION" ]; then
  echo "check-plan-shipped: a version argument is required" >&2
  usage
  exit 2
fi

# A tag outside vMAJOR.MINOR.PATCH (a pre-release like v0.4.0-rc1, a rehearsal
# tag) still matches the release workflow's v* trigger but has no release-lineage
# plan to check — SKIP (exit 0) rather than fail, mirroring prune-releases.sh, so
# such a release run does not go red at this step. Exit 2 is reserved for a
# genuinely missing argument (handled above).
if ! printf '%s' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "check-plan-shipped: '$VERSION' is not a vX.Y.Z version — no plan lineage to check (ok)"
  exit 0
fi

# Repo root = parent of this script's dir (works regardless of CWD).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Collect plan docs whose name ends in the version (e.g. 2026-07-05-v0.5.0.md
# for v0.5.0). nullglob so a no-match glob expands to nothing, not itself.
shopt -s nullglob
plans=( "$ROOT"/docs/plans/*"$VERSION".md )
shopt -u nullglob

if [ "${#plans[@]}" -eq 0 ]; then
  echo "check-plan-shipped: no plan doc for $VERSION (ok)"
  exit 0
fi

for plan in "${plans[@]}"; do
  rel="${plan#"$ROOT"/}"
  status_line="$(grep -i -m1 'Status:' "$plan" || true)"
  if [ -z "$status_line" ]; then
    echo "check-plan-shipped: $rel has no 'Status:' line — cannot confirm it is shipped" >&2
    exit 1
  fi
  if ! printf '%s' "$status_line" | grep -qiF "shipped in $VERSION"; then
    echo "check-plan-shipped: $rel is not marked shipped (expected 'Status: shipped in $VERSION'):" >&2
    echo "  $status_line" >&2
    exit 1
  fi
  echo "check-plan-shipped: $rel is marked shipped."
done

exit 0
