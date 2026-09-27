#!/bin/sh
# Applies every example the way its README does, in a throwaway HOME:
# dry run, apply, apply again (which must change nothing), then the
# example's own .check, if it has one.
# Usage: examples/check.sh <path to the cpb binary>   (CI builds it first)
set -eu
cpb=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
here=$(cd "$(dirname "$0")" && pwd)
chmod 755 "$here/.ci/claude" "$here/.ci/with-secret"
fail=0
ran=0
for dir in "$here"/[0-9][0-9]-*/; do
  [ -d "$dir" ] || { echo "no examples in $here"; exit 1; }
  name=$(basename "$dir")
  home=$(mktemp -d)
  mkdir -p "$home/bin" && ln -s "$cpb" "$home/bin/cpb"
  # Each example runs in its own `sh -e` (examples/.ci/check-one.sh), so the
  # first failing command fails it.
  if sh -e "$here/.ci/check-one.sh" "$dir" "$home" "$here/.ci" > "$home/log" 2>&1; then
    echo "ok    $name"; ran=$((ran + 1))
  else
    echo "FAIL  $name"; sed 's/^/      /' "$home/log" "$home"/*.out 2>/dev/null | tail -30; fail=1
  fi
  rm -rf "$home"
done
[ "$ran" -gt 0 ] || [ "$fail" -ne 0 ] || { echo "no example ran"; exit 1; }
exit $fail
