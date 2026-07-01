#!/usr/bin/env bash
# ferry bootstrap installer.
#
# Installs ONLY the `ferry` binary to ~/.local/bin — no sudo, no shell-rc edits,
# no package manager, and nothing else is run. Usage:
#
#   curl -fsSL https://raw.githubusercontent.com/REPPL/ferry/main/install.sh | bash
#
# Flags:
#   --init       after installing, chain `ferry init` (default: do NOT)
#
# INTEGRITY — honest wording: the pinned SHA256 below is verified against the
# downloaded binary, and the network path is FAIL-CLOSED — with no pinned checksum
# for the selected target it refuses to install rather than accept an unverified
# binary. Verification catches corruption or tampering IN TRANSIT. It is NOT a full
# security guarantee: the checksum ships from the same unauthenticated source as the
# binary, so a compromised source could serve a matching pair. Treat this as a
# personal-trust convenience, not a signature. (The release process fills in the
# per-target SHA256 values below.)
#
# FERRY_FAKE_BINARY=/path/to/binary — TEST-ONLY override: install that explicit real
# file instead of downloading. No implicit CWD/bin fallback; no placeholder.
set -euo pipefail

VERSION="${FERRY_VERSION:-latest}"
REPO="REPPL/ferry"
BIN_DIR="$HOME/.local/bin"
DO_INIT=0

for arg in "$@"; do
  case "$arg" in
    --init) DO_INIT=1 ;;
    *) echo "ferry install: unknown option: $arg" >&2; exit 2 ;;
  esac
done

# Detect OS/arch -> ferry-<goos>-<goarch>.
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$os" in
  darwin|linux) ;;
  *) echo "ferry install: unsupported OS '$os' (need darwin or linux)" >&2; exit 1 ;;
esac
case "$arch" in
  arm64|aarch64) arch="arm64" ;;
  x86_64|amd64)  arch="amd64" ;;
  *) echo "ferry install: unsupported arch '$arch'" >&2; exit 1 ;;
esac
asset="ferry-${os}-${arch}"

# Pinned SHA256 per target (release process fills these). On the public network
# path an EMPTY value means "no pinned checksum for this target" and the install
# REFUSES (fail-closed) — a downloaded binary is never installed unverified.
# Referenced dynamically via `eval` below, hence shellcheck can't see the use.
# shellcheck disable=SC2034
{
  sha_darwin_arm64="fd655e76aa2d3307f68ffaa1551cbc5d03bae6985c1b41161e1069ac529d27ef"
  sha_darwin_amd64="0d7b896a7e59a41938014de49fc7bcff07e1486bd444dff4da4b7e58f3bed799"
  sha_linux_arm64="1a1c4f5567026960c390b794cc2b8478dc4866b3832ed482d534ce92ce36482f"
  sha_linux_amd64="7645bb2876581914eb4706dbb7ff51a699d6f839bf287b72eb9fa36ed2bb29e4"
}
eval "expected_sha=\${sha_${os}_${arch}}"

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

# TEST-ONLY explicit artifact override. FERRY_FAKE_BINARY must point at a REAL
# existing file, which is installed verbatim (place + chmod) instead of a network
# download. There is NO implicit CWD/bin fallback and NO placeholder: absent this
# var, the only install source is the uname-selected, SHA-verified GitHub release.
from_network=0
if [ -n "${FERRY_FAKE_BINARY:-}" ]; then
  if [ ! -f "${FERRY_FAKE_BINARY}" ]; then
    echo "ferry install: FERRY_FAKE_BINARY=${FERRY_FAKE_BINARY} does not exist" >&2
    exit 1
  fi
  cp "${FERRY_FAKE_BINARY}" "$tmp"
elif [ "${FERRY_NO_NETWORK:-}" = "1" ]; then
  # Offline with no explicit artifact: install NOTHING (no placeholder). Report the
  # PATH advice and exit 0 so PATH/rc invariants can still be exercised offline.
  echo "ferry install: FERRY_NO_NETWORK=1 and no FERRY_FAKE_BINARY — skipping binary install."
  case ":${PATH}:" in
    *":${BIN_DIR}:"*) ;;
    *)
      echo ""
      echo "$BIN_DIR is not on your PATH. Add this line to your shell config:"
      echo "export PATH=\"\$HOME/.local/bin:\$PATH\""
      ;;
  esac
  exit 0
else
  # Public network path — fail closed without a pinned checksum for this target.
  if [ -z "$expected_sha" ]; then
    echo "ferry install: no pinned checksum for ${asset}; refusing to install an unverified binary" >&2
    exit 1
  fi
  if [ "$VERSION" = "latest" ]; then
    url="https://github.com/${REPO}/releases/latest/download/${asset}"
  else
    url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
  fi
  echo "ferry install: downloading ${asset} (${VERSION})..."
  curl -fsSL "$url" -o "$tmp"
  from_network=1
fi

# Integrity: verify the pinned SHA256 ONLY for a network download (the public path).
# The network path already aborted above if no pin exists, so a downloaded binary is
# always verified. The explicit FERRY_FAKE_BINARY test artifact is a real local file
# the caller chose, not a release download, so it is installed verbatim without the
# release pin (which describes the published binaries, not an arbitrary test file).
if [ "$from_network" = "1" ] && [ -n "$expected_sha" ]; then
  if command -v shasum >/dev/null 2>&1; then
    got="$(shasum -a 256 "$tmp" | awk '{print $1}')"
  else
    got="$(sha256sum "$tmp" | awk '{print $1}')"
  fi
  if [ "$got" != "$expected_sha" ]; then
    echo "ferry install: SHA256 mismatch for ${asset}" >&2
    echo "  expected $expected_sha" >&2
    echo "  got      $got" >&2
    exit 1
  fi
fi

# Install: user-owned ~/.local/bin, no sudo.
mkdir -p "$BIN_DIR"
cp "$tmp" "$BIN_DIR/ferry"
chmod +x "$BIN_DIR/ferry"
echo "ferry install: installed to $BIN_DIR/ferry"

# PATH advice — PRINT the line; NEVER edit a shell rc file.
case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *)
    echo ""
    echo "$BIN_DIR is not on your PATH. Add this line to your shell config:"
    echo "export PATH=\"\$HOME/.local/bin:\$PATH\""
    ;;
esac

# Next steps — do NOT run ferry init / brew / anything else by default.
if [ "$DO_INIT" = "1" ]; then
  echo ""
  echo "ferry install: running 'ferry init'..."
  "$BIN_DIR/ferry" init
else
  echo ""
  echo "Next: run 'ferry init' to get started."
fi
