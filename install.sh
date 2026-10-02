#!/bin/sh
set -e

# Every knob is a CPB_INSTALL_* variable: CPB_INSTALL_VERSION (a tag),
# CPB_INSTALL_DIR, CPB_INSTALL_DEFAULT_DIR, CPB_INSTALL_REPO,
# CPB_INSTALL_ASSET_PREFIX, CPB_INSTALL_DOWNLOAD_BASE, CPB_INSTALL_URL.
REPO="${CPB_INSTALL_REPO:-ramazanpolat/claude-playbooks}"
ASSET_PREFIX="${CPB_INSTALL_ASSET_PREFIX:-cpb}"
DEFAULT_INSTALL_DIR="${CPB_INSTALL_DEFAULT_DIR:-/usr/local/bin}"

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

if [ -n "${CPB_INSTALL_VERSION:-}" ]; then
  LATEST="$CPB_INSTALL_VERSION"
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

DOWNLOAD_BASE_URL="${CPB_INSTALL_DOWNLOAD_BASE:-https://github.com/${REPO}/releases/download}"
URL="${CPB_INSTALL_URL:-${DOWNLOAD_BASE_URL}/${LATEST}/${ASSET}}"

echo "Installing cpb from ${ASSET} ${LATEST} (${OS}/${ARCH})..."
TMP_FILE=$(mktemp "${TMPDIR:-/tmp}/cpb.XXXXXX")
trap 'if [ -n "$TMP_FILE" ]; then rm -f "$TMP_FILE"; fi' EXIT HUP INT TERM

curl -fsSL "$URL" -o "$TMP_FILE"

# Verify against the release's SHA256SUMS. A genuine mismatch always
# aborts; every unverifiable case (no sums published, malformed entry, no
# sha256 tool, CPB_INSTALL_URL override) warns and continues — the sums travel
# over the same channel as the binary, so they guard against corruption
# and truncation, not a compromised host.
if [ -n "${CPB_INSTALL_URL:-}" ]; then
  echo "Warning: CPB_INSTALL_URL override in use; skipping checksum verification"
else
  SUMS=$(curl -fsSL "${DOWNLOAD_BASE_URL}/${LATEST}/SHA256SUMS" 2>/dev/null || true)
  if [ -n "$SUMS" ]; then
    # Lowercased: sums files may carry uppercase hex, the tools emit lower.
    # sha256sum text mode writes "hash  name"; --binary mode "hash *name".
    want=$(printf '%s\n' "$SUMS" | awk -v a="$ASSET" '$2==a || $2=="*"a {print $1}' | tr 'A-F' 'a-f')
    matches=$(printf '%s\n' "$SUMS" | awk -v a="$ASSET" '$2==a || $2=="*"a' | grep -c . || true)
    if command -v sha256sum >/dev/null 2>&1; then
      got=$(sha256sum "$TMP_FILE" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
      got=$(shasum -a 256 "$TMP_FILE" | awk '{print $1}')
    else
      got=""
    fi
    if [ -z "$want" ]; then
      echo "Warning: ${ASSET} not listed in SHA256SUMS; skipping checksum verification"
    elif [ "$matches" != "1" ] || ! printf '%s' "$want" | grep -qiE '^[0-9a-f]{64}$'; then
      # A truncated or duplicated entry must not fail a legitimate binary
      # as "mismatch" — it is unverifiable, not wrong.
      echo "Warning: malformed SHA256SUMS entry for ${ASSET}; skipping checksum verification"
    elif [ -z "$got" ]; then
      echo "Warning: no sha256 tool found; skipping checksum verification"
    elif [ "$want" != "$got" ]; then
      echo "Error: checksum mismatch for ${ASSET} ${LATEST}"
      echo "  expected: $want"
      echo "  got:      $got"
      exit 1
    else
      echo "Checksum verified (sha256)."
    fi
  else
    echo "Warning: no SHA256SUMS published for ${LATEST}; skipping checksum verification"
  fi
fi

chmod +x "$TMP_FILE"

# Install to CPB_INSTALL_DIR when set, otherwise /usr/local/bin if writable, otherwise ~/.local/bin.
INSTALL_DIR="${CPB_INSTALL_DIR:-}"
if [ -n "$INSTALL_DIR" ]; then
  mkdir -p "$INSTALL_DIR"
elif [ -w "$DEFAULT_INSTALL_DIR" ]; then
  INSTALL_DIR="$DEFAULT_INSTALL_DIR"
else
  INSTALL_DIR="$HOME/.local/bin"
  mkdir -p "$INSTALL_DIR"
fi

# Want another name? Use a shell alias or a hard link: a symlink under any
# other name dispatches as a playbook launcher.
mv "$TMP_FILE" "$INSTALL_DIR/cpb"
TMP_FILE=""

echo ""
echo "Installed to $INSTALL_DIR/cpb"

# Warn if install dir is not on PATH.
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Warning: $INSTALL_DIR is not on your PATH. Add this to your shell config:" \
     && echo "  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac

# The installer never edits shell rc files — your dotfiles are yours.
echo ""
echo "Optional: enable shell completions by adding ONE of these lines to your rc file:"
echo "  echo 'source <(cpb completion zsh)'  >> ~/.zshrc     # zsh"
echo "  echo 'source <(cpb completion bash)' >> ~/.bashrc    # bash"

echo ""
echo "Done. Run: cpb --help"
