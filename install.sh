#!/bin/sh
set -e

REPO="ramazanpolat/claude-playbooks"
BINARY_NAME="claude-playbook"
DEFAULT_INSTALL_DIR="/usr/local/bin"

# Detect OS.
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin|linux) ;;
  *) echo "Error: unsupported OS: $OS" && exit 1 ;;
esac

# Detect architecture.
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)         ARCH="amd64" ;;
  aarch64|arm64)  ARCH="arm64" ;;
  *) echo "Error: unsupported architecture: $ARCH" && exit 1 ;;
esac

ASSET="${BINARY_NAME}-${OS}-${ARCH}"

# Fetch latest release tag.
echo "Fetching latest release..."
LATEST=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep '"tag_name"' | head -1 | cut -d'"' -f4)

if [ -z "$LATEST" ]; then
  echo "Error: could not determine latest release. Check your internet connection."
  exit 1
fi

URL="https://github.com/${REPO}/releases/download/${LATEST}/${ASSET}"

echo "Installing ${BINARY_NAME} ${LATEST} (${OS}/${ARCH})..."
curl -fsSL "$URL" -o /tmp/"$BINARY_NAME"
chmod +x /tmp/"$BINARY_NAME"

# Install to /usr/local/bin if writable, otherwise ~/.local/bin.
if [ -w "$DEFAULT_INSTALL_DIR" ]; then
  INSTALL_DIR="$DEFAULT_INSTALL_DIR"
else
  INSTALL_DIR="$HOME/.local/bin"
  mkdir -p "$INSTALL_DIR"
fi

mv /tmp/"$BINARY_NAME" "$INSTALL_DIR/$BINARY_NAME"

echo ""
echo "Installed to $INSTALL_DIR/$BINARY_NAME"

# Warn if install dir is not on PATH.
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Warning: $INSTALL_DIR is not on your PATH. Add this to your shell config:" \
     && echo "  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac

echo ""
echo "Done. Run: $BINARY_NAME --help"
