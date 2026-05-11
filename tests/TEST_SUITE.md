# claude-playbook Binary Test Suite

This suite is for Claude Code or Codex agents testing `claude-playbook` from a freshly built binary. The binary must not be installed. Every command should call the built binary by absolute path.

This is an operator-driven cmux acceptance suite. The agent must create a real cmux terminal pane, send commands to that pane, read the terminal screen between sections, and judge pass/fail from visible output. Do not run this suite as a hidden local script through `exec_command`, and do not collapse it into a background runner that bypasses the terminal UI.

Default branch under test:

```bash
main
```

For feature branches, set `BRANCH` before running the bootstrap step.

## Rules

- Run this in a new cmux terminal pane.
- The runner must be Codex or Claude Code driving that pane with `cmux send`, `cmux send-key`, and `cmux read-screen`, simulating a user at a terminal.
- Keep the test pane visible/inspectable and report its pane/surface refs.
- Read the screen after every major section before continuing.
- Clone `https://github.com/ramazanpolat/claude-playbooks` into a temporary directory.
- Switch to the test branch before building.
- Build the binary to a temporary path outside the repo.
- Do not run `install.sh`.
- Do not copy or move the binary into `/usr/local/bin`, `/opt/homebrew/bin`, or any other PATH install location.
- Use `CLAUDE_PLAYBOOKS_DIR` for temporary playbook storage.
- Use `CLAUDE_SHELL_CONFIG` only when testing alias writes, so the real `~/.zshrc` is not touched.
- Do not install the built binary.
- Do not modify the real `~/.claude-playbooks`, `~/.zshrc`, or installed `claude-playbook`.
- The live auth smoke test uses a temporary `HOME` seeded from the user's current Claude auth metadata, so the test does not write to the real `~/.claude` directory.

## cmux Pane Setup

From the active Codex or Claude Code pane:

```bash
cmux identify --id-format both
cmux tree --workspace <workspace-ref>
cmux new-pane --type terminal --direction right --workspace <workspace-ref> --focus false
cmux rename-tab --workspace <workspace-ref> --surface <surface-ref> claude-playbook-suite
```

Run all remaining commands inside the new terminal pane. The agent should send each command block to the pane, press Enter, wait for completion, then read output with:

```bash
cmux read-screen --workspace <workspace-ref> --surface <surface-ref> --scrollback --lines 160
```

Start a clean bash shell in the test pane before the first test command. This keeps shell strict mode local to the test pane and avoids modifying the agent's own shell:

```bash
bash --noprofile --norc
```

## Bootstrap And Build

Send this block to the new cmux pane, wait for it to finish, then inspect the visible output:

```bash
set -euo pipefail

export BRANCH="${BRANCH:-main}"
export SUITE_ROOT="${SUITE_ROOT:-$(mktemp -d -t claude-playbook-suite.XXXXXX)}"
export REPO="$SUITE_ROOT/repo"
export BIN_DIR="$SUITE_ROOT/bin"
export BIN="$BIN_DIR/claude-playbook"
export REAL_HOME="${REAL_HOME:-$HOME}"
export REAL_PATH="${REAL_PATH:-$PATH}"

mkdir -p "$BIN_DIR"

git clone https://github.com/ramazanpolat/claude-playbooks "$REPO"
cd "$REPO"
git fetch origin "$BRANCH"
git switch --track "origin/$BRANCH"
git rev-parse --short HEAD

go test ./...
go build -ldflags "-X github.com/ramazanpolat/claude-playbooks/cmd.Version=suite-test" -o "$BIN" .

"$BIN" --version
test ! -x /usr/local/bin/claude-playbook || echo "Note: installed claude-playbook exists, but this suite uses $BIN"
echo "BIN=$BIN"
echo "SUITE_ROOT=$SUITE_ROOT"
```

Expected:

- `go test ./...` passes.
- `"$BIN" --version` prints `claude-playbook version suite-test`.
- No command installs the binary.
- The agent records the visible commit hash and binary path from the terminal output before continuing.

## Isolated Binary Feature Tests

These tests use a fake `HOME`, fake global Claude auth files, a fake shell config, and a stub `claude` executable. They verify that the built binary syncs auth artifacts for newly created, installed, linked, run, and ad-hoc playbooks without launching real Claude Code.

Send this block to the same cmux pane after the bootstrap step. The agent must inspect the visible `TEST N` output as it runs and stop immediately on any `FAIL:` line:

```bash
set -euo pipefail
: "${BIN:?BIN must point to the built claude-playbook binary}"

export TEST_ROOT="${TEST_ROOT:-$(mktemp -d -t claude-playbook-isolated.XXXXXX)}"
export REAL_HOME="${REAL_HOME:-$HOME}"
export REAL_PATH="${REAL_PATH:-$PATH}"
export HOME="$TEST_ROOT/home"
export CLAUDE_PLAYBOOKS_DIR="$TEST_ROOT/playbooks"
export CLAUDE_SHELL_CONFIG="$TEST_ROOT/zshrc"
export STUB_DIR="$TEST_ROOT/stubs"

mkdir -p "$HOME/.claude" "$CLAUDE_PLAYBOOKS_DIR" "$STUB_DIR"
touch "$CLAUDE_SHELL_CONFIG"

cat > "$HOME/.claude/.credentials.json" <<'JSON'
{"source":"test-suite","token":"fake-token"}
JSON

cat > "$HOME/.claude/.claude.json" <<'JSON'
{
  "oauthAccount": {
    "emailAddress": "suite@example.com",
    "uuid": "suite-user"
  },
  "userID": "suite-user",
  "hasCompletedOnboarding": true,
  "lastOnboardingVersion": "suite",
  "installMethod": "test-suite"
}
JSON

cat > "$STUB_DIR/claude" <<'SH'
#!/bin/sh
echo "STUB_CLAUDE_CONFIG_DIR=$CLAUDE_CONFIG_DIR"
echo "STUB_ARGS=$*"
test -d "$CLAUDE_CONFIG_DIR"
test -f "$CLAUDE_CONFIG_DIR/.claude.json"
test -e "$CLAUDE_CONFIG_DIR/.credentials.json"
exit 0
SH
chmod +x "$STUB_DIR/claude"
export PATH="$STUB_DIR:$PATH"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_exists() {
  test -e "$1" || fail "missing path: $1"
}

assert_file() {
  test -f "$1" || fail "missing file: $1"
}

assert_symlink() {
  test -L "$1" || fail "missing symlink: $1"
}

assert_contains() {
  grep -Fq "$2" "$1" || fail "expected $1 to contain: $2"
}

assert_auth_synced() {
  dir="$1"
  assert_file "$dir/.claude.json"
  assert_symlink "$dir/.credentials.json"
  assert_contains "$dir/.claude.json" '"oauthAccount"'
  assert_contains "$dir/.claude.json" '"hasCompletedOnboarding": true'
}

echo "TEST 1: create --no-alias syncs credentials and account metadata"
"$BIN" create created --no-alias
assert_file "$CLAUDE_PLAYBOOKS_DIR/created/.playbook"
assert_auth_synced "$CLAUDE_PLAYBOOKS_DIR/created"

echo "TEST 2: create with alias writes only to CLAUDE_SHELL_CONFIG"
"$BIN" create created-alias --alias ca
assert_auth_synced "$CLAUDE_PLAYBOOKS_DIR/created-alias"
assert_contains "$CLAUDE_SHELL_CONFIG" "alias ca='CLAUDE_CONFIG_DIR=$CLAUDE_PLAYBOOKS_DIR/created-alias claude'"

echo "TEST 3: install local bundle syncs root and children"
SRC="$TEST_ROOT/source-bundle"
mkdir -p "$SRC/playbooks/child"
cat > "$SRC/.playbook" <<'EOF'
version = "0.1.0"
name = "bundle"
alias = "bundlealias"
description = "Suite bundle"

[[children]]
name = "child"
path = "playbooks/child"
alias = "childalias"
description = "Suite child"
EOF
printf '# Bundle\n' > "$SRC/CLAUDE.md"
printf '# Child\n' > "$SRC/playbooks/child/CLAUDE.md"

"$BIN" install "$SRC" --name installed --alias-all
assert_auth_synced "$CLAUDE_PLAYBOOKS_DIR/installed"
assert_auth_synced "$CLAUDE_PLAYBOOKS_DIR/installed/playbooks/child"
assert_contains "$CLAUDE_SHELL_CONFIG" "alias bundlealias='CLAUDE_CONFIG_DIR=$CLAUDE_PLAYBOOKS_DIR/installed claude'"
assert_contains "$CLAUDE_SHELL_CONFIG" "alias childalias='CLAUDE_CONFIG_DIR=$CLAUDE_PLAYBOOKS_DIR/installed/playbooks/child claude'"

echo "TEST 4: install --subdir --init syncs the cherry-picked playbook"
"$BIN" install "$SRC" --subdir playbooks/child --name installed-child --init --no-alias
assert_file "$CLAUDE_PLAYBOOKS_DIR/installed-child/.playbook"
assert_auth_synced "$CLAUDE_PLAYBOOKS_DIR/installed-child"

echo "TEST 5: link syncs credentials into the linked source directory"
LINK_SRC="$TEST_ROOT/link-source"
mkdir -p "$LINK_SRC"
cat > "$LINK_SRC/.playbook" <<'EOF'
version = "0.1.0"
name = "linked"
alias = "linkedalias"
EOF
printf '# Linked\n' > "$LINK_SRC/CLAUDE.md"

"$BIN" link "$LINK_SRC" --name linked --no-alias
assert_symlink "$CLAUDE_PLAYBOOKS_DIR/linked"
assert_auth_synced "$LINK_SRC"

echo "TEST 6: run syncs auth before executing claude and forwards flags"
RUN_PB="$CLAUDE_PLAYBOOKS_DIR/run-sync"
mkdir -p "$RUN_PB"
cat > "$RUN_PB/.playbook" <<'EOF'
version = "0.1.0"
name = "run-sync"
EOF
RUN_OUT="$TEST_ROOT/run.out"
"$BIN" run run-sync --probe value > "$RUN_OUT"
assert_auth_synced "$RUN_PB"
assert_contains "$RUN_OUT" "STUB_CLAUDE_CONFIG_DIR=$RUN_PB"
assert_contains "$RUN_OUT" "STUB_ARGS=--probe value"

echo "TEST 7: start syncs auth for ad-hoc directories"
START_DIR="$TEST_ROOT/ad-hoc"
START_OUT="$TEST_ROOT/start.out"
"$BIN" start "$START_DIR" --start-probe > "$START_OUT"
assert_auth_synced "$START_DIR"
assert_contains "$START_OUT" "STUB_CLAUDE_CONFIG_DIR=$START_DIR"
assert_contains "$START_OUT" "STUB_ARGS=--start-probe"

echo "TEST 8: list and info see the test playbooks"
LIST_OUT="$TEST_ROOT/list.out"
INFO_OUT="$TEST_ROOT/info.out"
"$BIN" list > "$LIST_OUT"
"$BIN" info installed > "$INFO_OUT"
assert_contains "$LIST_OUT" "created"
assert_contains "$LIST_OUT" "installed"
assert_contains "$INFO_OUT" "Name:"
assert_contains "$INFO_OUT" "Children:"

echo "PASS: isolated binary feature tests completed"
echo "TEST_ROOT=$TEST_ROOT"
export HOME="$REAL_HOME"
export PATH="$REAL_PATH"
echo "HOME restored to $HOME"
echo "PATH restored"
```

Expected:

- The final line is `PASS: isolated binary feature tests completed`.
- No test uses an installed `claude-playbook`.
- The only shell config written is `$CLAUDE_SHELL_CONFIG`.
- The real `~/.zshrc` and real `~/.claude-playbooks` are not modified.
- The agent captures `TEST_ROOT` from the terminal output for possible inspection.

## Live Auth Smoke Test

This is the regression test for the bug where a new playbook showed Claude Code's login method screen. It must run in a cmux terminal pane because it launches real interactive Claude Code.

Prerequisites:

- The host has Claude Code installed as `claude`.
- The user is already logged in to Claude Code in the normal/default environment.
- The user's current Claude account metadata exists in one of these locations:
  `~/.claude/.claude.json`, `~/.claude.json`, or an existing playbook under `~/.claude-playbooks`.

Send this block to the same cmux pane after the isolated tests. This section intentionally launches real interactive Claude Code in the pane; the agent must inspect the screen and interact with prompts like a user:

```bash
set -euo pipefail
: "${BIN:?BIN must point to the built claude-playbook binary}"

export LIVE_ROOT="${LIVE_ROOT:-$(mktemp -d -t claude-playbook-live.XXXXXX)}"
export LIVE_PLAYBOOKS_DIR="$LIVE_ROOT/playbooks"
export LIVE_SHELL_CONFIG="$LIVE_ROOT/zshrc"
export LIVE_HOME="$LIVE_ROOT/home"
export REAL_HOME="${REAL_HOME:-$HOME}"
export PATH="${REAL_PATH:-$PATH}"
mkdir -p "$LIVE_HOME/.claude"
chmod 700 "$LIVE_HOME" "$LIVE_HOME/.claude"
touch "$LIVE_SHELL_CONFIG"

command -v claude

SOURCE_STATE=""
for candidate in "$REAL_HOME/.claude/.claude.json" "$REAL_HOME/.claude.json"; do
  if [ -f "$candidate" ]; then
    SOURCE_STATE="$candidate"
    break
  fi
done
if [ -z "$SOURCE_STATE" ] && [ -d "$REAL_HOME/.claude-playbooks" ]; then
  SOURCE_STATE="$(find "$REAL_HOME/.claude-playbooks" -maxdepth 3 -name .claude.json -print -quit)"
fi
test -n "$SOURCE_STATE"
cp "$SOURCE_STATE" "$LIVE_HOME/.claude/.claude.json"

if [ -f "$REAL_HOME/.claude/.credentials.json" ]; then
  cp "$REAL_HOME/.claude/.credentials.json" "$LIVE_HOME/.claude/.credentials.json"
fi

export HOME="$LIVE_HOME"

CLAUDE_PLAYBOOKS_DIR="$LIVE_PLAYBOOKS_DIR" \
CLAUDE_SHELL_CONFIG="$LIVE_SHELL_CONFIG" \
  "$BIN" create live-auth --no-alias

test -e "$LIVE_PLAYBOOKS_DIR/live-auth/.credentials.json"
test -f "$LIVE_PLAYBOOKS_DIR/live-auth/.claude.json"

echo "Launching live Claude Code auth smoke test."
echo "PASS condition: normal Claude Code prompt appears without login method selection."
echo "FAIL condition: screen shows Select login method, OAuth URL, or Paste code here."
echo "Exit Claude Code with /exit or Ctrl-C after observing the result."

CLAUDE_PLAYBOOKS_DIR="$LIVE_PLAYBOOKS_DIR" \
CLAUDE_SHELL_CONFIG="$LIVE_SHELL_CONFIG" \
  "$BIN" run live-auth
```

Use `cmux read-screen` to inspect the pane.

If Claude Code shows a workspace trust prompt, select the trusted option only if the path is the expected repo checkout, then continue inspecting for auth prompts.

Pass:

- Claude Code starts normally for the `live-auth` playbook.
- The screen does not show `Select login method`.
- The screen does not show `Paste code here if prompted`.
- The screen does not show a `claude.com/cai/oauth/authorize` URL.
- The real `~/.claude`, `~/.claude-playbooks`, and `~/.zshrc` are not modified.

Fail:

- Any login method menu appears.
- Any OAuth URL appears.
- Claude asks for an auth code.

After the live result is observed, exit Claude Code with `/exit` or Ctrl-C.

## Cleanup

Cleanup is optional after the isolated tests. After the live auth smoke test, cleanup is recommended because `$LIVE_HOME/.claude` may contain copied Claude auth artifacts.

```bash
for d in "${SUITE_ROOT:-}" "${TEST_ROOT:-}" "${LIVE_ROOT:-}"; do
  if [ -n "$d" ] && [ "$d" != "/" ]; then
    rm -rf "$d"
  fi
done
```

## Report Template

When reporting results, include:

```text
Branch:
Commit:
Binary path:
go test result:
Isolated binary suite result:
Live auth smoke result:
Did login method screen appear:
Temp roots kept for inspection:
Notes:
```
