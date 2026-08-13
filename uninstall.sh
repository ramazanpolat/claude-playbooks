#!/bin/sh
set -e

DEFAULT_INSTALL_DIR="${DEFAULT_INSTALL_DIR:-/usr/local/bin}"

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

# Fully resolve a path through symlinks. Fails (printing nothing) when the
# path cannot be resolved — a dangling link, or a system with neither
# readlink -f nor realpath. Callers treat failure as "not ours" and leave
# the file alone.
resolve_path() {
  readlink -f "$1" 2>/dev/null && return 0
  realpath "$1" 2>/dev/null && return 0
  return 1
}

# Remove playbook launcher symlinks (v2.13.0+): only links that RESOLVE to
# the binary being uninstalled are ours. Matching on the target's basename
# would delete a user's own `foo -> /elsewhere/cpb`; a dangling link is
# never ours either, because the binary still exists while this runs. The
# CLI entries themselves (claude-playbook, cpb) are handled by
# remove_target, not here.
remove_launchers_in() {
  dir="$1"
  bin_real="$2"
  [ -d "$dir" ] || return 0
  [ -n "$bin_real" ] || return 0
  for link in "$dir"/*; do
    [ -L "$link" ] || continue
    base=$(basename "$link")
    [ "$base" = "claude-playbook" ] && continue
    [ "$base" = "cpb" ] && continue
    link_real=$(resolve_path "$link") || continue
    if [ "$link_real" = "$bin_real" ]; then
      rm -f "$link"
      echo "Removed launcher $link"
      REMOVED=1
    fi
  done
}

# Remove one installation: the launchers resolving to this binary (both in
# its own directory and in the ~/.local/bin fallback), the cpb symlink
# beside it, then the binary itself.
remove_install_at() {
  bin="$1"
  if [ -e "$bin" ] || [ -L "$bin" ]; then
    if bin_real=$(resolve_path "$bin"); then
      remove_launchers_in "$(dirname "$bin")" "$bin_real"
      remove_launchers_in "$HOME/.local/bin" "$bin_real"
    else
      echo "Note: cannot resolve symlinks on this system; launchers near $bin were not swept." >&2
    fi
  fi
  remove_target "$(dirname "$bin")/cpb"
  remove_target "$bin"
}

# Remove the completion lines install.sh appended to shell rc files; without
# this every new shell errors with 'command not found' after uninstall.
remove_completion_lines() {
  rc_file="$1"
  [ -f "$rc_file" ] || return 0
  changed=0
  for name in claude-playbook cpb; do
    for shell_type in bash zsh; do
      line="source <($name completion $shell_type)"
      if grep -qxF "$line" "$rc_file"; then
        changed=1
      fi
    done
  done
  [ "$changed" -eq 1 ] || return 0
  # Edit the SYMLINK TARGET, atomically: filter into a same-directory temp
  # file, restore the original's mode, then rename over the target. The
  # original survives any failure in between, and a stow/chezmoi-symlinked
  # ~/.zshrc keeps its symlink because only the target is replaced.
  target="$rc_file"
  if [ -L "$rc_file" ]; then
    target=$(resolve_path "$rc_file") || {
      echo "Note: cannot resolve symlinked $rc_file on this system; completion lines left in place." >&2
      return 0
    }
  fi
  tmp=$(mktemp "${target}.cpb.XXXXXX") || return 0
  st=0
  grep -vxF -e "source <(claude-playbook completion bash)" \
            -e "source <(claude-playbook completion zsh)" \
            -e "source <(cpb completion bash)" \
            -e "source <(cpb completion zsh)" \
            "$target" > "$tmp" || st=$?
  # grep exits 1 when every line was filtered out (valid); >1 is a real
  # read or write error — keep the original untouched.
  if [ "$st" -gt 1 ]; then
    rm -f "$tmp"
    echo "Warning: could not filter $rc_file; left unchanged." >&2
    return 0
  fi
  mode=$(stat -f %Lp "$target" 2>/dev/null || stat -c %a "$target" 2>/dev/null) || mode=""
  [ -n "$mode" ] && chmod "$mode" "$tmp"
  mv -f "$tmp" "$target"
  echo "Removed completion lines from $rc_file"
}

REMOVED=0

if [ -n "${INSTALL_DIR:-}" ]; then
  remove_install_at "$INSTALL_DIR/claude-playbook"
else
  remove_install_at "$DEFAULT_INSTALL_DIR/claude-playbook"
  remove_install_at "$HOME/.local/bin/claude-playbook"

  FOUND=$(command -v claude-playbook 2>/dev/null || true)
  if [ -n "$FOUND" ]; then
    remove_install_at "$FOUND"
  fi
fi

remove_completion_lines "$HOME/.bashrc"
remove_completion_lines "$HOME/.zshrc"

if [ "$REMOVED" -eq 0 ]; then
  echo "claude-playbook was not found in the expected install locations."
fi

echo ""
echo "Playbooks were not touched: ${CLAUDE_PLAYBOOKS_DIR:-$HOME/.claude-playbooks}"
