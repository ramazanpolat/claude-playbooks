#!/bin/sh
# The upgrade from the previous release: every example the OLD binary can
# apply is applied with it, in a throwaway HOME with a made-up machine login,
# then the NEW binary takes over the same state and must read it exactly as
# the old one did:
#   - SHOW CREATE ALL is byte-identical from both binaries;
#   - re-applying the example with the new binary changes nothing;
#   - EXPLAIN --json of every playbook (what a launch sets) is identical;
#   - auth status --json is identical;
#   - every playbook launches (the stand-in claude records the launch);
#   - every playbook drops cleanly;
#   - the machine's login store and state are byte-identical throughout.
# The examples applied are the OLD release's own (its tag's examples/): the
# state a user of that release has. An example the old binary refuses is
# skipped and listed: it has nothing to upgrade from.
# Usage: examples/upgrade.sh <old cpb> <new cpb> [<old release's examples dir>]
#   (default: this directory's examples, for a quick local run)
set -eu
abs() { echo "$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"; }
old=$(abs "$1")
new=$(abs "$2")
here=$(cd "$(dirname "$0")" && pwd)
src=$(cd "${3:-$here}" && pwd)
chmod 755 "$here/.ci/claude" "$here/.ci/with-secret"
export FAKE_MARKETS=""
fail=0
for dir in "$src"/[0-9][0-9]-*/; do
  name=$(basename "$dir")
  entry=playbook.cpb
  [ -f "$dir/.entry" ] && entry=$(cat "$dir/.entry")
  home=$(mktemp -d)
  mkdir -p "$home/bin" "$home/.claude"
  if (
    export HOME="$home" PATH="$here/.ci:$home/bin:$PATH" CPB_SECRET_HELPER=
    use() { ln -sf "$1" "$home/bin/cpb"; }
    # A made-up machine login and account state, which must never change.
    printf '{"claudeAiOauth":{"accessToken":"UPGRADE-MACHINE"}}' > "$home/.claude/.credentials.json"
    printf '{"hasCompletedOnboarding":true,"oauthAccount":{"accountUuid":"upgrade-machine"}}' > "$home/.claude.json"
    cp "$home/.claude/.credentials.json" "$home/machine.before"
    cp "$home/.claude.json" "$home/state.before"
    cd "$dir"
    use "$old"
    [ -f .setup ] && sh .setup
    if ! cpb APPLY "$entry" --dry-run > "$home/old-dry.out" 2>&1; then
      echo "skip  $name (the old release refuses it)" > "$home/skip"
      exit 3
    fi
    cpb APPLY "$entry" --yes > "$home/old-apply.out"
    cpb SHOW CREATE ALL > "$home/old.cpb"
    cpb auth status --json > "$home/old-auth.json"
    pbs=$(cpb SHOW PLAYBOOKS --json | python3 -c 'import json,sys; [print(p["name"]) for p in json.load(sys.stdin)]')
    for pb in $pbs; do cpb EXPLAIN PLAYBOOK "$pb" --json > "$home/old-explain-$pb.json"; done

    use "$new"
    cpb SHOW CREATE ALL > "$home/new.cpb"
    cmp -s "$home/old.cpb" "$home/new.cpb" || { echo "SHOW CREATE ALL differs:"; diff "$home/old.cpb" "$home/new.cpb" | head -20; exit 1; }
    cpb APPLY "$entry" --yes > "$home/new-apply.out"
    grep -q " 0 created, 0 changed, " "$home/new-apply.out" || { echo "the new binary changed the old state:"; cat "$home/new-apply.out"; exit 1; }
    cpb auth status --json > "$home/new-auth.json"
    cmp -s "$home/old-auth.json" "$home/new-auth.json" || { echo "auth status differs"; diff "$home/old-auth.json" "$home/new-auth.json" | head -20; exit 1; }
    for pb in $pbs; do
      cpb EXPLAIN PLAYBOOK "$pb" --json > "$home/new-explain-$pb.json"
      cmp -s "$home/old-explain-$pb.json" "$home/new-explain-$pb.json" || { echo "EXPLAIN $pb differs"; diff "$home/old-explain-$pb.json" "$home/new-explain-$pb.json" | head -20; exit 1; }
    done
    for pb in $pbs; do
      cfg=$(cpb SHOW PLAYBOOK "$pb" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])')
      rm -f "$cfg/.fake-claude/launch-env"
      cpb run "$pb" > /dev/null 2>&1 || { echo "run $pb failed"; exit 1; }
      test -f "$cfg/.fake-claude/launch-env" || { echo "run $pb did not launch claude"; exit 1; }
    done
    for pb in $pbs; do
      cpb "DROP PLAYBOOK $pb --yes" > /dev/null
      ! cpb SHOW PLAYBOOK "$pb" > /dev/null 2>&1 || { echo "$pb is still there after DROP"; exit 1; }
    done
    cmp -s "$home/.claude/.credentials.json" "$home/machine.before" || { echo "the machine's login store changed"; exit 1; }
    cmp -s "$home/.claude.json" "$home/state.before" || { echo "the machine's account state changed"; exit 1; }
  ) > "$home/log" 2>&1; then rc=0; else rc=$?; fi
  case $rc in
    0) echo "ok    $name" ;;
    3) cat "$home/skip" ;;
    *) echo "FAIL  $name"; sed 's/^/      /' "$home/log" | tail -30; fail=1 ;;
  esac
  rm -rf "$home"
done
exit $fail
