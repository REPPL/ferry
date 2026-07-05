#!/usr/bin/env bash
# ferry release pruner — enforces the release-retention policy after a new release.
#
# Policy: keep the LATEST release of the current line, plus the LAST release of every
# other minor/major line. A "line" is a (MAJOR, MINOR) pair; a release is vMAJOR.MINOR.PATCH.
# For each line, only the highest PATCH is kept; older patches in that line are pruned.
# Net effect: exactly one release per line survives (its newest patch), so shipping
# v0.1.2 prunes v0.1.1, while shipping v0.2.0 keeps the last v0.1.x alongside it.
#
# Pruning is DESTRUCTIVE: it deletes the GitHub Release and its binary assets. It
# NEVER deletes the git tag — tags are immutable once pushed, so a pruned version's
# tag (and the commit it points at) stays reachable for provenance and rebuilds.
#
#   scripts/prune-releases.sh --current vX.Y.Z          prune superseded releases
#   scripts/prune-releases.sh --current vX.Y.Z --dry-run  show what WOULD be pruned
#
# The --current release (the one just published) is NEVER pruned, even if this script
# is run twice. Requires the `gh` CLI, authenticated (GITHUB_TOKEN in CI).
set -euo pipefail

CURRENT=""
DRY_RUN=0
while [ $# -gt 0 ]; do
  case "$1" in
    --current) CURRENT="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    *) echo "prune-releases: unknown option: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$CURRENT" ]; then
  echo "prune-releases: --current vX.Y.Z is required (the release just published)" >&2
  exit 2
fi

# Validate the tag shape: v<major>.<minor>.<patch>, all numeric. A tag outside
# that shape (a pre-release like v0.4.0-rc1, a rehearsal tag) still matches the
# release workflow's v* trigger but is not part of the retention lineage, so
# there is nothing to prune for it — SKIP (exit 0) rather than fail, or every
# such release run would go red at this step after a successful publish.
semver_re='^v[0-9]+\.[0-9]+\.[0-9]+$'
if ! printf '%s' "$CURRENT" | grep -Eq "$semver_re"; then
  echo "prune-releases: '$CURRENT' is not a vMAJOR.MINOR.PATCH tag — not part of the retention lineage; skipping prune."
  exit 0
fi

command -v gh >/dev/null 2>&1 || { echo "prune-releases: need the gh CLI" >&2; exit 1; }

# Split a vX.Y.Z tag into its numeric parts via a portable field split.
parts() { printf '%s' "${1#v}" | tr '.' ' '; }

# List every published release tag that matches vX.Y.Z (ignore drafts, pre-releases,
# and any non-semver tags like the internal wave tags w-1..w4). Newline-separated;
# no mapfile / associative arrays, so this runs on macOS bash 3.2 as well as CI bash 5.
ALL="$(gh release list --limit 200 --json tagName,isDraft,isPrerelease \
  --jq '.[] | select(.isDraft==false and .isPrerelease==false) | .tagName' \
  | grep -E "$semver_re" || true)"

if [ -z "$ALL" ]; then
  echo "prune-releases: no semver releases found; nothing to do."
  exit 0
fi

# keeper_patch <major.minor> — highest patch published in that line (the release to
# keep). Computed by scanning ALL each time; the release set is tiny (one per line in
# steady state), so the O(n^2) scan is irrelevant and keeps us array-free/portable.
keeper_patch() {
  local want="$1" best=-1 t maj min pat
  for t in $ALL; do
    read -r maj min pat <<<"$(parts "$t")"
    if [ "$maj.$min" = "$want" ] && [ "$pat" -gt "$best" ]; then
      best="$pat"
    fi
  done
  echo "$best"
}

# Safety: the current release must be its line's keeper. If a HIGHER patch than the
# current one already exists in the current line, something is wrong (out-of-order
# release) — refuse to prune rather than risk deleting the newer one.
read -r cmaj cmin cpat <<<"$(parts "$CURRENT")"
cur_line="$cmaj.$cmin"
cur_keeper="$(keeper_patch "$cur_line")"
if [ "$cur_keeper" -gt "$cpat" ]; then
  echo "prune-releases: a newer patch than $CURRENT already exists in line $cur_line" \
       "(keeper patch=$cur_keeper); refusing to prune." >&2
  exit 1
fi

# Build the prune list: every release that is not its line's keeper, and never the
# current release itself.
PRUNE=""
for tag in $ALL; do
  if [ "$tag" = "$CURRENT" ]; then
    continue
  fi
  read -r maj min pat <<<"$(parts "$tag")"
  if [ "$pat" -ne "$(keeper_patch "$maj.$min")" ]; then
    PRUNE="$PRUNE$tag"$'\n'
  fi
done
PRUNE="$(printf '%s' "$PRUNE" | grep -E "$semver_re" || true)"

echo "prune-releases: current=$CURRENT"
echo "prune-releases: keepers (one per line):"
keepers=""
for tag in $ALL; do
  read -r maj min pat <<<"$(parts "$tag")"
  if [ "$pat" -eq "$(keeper_patch "$maj.$min")" ]; then
    keepers="$keepers  $tag"$'\n'
  fi
done
printf '%s' "$keepers" | sort -u

if [ -z "$PRUNE" ]; then
  echo "prune-releases: nothing to prune."
  exit 0
fi

prune_count="$(printf '%s\n' "$PRUNE" | grep -c .)"
echo "prune-releases: pruning $prune_count superseded release(s):"
printf '%s\n' "$PRUNE" | sed 's/^/  /'

if [ "$DRY_RUN" -eq 1 ]; then
  echo "prune-releases: --dry-run — no releases deleted (tags are never touched)."
  exit 0
fi

# Delete the Release + its binary assets for each pruned version, and ONLY the
# Release: the git tag is left in place. PRUNE is a newline-delimited string (not an
# array), so iterate it line by line. There is deliberately no --cleanup-tag and no
# `git tag -d` / `git push --delete` anywhere here — tags are immutable once pushed.
while IFS= read -r tag; do
  [ -z "$tag" ] && continue
  echo "prune-releases: deleting release $tag (its tag is kept)"
  gh release delete "$tag" --yes
done <<< "$PRUNE"
echo "prune-releases: done — pruned $prune_count release(s); all tags retained."
