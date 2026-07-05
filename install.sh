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
# INTEGRITY — honest wording: the installer fetches `checksums.txt` from the SAME
# release it downloads the binary from, looks up this target's SHA256 in it, and
# verifies the download against that hash. The network path is FAIL-CLOSED — an
# unfetchable checksums.txt, no entry for the selected target, or a hash mismatch
# each abort the install with nothing written. Verification catches corruption or
# tampering IN TRANSIT. It is NOT a full security guarantee: checksums.txt ships from
# the same unauthenticated source as the binary, so a compromised source could serve
# a matching pair. Treat this as a personal-trust convenience, not a signature — for
# a real signature, verify the binary's build-provenance attestation (see README).
#
# FERRY_FAKE_BINARY=/path/to/binary — TEST-ONLY override: install that explicit real
# file instead of downloading. No implicit CWD/bin fallback; no placeholder.
# FERRY_RELEASE_BASE_URL=<url> — TEST-ONLY override: fetch the binary and
# checksums.txt from <url>/ instead of the GitHub release, so the fetch+verify path
# can be exercised offline. file:// URLs ONLY (anything else is refused) and its
# use is announced on stdout. Unset in normal use.
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

# Resolve the release base URL: the binary and its checksums.txt come from the SAME
# release. FERRY_RELEASE_BASE_URL (TEST-ONLY) overrides the GitHub location so the
# fetch+verify path can run offline against a local release layout. It is
# restricted to file:// URLs — a remote override would silently redirect the entire
# trust root (the binary AND the checksums it is verified against), so anything
# else is refused, and an active override is always announced loudly.
if [ -n "${FERRY_RELEASE_BASE_URL:-}" ]; then
  case "$FERRY_RELEASE_BASE_URL" in
    file://*) ;;
    *)
      echo "ferry install: FERRY_RELEASE_BASE_URL is TEST-ONLY and must be a file:// URL; refusing '${FERRY_RELEASE_BASE_URL}'" >&2
      exit 1
      ;;
  esac
  echo "ferry install: TEST-ONLY release-source override active: ${FERRY_RELEASE_BASE_URL}"
  base_url="${FERRY_RELEASE_BASE_URL%/}"
elif [ "$VERSION" = "latest" ]; then
  base_url="https://github.com/${REPO}/releases/latest/download"
else
  base_url="https://github.com/${REPO}/releases/download/${VERSION}"
fi

# Filled from the release's checksums.txt on the network path only.
expected_sha=""

tmp="$(mktemp)"
sums="$(mktemp)"
trap 'rm -f "$tmp" "$sums"' EXIT

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
  # Public network path. Fetch checksums.txt from the SAME release FIRST and fail
  # closed if it is missing or unfetchable — a binary is never installed unverified.
  echo "ferry install: fetching checksums.txt (${VERSION})..."
  if ! curl -fsSL "${base_url}/checksums.txt" -o "$sums"; then
    echo "ferry install: could not fetch checksums.txt from the release; refusing to install an unverified binary" >&2
    exit 1
  fi
  # Look up this target's hash. checksums.txt lines are `<sha256>  <asset>`.
  # Strip a trailing CR so a CRLF-mangled manifest still diagnoses correctly
  # (the hash comparison below is what gates the install, not this lookup).
  expected_sha="$(awk -v a="$asset" '{ sub(/\r$/, "", $2) } $2 == a { print $1; exit }' "$sums")"
  if [ -z "$expected_sha" ]; then
    echo "ferry install: checksums.txt has no entry for ${asset}; refusing to install" >&2
    exit 1
  fi
  echo "ferry install: downloading ${asset} (${VERSION})..."
  curl -fsSL "${base_url}/${asset}" -o "$tmp"
  from_network=1
fi

# Integrity: verify the download against the checksums.txt hash (network path only).
# The network path aborts above if checksums.txt or this target's entry is missing,
# so a downloaded binary is ALWAYS verified before install. The FERRY_FAKE_BINARY
# test artifact is a real local file the caller chose, not a release download, so it
# is installed verbatim (checksums.txt describes the published binaries only).
if [ "$from_network" = "1" ]; then
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
  # Show ferry's banner (product identity + next-step hints) by running the
  # just-installed binary with no args. Bare `ferry` only prints the banner
  # (cmd/root.go) — it reads no config and never prompts — so this is the single
  # source of truth for the ASCII art rather than duplicating it here. A cosmetic
  # banner must never fail-close a successful install, so any non-zero exit is
  # ignored. The banner prints no `export PATH` line, so the exactly-one-PATH-line
  # contract (evals: AC-install-prints-path-line) is preserved.
  "$BIN_DIR/ferry" || true
fi
