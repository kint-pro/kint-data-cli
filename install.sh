#!/bin/sh
set -e

REPO="kint-pro/kint-data-cli"
BINARY="kint-data"
INSTALL_DIR="/usr/local/bin"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" && exit 1 ;;
esac

VERSION=$(curl -sfL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | sed 's/.*"v//;s/".*//')
if [ -z "$VERSION" ]; then
  echo "Failed to fetch latest version" && exit 1
fi

ARCHIVE="kint-data-cli_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/v${VERSION}"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

echo "Downloading kint-data v${VERSION} (${OS}/${ARCH})..."
curl -sfL "$BASE/$ARCHIVE" -o "$TMPDIR/$ARCHIVE"
curl -sfL "$BASE/checksums.txt" -o "$TMPDIR/checksums.txt"

EXPECTED=$(grep " $ARCHIVE\$" "$TMPDIR/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "$TMPDIR/$ARCHIVE" | cut -d' ' -f1)
else
  ACTUAL=$(shasum -a 256 "$TMPDIR/$ARCHIVE" | cut -d' ' -f1)
fi
if [ -z "$EXPECTED" ] || [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "Checksum verification failed for $ARCHIVE" && exit 1
fi

tar -xz -C "$TMPDIR" -f "$TMPDIR/$ARCHIVE"

if [ -w "$INSTALL_DIR" ]; then
  mv "$TMPDIR/$BINARY" "$INSTALL_DIR/$BINARY"
else
  sudo mv "$TMPDIR/$BINARY" "$INSTALL_DIR/$BINARY"
fi

echo "Installed $BINARY v${VERSION} to $INSTALL_DIR/$BINARY"
echo
echo 'Tip: alias kd="kint-data"'
