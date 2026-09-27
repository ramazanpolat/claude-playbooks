# One example of examples/upgrade.sh, run as `sh -e`: every command that
# fails ends the run. Exit 3: the old release refuses the example.
# Arguments: <old cpb> <new cpb> <example dir> <throwaway HOME> <examples/.ci>
old=$1 new=$2 dir=$3 home=$4 ci=$5
entry=playbook.cpb
[ -f "$dir/.entry" ] && entry=$(cat "$dir/.entry")
mkdir -p "$home/bin" "$home/.claude"
export HOME="$home" PATH="$ci:$home/bin:$PATH" CPB_SECRET_HELPER= FAKE_MARKETS=""
use() { ln -sf "$1" "$home/bin/cpb"; }
# A made-up machine login and account state, which must never change.
printf '{"claudeAiOauth":{"accessToken":"UPGRADE-MACHINE"}}' > "$home/.claude/.credentials.json"
printf '{"hasCompletedOnboarding":true,"oauthAccount":{"accountUuid":"upgrade-machine"}}' > "$home/.claude.json"
cp "$home/.claude/.credentials.json" "$home/machine.before"
cp "$home/.claude.json" "$home/state.before"
cd "$dir"
use "$old"
if [ -f .setup ]; then sh -e .setup; fi
if ! cpb APPLY "$entry" --dry-run > "$home/old-dry.out" 2>&1; then
  cat "$home/old-dry.out"
  exit 3
fi
cpb APPLY "$entry" --yes > "$home/old-apply.out"
cpb SHOW CREATE ALL > "$home/old.cpb"
cpb auth status --json > "$home/old-auth.json"
cpb SHOW PLAYBOOKS --json > "$home/playbooks.json"
pbs=$(python3 -c 'import json,sys; [print(p["name"]) for p in json.load(open(sys.argv[1]))]' "$home/playbooks.json")
echo "$pbs" | grep -c . > "$home/count" || true
for pb in $pbs; do cpb EXPLAIN PLAYBOOK "$pb" --json > "$home/old-explain-$pb.json"; done

use "$new"
cpb SHOW CREATE ALL > "$home/new.cpb"
if ! cmp -s "$home/old.cpb" "$home/new.cpb"; then echo "SHOW CREATE ALL differs:"; diff "$home/old.cpb" "$home/new.cpb" | head -20; exit 1; fi
cpb APPLY "$entry" --yes > "$home/new-apply.out"
if ! grep -q " 0 created, 0 changed, " "$home/new-apply.out"; then echo "the new binary changed the old state:"; cat "$home/new-apply.out"; exit 1; fi
cpb auth status --json > "$home/new-auth.json"
if ! cmp -s "$home/old-auth.json" "$home/new-auth.json"; then echo "auth status differs:"; diff "$home/old-auth.json" "$home/new-auth.json" | head -20; exit 1; fi
for pb in $pbs; do
  cpb EXPLAIN PLAYBOOK "$pb" --json > "$home/new-explain-$pb.json"
  if ! cmp -s "$home/old-explain-$pb.json" "$home/new-explain-$pb.json"; then echo "EXPLAIN $pb differs:"; diff "$home/old-explain-$pb.json" "$home/new-explain-$pb.json" | head -20; exit 1; fi
done
for pb in $pbs; do
  cfg=$(cpb SHOW PLAYBOOK "$pb" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])')
  rm -f "$cfg/.fake-claude/launch-env"
  cpb run "$pb" > /dev/null 2>&1
  if [ ! -f "$cfg/.fake-claude/launch-env" ]; then echo "run $pb did not launch claude"; exit 1; fi
done
for pb in $pbs; do
  cpb DROP PLAYBOOK "$pb" --yes > /dev/null
  if cpb SHOW PLAYBOOK "$pb" > /dev/null 2>&1; then echo "$pb is still there after DROP"; exit 1; fi
done
if ! cmp -s "$home/.claude/.credentials.json" "$home/machine.before"; then echo "the machine's login store changed"; exit 1; fi
if ! cmp -s "$home/.claude.json" "$home/state.before"; then echo "the machine's account state changed"; exit 1; fi
