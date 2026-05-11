#!/bin/sh
set -e

INSTALL_NAME="${INSTALL_NAME:-${BINARY_NAME:-claude-playbook}}"
DEFAULT_INSTALL_DIR="${DEFAULT_INSTALL_DIR:-/usr/local/bin}"

case "$INSTALL_NAME" in
  ""|*/*)
    echo "Error: INSTALL_NAME must be a command name, not a path"
    exit 1
    ;;
esac

remove_target() {
  target="$1"
  if [ -z "$target" ]; then
    return 0
  fi
  case "$target" in
    /*) ;;
    *) return 0 ;;
  esac
  if [ ! -e "$target" ] && [ ! -L "$target" ]; then
    return 0
  fi

  rm -f "$target"
  echo "Removed $target"
  REMOVED=1
}

REMOVED=0

if [ -n "${INSTALL_DIR:-}" ]; then
  remove_target "$INSTALL_DIR/$INSTALL_NAME"
else
  remove_target "$DEFAULT_INSTALL_DIR/$INSTALL_NAME"
  remove_target "$HOME/.local/bin/$INSTALL_NAME"

  FOUND=$(command -v "$INSTALL_NAME" 2>/dev/null || true)
  remove_target "$FOUND"
fi

if [ "$REMOVED" -eq 0 ]; then
  echo "$INSTALL_NAME was not found in the expected install locations."
fi

echo ""
echo "Playbooks were not touched: ${CLAUDE_PLAYBOOKS_DIR:-$HOME/.claude-playbooks}"
