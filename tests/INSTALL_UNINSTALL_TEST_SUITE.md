# Install And Uninstall Test Suite (herdr + sprite)

This suite verifies `install.sh` and `uninstall.sh` without touching the
user's real `claude-playbook` installation, shell config, or playbooks.

It clones the repo, switches to the target branch, builds a temporary release
asset, installs that asset into a temporary bin directory, verifies the `cpb`
symlink the installer creates, then uninstalls it. `uninstall.sh` delegates
to `self-uninstall --binary-only`, so these steps exercise the CLI's own
cleanup path end-to-end.

This is an **operator-driven herdr acceptance suite**. The agent drives a real
**herdr pane** holding a **`sprite console`** into an isolated Sprite VM, sends
each step visibly with `herdr pane send-text` + `send-keys Enter`, reads the
terminal with `herdr pane read ... --source visible --format text` between
steps, and reports pass/fail from what appeared on screen. Do not run this
suite as a hidden local script, and do not collapse it into a single
background heredoc.

Default branch under test:

```bash
main
```

For feature branches, set `BRANCH` before running the test body.

## Requirements

- `herdr` on the host (the agent runs inside a herdr pane).
- `sprite` CLI authenticated on the host.
- A sprite with `go` and `git` (GitHub auth only while the repo is private).
  The sprite `first` qualifies.

## Rules

- All paths in this suite live under the suite temp directory **inside the
  sprite** — the host filesystem is never involved.
- Do not use `/usr/local/bin` or `~/.local/bin` (not even the sprite's).
- Do not run `install.sh` or `uninstall.sh` without `INSTALL_DIR`.
- Never split panes with `--current`; split from the agent's own
  `$HERDR_PANE_ID`.
- Guard destructive in-pane commands with a hostname check
  (`[ "$(hostname)" = <sprite-name> ]`) — a sprite console can silently drop
  back to the host shell.
- Inspect the pane output after each step before continuing.

## herdr Pane Setup

```bash
herdr pane split "$HERDR_PANE_ID" --direction right --no-focus
# capture the returned pane_id → $P
herdr pane send-text "$P" 'sprite console -s <sprite-name>'
herdr pane send-keys "$P" Enter
herdr pane send-text "$P" 'echo GUARD=$(hostname)'   # expect GUARD=<sprite-name>
herdr pane send-keys "$P" Enter
herdr pane read "$P" --source visible --format text
```

Run every test step below inside that pane.

## Test Body

### Step 1: Prepare Temp Clone And Fake Release Asset

Send this block to the pane, wait, then verify the output shows `SETUP_OK`:

```bash
set -euo pipefail

export BRANCH="${BRANCH:-main}"
export REPO_URL="${REPO_URL:-https://github.com/ramazanpolat/claude-playbooks}"
export SUITE_ROOT="${SUITE_ROOT:-$(mktemp -d -t cpb-install-suite.XXXXXX)}"
export REPO="$SUITE_ROOT/repo"
export BIN="$SUITE_ROOT/build/claude-playbook"
export RELEASE_ROOT="$SUITE_ROOT/releases"
export VERSION="v-suite"
export INSTALL_DIR="$SUITE_ROOT/install-bin"
export HOME_SANDBOX="$SUITE_ROOT/home"
export CLAUDE_PLAYBOOKS_DIR="$SUITE_ROOT/playbooks"

mkdir -p "$SUITE_ROOT/build" "$RELEASE_ROOT/$VERSION" "$INSTALL_DIR" \
         "$HOME_SANDBOX" "$CLAUDE_PLAYBOOKS_DIR"

git clone "$REPO_URL" "$REPO"
cd "$REPO"
git fetch origin "$BRANCH"
git switch --track "origin/$BRANCH" 2>/dev/null || git switch "$BRANCH"

env -u CLAUDE_PLAYBOOKS_DIR go test ./...
go build -ldflags "-X github.com/ramazanpolat/claude-playbooks/cmd.Version=suite-install-test" -o "$BIN" .

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in darwin|linux) ;; *) echo "unsupported OS: $OS" >&2; exit 1 ;; esac
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

ASSET="$RELEASE_ROOT/$VERSION/claude-playbook-$OS-$ARCH"
cp "$BIN" "$ASSET"
chmod +x "$ASSET"
# Real releases carry SHA256SUMS; generate one so installs exercise the
# checksum-verification path.
( cd "$RELEASE_ROOT/$VERSION" && { sha256sum claude-playbook-* 2>/dev/null || shasum -a 256 claude-playbook-*; } > SHA256SUMS )

fail() { echo "FAIL: $*" >&2; exit 1; }
assert_exists()     { test -e "$1" || fail "missing path: $1"; }
assert_not_exists() { test ! -e "$1" || fail "unexpected path exists: $1"; }
assert_contains()   { grep -Fq "$2" "$1" || fail "expected $1 to contain: $2"; }

echo "SETUP_OK"
echo "BRANCH=$BRANCH"
echo "COMMIT=$(git rev-parse --short HEAD)"
echo "ASSET=$ASSET"
echo "INSTALL_DIR=$INSTALL_DIR"
echo "SUITE_ROOT=$SUITE_ROOT"
```

Expected:

- `go test ./...` passes.
- The output includes `SETUP_OK`.
- The agent records `COMMIT`, `ASSET`, `INSTALL_DIR`, and `SUITE_ROOT`.

### Step 2: Install Default Command Name

```bash
echo "TEST 1: install default command name into temp INSTALL_DIR"
VERSION="$VERSION" \
DOWNLOAD_BASE_URL="file://$RELEASE_ROOT" \
INSTALL_DIR="$INSTALL_DIR" \
  sh "$REPO/install.sh"

assert_exists "$INSTALL_DIR/claude-playbook"
assert_exists "$INSTALL_DIR/cpb"
"$INSTALL_DIR/claude-playbook" --version | tee "$SUITE_ROOT/default-version.out"
assert_contains "$SUITE_ROOT/default-version.out" "suite-install-test"
"$INSTALL_DIR/cpb" --version | tee "$SUITE_ROOT/cpb-version.out"
assert_contains "$SUITE_ROOT/cpb-version.out" "suite-install-test"
echo "TEST 1 PASS"
```

Expected: `Installed to .../claude-playbook`, `Created symlink .../cpb ->
claude-playbook`, both names print the version string, ends with
`TEST 1 PASS`.

### Step 3: Uninstall Removes Both Names

```bash
echo "TEST 2: uninstall removes claude-playbook and cpb from temp INSTALL_DIR"
HOME="$HOME_SANDBOX" INSTALL_DIR="$INSTALL_DIR" sh "$REPO/uninstall.sh"
assert_not_exists "$INSTALL_DIR/claude-playbook"
assert_not_exists "$INSTALL_DIR/cpb"
echo "TEST 2 PASS"
```

Expected: uninstall.sh delegates to `self-uninstall --binary-only`; the
output is the CLI's `Removed:` list naming the binary and the sibling
symlink, then `Playbooks were not touched: ...`, then `TEST 2 PASS`.

### Step 4: Reinstall For The User-Flow Test

```bash
echo "TEST 3: reinstall into temp INSTALL_DIR"
VERSION="$VERSION" \
DOWNLOAD_BASE_URL="file://$RELEASE_ROOT" \
INSTALL_DIR="$INSTALL_DIR" \
  sh "$REPO/install.sh"

assert_exists "$INSTALL_DIR/claude-playbook"
assert_exists "$INSTALL_DIR/cpb"
echo "TEST 3 PASS"
```

Expected: same as TEST 1; ends with `TEST 3 PASS`.

### Step 5: Use `cpb` Like A User

```bash
echo "TEST 4: cpb symlink can run CLI features without touching real playbooks"
HOME="$HOME_SANDBOX" "$INSTALL_DIR/cpb" create cpb-smoke --no-alias
assert_exists "$CLAUDE_PLAYBOOKS_DIR/cpb-smoke"
assert_exists "$CLAUDE_PLAYBOOKS_DIR/cpb-smoke/CLAUDE.md"
assert_not_exists "$CLAUDE_PLAYBOOKS_DIR/cpb-smoke/.playbook"
echo "TEST 4 PASS"
```

Expected: `Created playbook "cpb-smoke"` under the suite temp directory. Flat
model: the playbook is a bare directory with a starter `CLAUDE.md` and **no
`.playbook` manifest**. Ends with `TEST 4 PASS`.

### Step 6: Final Uninstall Leaves Playbooks Intact

```bash
echo "TEST 5: uninstall removes both names, playbooks survive"
HOME="$HOME_SANDBOX" INSTALL_DIR="$INSTALL_DIR" sh "$REPO/uninstall.sh"
assert_not_exists "$INSTALL_DIR/claude-playbook"
assert_not_exists "$INSTALL_DIR/cpb"
assert_exists "$CLAUDE_PLAYBOOKS_DIR/cpb-smoke"
echo "TEST 5 PASS"

echo "PASS: install/uninstall suite completed"
echo "BRANCH=$BRANCH"
echo "COMMIT=$(git rev-parse --short HEAD)"
echo "SUITE_ROOT=$SUITE_ROOT"
```

Expected:

- Final line `PASS: install/uninstall suite completed`.
- The sprite's (and host's) real `claude-playbook`, `~/.claude-playbooks`, and
  shell config are untouched.

## Parallel Variant (optional, herdr workspace)

The whole suite is self-contained under `SUITE_ROOT`, so it parallelizes: move
a fresh pane to a new herdr workspace (`herdr pane move <id> --new-workspace
--label cpb-install-parallel`), split once per instance, open a `sprite
console` in each, and run the full body concurrently with per-pane
`SUITE_ROOT`s. Pass = every pane independently reaches
`PASS: install/uninstall suite completed`.

## Cleanup

Cleanup is optional. In the sprite pane, use the printed `SUITE_ROOT`:

```bash
chmod -R u+w "$SUITE_ROOT" 2>/dev/null || true
rm -rf "$SUITE_ROOT"
```

On the host, close the extra pane(s) with `herdr pane close <id>`.
