#!/bin/sh
set -e

# Every knob is a CPB_INSTALL_* variable: CPB_INSTALL_VERSION (a tag),
# CPB_INSTALL_DIR, CPB_INSTALL_DEFAULT_DIR, CPB_INSTALL_REPO,
# CPB_INSTALL_ASSET_PREFIX, CPB_INSTALL_DOWNLOAD_BASE, CPB_INSTALL_URL,
# CPB_INSTALL_API_BASE (where the latest release is looked up). A
# GITHUB_TOKEN in the environment authenticates that lookup on GitHub's own
# API, never on another base.
REPO="${CPB_INSTALL_REPO:-ramazanpolat/claude-playbooks}"
API_BASE="${CPB_INSTALL_API_BASE:-https://api.github.com}"
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
  # Fetch latest release tag. The status decides the message: GitHub
  # answers an unauthenticated request over its rate limit with 403 or
  # 429, and a shared IP (an office, a campus, a CI fleet) reaches it soon.
  # A curl that fails (no answer, a transfer cut short) is no answer, even
  # when it wrote a status first.
  echo "Fetching latest release..."
  API_URL="${API_BASE}/repos/${REPO}/releases/latest"
  BODY=$(mktemp "${TMPDIR:-/tmp}/cpb-latest.XXXXXX")
  trap 'rm -f "$BODY"' EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  TOKEN=""
  if [ -n "${GITHUB_TOKEN:-}" ] && [ "$API_BASE" = "https://api.github.com" ]; then
    case "$GITHUB_TOKEN" in
      *[!A-Za-z0-9_.-]*) echo "Note: GITHUB_TOKEN has characters a GitHub token does not; the lookup runs without it." ;;
      *) TOKEN=$GITHUB_TOKEN ;;
    esac
  fi
  if [ -n "$TOKEN" ]; then
    # On stdin as a config line, never on curl's command line, where ps
    # would show it; and no redirect is followed, so it reaches no other
    # host.
    CODE=$(printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" \
      | curl -sS -K - -o "$BODY" -w '%{http_code}' "$API_URL") || CODE=000
  else
    CODE=$(curl -sSL -o "$BODY" -w '%{http_code}' "$API_URL") || CODE=000
  fi
  case "$CODE" in
    200)
      LATEST=$(grep '"tag_name"' "$BODY" | head -1 | cut -d'"' -f4)
      ;;
    403|429)
      echo "Error: GitHub rate-limited this request (HTTP $CODE; unauthenticated requests share your IP): set CPB_INSTALL_VERSION=<tag> to skip the lookup, or retry later."
      exit 1
      ;;
    000)
      echo "Error: could not get an answer from ${API_BASE} to find the latest release. Check your internet connection, or set CPB_INSTALL_VERSION=<tag> to skip the lookup."
      exit 1
      ;;
    *)
      echo "Error: could not determine the latest release (HTTP $CODE from ${API_BASE}). Check your internet connection, or set CPB_INSTALL_VERSION=<tag> to skip the lookup."
      exit 1
      ;;
  esac
  rm -f "$BODY"
  trap - EXIT HUP INT TERM
fi

if [ -z "$LATEST" ]; then
  echo "Error: could not determine the latest release (no tag_name in the answer from ${API_BASE}). Set CPB_INSTALL_VERSION=<tag> to skip the lookup."
  exit 1
fi

# fetch URL FILE: a release download (the asset, SHA256SUMS, LICENSE,
# NOTICE) into FILE that rides out a transient failure: an HTTP 408, 429 or
# 5xx, a timeout, or a connection refused, reset or cut short (curl exit 7,
# 18, 28, 35, 52, 55, 56), up to three attempts two seconds apart. Every
# attempt starts on an empty FILE, so a cut-short attempt leaves nothing for
# the next one to append to; curl's own --retry is not used, since it can
# keep a failed attempt's bytes in a stream. A 404 is not transient: it fails
# at once, with curl's own message. FETCH_CODE holds the last HTTP status.
# The latest-release lookup above never retries: its 403 and 429 are a rate
# limit, reported at once.
fetch() {
  n=1
  while :; do
    : > "$2"
    FETCH_CODE=$(curl -sSL -w '%{http_code}' -o "$2" "$1") && rc=0 || rc=$?
    case "$rc:$FETCH_CODE" in
      0:2??) return 0 ;;
      0:408|0:429|0:5??|7:*|18:*|28:*|35:*|52:*|55:*|56:*) ;;
      0:*) echo "curl: (22) The requested URL returned error: $FETCH_CODE" >&2; return 22 ;;
      *) return $rc ;;
    esac
    if [ "$n" -ge 3 ]; then
      if [ "$rc" = 0 ]; then
        echo "curl: (22) The requested URL returned error: $FETCH_CODE" >&2
        return 22
      fi
      return $rc
    fi
    n=$((n + 1))
    sleep 2
  done
}

DOWNLOAD_BASE_URL="${CPB_INSTALL_DOWNLOAD_BASE:-https://github.com/${REPO}/releases/download}"
URL="${CPB_INSTALL_URL:-${DOWNLOAD_BASE_URL}/${LATEST}/${ASSET}}"

echo "Installing cpb from ${ASSET} ${LATEST} (${OS}/${ARCH})..."
TMP_FILE=$(mktemp "${TMPDIR:-/tmp}/cpb.XXXXXX")
SUMS_FILE=""
trap 'if [ -n "$TMP_FILE" ]; then rm -f "$TMP_FILE"; fi; if [ -n "$SUMS_FILE" ]; then rm -f "$SUMS_FILE"; fi' EXIT
# A signal ends the script; the EXIT trap then cleans up. A trap that only
# cleaned up would let the script run on after Ctrl-C.
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

fetch "$URL" "$TMP_FILE"

# Verify against the release's SHA256SUMS. A genuine mismatch, conflicting
# hashes for the asset, or an entry for it that is not a hash, aborts; the
# unverifiable cases (no sums published, the asset not listed, no sha256
# tool, CPB_INSTALL_URL override) warn and continue — the sums travel
# over the same channel as the binary, so they guard against corruption
# and truncation, not a compromised host.
if [ -n "${CPB_INSTALL_URL:-}" ]; then
  echo "Warning: CPB_INSTALL_URL override in use; skipping checksum verification"
else
  # Read only once fetched whole: a failed download leaves no partial list.
  SUMS_FILE=$(mktemp "${TMPDIR:-/tmp}/cpb-sums.XXXXXX")
  if fetch "${DOWNLOAD_BASE_URL}/${LATEST}/SHA256SUMS" "$SUMS_FILE" 2>/dev/null; then
    SUMS=$(cat "$SUMS_FILE")
  else
    SUMS=""
  fi
  rm -f "$SUMS_FILE"; SUMS_FILE=""
  if [ -n "$SUMS" ]; then
    # Lowercased: sums files may carry uppercase hex, the tools emit lower.
    # sha256sum text mode writes "hash  name"; --binary mode "hash *name".
    lines=$(printf '%s\n' "$SUMS" | awk -v a="$ASSET" '$2==a || $2=="*"a {print $1}' | tr 'A-F' 'a-f')
    want=$(printf '%s\n' "$lines" | grep . | sort -u || true)
    if command -v sha256sum >/dev/null 2>&1; then
      got=$(sha256sum "$TMP_FILE" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
      got=$(shasum -a 256 "$TMP_FILE" | awk '{print $1}')
    else
      got=""
    fi
    if [ -z "$want" ]; then
      echo "Warning: ${ASSET} not listed in SHA256SUMS; skipping checksum verification"
    elif printf '%s\n' "$want" | grep -qvE '^[0-9a-f]{64}$'; then
      # An entry for this asset that is not a hash cannot vouch for it.
      echo "Error: malformed SHA256SUMS entry for ${ASSET} ${LATEST}; refusing a binary that cannot be verified"
      exit 1
    elif [ "$(printf '%s\n' "$want" | grep -c .)" != "1" ]; then
      # Two different hashes for one asset are conflicting evidence. The
      # same hash listed twice verifies as one.
      echo "Error: checksum mismatch for ${ASSET} ${LATEST}: SHA256SUMS lists conflicting hashes for it"
      exit 1
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

# The licence travels with the binary: the release's LICENSE and NOTICE go
# to <prefix>/share/doc/cpb, <prefix> being the install directory's parent
# (~/.local/share/doc/cpb for ~/.local/bin). A release without them, or a
# directory that cannot be written, is a note, never a failed install: both
# are also in the repository. A CPB_INSTALL_URL override names no release,
# so there is nothing to fetch.
if [ -z "${CPB_INSTALL_URL:-}" ]; then
  DOC_DIR="$(dirname "$INSTALL_DIR")/share/doc/cpb"
  doc_missing=""
  for f in LICENSE NOTICE; do
    TMP_FILE=$(mktemp "${TMPDIR:-/tmp}/cpb.XXXXXX")
    if fetch "${DOWNLOAD_BASE_URL}/${LATEST}/$f" "$TMP_FILE" 2>/dev/null \
      && chmod 644 "$TMP_FILE" && mkdir -p "$DOC_DIR" 2>/dev/null && mv "$TMP_FILE" "$DOC_DIR/$f" 2>/dev/null; then
      TMP_FILE=""
    else
      rm -f "$TMP_FILE"; TMP_FILE=""
      doc_missing="$doc_missing $f"
    fi
  done
  if [ -z "$doc_missing" ]; then
    echo "Licence: $DOC_DIR/LICENSE and NOTICE (Apache-2.0)"
  else
    echo "Note: could not place${doc_missing} in $DOC_DIR; see https://github.com/${REPO}/releases/tag/${LATEST}"
  fi
fi

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
