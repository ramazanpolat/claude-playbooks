#!/bin/sh
# Applies every example the way its README does, in a throwaway HOME:
# dry run, apply, apply again (which must change nothing).
# Usage: examples/check.sh <path to the cpb binary>   (CI builds it first)
set -eu
cpb=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
here=$(cd "$(dirname "$0")" && pwd)
chmod 755 "$here/.ci/claude" "$here/.ci/with-secret"
export FAKE_MARKETS=""
fail=0
for dir in "$here"/[0-9][0-9]-*/; do
  name=$(basename "$dir")
  entry=playbook.cpb
  [ -f "$dir/.entry" ] && entry=$(cat "$dir/.entry")
  home=$(mktemp -d)
  bin="$home/bin"
  mkdir -p "$bin" && ln -s "$cpb" "$bin/cpb"
  (
    export HOME="$home" PATH="$here/.ci:$bin:$PATH" CPB_SECRET_HELPER=
    cd "$dir"
    [ -f .setup ] && sh .setup
    cpb APPLY "$entry" --dry-run > "$home/dry.out"
    cpb APPLY "$entry" --yes > "$home/apply.out"
    cpb APPLY "$entry" --yes > "$home/again.out"
    grep -q " 0 created, 0 changed, " "$home/again.out"
  ) > "$home/log" 2>&1 && echo "ok    $name" || { echo "FAIL  $name"; sed 's/^/      /' "$home/log" "$home"/*.out 2>/dev/null | tail -30; fail=1; }
  rm -rf "$home"
done
exit $fail
