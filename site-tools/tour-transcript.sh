#!/bin/sh
# Runs the commands the tour shows, in the tour's order, in throwaway homes, and
# prints each one as the page writes it ("$ command") followed by its real
# output, paths shown as /home/you. tour-outputs.py compares the tour with this
# (the check), or rewrites the tour from it (a site pass).
#
# Usage: site-tools/tour-transcript.sh <path to the cpb binary>
# Run it from a tree that has examples/ (the `claude` stand-in and example 18's
# session setup). Needs clickhouse (CPB_CLICKHOUSE, or on PATH) for the WHERE
# example; without it that command is left out and the page's copy is not checked.
set -u
cpb_bin=$1
cpb=$(cd "$(dirname "$cpb_bin")" && pwd)/$(basename "$cpb_bin")
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
chmod 755 "$repo/examples/.ci/claude" "$here/stand-in-secret-helper"

H=$(mktemp -d)
H2=$(mktemp -d)
trap 'rm -rf "$H" "$H2"' EXIT
mkdir -p "$H/bin" "$H2/bin"
ln -s "$cpb" "$H/bin/cpb"
ln -s "$cpb" "$H2/bin/cpb"
cp "$here/stand-in-secret-helper" "$H/helper"
export HOME=$H PATH="$repo/examples/.ci:$H/bin:$PATH" FAKE_MARKETS=""
unset CPB_SECRET_HELPER
cd "$H"

norm() { sed "s#$H2#/home/you#g; s#$H#/home/you#g"; }
# run <command as the page writes it> <argv...>
run() { printf '$ %s\n' "$1"; shift; "$@" 2>&1 | norm; }

# the quick start's playbooks exist before the rest of the tour
cpb CREATE PLAYBOOK work > /dev/null
cpb CREATE PLAYBOOK side ISOLATED LOGIN > /dev/null
cpb CREATE PLAYBOOK sre SANDBOX > /dev/null

echo "### try it"
run 'cpb CREATE PLAYBOOK scratch' cpb CREATE PLAYBOOK scratch
run 'cpb SHOW PLAYBOOK scratch' cpb SHOW PLAYBOOK scratch
run 'cpb DROP PLAYBOOK scratch --yes' cpb DROP PLAYBOOK scratch --yes

echo "### routing"
run 'cpb CREATE ENV router SET ANTHROPIC_BASE_URL=http://localhost:4000/v1' cpb CREATE ENV router SET ANTHROPIC_BASE_URL=http://localhost:4000/v1
run 'cpb CREATE PLAYBOOK glm ISOLATED LOGIN' cpb CREATE PLAYBOOK glm ISOLATED LOGIN
run 'cpb ALTER PLAYBOOK glm USE ENV router SET VAR ANTHROPIC_MODEL=glm-5.3' cpb ALTER PLAYBOOK glm USE ENV router SET VAR ANTHROPIC_MODEL=glm-5.3
run 'cpb ALTER PLAYBOOK glm BLOCK VAR ANTHROPIC_API_KEY' cpb ALTER PLAYBOOK glm BLOCK VAR ANTHROPIC_API_KEY
run "cpb ALTER PLAYBOOK glm ADD MODEL 'glm-5.3' LABEL 'GLM 5.3' DESCRIPTION 'via the router'" cpb ALTER PLAYBOOK glm ADD MODEL 'glm-5.3' LABEL 'GLM 5.3' DESCRIPTION 'via the router'
run "cpb ALTER PLAYBOOK glm ADD MODEL 'glm-5.3-flash' LABEL 'GLM 5.3 Flash' BEHAVES AS 'claude-sonnet-5'" cpb ALTER PLAYBOOK glm ADD MODEL 'glm-5.3-flash' LABEL 'GLM 5.3 Flash' BEHAVES AS 'claude-sonnet-5'
run 'cpb ALTER PLAYBOOK glm SET MODEL PICKER ONLY' cpb ALTER PLAYBOOK glm SET MODEL PICKER ONLY
run 'cpb EXPLAIN PLAYBOOK glm' cpb EXPLAIN PLAYBOOK glm

echo "### recipes"
mkdir -p "$H/skills-src/review"
printf '# review\n' > "$H/skills-src/review/SKILL.md"
cat > "$H/reviewer.cpb" <<EOT
-- reviewer.cpb: a recipe (it names no playbook)
ALTER PLAYBOOK
  ADD MCP SERVER files COMMAND 'npx' ARGS '-y' '@modelcontextprotocol/server-filesystem' '/srv/notes'
  ALLOW TOOL 'Bash(gh pr *)'  DENY TOOL 'Bash(git push *)'
  ADD SKILL review FROM '$H/skills-src/review'
  SET MODEL 'claude-opus-5-5';
EOT
run 'cpb APPLY reviewer.cpb TO reviewer --dry-run' cpb APPLY reviewer.cpb TO reviewer --dry-run
run 'cpb APPLY reviewer.cpb TO reviewer' cpb APPLY reviewer.cpb TO reviewer
run 'cpb SHOW CREATE PLAYBOOK reviewer --skip-secrets' cpb SHOW CREATE PLAYBOOK reviewer --skip-secrets

echo "### mcp and secrets"
export CPB_SECRET_HELPER="$H/helper"
cpb CREATE PLAYBOOK researcher NO LAUNCHER > /dev/null
cpb ALTER PLAYBOOK researcher ADD MCP SERVER files COMMAND npx ARGS -y @modelcontextprotocol/server-filesystem /srv/notes > /dev/null
run "cpb ALTER PLAYBOOK researcher ADD MCP SERVER sentry URL 'https://mcp.sentry.dev/mcp' HEADER 'Authorization' FROM 'keychain:sentry-auth'" cpb ALTER PLAYBOOK researcher ADD MCP SERVER sentry URL 'https://mcp.sentry.dev/mcp' HEADER 'Authorization' FROM 'keychain:sentry-auth'
run 'cpb EXPLAIN PLAYBOOK researcher' cpb EXPLAIN PLAYBOOK researcher
run 'cpb SHOW CREATE PLAYBOOK researcher --skip-secrets' cpb SHOW CREATE PLAYBOOK researcher --skip-secrets

echo "### sql"
run 'cpb "SELECT name, envs, isolated_login FROM PLAYBOOKS"' cpb "SELECT name, envs, isolated_login FROM PLAYBOOKS"
if [ -x "${CPB_CLICKHOUSE:-/nonexistent}" ] || command -v clickhouse > /dev/null 2>&1 || command -v ch > /dev/null 2>&1; then
  run 'cpb "SELECT playbook, key FROM VARS WHERE effective ORDER BY playbook, key"' cpb "SELECT playbook, key FROM VARS WHERE effective ORDER BY playbook, key"
fi

echo "### install"
run 'cpb --version' cpb --version

echo "### sessions"
# Example 18's own setup: a sleep stands in for claude, with a session file
# shaped like Claude Code's. Its home is separate, so the playbooks above do not
# list a `worker`.
sed -e '/^# The live session, with its transcript/,$d' -e '/^trap /d' \
    -e 's#^sleep 300 &#sleep 60 </dev/null >/dev/null 2>\&1 \&#' "$repo/examples/18-sessions/.check" > "$H2/setup.sh"
(
  export HOME=$H2 PATH="$repo/examples/.ci:$H2/bin:$PATH"
  unset CPB_SECRET_HELPER
  cd "$H2"
  cpb CREATE PLAYBOOK worker NO LAUNCHER > /dev/null
  . "$H2/setup.sh"
  printf '$ cpb SHOW SESSIONS\n'
  cpb SHOW SESSIONS 2>&1 | sed "s#$H2#/home/you#g"
  kill "$pid" 2> /dev/null
)
