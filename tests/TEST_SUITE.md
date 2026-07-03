# claude-playbook Binary Test Suite (herdr + sprite)

This suite is for a Claude Code (or compatible) agent testing `claude-playbook`
from a freshly built binary. The binary must not be installed on the host.
Every command calls the built binary by absolute path.

This is an **operator-driven herdr acceptance suite**. The agent drives a real
**herdr pane** that holds a **`sprite console`** into an isolated Sprite VM.
All test commands run inside the sprite; the host's real `~/.claude-playbooks`,
`~/.zshrc`, `~/.claude`, and installed `claude-playbook` are never touched —
isolation comes from the VM, not from environment gymnastics. Do not run this
suite as a hidden local script; keep the pane visible and read it between
sections.

Default branch under test:

```bash
main
```

For feature branches, set `BRANCH` before running the bootstrap step.

## Requirements

- `herdr` on the host (the agent runs inside a herdr pane; `HERDR_ENV=1`).
- `sprite` CLI authenticated on the host.
- A sprite with: `go`, `git`, and — for the live/real-user sections — `claude`
  installed plus a working long-lived token at
  `~/.config/claude-code/oauth-token` (exported as `CLAUDE_CODE_OAUTH_TOKEN`
  in the sprite's shell rc). GitHub auth (`gh auth status`) is required only
  while this repo is private. The sprite `first` satisfies all of this; any
  sprite provisioned per the `test-on-sprite` skill works.

## Rules

- Drive the pane with `herdr pane send-text` + `herdr pane send-keys ... Enter`,
  and read output with `herdr pane read ... --source visible --format text`.
  Simulate a user at a terminal; inspect the screen after every major section.
- Never split with `--current`; always split from the agent's own explicit
  pane id (`$HERDR_PANE_ID`).
- **Guard every destructive in-pane command with a hostname check** — a
  `sprite console` can silently drop back to the host shell, and unguarded
  commands then run on the host:
  `if [ "$(hostname)" = <sprite-name> ]; then ...; else echo GUARD_FAIL; fi`
- Build the binary to a path outside the repo (e.g. under the suite temp
  root). Do not run `install.sh`. Do not copy the binary into any PATH
  location — in the sprite or on the host.
- Use `CLAUDE_PLAYBOOKS_DIR` and `CLAUDE_SHELL_CONFIG` under a suite temp root
  inside the sprite, so repeated runs don't require a checkpoint restore.
- zsh gotcha: after writing an alias, `source ~/.zshrc && <alias>` fails —
  zsh resolves aliases at parse time. Send `source` and the alias as two
  separate commands.

## herdr Pane Setup

From the agent's own pane (id in `$HERDR_PANE_ID`):

```bash
herdr pane split "$HERDR_PANE_ID" --direction right --no-focus
# capture the returned pane_id, e.g. w5:pX — call it $P below
herdr pane send-text "$P" 'sprite console -s <sprite-name>'
herdr pane send-keys "$P" Enter
# verify the console is live before anything else:
herdr pane send-text "$P" 'echo GUARD=$(hostname)'
herdr pane send-keys "$P" Enter
herdr pane read "$P" --source visible --format text   # expect GUARD=<sprite-name>
```

Re-run the guard check any time the pane's prompt looks unexpected.

## Bootstrap And Build (in the sprite pane)

Send this block to the pane, wait, then read the output:

```bash
set -euo pipefail
export BRANCH="${BRANCH:-main}"
export SUITE_ROOT="${SUITE_ROOT:-$(mktemp -d -t cpb-suite.XXXXXX)}"
export REPO="$SUITE_ROOT/repo"
export BIN="$SUITE_ROOT/bin/claude-playbook"
mkdir -p "$SUITE_ROOT/bin"

git clone https://github.com/ramazanpolat/claude-playbooks "$REPO"
cd "$REPO"
git fetch origin "$BRANCH"
git switch --track "origin/$BRANCH" 2>/dev/null || git switch "$BRANCH"
git rev-parse --short HEAD

go test ./...
go build -ldflags "-X github.com/ramazanpolat/claude-playbooks/cmd.Version=suite-test" -o "$BIN" .

"$BIN" --version
echo "BIN=$BIN"
echo "SUITE_ROOT=$SUITE_ROOT"
```

If the in-sprite clone fails (no GitHub auth for a private repo), transfer the
tree from the host instead: `git archive --format=tar -o /tmp/src.tar <BRANCH>`
locally, `sprite file push -s <sprite> /tmp/src.tar /home/sprite/src.tar`
(push to the home dir — pushing into a fresh subdirectory fails), then extract
in the pane. Record the commit hash from the host in that case.

Expected:

- `go test ./...` passes.
- `"$BIN" --version` prints `claude-playbook version suite-test`.
- Nothing is installed. The agent records the commit hash and `BIN` path.

## Isolated Binary Feature Tests (flat model)

These run inside the sprite with a fake `HOME`, fake auth files, a fake shell
config, and a stub `claude` executable. They verify the **v4 flat model**: one
install = one playbook, `.playbook` optional and metadata-only, no children.

Send this block to the pane; inspect each `TEST N` line and stop on any
`FAIL:`:

```bash
set -euo pipefail
: "${BIN:?BIN must point to the built claude-playbook binary}"

export TEST_ROOT="${TEST_ROOT:-$(mktemp -d -t cpb-isolated.XXXXXX)}"
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
  "oauthAccount": { "emailAddress": "suite@example.com", "uuid": "suite-user" },
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

fail() { echo "FAIL: $*" >&2; exit 1; }
assert_exists()    { test -e "$1" || fail "missing path: $1"; }
assert_not_exists(){ test ! -e "$1" || fail "unexpected path: $1"; }
assert_file()      { test -f "$1" || fail "missing file: $1"; }
assert_symlink()   { test -L "$1" || fail "missing symlink: $1"; }
assert_contains()  { grep -Fq "$2" "$1" || fail "expected $1 to contain: $2"; }
assert_auth_synced() {
  dir="$1"
  assert_file "$dir/.claude.json"
  assert_symlink "$dir/.credentials.json"
  assert_contains "$dir/.claude.json" '"oauthAccount"'
  assert_contains "$dir/.claude.json" '"hasCompletedOnboarding": true'
}

echo "TEST 1: create makes a bare playbook (no .playbook), syncs auth, writes CLAUDE.md"
"$BIN" create created --no-alias
assert_not_exists "$CLAUDE_PLAYBOOKS_DIR/created/.playbook"
assert_file "$CLAUDE_PLAYBOOKS_DIR/created/CLAUDE.md"
assert_auth_synced "$CLAUDE_PLAYBOOKS_DIR/created"

echo "TEST 2: create with alias writes only to CLAUDE_SHELL_CONFIG"
"$BIN" create created-alias --alias ca
assert_auth_synced "$CLAUDE_PLAYBOOKS_DIR/created-alias"
assert_contains "$CLAUDE_SHELL_CONFIG" "alias ca='CLAUDE_CONFIG_DIR=\"$CLAUDE_PLAYBOOKS_DIR/created-alias\" claude'"

echo "TEST 3: install local dir with manifest is one flat playbook"
SRC="$TEST_ROOT/source-pb"
mkdir -p "$SRC"
cat > "$SRC/.playbook" <<'EOF'
version = "0.1.0"
name = "srcpb"
alias = "srcalias"
description = "Suite playbook"
homepage = "https://example.com/suite"
author = "Suite Author"
EOF
printf '# Source PB\n' > "$SRC/CLAUDE.md"
"$BIN" install "$SRC" --name installed
assert_auth_synced "$CLAUDE_PLAYBOOKS_DIR/installed"
assert_contains "$CLAUDE_SHELL_CONFIG" "alias srcalias='CLAUDE_CONFIG_DIR=\"$CLAUDE_PLAYBOOKS_DIR/installed\" claude'"

echo "TEST 4: install a BARE local dir (no .playbook) succeeds — flat model"
BARE="$TEST_ROOT/bare-src"
mkdir -p "$BARE"
printf '# Bare\n' > "$BARE/CLAUDE.md"
"$BIN" install "$BARE" --name bare-installed --no-alias
assert_exists "$CLAUDE_PLAYBOOKS_DIR/bare-installed/CLAUDE.md"
assert_not_exists "$CLAUDE_PLAYBOOKS_DIR/bare-installed/.playbook"
assert_auth_synced "$CLAUDE_PLAYBOOKS_DIR/bare-installed"

echo "TEST 5: install --subdir cherry-picks one slice of a monorepo"
MONO="$TEST_ROOT/monorepo"
mkdir -p "$MONO/playbooks/sre" "$MONO/playbooks/dba"
printf '# SRE\n' > "$MONO/playbooks/sre/CLAUDE.md"
printf '# DBA\n' > "$MONO/playbooks/dba/CLAUDE.md"
"$BIN" install "$MONO" --subdir playbooks/sre --name sre --no-alias
assert_file "$CLAUDE_PLAYBOOKS_DIR/sre/CLAUDE.md"
assert_not_exists "$CLAUDE_PLAYBOOKS_DIR/sre/playbooks"
assert_auth_synced "$CLAUDE_PLAYBOOKS_DIR/sre"

echo "TEST 6: link symlinks an external dir and syncs auth into the source"
LINK_SRC="$TEST_ROOT/link-source"
mkdir -p "$LINK_SRC"
printf '# Linked\n' > "$LINK_SRC/CLAUDE.md"
"$BIN" link "$LINK_SRC" --name linked --no-alias
assert_symlink "$CLAUDE_PLAYBOOKS_DIR/linked"
assert_auth_synced "$LINK_SRC"

echo "TEST 7: run syncs auth before executing claude and forwards flags"
RUN_PB="$CLAUDE_PLAYBOOKS_DIR/run-sync"
mkdir -p "$RUN_PB"
RUN_OUT="$TEST_ROOT/run.out"
"$BIN" run run-sync --probe value > "$RUN_OUT"
assert_auth_synced "$RUN_PB"
assert_contains "$RUN_OUT" "STUB_CLAUDE_CONFIG_DIR=$RUN_PB"
assert_contains "$RUN_OUT" "STUB_ARGS=--probe value"

echo "TEST 8: start syncs auth for ad-hoc directories"
START_DIR="$TEST_ROOT/ad-hoc"
START_OUT="$TEST_ROOT/start.out"
"$BIN" start "$START_DIR" --start-probe > "$START_OUT"
assert_auth_synced "$START_DIR"
assert_contains "$START_OUT" "STUB_CLAUDE_CONFIG_DIR=$START_DIR"

echo "TEST 9: list is flat; info shows manifest metadata and no Children"
LIST_OUT="$TEST_ROOT/list.out"; INFO_OUT="$TEST_ROOT/info.out"
"$BIN" list > "$LIST_OUT"
"$BIN" info installed > "$INFO_OUT"
assert_contains "$LIST_OUT" "created"
assert_contains "$LIST_OUT" "bare-installed"
assert_contains "$INFO_OUT" "Homepage:    https://example.com/suite"
assert_contains "$INFO_OUT" "Author:      Suite Author"
if grep -q "Children:" "$INFO_OUT"; then fail "info still prints Children"; fi

echo "TEST 10: rename updates alias; dealias and delete clean up"
"$BIN" rename created-alias renamed --alias ra
assert_contains "$CLAUDE_SHELL_CONFIG" "alias ra="
"$BIN" dealias renamed
if grep -q "alias ra=" "$CLAUDE_SHELL_CONFIG"; then fail "dealias left alias"; fi
"$BIN" delete bare-installed -y
assert_not_exists "$CLAUDE_PLAYBOOKS_DIR/bare-installed"

echo "PASS: isolated binary feature tests completed"
echo "TEST_ROOT=$TEST_ROOT"
export HOME="$REAL_HOME"; export PATH="$REAL_PATH"
echo "HOME and PATH restored"
```

Expected:

- Final line `PASS: isolated binary feature tests completed`.
- No test used an installed `claude-playbook`; only `$CLAUDE_SHELL_CONFIG` was
  written; the sprite's real `~/.zshrc` and `~/.claude-playbooks` unmodified.

## Live Auth Smoke Test (real Claude in the sprite)

Regression test for new playbooks showing Claude Code's login screen. Runs
real interactive Claude Code in the pane, authenticated via the sprite's
long-lived token — no host auth files are copied anywhere.

Send to the pane (guarded):

```bash
if [ "$(hostname)" = <sprite-name> ]; then
  export CLAUDE_CODE_OAUTH_TOKEN=$(cat ~/.config/claude-code/oauth-token)
  export LIVE_ROOT=$(mktemp -d -t cpb-live.XXXXXX)
  CLAUDE_PLAYBOOKS_DIR="$LIVE_ROOT/playbooks" CLAUDE_SHELL_CONFIG="$LIVE_ROOT/zshrc" \
    "$BIN" create live-auth --no-alias
  CLAUDE_PLAYBOOKS_DIR="$LIVE_ROOT/playbooks" "$BIN" run live-auth
else echo GUARD_FAIL; fi
```

Read the pane after ~8s.

Pass:

- Claude Code reaches its normal prompt (a workspace-trust prompt is fine —
  accept it and keep inspecting).
- No `Select login method`, no `Paste code here`, no
  `claude.com/cai/oauth/authorize` URL.

Fail: any login-method menu, OAuth URL, or auth-code prompt.

Exit Claude with `/exit` (send it as text + Enter). If the TUI wedges, find the
PID via a side channel (`sprite exec -s <sprite> -- pgrep -x claude`) and kill
it — never guess and press keys blind; that can advance a login flow.

## Real-User Test: drive a real playbook end to end

This is the acceptance test the binary exists for: install a real playbook,
run it via its alias, and use it — typing into Claude like a user. It uses the
Kommander playbook because its task system gives observable state transitions
and a statusline to check.

All commands go to the sprite pane, guarded. Claude turns cost real tokens;
keep prompts short.

1. **Install the playbook** (repo root has `.playbook` with
   `subdir = "playbook"` — exercises the manifest-subdir path):

   ```bash
   "$BIN" install https://github.com/ramazanpolat/kommander-playbook --name kmd --alias kmd
   ```

   Expected: `Installed "kmd"`, alias `kmd` written, alias target ends in
   `/kmd/playbook` (the manifest subdir).

2. **Run it via the alias** — two separate sends (zsh parse-time gotcha):

   ```bash
   source ~/.zshrc
   ```
   ```bash
   kmd
   ```

   Read the pane: Claude Code starts (trust prompt → accept). PASS: no login
   screen; the Kommander SessionStart banner/task menu appears.

3. **Check the statusline**: read the pane bottom. Expected shape:
   `[<name> <version>] //TASK: none //DIR: ... //GIT: ...`.

4. **Create a task by typing** — send a short task description, e.g.:

   ```
   new task: suite-smoke -- verify kommander works in this sprite
   ```

   Wait for the turn to finish (re-read the pane every ~15s). Expected: Claude
   creates the task folder and acquires the lock. Verify via side channel:

   ```bash
   sprite exec -s <sprite> -- bash -lc 'ls ~/.claude-playbooks/kmd/playbook/data/tasks/'
   ```

   Expected: `suite-smoke/` with `TASK.md` and `.lock.held`. The statusline
   now shows `//TASK: suite-smoke`.

5. **Complete the task by typing**:

   ```
   complete current task
   ```

   Expected: Claude updates TASK.md to Completed, releases the lock, renames
   the folder to `DONE--suite-smoke`, and prints the next-action menu. Verify
   via the side channel that `DONE--suite-smoke/` exists and `.lock.held` is
   gone; statusline returns to `//TASK: none`.

6. **Exit** Claude with `/exit`.

Pass: every expected state transition observed both on screen and on disk.

## Parallel Multi-Instance Test (herdr workspace)

Tests safe concurrency of the binary across simultaneous users. Create a
dedicated herdr workspace so the panes don't crowd the working tab: move a
fresh pane to a new workspace (`herdr pane move <id> --new-workspace --label
cpb-parallel`), then split it N-1 times. Each pane opens its own `sprite
console` (same sprite is fine) and gets its own `CLAUDE_PLAYBOOKS_DIR` and
`CLAUDE_SHELL_CONFIG` under a per-pane temp root.

In all panes at once, run the isolated-feature pattern's TEST 1–5 with
pane-unique names. Then, as a shared-state race check, point two panes at the
SAME `CLAUDE_PLAYBOOKS_DIR`+`CLAUDE_SHELL_CONFIG` and have them `create`
differently-named playbooks concurrently.

Pass:

- Every pane's suite reports PASS independently.
- In the shared-root race, both playbooks exist and both aliases are present
  in the shared shell config (no lost writes, no interleaved/corrupt lines).

## Cleanup

In the sprite pane: `rm -rf "$SUITE_ROOT" "$TEST_ROOT" "$LIVE_ROOT"` and
`"$BIN" delete kmd -y` (or remove the playbooks root used). On the host: close
the extra pane(s) (`herdr pane close <id>`) and the workspace if one was
created. The sprite may simply be restored to its ready checkpoint instead.

## Report Template

```text
Sprite:
Pane(s):
Branch:
Commit:
Binary path:
go test result:
Isolated binary suite result:
Live auth smoke result:
Real-user kommander result (task create / statusline / complete):
Parallel result:
Temp roots kept for inspection:
Notes:
```
