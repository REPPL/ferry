#!/usr/bin/env bash
# ferry checksum manifest generator — writes bin/checksums.txt, the SHA256 of
# every released ferry binary in the canonical `sha256sum` / `shasum -a 256 -c`
# format (`<sha256>  <asset-name>`).
#
# The release workflow uploads checksums.txt as a release ASSET; install.sh
# fetches it from the release it is installing and verifies each downloaded
# binary against it (fail-closed). No checksum is ever pinned into install.sh or
# committed to a branch.
#
# It hashes the binaries already present in bin/ — run `make build` first — and
# never rebuilds, so the bytes it hashes are exactly the bytes that ship.
# Portable across macOS (shasum) and Linux (sha256sum).
set -euo pipefail

# Repo root = parent of this script's dir (works regardless of CWD).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BINDIR="$ROOT/bin"
BINARY="ferry"
OUT="$BINDIR/checksums.txt"

# Pick a SHA256 tool: sha256sum (Linux) or `shasum -a 256` (macOS).
if command -v sha256sum >/dev/null 2>&1; then
  sha_of() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha_of() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  echo "gen-checksums: need sha256sum or shasum" >&2
  exit 1
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

# Enumerate the built binaries by the SAME glob the release workflow uploads
# and attests (bin/ferry-*), so the manifest covers exactly the shipped asset
# set by construction — a target added to (or removed from) the Makefile can
# never produce an unchecksummed release asset or a post-tag manifest failure.
found=0
for bin in "$BINDIR/$BINARY"-*; do
  [ -f "$bin" ] || continue
  found=1
  name="$(basename "$bin")"
  # Canonical two-space form: `<sha256>  <asset-name>`, verifiable with
  # `shasum -a 256 -c` / `sha256sum -c` from a directory holding the asset.
  printf '%s  %s\n' "$(sha_of "$bin")" "$name" >> "$tmp"
done
if [ "$found" -eq 0 ]; then
  echo "gen-checksums: no $BINARY-* binaries in $BINDIR (run 'make build' first)" >&2
  exit 1
fi

mv "$tmp" "$OUT"
echo "gen-checksums: wrote $OUT"
cat "$OUT"
