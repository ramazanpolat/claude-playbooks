#!/bin/sh
# The upgrade from the previous release: every example the OLD binary can
# apply is applied with it, in a throwaway HOME with a made-up machine login,
# then the NEW binary takes over the same state and must read it exactly as
# the old one did:
#   - SHOW CREATE ALL is byte-identical from both binaries;
#   - re-applying the example with the new binary changes nothing;
#   - EXPLAIN --json of every playbook (what a launch sets) is identical in
#     every key the old release printed; a key the new release adds is
#     listed on the example's line, not compared;
#   - auth status --json is identical;
#   - every playbook launches (the stand-in claude records the launch),
#     except one sandboxed on every launch: there is no sbx here, and the
#     log says it was skipped;
#   - every playbook drops cleanly;
#   - the machine's login store and state are byte-identical throughout.
# The examples applied are the OLD release's own (its tag's examples/): the
# state a user of that release has. An example the old binary refuses is
# skipped and listed: it has nothing to upgrade from.
# Usage: examples/upgrade.sh <old cpb> <new cpb> [<old release's examples dir>]
#   (default: this directory's examples, for a quick local run)
#
# Each example runs in its own `sh -e` (examples/.ci/upgrade-one.sh): errexit
# is ignored inside an if-condition or an AND-OR list, so a subshell here
# could not be trusted to stop at the first failing command.
set -eu
abs() { echo "$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"; }
old=$(abs "$1")
new=$(abs "$2")
here=$(cd "$(dirname "$0")" && pwd)
src=$(cd "${3:-$here}" && pwd)
chmod 755 "$here/.ci/claude" "$here/secret-helper/cpb-secret-file"
# Both binaries must run here at all, or every example would read as a skip.
for b in "$old" "$new"; do
  "$b" --version > /dev/null 2>&1 || { echo "cannot run $b:"; "$b" --version 2>&1 || true; exit 1; }
done
fail=0
ran=0
for dir in "$src"/[0-9][0-9]-*/; do
  [ -d "$dir" ] || { echo "no examples in $src"; exit 1; }
  name=$(basename "$dir")
  home=$(mktemp -d)
  if sh -e "$here/.ci/upgrade-one.sh" "$old" "$new" "$dir" "$home" "$here/.ci" > "$home/log" 2>&1; then rc=0; else rc=$?; fi
  case $rc in
    0) skipped=""
       [ -f "$home/launch-skipped" ] && skipped="; launch skipped for $(tr '\n' ' ' < "$home/launch-skipped" | sed 's/ $//'): sandboxed on every launch, and no sbx here"
       added=""
       [ -s "$home/explain-added" ] && added="; EXPLAIN adds $(sort -u "$home/explain-added" | tr '\n' ' ' | sed 's/ $//')"
       echo "ok    $name ($(cat "$home/count") playbooks$skipped$added)"; ran=$((ran + 1)) ;;
    3) echo "skip  $name (the old release refuses it)" ;;
    *) echo "FAIL  $name"; sed 's/^/      /' "$home/log" | tail -30; fail=1 ;;
  esac
  rm -rf "$home"
done
[ "$ran" -gt 0 ] || { echo "no example upgraded"; exit 1; }
exit $fail
