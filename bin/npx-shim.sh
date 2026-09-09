#!/bin/sh
# npx entry point for claude-playbook.
#
#   npx github:ramazanpolat/claude-playbooks <args...>
#   npx cpb <args...>                        (when published to npm)
#
# Behavior:
#   1. claude-playbook/cpb already on PATH -> exec it. The installed binary
#      is the source of truth; npx just routes to it. No download, no
#      version games: update the installed one with `cpb self-update`.
#   2. Not installed (first run) -> bootstrap: download the release binary,
#      verify against the release's SHA256SUMS, install to ~/.local/bin
#      (user-owned only -- never /usr/local/bin, never sudo), create the
#      cpb link, announce it, exec it.
#   3. CPB_NPX_BOOTSTRAP=0 -> no install, no delegation: fetch (or reuse
#      cache) under ~/.claude-playbooks/bin/<tag>/ and exec from there.
#      Ephemeral mode; also the way to test a pinned version alongside an
#      installed one:
#        CPB_NPX_BOOTSTRAP=0 CPB_VERSION=v3.8.0 npx cpb --version
#
# Version resolution for downloads, first match wins:
#   1. CPB_VERSION env (e.g. "v3.9.1") -- escape hatch / testing
#   2. this package's version (npm_package_version under npx) -- so
#      `npx github:...#v3.9.1` runs that release, not latest. Bump
#      package.json "version" together with the release tag.
#   3. latest release from the GitHub API -- same as install.sh
set -e

REPO="${REPO:-ramazanpolat/claude-playbooks}"
ASSET_PREFIX="${ASSET_PREFIX:-claude-playbook}"
DOWNLOAD_BASE_URL="${DOWNLOAD_BASE_URL:-https://github.com/${REPO}/releases/download}"
CACHE_ROOT="${CPB_NPX_CACHE:-$HOME/.claude-playbooks/bin}"
INSTALL_DIR="${CPB_NPX_INSTALL_DIR:-$HOME/.local/bin}"

# Detect OS.
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin|linux) ;;
  *) echo "Error: unsupported OS: $OS (native Windows is not supported; use WSL)" >&2 && exit 1 ;;
esac

# Detect architecture.
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)         ARCH="amd64" ;;
  aarch64|arm64)  ARCH="arm64" ;;
  *) echo "Error: unsupported architecture: $ARCH" >&2 && exit 1 ;;
esac

ASSET="${ASSET_PREFIX}-${OS}-${ARCH}"

# Delegate to an installed claude-playbook. Skipped in ephemeral mode so a
# pinned CPB_VERSION can run beside the install.
if [ "${CPB_NPX_BOOTSTRAP:-}" != "0" ]; then
  if command -v cpb >/dev/null 2>&1; then
    exec "$(command -v cpb)" "$@"
  fi
  if command -v claude-playbook >/dev/null 2>&1; then
    exec "$(command -v claude-playbook)" "$@"
  fi
fi

# Resolve which release tag to fetch.
TAG=""
if [ -n "${CPB_VERSION:-}" ]; then
  TAG="$CPB_VERSION"
elif [ -n "${npm_package_version:-}" ] && [ "$npm_package_version" != "0.0.0" ]; then
  TAG="v$npm_package_version"
else
  TAG=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | cut -d'"' -f4)
fi
if [ -z "$TAG" ]; then
  echo "Error: could not determine which release to fetch. Check your internet connection," >&2
  echo "or pin one explicitly: CPB_VERSION=v3.9.1 npx github:${REPO} ..." >&2
  exit 1
fi

if [ "${CPB_NPX_BOOTSTRAP:-}" = "0" ]; then
  MODE="ephemeral"
  BIN_DIR="${CACHE_ROOT}/${TAG}"
else
  MODE="bootstrap"
  BIN_DIR="$INSTALL_DIR"
fi
BIN="${BIN_DIR}/claude-playbook"

if [ ! -x "$BIN" ]; then
  echo "Fetching claude-playbook ${TAG} (${OS}/${ARCH}) to ${BIN_DIR}..." >&2
  mkdir -p "$BIN_DIR"
  # mktemp inside the target dir: the final mv stays on one filesystem.
  TMP_FILE=$(mktemp "${BIN_DIR}/.claude-playbook.XXXXXX")
  trap 'if [ -n "$TMP_FILE" ]; then rm -f "$TMP_FILE"; fi' EXIT HUP INT TERM

  curl -fsSL "${DOWNLOAD_BASE_URL}/${TAG}/${ASSET}" -o "$TMP_FILE"

  # Verify against the release's SHA256SUMS. Same policy as install.sh:
  # a genuine mismatch always aborts; every unverifiable case (no sums
  # published, malformed entry, no sha256 tool) warns and continues — the
  # sums travel over the same channel as the binary, so they guard against
  # corruption and truncation, not a compromised host.
  SUMS=$(curl -fsSL "${DOWNLOAD_BASE_URL}/${TAG}/SHA256SUMS" 2>/dev/null || true)
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
      echo "Warning: ${ASSET} not listed in SHA256SUMS; skipping checksum verification" >&2
    elif [ "$matches" != "1" ] || ! printf '%s' "$want" | grep -qiE '^[0-9a-f]{64}$'; then
      # A truncated or duplicated entry must not fail a legitimate binary
      # as "mismatch" — it is unverifiable, not wrong.
      echo "Warning: malformed SHA256SUMS entry for ${ASSET}; skipping checksum verification" >&2
    elif [ -z "$got" ]; then
      echo "Warning: no sha256 tool found; skipping checksum verification" >&2
    elif [ "$want" != "$got" ]; then
      echo "Error: checksum mismatch for ${ASSET} ${TAG}" >&2
      echo "  expected: $want" >&2
      echo "  got:      $got" >&2
      exit 1
    else
      echo "Checksum verified (sha256)." >&2
    fi
  else
    echo "Warning: no SHA256SUMS published for ${TAG}; skipping checksum verification" >&2
  fi

  chmod +x "$TMP_FILE"
  mv "$TMP_FILE" "$BIN"
  TMP_FILE=""
  FETCHED=1
fi

if [ "$MODE" = "bootstrap" ]; then
  # cpb is the short name for the same binary. Relative link, exactly like
  # install.sh: a symlink to the binary under any other name is treated as
  # a playbook launcher and dispatched accordingly.
  rm -f "${BIN_DIR}/cpb"
  ln -s claude-playbook "${BIN_DIR}/cpb"

  if [ -n "${FETCHED:-}" ]; then
    echo "" >&2
    echo "Installed claude-playbook ${TAG} to ${BIN_DIR}" >&2
    echo "  Uninstall anytime: cpb self-uninstall --keep-data" >&2
  fi
  case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *) echo "Warning: $BIN_DIR is not on your PATH. Add this to your shell config:" >&2 \
       && echo "  export PATH=\"$BIN_DIR:\$PATH\"" >&2 ;;
  esac
fi

# Exec with argv[0] = the binary's own path: the multicall dispatch in the
# Go binary then behaves exactly like a direct claude-playbook invocation
# ("cpb" is only a short alias for the same root command).
exec "$BIN" "$@"
