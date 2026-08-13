#!/bin/sh
set -e

REPO="${REPO:-ramazanpolat/claude-playbooks}"
ASSET_PREFIX="${ASSET_PREFIX:-claude-playbook}"
DEFAULT_INSTALL_DIR="${DEFAULT_INSTALL_DIR:-/usr/local/bin}"

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

ASSET="${ASSET_PREFIX}-${OS}-${ARCH}"

if [ -n "${VERSION:-}" ]; then
  LATEST="$VERSION"
else
  # Fetch latest release tag.
  echo "Fetching latest release..."
  LATEST=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | cut -d'"' -f4)
fi

if [ -z "$LATEST" ]; then
  echo "Error: could not determine latest release. Check your internet connection."
  exit 1
fi

DOWNLOAD_BASE_URL="${DOWNLOAD_BASE_URL:-https://github.com/${REPO}/releases/download}"
URL="${INSTALL_URL:-${DOWNLOAD_BASE_URL}/${LATEST}/${ASSET}}"

echo "Installing claude-playbook from ${ASSET} ${LATEST} (${OS}/${ARCH})..."
TMP_FILE=$(mktemp "${TMPDIR:-/tmp}/claude-playbook.XXXXXX")
trap 'if [ -n "$TMP_FILE" ]; then rm -f "$TMP_FILE"; fi' EXIT HUP INT TERM

curl -fsSL "$URL" -o "$TMP_FILE"
chmod +x "$TMP_FILE"

# Install to INSTALL_DIR when set, otherwise /usr/local/bin if writable, otherwise ~/.local/bin.
if [ -n "${INSTALL_DIR:-}" ]; then
  mkdir -p "$INSTALL_DIR"
elif [ -w "$DEFAULT_INSTALL_DIR" ]; then
  INSTALL_DIR="$DEFAULT_INSTALL_DIR"
else
  INSTALL_DIR="$HOME/.local/bin"
  mkdir -p "$INSTALL_DIR"
fi

mv "$TMP_FILE" "$INSTALL_DIR/claude-playbook"
TMP_FILE=""

# cpb is the short name for the same binary. Want another name? Create your
# own alias or symlink.
rm -f "$INSTALL_DIR/cpb"
ln -s "claude-playbook" "$INSTALL_DIR/cpb"
echo "Created symlink $INSTALL_DIR/cpb -> claude-playbook"

echo ""
echo "Installed to $INSTALL_DIR/claude-playbook"

# Warn if install dir is not on PATH.
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Warning: $INSTALL_DIR is not on your PATH. Add this to your shell config:" \
     && echo "  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac

# Install shell completions
install_completion() {
  local rc_file="$1"
  local shell_type="$2"
  if [ -f "$rc_file" ]; then
    for name in claude-playbook cpb; do
      if ! grep -q "$name completion $shell_type" "$rc_file"; then
        echo "source <($name completion $shell_type)" >> "$rc_file"
        echo "Added $shell_type completion for $name to $rc_file"
      fi
    done
  fi
}

echo ""
echo "Setting up shell completions..."
install_completion "$HOME/.bashrc" "bash"
install_completion "$HOME/.zshrc" "zsh"

echo ""
echo "Done. Run: claude-playbook --help (or cpb --help)"
echo "(You may need to restart your terminal or run 'source ~/.bashrc' or 'source ~/.zshrc' for completions to take effect)"
