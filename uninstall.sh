#!/bin/sh
set -e

CPB_INSTALL_DEFAULT_DIR="${CPB_INSTALL_DEFAULT_DIR:-/usr/local/bin}"

# The uninstall logic lives in ONE place: the binary itself. Launcher
# ownership and completion-line cleanup need the same
# judgment the CLI already implements and tests — duplicating it in shell is
# how uninstallers grow divergent bugs. This script only decides WHICH
# binaries to hand the job to, and keeps a literal-artifact fallback for a
# binary that is broken or too old to know --binary-only.

# Fallback: remove only the literal artifact install.sh writes; nothing
# here guesses at ownership.
REMOVED=0
remove_artifacts_in() {
  dir="$1"
  [ -n "$dir" ] || return 0
  if [ -e "$dir/cpb" ] || [ -L "$dir/cpb" ]; then
    rm -f "$dir/cpb"
    echo "Removed $dir/cpb"
    REMOVED=1
  fi
}

# Candidate binaries. The PATH-active one comes first — it defines what the
# user actually runs — then the fixed install locations, so a dual install
# (e.g. ~/.local/bin plus a later sudo install) is cleaned everywhere, as
# the pre-delegation script did.
if [ -n "${CPB_INSTALL_DIR:-}" ]; then
  set -- "$CPB_INSTALL_DIR/cpb"
else
  set -- "$(command -v cpb 2>/dev/null || true)" \
         "$CPB_INSTALL_DEFAULT_DIR/cpb" \
         "$HOME/.local/bin/cpb"
fi

DELEGATED=0
FELL_BACK=0
HANDLED=0
DONE=""
for BIN in "$@"; do
  [ -n "$BIN" ] || continue
  [ -e "$BIN" ] || [ -L "$BIN" ] || continue
  case "$DONE" in *"|$BIN|"*) continue ;; esac
  DONE="$DONE|$BIN|"
  HANDLED=1
  # Delegate only to a binary that runs AND knows --binary-only; an older
  # release passes --version yet would die on the unknown flag, so probe
  # its help text first instead of exec-ing into a guaranteed failure.
  if [ -x "$BIN" ] && "$BIN" --version >/dev/null 2>&1 \
      && "$BIN" self-uninstall --help 2>/dev/null | grep -q -- "--binary-only"; then
    "$BIN" self-uninstall --binary-only --yes
    DELEGATED=1
    # The candidate may be a symlink whose physical target the delegated
    # uninstall just removed (package-manager-style indirection): the
    # reserved name is excluded from the launcher sweep, so clear the
    # now-dangling PATH entry here. Only a dangling link is touched: if the
    # binary survived (e.g. permission denied), it still resolves and stays.
    if [ -L "$BIN" ] && [ ! -e "$BIN" ]; then
      rm -f "$BIN"
      echo "Removed $BIN"
    fi
  else
    remove_artifacts_in "$(dirname "$BIN")"
    FELL_BACK=1
  fi
done

if [ "$HANDLED" -eq 0 ]; then
  if [ -n "${CPB_INSTALL_DIR:-}" ]; then
    remove_artifacts_in "$CPB_INSTALL_DIR"
  else
    remove_artifacts_in "$CPB_INSTALL_DEFAULT_DIR"
    remove_artifacts_in "$HOME/.local/bin"
  fi
fi

if [ "$DELEGATED" -eq 0 ] && [ "$REMOVED" -eq 0 ]; then
  echo "cpb was not found in the expected install locations."
fi
if [ "$FELL_BACK" -eq 1 ] && [ "$REMOVED" -eq 1 ]; then
  echo "Note: that binary could not run the built-in uninstaller, so launcher"
  echo "symlinks and any completion lines in your shell rc files were not"
  echo "cleaned up for it."
fi

if [ "$DELEGATED" -eq 0 ]; then
  echo ""
  echo "Playbooks were not touched: ${CPB_PLAYBOOKS_DIR:-$HOME/.claude-playbooks}"
fi
