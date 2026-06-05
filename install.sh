#!/bin/sh
# qvr installer for Linux and macOS (also works under WSL / Git Bash on Windows).
#
#   curl -fsSL https://raw.githubusercontent.com/raks097/quiver/main/install.sh | sh
#
# Downloads the prebuilt release binary (UI embedded) for your OS/arch from
# GitHub Releases, verifies its checksum, and installs it to a directory on PATH.
#
# Env overrides:
#   QVR_VERSION   pin a version, e.g. v0.12.0 (default: latest release)
#   QVR_INSTALL_DIR   install location (default: /usr/local/bin, ~/.local/bin if unwritable)
set -eu

REPO="raks097/quiver"
BINARY="qvr"

info() { printf '\033[1;34m==>\033[0m %s\n' "$1"; }
err() { printf '\033[1;31merror:\033[0m %s\n' "$1" >&2; exit 1; }

# --- prerequisites --------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
  dl() { curl -fsSL "$1"; }
  dlo() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  dl() { wget -qO- "$1"; }
  dlo() { wget -qO "$2" "$1"; }
else
  err "need curl or wget"
fi

# --- detect platform ------------------------------------------------------
OS="$(uname -s)"
case "$OS" in
  Linux) OS="Linux" ;;
  Darwin) OS="Darwin" ;;
  *) err "unsupported OS '$OS' — use install.ps1 on native Windows" ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64 | amd64) ARCH="x86_64" ;;
  arm64 | aarch64) ARCH="arm64" ;;
  *) err "unsupported architecture '$ARCH'" ;;
esac

# --- resolve version ------------------------------------------------------
VERSION="${QVR_VERSION:-}"
if [ -z "$VERSION" ]; then
  info "Resolving latest release..."
  VERSION="$(dl "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name":' | head -n1 | cut -d'"' -f4)"
  [ -n "$VERSION" ] || err "could not determine latest version (set QVR_VERSION)"
fi

ASSET="${BINARY}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/${REPO}/releases/download/${VERSION}"
info "Installing ${BINARY} ${VERSION} (${OS}/${ARCH})"

# --- download + verify ----------------------------------------------------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
dlo "${BASE}/${ASSET}" "${TMP}/${ASSET}" || err "download failed: ${BASE}/${ASSET}"

dlo "${BASE}/checksums.txt" "${TMP}/checksums.txt" || err "checksums.txt unavailable"

if command -v sha256sum >/dev/null 2>&1; then
  SUM="$(sha256sum "${TMP}/${ASSET}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  SUM="$(shasum -a 256 "${TMP}/${ASSET}" | awk '{print $1}')"
else
  err "need sha256sum or shasum for checksum verification"
fi
grep -q "$SUM  $ASSET" "${TMP}/checksums.txt" \
  || err "checksum mismatch for ${ASSET}"
info "Checksum verified"

tar -xzf "${TMP}/${ASSET}" -C "$TMP"
[ -f "${TMP}/${BINARY}" ] || err "archive did not contain ${BINARY}"
chmod +x "${TMP}/${BINARY}"

# --- install --------------------------------------------------------------
DIR="${QVR_INSTALL_DIR:-/usr/local/bin}"
if [ ! -d "$DIR" ] || [ ! -w "$DIR" ]; then
  if [ -w "${DIR%/*}" ] 2>/dev/null; then
    mkdir -p "$DIR"
  else
    DIR="${HOME}/.local/bin"
    mkdir -p "$DIR"
  fi
fi

# Atomic replace via temp file + rename (a plain overwrite of a running binary
# corrupts its code-signing vnode on macOS).
TMP_BIN="${DIR}/.${BINARY}.tmp.$$"
if mv "${TMP}/${BINARY}" "$TMP_BIN" 2>/dev/null && mv -f "$TMP_BIN" "${DIR}/${BINARY}" 2>/dev/null; then
  :
else
  info "Elevating to write ${DIR} (sudo)"
  sudo mv "${TMP}/${BINARY}" "$TMP_BIN" && sudo mv -f "$TMP_BIN" "${DIR}/${BINARY}"
fi

info "Installed to ${DIR}/${BINARY}"
case ":${PATH}:" in
  *":${DIR}:"*) ;;
  *) printf '\033[1;33mnote:\033[0m %s is not on your PATH — add it:\n  export PATH="%s:$PATH"\n' "$DIR" "$DIR" ;;
esac
"${DIR}/${BINARY}" version || true
