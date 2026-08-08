#!/usr/bin/env bash
# consistency-lint — deterministic repo-convention checks that gate CI.
#
#   1. No two ADRs in .abcd/development/decisions/ share an NNNN prefix. This is the
#      ADR-race mitigation (ADR 0001): authors take the next free number
#      optimistically on their branch, and a collision blocks the merge so the
#      second branch to land renumbers.
#   2. Every ADR is named sequentially as NNNN-title.md, never <date>--<slug>.
#   3. No prose or config file points session handoff at .abcd/work/NEXT.md — the
#      handoff lives in the private .abcd/.work.local/ layer (ADR 0002). Scope is the
#      human/agent-facing surface (docs, scripts, hooks, templates); Go sources
#      are exempt because their tests legitimately assert the OLD path is now
#      absent. The developer-record .abcd/development/plans/ and research/ (which
#      describe past and investigative state) and the changelog are exempt
#      too (they describe past state), as is this script itself.
#   4. No Go source outside internal/dotfile/dotfile.go re-inlines either
#      boundary-refusal message body. doctor recognises a plan-time containment
#      refusal by these exact strings, so a re-inlined copy can drift and
#      silently turn a security-invariant [fail] into a [pass].
#
# Exit non-zero on the first failing invariant set; print every offending path.
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
decisions=.abcd/development/decisions

if [ -d "$decisions" ]; then
  # 1. duplicate NNNN prefixes
  dupes=$(find "$decisions" -maxdepth 1 -name '[0-9][0-9][0-9][0-9]-*.md' -exec basename {} \; \
    | sed -E 's/^([0-9]{4})-.*/\1/' | sort | uniq -d || true)
  if [ -n "$dupes" ]; then
    echo "consistency-lint: duplicate ADR number(s) in $decisions/ — renumber the loser:" >&2
    echo "$dupes" | sed 's/^/  /' >&2
    fail=1
  fi

  # 2. ADR files not named NNNN-title.md (README.md is allowed)
  # The title must start with a LETTER after the NNNN- prefix, so a date-format
  # name (2026-07-06--slug.md) — whose "title" would start with a digit — is
  # rejected, not mistaken for a well-formed sequential name.
  badnames=$(find "$decisions" -maxdepth 1 -name '*.md' -exec basename {} \; \
    | grep -vE '^[0-9]{4}-[a-z][a-z0-9-]*\.md$' | grep -viE '^README\.md$' || true)
  if [ -n "$badnames" ]; then
    echo "consistency-lint: ADR file(s) not named NNNN-title.md:" >&2
    echo "$badnames" | sed 's/^/  /' >&2
    fail=1
  fi
fi

# 3. stale .abcd/work/NEXT.md handoff references (must be .abcd/.work.local/NEXT.md)
hits=$(git ls-files -- ':!.abcd/development/plans/' ':!.abcd/development/research/' ':!CHANGELOG.md' ':!scripts/consistency-lint.sh' ':!*.go' \
  | xargs -r grep -nF '.abcd/work/NEXT.md' 2>/dev/null || true)
if [ -n "$hits" ]; then
  echo "consistency-lint: session handoff is .abcd/.work.local/NEXT.md, not .abcd/work/NEXT.md:" >&2
  echo "$hits" | sed 's/^/  /' >&2
  fail=1
fi

# 4. re-inlined boundary-refusal bodies. `ferry doctor` proves its ~/.ssh and
#    containment invariants by matching a plan's warnings against these exact
#    strings, so a domain that spells its own copy silently drops out of the
#    check — its [fail] and non-zero exit become a [pass], with no compile error.
#    They are defined once in internal/dotfile/dotfile.go; every other Go file
#    must reference dotfile.RefusalSSHBody / dotfile.RefusalEscapeBody. Test
#    files are exempt: they legitimately assert the rendered wording.
for body in 'ferry never manages paths under ~/.ssh' 'invalid managed path (escapes $HOME)'; do
  hits=$(git ls-files -- '*.go' ':!internal/dotfile/dotfile.go' ':!*_test.go' \
    | xargs -r grep -nF -- "$body" 2>/dev/null || true)
  if [ -n "$hits" ]; then
    echo "consistency-lint: boundary-refusal body re-inlined (use dotfile.RefusalSSHBody / dotfile.RefusalEscapeBody so doctor's invariant check cannot drift):" >&2
    echo "$hits" | sed 's/^/  /' >&2
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "consistency-lint: FAILED" >&2
  exit 1
fi
echo "consistency-lint: OK"
