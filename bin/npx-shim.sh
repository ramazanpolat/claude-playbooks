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
#
# The package's version can name a release that is not published yet: the
# release-prep commit is on main before its tag publishes. When that
# release's binary is missing (HTTP 404), the shim runs the newest
# published release instead and says so on stderr -- never an rc, never
# another major. An explicit CPB_VERSION is never replaced.
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
#
# Under npx, this package's own bin dir sits first on PATH, so a naive
# `command -v cpb` finds THIS SHIM (bin/cpb -> npx-shim.sh) and exec'ing it
# loops forever. Resolve symlinks on both sides and only delegate to a
# candidate that is a different file than this script.
resolve_symlinks() {
  _p="$1"
  while [ -L "$_p" ]; do
    _l=$(readlink "$_p")
    case "$_l" in
      /*) _p="$_l" ;;
      *)  _p="$(dirname "$_p")/$_l" ;;
    esac
  done
  printf '%s' "$_p"
}
SELF=$(resolve_symlinks "$0")

# Print the first <name> on PATH that is not this script itself.
find_other() {
  _name="$1"
  _saveIFS=$IFS; IFS=:
  for _dir in $PATH; do
    [ -n "$_dir" ] || _dir=.
    [ -x "$_dir/$_name" ] || continue
    if [ "$(resolve_symlinks "$_dir/$_name")" != "$SELF" ]; then
      IFS=$_saveIFS
      printf '%s' "$_dir/$_name"
      return 0
    fi
  done
  IFS=$_saveIFS
  return 1
}

if [ "${CPB_NPX_BOOTSTRAP:-}" != "0" ]; then
  FOUND=$(find_other cpb) || FOUND=""
  if [ -n "$FOUND" ]; then
    exec "$FOUND" "$@"
  fi
  FOUND=$(find_other claude-playbook) || FOUND=""
  if [ -n "$FOUND" ]; then
    exec "$FOUND" "$@"
  fi
fi

# The newest published, non-prerelease release's tag (GitHub's own notion,
# as install.sh uses), or nothing.
latest_tag() {
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null \
    | grep '"tag_name"' | head -1 | cut -d'"' -f4
}

# Resolve which release tag to fetch.
TAG=""
TAG_FROM=""
if [ -n "${CPB_VERSION:-}" ]; then
  TAG="$CPB_VERSION"
  TAG_FROM=env
elif [ -n "${npm_package_version:-}" ] && [ "$npm_package_version" != "0.0.0" ]; then
  TAG="v$npm_package_version"
  TAG_FROM=package
else
  TAG=$(latest_tag) || TAG=""
  TAG_FROM=latest
fi
if [ -z "$TAG" ]; then
  echo "Error: could not determine which release to fetch. Check your internet connection," >&2
  echo "or pin one explicitly: CPB_VERSION=v3.9.1 npx github:${REPO} ..." >&2
  exit 1
fi

if [ "${CPB_NPX_BOOTSTRAP:-}" = "0" ]; then
  MODE="ephemeral"
else
  MODE="bootstrap"
fi
# BIN_DIR and BIN for $TAG: the per-tag cache in ephemeral mode.
set_bin() {
  if [ "$MODE" = "ephemeral" ]; then
    BIN_DIR="${CACHE_ROOT}/${TAG}"
  else
    BIN_DIR="$INSTALL_DIR"
  fi
  BIN="${BIN_DIR}/claude-playbook"
}
set_bin

# Download $TAG's binary to $BIN, verified. Returns 4 when the release has
# no such asset (HTTP 404), 1 on any other failure. Called from `||`, where
# set -e does not apply, so every step checks its own result.
TMP_FILE=""
trap 'if [ -n "$TMP_FILE" ]; then rm -f "$TMP_FILE"; fi' EXIT HUP INT TERM
download() {
  echo "Fetching claude-playbook ${TAG} (${OS}/${ARCH}) to ${BIN_DIR}..." >&2
  mkdir -p "$BIN_DIR" || return 1
  # mktemp inside the target dir: the final mv stays on one filesystem.
  TMP_FILE=$(mktemp "${BIN_DIR}/.claude-playbook.XXXXXX") || return 1

  code=$(curl -sSL -o "$TMP_FILE" -w '%{http_code}' "${DOWNLOAD_BASE_URL}/${TAG}/${ASSET}") || code=""
  case "$code" in
    200) ;;
    404) rm -f "$TMP_FILE"; TMP_FILE=""; return 4 ;;
    *) echo "Error: could not download ${ASSET} ${TAG} (${code:-no response})" >&2; return 1 ;;
  esac

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

  chmod +x "$TMP_FILE" || return 1
  mv "$TMP_FILE" "$BIN" || return 1
  TMP_FILE=""
  FETCHED=1
}

# A release named by the package but not published yet: the newest
# published one, or nothing (and why, on stderr).
fallback_tag() {
  _new=$(latest_tag) || _new=""
  _major=${TAG#v}; _major=${_major%%.*}
  if ! printf '%s\n' "$_new" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "Error: claude-playbook ${TAG} is not published yet, and no published vX.Y.Z release was found to run instead (latest: ${_new:-none})." >&2
    return 1
  fi
  _nmajor=${_new#v}; _nmajor=${_nmajor%%.*}
  if [ "$_new" = "$TAG" ] || [ "$_nmajor" != "$_major" ]; then
    echo "Error: claude-playbook ${TAG} has no ${ASSET} yet, and the newest published release (${_new}) cannot stand in for it. Try again later, or pin one: CPB_VERSION=vX.Y.Z" >&2
    return 1
  fi
  printf '%s' "$_new"
}

if [ ! -x "$BIN" ]; then
  rc=0; download || rc=$?
  if [ "$rc" = 4 ] && [ "$TAG_FROM" = package ]; then
    NEW=$(fallback_tag) || exit 1
    echo "cpb ${TAG} is not published yet; running ${NEW}" >&2
    TAG=$NEW
    set_bin
    rc=0
    if [ ! -x "$BIN" ]; then download || rc=$?; fi
  fi
  if [ "$rc" = 4 ]; then
    echo "Error: claude-playbook ${TAG} has no ${ASSET} (HTTP 404). Check the version, or pin one: CPB_VERSION=vX.Y.Z" >&2
  fi
  [ "$rc" = 0 ] || exit 1
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
