#!/bin/sh
# Keeps the site honest against a real cpb build, in throwaway HOMEs:
#   - site/tour.html: re-runs the commands it shows and asserts the lines a
#     reader relies on (not a byte diff: PIDs, timestamps and temp paths vary),
#     and diffs its `cpb tui` blocks against the real goldens;
#   - site/index.html: applies site-tools/showcase.cpb, runs a throwaway
#     `cpb start --delete`, checks the launch commands in runtimes.json against
#     this cpb's flags and backends, and checks that every card's data is
#     exactly what cpb reports (site-tools/showcase-data.py); check-sprites.py
#     checks the logos the page asks for exist; sync-sprite.py checks every
#     page carries the same sprite;
#   - site/templates.html: the customizer's own logic (unit tests, under Node),
#     that its reserved names are cpb's (check-keywords.py), that site/p/ is
#     what it renders, and a large set of option combinations
#     planned and applied with this cpb (check-templates.py). The template
#     files themselves are checked by the Site templates workflow.
# If a statement stops parsing or an output changes shape, this fails loudly
# instead of letting the site drift from the grammar.
#
# Usage: site-tools/verify-snippets.sh <path to the cpb binary>
set -eu
cpb_bin=$1
cpb=$(cd "$(dirname "$cpb_bin")" && pwd)/$(basename "$cpb_bin")
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
ci="$repo/examples/.ci"
chmod 755 "$ci/claude" "$here/stand-in-secret-helper"

home=$(mktemp -d)
trap 'rm -rf "$home"' EXIT
mkdir -p "$home/bin"
ln -s "$cpb" "$home/bin/cpb"
export HOME="$home" PATH="$ci:$home/bin:$PATH" CPB_SECRET_HELPER="$here/stand-in-secret-helper" FAKE_MARKETS=""
cd "$home"

fail=0
# check <description> <pattern> <<EOF / with $out already captured
# Reads the candidate text from $out (set by the caller just before calling).
check() {
  desc=$1 pattern=$2
  case $out in
    *"$pattern"*) ;;
    *)
      echo "FAIL  $desc: expected to see: $pattern" >&2
      echo "      got: $out" >&2
      fail=1
      ;;
  esac
}

echo "== hero: the four-line quick start =="
out=$(cpb CREATE PLAYBOOK work); check "hero: CREATE PLAYBOOK work" 'Created playbook "work"'
out=$(cpb CREATE PLAYBOOK side ISOLATED LOGIN); check "hero: ISOLATED LOGIN" 'Login isolated'
out=$(cpb CREATE PLAYBOOK sre SANDBOX); check "hero: SANDBOX" 'Always sandboxed'
out=$(cpb ALTER PLAYBOOK work SET SANDBOX secrets=env); check "sandbox: SET SANDBOX key" "sandbox   secrets=env"
out=$(cpb SHOW PLAYBOOK work); check "sandbox: SHOW lists the key" "Sandbox:    no (secrets=env)"
out=$(cpb ALTER PLAYBOOK work SET SANDBOX host=me@buildbox); check "sandbox: SET SANDBOX host" "sandbox   host=me@buildbox"
out=$(cpb ALTER PLAYBOOK work UNSET SANDBOX host); check "sandbox: UNSET SANDBOX host" "Altered PLAYBOOK work"

echo "== try it: scratch playbook =="
out=$(cpb CREATE PLAYBOOK scratch); check "scratch: created" 'Created playbook "scratch"'
out=$(cpb SHOW PLAYBOOK scratch); check "scratch: SHOW PLAYBOOK" 'Sandbox:    no'
out=$(cpb DROP PLAYBOOK scratch --yes); check "scratch: dropped" 'Deleted playbook "scratch"'

echo "== routing: env set, isolated login, model picker =="
out=$(cpb CREATE ENV router SET ANTHROPIC_BASE_URL=http://localhost:4000/v1); check "routing: CREATE ENV" "Created ENV router"
out=$(cpb CREATE PLAYBOOK glm ISOLATED LOGIN); check "routing: CREATE PLAYBOOK glm" 'Created playbook "glm"'
out=$(cpb ALTER PLAYBOOK glm USE ENV router SET VAR ANTHROPIC_MODEL=glm-5.3); check "routing: USE ENV" "env sets  router"
out=$(cpb ALTER PLAYBOOK glm BLOCK VAR ANTHROPIC_API_KEY); check "routing: BLOCK VAR" "blocked   ANTHROPIC_API_KEY"
out=$(cpb ALTER PLAYBOOK glm ADD MODEL 'glm-5.3' LABEL 'GLM 5.3' DESCRIPTION 'via the router'); check "routing: ADD MODEL" "model     glm-5.3"
out=$(cpb ALTER PLAYBOOK glm ADD MODEL 'glm-5.3-flash' LABEL 'GLM 5.3 Flash' BEHAVES AS 'claude-sonnet-5'); check "routing: ADD MODEL flash" "model     glm-5.3-flash"
out=$(cpb ALTER PLAYBOOK glm SET MODEL PICKER ONLY); check "routing: SET MODEL PICKER ONLY" "picker    only"
out=$(cpb EXPLAIN PLAYBOOK glm); check "routing: EXPLAIN shows the picker" 'Model picker: only: glm-5.3 (GLM 5.3), glm-5.3-flash (GLM 5.3 Flash)'

echo "== recipes: APPLY --dry-run, APPLY, SHOW CREATE =="
mkdir -p "$home/skills-src/review"
printf '# review\n' > "$home/skills-src/review/SKILL.md"
cat > "$home/reviewer.cpb" <<EOF
ALTER PLAYBOOK
  ADD MCP SERVER files COMMAND 'npx' ARGS '-y' '@modelcontextprotocol/server-filesystem' '/srv/notes'
  ALLOW TOOL 'Bash(gh pr *)'  DENY TOOL 'Bash(git push *)'
  ADD SKILL review FROM '$home/skills-src/review'
  SET MODEL 'claude-opus-5-5';
EOF
out=$(cpb APPLY reviewer.cpb TO reviewer --dry-run); check "recipe: dry-run" "Would apply reviewer.cpb: 1 created, 1 changed"
out=$(cpb APPLY reviewer.cpb TO reviewer); check "recipe: apply" "Applied reviewer.cpb: 1 created, 1 changed"
out=$(cpb SHOW CREATE PLAYBOOK reviewer --skip-secrets); check "recipe: SHOW CREATE has the MCP server" "ADD MCP SERVER files"

echo "== mcp + secret ref =="
out=$(cpb CREATE PLAYBOOK researcher NO LAUNCHER); check "mcp: created" 'Created playbook "researcher"'
out=$(cpb ALTER PLAYBOOK researcher ADD MCP SERVER files COMMAND npx ARGS -y @modelcontextprotocol/server-filesystem /srv/notes); check "mcp: add files server" "MCP server files"
out=$(cpb ALTER PLAYBOOK researcher ADD MCP SERVER sentry URL https://mcp.sentry.dev/mcp HEADER Authorization FROM keychain:sentry-auth); check "mcp: add sentry server" "MCP server sentry"
out=$(cpb EXPLAIN PLAYBOOK researcher); check "mcp: EXPLAIN shows the placeholder var" "CPB_MCP_SENTRY_H_AUTHORIZATION_"

echo "== sessions + SELECT =="
out=$(cpb "SELECT name, envs, isolated_login FROM PLAYBOOKS"); check "select: built-in table" "$(printf 'glm\trouter\ttrue')"
if [ -x "${CPB_CLICKHOUSE:-/nonexistent}" ] || command -v clickhouse >/dev/null 2>&1 || command -v ch >/dev/null 2>&1; then
  out=$(cpb "SELECT playbook, key FROM VARS WHERE effective ORDER BY playbook, key"); check "select: WHERE via clickhouse" "ANTHROPIC_BASE_URL"
else
  echo "skip   select WHERE: no clickhouse on PATH"
fi
out=$(cpb SHOW SESSIONS); check "sessions: no live sessions in a fresh HOME" "No live Claude Code sessions."

echo "== install =="
out=$(cpb --version); check "install: version banner" "cpb version"

echo "== the pages' example count is the number of examples =="
n=$(ls -d "$repo"/examples/[0-9]* | wc -l | tr -d ' ')
for f in index.html tour.html; do
  if ! grep -q "$n examples" "$repo/site/$f"; then
    echo "FAIL  site/$f does not say \"$n examples\": examples/ holds $n numbered examples" >&2
    fail=1
  fi
done

echo "== tour: every pasted output is what cpb prints =="
# tour-transcript.sh runs the page's commands in homes of its own and prints
# each with its real output; tour-outputs.py compares the page with that.
if "$here/tour-transcript.sh" "$cpb" > "$home/tour-transcript.txt" 2> "$home/tour-transcript.err"; then
  if ! python3 "$here/tour-outputs.py" "$repo/site/tour.html" "$home/tour-transcript.txt"; then
    fail=1
  fi
else
  echo "FAIL  tour-transcript.sh failed:" >&2
  cat "$home/tour-transcript.err" >&2
  fail=1
fi

echo "== tui: the tour's screens match the real goldens =="
if ! python3 "$here/check-tui-goldens.py" "$repo/site/tour.html" "$repo/internal/tui/testdata"; then
  fail=1
fi

echo "== home: every card is what cpb reports for showcase.cpb =="
# Its own HOME and stand-ins; the data comes from SHOW/EXPLAIN --json, the
# APPLY --dry-run --json plan and the files cpb wrote.
if ! python3 "$here/showcase-data.py" --cpb "$cpb" --check; then
  fail=1
fi

echo "== sprite: every page carries the same one, and every logo asked for is in it =="
if ! python3 "$here/sync-sprite.py" --check; then
  fail=1
fi
if ! python3 "$here/check-sprites.py"; then
  fail=1
fi

echo "== templates: the customizer's logic =="
if ! node "$here/test-customizer.js"; then
  fail=1
fi

echo "== templates: the customizer's reserved names are cpb's =="
if ! python3 "$here/check-keywords.py" --cpb "$cpb"; then
  fail=1
fi

echo "== templates: site/p/ is what the customizer renders, and its output applies with this cpb =="
# The page and this check share site/customizer-core.js. It checks site/p/ is
# what the code renders, then dry-runs and applies the selections in throwaway
# HOMEs (its own).
if ! python3 "$here/check-templates.py" --cpb "$cpb"; then
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo "verify-snippets: FAILED" >&2
  exit 1
fi
echo "verify-snippets: all snippets check out"
