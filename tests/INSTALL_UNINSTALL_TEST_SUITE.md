# Install And Uninstall Test Suite

This suite verifies `install.sh` and `uninstall.sh` without touching the user's real `claude-playbook` installation, shell config, or playbooks.

It clones the repo, switches to the target branch, builds a temporary release asset, installs that asset into a temporary bin directory, verifies a custom command name such as `cpb`, then uninstalls it.

This is an operator-driven cmux acceptance suite. Codex or Claude Code must drive a real cmux terminal pane, send commands visibly to that pane, read terminal output between steps, and report pass/fail from what appeared on screen. Do not run this suite as a hidden local script through `exec_command`, and do not collapse the whole suite into a single background heredoc.

Default branch under test:

```bash
main
```

For feature branches, set `BRANCH` before running the test body.

## Rules

- Do not use `/usr/local/bin`.
- Do not use `~/.local/bin`.
- Do not run `install.sh` without `INSTALL_DIR`.
- Do not run `uninstall.sh` without `INSTALL_DIR`.
- Do not modify the real `~/.claude-playbooks`.
- Do not modify the real shell config.
- All installed binaries must live under the suite temp directory.
- The runner must be Codex or Claude Code controlling cmux with `cmux send`, `cmux send-key`, and `cmux read-screen`, simulating an actual terminal user.
- Run each step visibly in the cmux pane and inspect output before moving to the next step.

## cmux Pane Setup

Run this suite in a new cmux terminal pane:

```bash
cmux identify --id-format both
cmux tree --workspace <workspace-ref>
cmux new-pane --type terminal --direction right --workspace <workspace-ref> --focus false
cmux rename-tab --workspace <workspace-ref> --surface <surface-ref> install-uninstall-suite
```

Start a clean bash shell in the test pane:

```bash
bash --noprofile --norc
```

Run every test step below inside that bash shell. After each step, inspect the pane with `cmux read-screen` before continuing.

## Test Body

### Step 1: Prepare Temp Clone And Fake Release Asset

Send this block to the cmux pane, wait for it to finish, then verify the visible output shows `SETUP_OK`:

```bash
set -euo pipefail

export BRANCH="${BRANCH:-main}"
export REPO_URL="${REPO_URL:-https://github.com/ramazanpolat/claude-playbooks}"
export SUITE_ROOT="${SUITE_ROOT:-$(mktemp -d -t claude-playbook-install-suite.XXXXXX)}"
export REPO="$SUITE_ROOT/repo"
export BIN="$SUITE_ROOT/build/claude-playbook"
export RELEASE_ROOT="$SUITE_ROOT/releases"
export VERSION="v-suite"
export INSTALL_DIR="$SUITE_ROOT/install-bin"
export HOME="$SUITE_ROOT/home"
export CLAUDE_PLAYBOOKS_DIR="$SUITE_ROOT/playbooks"
export CLAUDE_SHELL_CONFIG="$SUITE_ROOT/zshrc"

mkdir -p "$SUITE_ROOT/build" "$RELEASE_ROOT/$VERSION" "$INSTALL_DIR" "$HOME" "$CLAUDE_PLAYBOOKS_DIR"
touch "$CLAUDE_SHELL_CONFIG"

git clone "$REPO_URL" "$REPO"
cd "$REPO"
git fetch origin "$BRANCH"
git switch --track "origin/$BRANCH"

env -u CLAUDE_PLAYBOOKS_DIR -u CLAUDE_SHELL_CONFIG go test ./...
go build -ldflags "-X github.com/ramazanpolat/claude-playbooks/cmd.Version=suite-install-test" -o "$BIN" .

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  darwin|linux) ;;
  *) echo "unsupported OS: $OS" >&2; exit 1 ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

ASSET="$RELEASE_ROOT/$VERSION/claude-playbook-$OS-$ARCH"
cp "$BIN" "$ASSET"
chmod +x "$ASSET"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_exists() {
  test -e "$1" || fail "missing path: $1"
}

assert_not_exists() {
  test ! -e "$1" || fail "unexpected path exists: $1"
}

assert_contains() {
  grep -Fq "$2" "$1" || fail "expected $1 to contain: $2"
}

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

Send this block to the cmux pane:

```bash
echo "TEST 1: install default command name into temp INSTALL_DIR"
VERSION="$VERSION" \
DOWNLOAD_BASE_URL="file://$RELEASE_ROOT" \
INSTALL_DIR="$INSTALL_DIR" \
  sh "$REPO/install.sh"

assert_exists "$INSTALL_DIR/claude-playbook"
"$INSTALL_DIR/claude-playbook" --version | tee "$SUITE_ROOT/default-version.out"
assert_contains "$SUITE_ROOT/default-version.out" "suite-install-test"
echo "TEST 1 PASS"
```

Expected:

- The output says `Installed to .../claude-playbook`.
- `claude-playbook version suite-install-test` is visible.
- The output ends with `TEST 1 PASS`.

### Step 3: Uninstall Default Command Name

Send this block to the cmux pane:

```bash
echo "TEST 2: uninstall default command name from temp INSTALL_DIR"
INSTALL_DIR="$INSTALL_DIR" sh "$REPO/uninstall.sh"
assert_not_exists "$INSTALL_DIR/claude-playbook"
echo "TEST 2 PASS"
```

Expected:

- The output says `Removed .../claude-playbook`.
- The output says playbooks were not touched.
- The output ends with `TEST 2 PASS`.

### Step 4: Install Custom Command Name `cpb`

Send this block to the cmux pane:

```bash
echo "TEST 3: install custom command name cpb with INSTALL_NAME"
VERSION="$VERSION" \
DOWNLOAD_BASE_URL="file://$RELEASE_ROOT" \
INSTALL_DIR="$INSTALL_DIR" \
INSTALL_NAME=cpb \
  sh "$REPO/install.sh"

assert_exists "$INSTALL_DIR/cpb"
assert_not_exists "$INSTALL_DIR/claude-playbook"
"$INSTALL_DIR/cpb" --version | tee "$SUITE_ROOT/cpb-version.out"
assert_contains "$SUITE_ROOT/cpb-version.out" "suite-install-test"
echo "TEST 3 PASS"
```

Expected:

- The output says `Installed to .../cpb`.
- `$INSTALL_DIR/cpb` exists.
- `$INSTALL_DIR/claude-playbook` does not exist.
- `cpb` prints `claude-playbook version suite-install-test`.
- The output ends with `TEST 3 PASS`.

### Step 5: Use `cpb` Like A User

Send this block to the cmux pane:

```bash
echo "TEST 4: custom command can run CLI features without touching real playbooks"
"$INSTALL_DIR/cpb" create cpb-smoke --no-alias
assert_exists "$CLAUDE_PLAYBOOKS_DIR/cpb-smoke/.playbook"
echo "TEST 4 PASS"
```

Expected:

- The visible command is `"$INSTALL_DIR/cpb" create cpb-smoke --no-alias`.
- The output says `Created playbook "cpb-smoke"` under the suite temp directory.
- The output ends with `TEST 4 PASS`.

### Step 6: Uninstall Custom Command Name `cpb`

Send this block to the cmux pane:

```bash
echo "TEST 5: uninstall custom command name cpb with INSTALL_NAME"
INSTALL_DIR="$INSTALL_DIR" INSTALL_NAME=cpb sh "$REPO/uninstall.sh"
assert_not_exists "$INSTALL_DIR/cpb"
assert_exists "$CLAUDE_PLAYBOOKS_DIR/cpb-smoke/.playbook"
echo "TEST 5 PASS"
```

Expected:

- The output says `Removed .../cpb`.
- The `cpb-smoke` playbook remains in the suite temp playbooks directory.
- The output ends with `TEST 5 PASS`.

### Step 7: Verify `BINARY_NAME=cpb` Compatibility

Send this block to the cmux pane:

```bash
echo "TEST 6: BINARY_NAME remains a compatibility alias for INSTALL_NAME"
VERSION="$VERSION" \
DOWNLOAD_BASE_URL="file://$RELEASE_ROOT" \
INSTALL_DIR="$INSTALL_DIR" \
BINARY_NAME=cpb \
  sh "$REPO/install.sh"

assert_exists "$INSTALL_DIR/cpb"
INSTALL_DIR="$INSTALL_DIR" BINARY_NAME=cpb sh "$REPO/uninstall.sh"
assert_not_exists "$INSTALL_DIR/cpb"

echo "PASS: install/uninstall suite completed"
echo "BRANCH=$BRANCH"
echo "COMMIT=$(git rev-parse --short HEAD)"
echo "SUITE_ROOT=$SUITE_ROOT"
```

Expected:

- The output says `Installed to .../cpb`.
- The output says `Removed .../cpb`.
- The final line is `PASS: install/uninstall suite completed`.
- `$INSTALL_DIR/cpb` exists after the custom install test.
- `$INSTALL_DIR/cpb` is removed after the custom uninstall test.
- `$INSTALL_DIR/claude-playbook` is not created during the `cpb` install test.
- The real installed `claude-playbook` binary is not touched.
- The real `~/.claude-playbooks` directory is not touched.

## Cleanup

Cleanup is optional. Use the `SUITE_ROOT` path printed by the suite:

```bash
chmod -R u+w /path/printed/as/SUITE_ROOT 2>/dev/null || true
rm -rf /path/printed/as/SUITE_ROOT
```
