#!/bin/bash
set -e

# Quiver (qvr) installer
# Usage:
#   curl -sSL https://raw.githubusercontent.com/quiver-cli/qvr/main/scripts/install.sh | sh
#
# Downloads the latest release binary for your OS/arch from GitHub
# Releases. Falls back to `go install github.com/quiver-cli/qvr@latest`
# when no release tarball is available.

REPO="quiver-cli/qvr"
INSTALL_DIR="/usr/local/bin"
BINARY="qvr"

# Detect OS and architecture
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
  *)       echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

case "$OS" in
  linux|darwin) ;;
  *)            echo "Unsupported OS: $OS" >&2; exit 1 ;;
esac

# Get latest release tag
echo "Fetching latest release..." >&2
LATEST=$(curl -sSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "$LATEST" ]; then
  echo "Could not determine latest version. Building from source..." >&2
  if command -v go &>/dev/null; then
    go install "github.com/${REPO}@latest"
    GOBIN="$(go env GOPATH)/bin"
    if [ -f "${GOBIN}/qvr" ]; then
      sudo cp "${GOBIN}/qvr" "${INSTALL_DIR}/qvr" 2>/dev/null || cp "${GOBIN}/qvr" "${INSTALL_DIR}/qvr"
      echo "Installed qvr to ${INSTALL_DIR}/qvr (built from source)" >&2
      exit 0
    fi
  fi
  echo "Go not found. Install Go first: https://go.dev/dl/" >&2
  exit 1
fi

# Download binary
URL="https://github.com/${REPO}/releases/download/${LATEST}/qvr_${LATEST#v}_${OS}_${ARCH}.tar.gz"
echo "Downloading qvr ${LATEST} for ${OS}/${ARCH}..." >&2

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

curl -sSL "$URL" -o "${TMP}/qvr.tar.gz"
tar -xzf "${TMP}/qvr.tar.gz" -C "$TMP"

# Install
if [ -w "$INSTALL_DIR" ]; then
  mv "${TMP}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
else
  sudo mv "${TMP}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
fi

chmod +x "${INSTALL_DIR}/${BINARY}"

echo "Installed qvr ${LATEST} to ${INSTALL_DIR}/${BINARY}" >&2
echo "Run 'qvr --help' to get started." >&2
