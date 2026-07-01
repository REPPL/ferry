#!/usr/bin/env bash
# ferry checksum pinner — populates install.sh's per-target sha_* pins from the
# real cross-compiled binaries, so nobody hand-pastes SHA256 values.
#
#   scripts/pin-checksums.sh           build (make build) + write pins into install.sh
#   scripts/pin-checksums.sh --check   verify install.sh pins match fresh binaries;
#                                      exit non-zero on mismatch, edit NOTHING
#
# Idempotent: it rewrites each `sha_<goos>_<arch>="..."` line (empty OR already
# filled) to the current binary's hash, touching only those four lines and
# preserving the rest of install.sh byte-for-byte. Portable across macOS (BSD)
# and Linux (GNU): uses an awk + temp-file rewrite, never `sed -i`.
set -euo pipefail

# Repo root = parent of this script's dir (works regardless of CWD).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
INSTALL_SH="$ROOT/install.sh"
BINDIR="$ROOT/bin"
BINARY="ferry"
TARGETS="darwin/arm64 darwin/amd64 linux/arm64 linux/amd64"

CHECK=0
for arg in "$@"; do
  case "$arg" in
    --check) CHECK=1 ;;
    *) echo "pin-checksums: unknown option: $arg" >&2; exit 2 ;;
  esac
done

# Pick a SHA256 tool: sha256sum (Linux) or `shasum -a 256` (macOS).
if command -v sha256sum >/dev/null 2>&1; then
  sha_of() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha_of() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  echo "pin-checksums: need sha256sum or shasum" >&2
  exit 1
fi

# Build the four binaries (unless --check, which still needs them to compare).
# Forward VERSION (if set, e.g. from `make pin-checksums VERSION=vX.Y.Z` or the
# release workflow) so the pinned hashes match the version-stamped binaries.
echo "pin-checksums: building binaries (make build${VERSION:+ VERSION=$VERSION})..."
make -C "$ROOT" build VERSION="${VERSION:-}"

# Read the pinned value for sha_<var> from install.sh (empty string if unset).
# Portable across BSD/GNU awk: no 3-arg match(); split on the double-quotes.
pinned_value() {
  awk -v v="$1" '
    $0 ~ "^[[:space:]]*"v"=\"[^\"]*\"[[:space:]]*$" {
      n = split($0, parts, "\""); print parts[2]; exit
    }
  ' "$INSTALL_SH"
}

# Rewrite install.sh so sha_<var>="<hash>", touching only that one line.
rewrite_pin() {
  local var="$1" hash="$2" tmp
  tmp="$(mktemp)"
  awk -v v="$var" -v h="$hash" '
    $0 ~ "^([[:space:]]*)"v"=\"[^\"]*\"[[:space:]]*$" {
      match($0, /^[[:space:]]*/); indent=substr($0, 1, RLENGTH)
      print indent v "=\"" h "\""
      next
    }
    { print }
  ' "$INSTALL_SH" > "$tmp"
  mv "$tmp" "$INSTALL_SH"
}

fail=0
declare -a summary
for target in $TARGETS; do
  goos="${target%/*}"; goarch="${target#*/}"
  var="sha_${goos}_${goarch}"
  bin="$BINDIR/$BINARY-$goos-$goarch"
  if [ ! -f "$bin" ]; then
    echo "pin-checksums: missing binary $bin (did 'make build' succeed?)" >&2
    exit 1
  fi
  hash="$(sha_of "$bin")"

  if [ "$CHECK" = "1" ]; then
    cur="$(pinned_value "$var")"
    if [ "$cur" = "$hash" ]; then
      summary+=("  OK    $var matches $bin")
    else
      summary+=("  STALE $var: pinned='${cur:-<empty>}' actual='$hash'")
      fail=1
    fi
  else
    rewrite_pin "$var" "$hash"
    summary+=("  pinned $var=$hash")
  fi
done

if [ "$CHECK" = "1" ]; then
  printf '%s\n' "${summary[@]}"
  if [ "$fail" != "0" ]; then
    echo "pin-checksums: install.sh pins are STALE (run scripts/pin-checksums.sh to fix)" >&2
    exit 1
  fi
  echo "pin-checksums: all pins match freshly-built binaries."
  exit 0
fi

# Write path: verify what we just wrote by re-reading + re-hashing.
printf '%s\n' "${summary[@]}"
echo "pin-checksums: verifying install.sh pins against binaries..."
for target in $TARGETS; do
  goos="${target%/*}"; goarch="${target#*/}"
  var="sha_${goos}_${goarch}"
  bin="$BINDIR/$BINARY-$goos-$goarch"
  cur="$(pinned_value "$var")"
  hash="$(sha_of "$bin")"
  if [ -z "$cur" ]; then
    echo "pin-checksums: $var is still EMPTY after write" >&2; fail=1
  elif [ "$cur" != "$hash" ]; then
    echo "pin-checksums: $var mismatch after write (pinned=$cur actual=$hash)" >&2; fail=1
  fi
done
if [ "$fail" != "0" ]; then
  echo "pin-checksums: verification FAILED" >&2
  exit 1
fi
echo "pin-checksums: done — all four pins filled and verified."
