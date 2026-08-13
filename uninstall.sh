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

# Remove playbook launcher symlinks (v2.13.0+): symlinks in dir whose literal
# target names the binary being uninstalled. Launchers point at the stable
# PATH entry by name, so a literal-target basename match identifies them
# without resolving chains. The CLI entries themselves (INSTALL_NAME, cpb)
# are handled by remove_target, not here.
remove_launchers_in() {
  dir="$1"
  [ -d "$dir" ] || return 0
  for link in "$dir"/*; do
    [ -L "$link" ] || continue
    base=$(basename "$link")
    [ "$base" = "$INSTALL_NAME" ] && continue
    [ "$base" = "cpb" ] && continue
    dest=$(readlink "$link" 2>/dev/null) || continue
    destbase=$(basename "$dest")
    if [ "$destbase" = "$INSTALL_NAME" ] || [ "$destbase" = "cpb" ] || [ "$destbase" = "claude-playbook" ]; then
      rm -f "$link"
      echo "Removed launcher $link"
      REMOVED=1
    fi
  done
}

# Remove the completion lines install.sh appended to shell rc files; without
# this every new shell errors with 'command not found' after uninstall.
remove_completion_lines() {
  rc_file="$1"
  [ -f "$rc_file" ] || return 0
  changed=0
  for name in "$INSTALL_NAME" cpb; do
    for shell_type in bash zsh; do
      line="source <($name completion $shell_type)"
      if grep -qxF "$line" "$rc_file"; then
        changed=1
      fi
    done
  done
  [ "$changed" -eq 1 ] || return 0
  tmp=$(mktemp "${rc_file}.tmp.XXXXXX")
  grep -vxF -e "source <($INSTALL_NAME completion bash)" \
            -e "source <($INSTALL_NAME completion zsh)" \
            -e "source <(cpb completion bash)" \
            -e "source <(cpb completion zsh)" \
            "$rc_file" > "$tmp" || true
  mv "$tmp" "$rc_file"
  echo "Removed completion lines from $rc_file"
}

REMOVED=0

if [ -n "${INSTALL_DIR:-}" ]; then
  remove_launchers_in "$INSTALL_DIR"
  remove_launchers_in "$HOME/.local/bin"
  remove_target "$INSTALL_DIR/$INSTALL_NAME"
  if [ "$INSTALL_NAME" = "claude-playbook" ]; then
    remove_target "$INSTALL_DIR/cpb"
  fi
else
  remove_launchers_in "$DEFAULT_INSTALL_DIR"
  remove_launchers_in "$HOME/.local/bin"
  remove_target "$DEFAULT_INSTALL_DIR/$INSTALL_NAME"
  remove_target "$HOME/.local/bin/$INSTALL_NAME"
  if [ "$INSTALL_NAME" = "claude-playbook" ]; then
    remove_target "$DEFAULT_INSTALL_DIR/cpb"
    remove_target "$HOME/.local/bin/cpb"
  fi

  FOUND=$(command -v "$INSTALL_NAME" 2>/dev/null || true)
  remove_target "$FOUND"
fi

remove_completion_lines "$HOME/.bashrc"
remove_completion_lines "$HOME/.zshrc"

if [ "$REMOVED" -eq 0 ]; then
  echo "$INSTALL_NAME was not found in the expected install locations."
fi

echo ""
echo "Playbooks were not touched: ${CLAUDE_PLAYBOOKS_DIR:-$HOME/.claude-playbooks}"
