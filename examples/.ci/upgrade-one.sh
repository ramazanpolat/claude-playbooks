# One example of examples/upgrade.sh, run as `sh -e`: every command that
# fails ends the run. Exit 3: the old release refuses the example.
# Arguments: <old cpb> <new cpb> <example dir> <throwaway HOME> <examples/.ci>
old=$1 new=$2 dir=$3 home=$4 ci=$5
entry=playbook.cpb
[ -f "$dir/.entry" ] && entry=$(cat "$dir/.entry")
mkdir -p "$home/bin" "$home/.claude"
export HOME="$home" PATH="$ci:$ci/../secret-helper:$home/bin:$PATH" CPB_SECRET_HELPER= FAKE_MARKETS=""
use() { ln -sf "$1" "$home/bin/cpb"; }
# A made-up machine login and account state, which must never change.
printf '{"claudeAiOauth":{"accessToken":"UPGRADE-MACHINE"}}' > "$home/.claude/.credentials.json"
printf '{"hasCompletedOnboarding":true,"oauthAccount":{"accountUuid":"upgrade-machine"}}' > "$home/.claude.json"
cp "$home/.claude/.credentials.json" "$home/machine.before"
cp "$home/.claude.json" "$home/state.before"
cd "$dir"
use "$old"
if [ -f .setup ]; then sh -e .setup; fi
# Exit 1 is the old release refusing a statement it does not know: a skip.
# Anything else (a crash, a signal) is a failure, never a skip.
if cpb APPLY "$entry" --dry-run > "$home/old-dry.out" 2>&1; then dry=0; else dry=$?; fi
if [ "$dry" -eq 1 ]; then cat "$home/old-dry.out"; exit 3; fi
if [ "$dry" -ne 0 ]; then echo "the old binary failed (exit $dry), not a refusal:"; cat "$home/old-dry.out"; exit 1; fi
cpb APPLY "$entry" --yes > "$home/old-apply.out"
cpb SHOW CREATE ALL > "$home/old.cpb"
cpb auth status --json > "$home/old-auth.json"
cpb SHOW PLAYBOOKS --json > "$home/playbooks.json"
pbs=$(python3 -c 'import json,sys; [print(p["name"]) for p in json.load(open(sys.argv[1]))]' "$home/playbooks.json")
echo "$pbs" | grep -c . > "$home/count" || true
# The list is checked against the registry on disk, so an empty or short
# list cannot make the per-playbook checks below pass vacuously.
ondisk=$(ls "$home/.claude-playbooks" 2>/dev/null | sort | tr '\n' ' ')
listed=$(echo "$pbs" | sort | tr '\n' ' ' | sed 's/^ *//')
if [ "$(echo $ondisk)" != "$(echo $listed)" ]; then echo "SHOW PLAYBOOKS lists [$listed], the registry holds [$ondisk]"; exit 1; fi
for pb in $pbs; do cpb EXPLAIN PLAYBOOK "$pb" --json > "$home/old-explain-$pb.json"; done

use "$new"
cpb SHOW PLAYBOOKS --json > "$home/new-playbooks.json"
newpbs=$(python3 -c 'import json,sys; [print(p["name"]) for p in json.load(open(sys.argv[1]))]' "$home/new-playbooks.json")
if [ "$(echo $newpbs)" != "$(echo $pbs)" ]; then echo "the new binary lists [$newpbs], the old one [$pbs]"; exit 1; fi
cpb SHOW CREATE ALL > "$home/new.cpb"
# Rendered as the previous release renders the same state, where the grammar
# changed on purpose since (examples/.ci/prev-syntax.py says what, and when
# to delete it); any other difference fails.
python3 "$ci/prev-syntax.py" render "$home/new.cpb" > "$home/new-as-prev.cpb"
if ! cmp -s "$home/old.cpb" "$home/new-as-prev.cpb"; then echo "SHOW CREATE ALL differs:"; diff "$home/old.cpb" "$home/new-as-prev.cpb" | head -20; exit 1; fi
# An example in the previous grammar is re-applied from a translated copy;
# the others in place, where their relative paths were resolved.
adir=$dir
if grep -qs 'ISOLATED LOGIN' "$dir"/*.cpb; then python3 "$ci/prev-syntax.py" translate "$dir" "$home/ex"; adir="$home/ex"; fi
(cd "$adir" && cpb APPLY "$entry" --yes) > "$home/new-apply.out"
if ! grep -q " 0 created, 0 changed, " "$home/new-apply.out"; then echo "the new binary changed the old state:"; cat "$home/new-apply.out"; exit 1; fi
cpb auth status --json > "$home/new-auth.json"
if ! cmp -s "$home/old-auth.json" "$home/new-auth.json"; then echo "auth status differs:"; diff "$home/old-auth.json" "$home/new-auth.json" | head -20; exit 1; fi
for pb in $pbs; do
  cpb EXPLAIN PLAYBOOK "$pb" --json > "$home/new-explain-$pb.json"
  # Every key the old binary printed is compared; one the new binary adds is
  # listed, not compared (examples/.ci/same-explain.py).
  if ! python3 "$ci/same-explain.py" "$home/old-explain-$pb.json" "$home/new-explain-$pb.json" > "$home/explain-cmp"; then echo "EXPLAIN $pb differs:"; cat "$home/explain-cmp"; exit 1; fi
  cat "$home/explain-cmp" >> "$home/explain-added"
done
for pb in $pbs; do
  # A playbook sandboxed on every launch needs sbx, which this job does not
  # have: its launch would refuse. The example's own .check covers that
  # refusal; here the launch is skipped, and upgrade.sh's line says so.
  info=$(cpb SHOW PLAYBOOK "$pb" --json | python3 -c 'import json,sys; v=json.load(sys.stdin); print(v["sandbox"]["always"], v["path"])')
  if [ "${info%% *}" = True ]; then echo "$pb" >> "$home/launch-skipped"; continue; fi
  cfg=${info#* }
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
