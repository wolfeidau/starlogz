#!/usr/bin/env bash
set -euo pipefail

REPO="wolfeidau/starlogz"
PROJECT="starlogz"
BINARY="starlogz-server"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
VERSION="${VERSION:-}"

usage() {
  cat <<EOF
Usage: $0 [OPTIONS]

Install starlogz from GitHub releases.

Options:
  -v, --version VERSION   Release version to install (default: latest)
  -d, --dir DIR           Installation directory (default: /usr/local/bin)
  -h, --help              Show this help message
EOF
  exit 0
}

die() {
  echo "error: $*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -v|--version) VERSION="$2"; shift 2 ;;
    -d|--dir)     INSTALL_DIR="$2"; shift 2 ;;
    -h|--help)    usage ;;
    *) die "unknown option: $1" ;;
  esac
done

# Detect OS
OS="$(uname -s)"
case "$OS" in
  Darwin) OS="Darwin" ;;
  Linux)  OS="Linux" ;;
  *) die "unsupported OS: $OS" ;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)         ARCH="x86_64" ;;
  aarch64|arm64)  ARCH="arm64" ;;
  i386|i686)      ARCH="i386" ;;
  *) die "unsupported architecture: $ARCH" ;;
esac

# Resolve latest version if not specified
if [[ -z "$VERSION" ]]; then
  echo "Fetching latest release version..."
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' \
    | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
  [[ -n "$VERSION" ]] || die "failed to resolve latest version"
fi

echo "Installing ${PROJECT} ${VERSION} (${OS}/${ARCH}) to ${INSTALL_DIR}"

ARCHIVE="${PROJECT}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

# sha256 command differs between macOS and Linux
if command -v sha256sum &>/dev/null; then
  SHA256_CMD="sha256sum"
elif command -v shasum &>/dev/null; then
  SHA256_CMD="shasum -a 256"
else
  die "sha256sum or shasum is required"
fi

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

# Download archive and checksum file
echo "Downloading ${ARCHIVE}..."
curl -fsSL "${BASE_URL}/${ARCHIVE}" -o "${TMPDIR}/${ARCHIVE}"

CHECKSUM_FILE="${PROJECT}_${VERSION#v}_checksums.txt"
echo "Downloading ${CHECKSUM_FILE}..."
curl -fsSL "${BASE_URL}/${CHECKSUM_FILE}" -o "${TMPDIR}/${CHECKSUM_FILE}"

# Verify checksum
echo "Verifying checksum..."
EXPECTED="$(grep " ${ARCHIVE}$" "${TMPDIR}/${CHECKSUM_FILE}" | awk '{print $1}')"
[[ -n "$EXPECTED" ]] || die "checksum not found for ${ARCHIVE}"

ACTUAL="$(${SHA256_CMD} "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')"
[[ "$EXPECTED" == "$ACTUAL" ]] || die "checksum mismatch (expected ${EXPECTED}, got ${ACTUAL})"

# Extract and install
echo "Extracting..."
tar -xzf "${TMPDIR}/${ARCHIVE}" -C "${TMPDIR}"

[[ -f "${TMPDIR}/${BINARY}" ]] || die "binary not found in archive"
chmod +x "${TMPDIR}/${BINARY}"

echo "Installing to ${INSTALL_DIR}/${BINARY}..."
if [[ -w "$INSTALL_DIR" ]]; then
  mv "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
else
  sudo mv "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
fi

echo "Installed: $("${INSTALL_DIR}/${BINARY}" --version 2>/dev/null || echo "${INSTALL_DIR}/${BINARY}")"
