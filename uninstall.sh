#!/bin/sh
set -e

DEFAULT_INSTALL_DIR="${DEFAULT_INSTALL_DIR:-/usr/local/bin}"

# Locate the installed binary.
BIN=""
if [ -n "${INSTALL_DIR:-}" ]; then
  [ -x "$INSTALL_DIR/claude-playbook" ] && BIN="$INSTALL_DIR/claude-playbook"
else
  for c in "$DEFAULT_INSTALL_DIR/claude-playbook" "$HOME/.local/bin/claude-playbook"; do
    if [ -x "$c" ]; then
      BIN="$c"
      break
    fi
  done
  if [ -z "$BIN" ]; then
    BIN=$(command -v claude-playbook 2>/dev/null || true)
  fi
fi

# The uninstall logic lives in ONE place: the binary itself. Launcher
# ownership, the cpb sibling, and completion-line cleanup all need the same
# judgment the CLI already implements and tests — duplicating it in shell is
# how uninstallers grow divergent bugs.
if [ -n "$BIN" ] && "$BIN" --version >/dev/null 2>&1; then
  exec "$BIN" self-uninstall --binary-only --yes
fi

# Fallback: no runnable binary. Remove only the two literal artifacts
# install.sh writes; nothing here guesses at ownership.
REMOVED=0
remove_artifacts_in() {
  dir="$1"
  [ -n "$dir" ] || return 0
  # cpb is ours only as the exact `cpb -> claude-playbook` link install.sh
  # creates (dangling included); anything else under that name is foreign.
  if [ -L "$dir/cpb" ] && [ "$(readlink "$dir/cpb" 2>/dev/null)" = "claude-playbook" ]; then
    rm -f "$dir/cpb"
    echo "Removed $dir/cpb"
    REMOVED=1
  fi
  if [ -e "$dir/claude-playbook" ] || [ -L "$dir/claude-playbook" ]; then
    rm -f "$dir/claude-playbook"
    echo "Removed $dir/claude-playbook"
    REMOVED=1
  fi
}

if [ -n "${INSTALL_DIR:-}" ]; then
  remove_artifacts_in "$INSTALL_DIR"
else
  remove_artifacts_in "$DEFAULT_INSTALL_DIR"
  remove_artifacts_in "$HOME/.local/bin"
fi

if [ "$REMOVED" -eq 0 ]; then
  echo "claude-playbook was not found in the expected install locations."
else
  echo "Note: the binary could not be run, so launcher symlinks and any"
  echo "completion lines in your shell rc files were not cleaned up."
fi

echo ""
echo "Playbooks were not touched: ${CLAUDE_PLAYBOOKS_DIR:-$HOME/.claude-playbooks}"
