#!/bin/sh
# Every template name the docs tell users to play must exist in this tree.
# `cpb play <name>` reads site/p/<name>.cpb at the release's own tag, so a
# template missing here is a 404 for everyone on that release, for good.
# Run on every change, it fails before a tag rather than after.
# Usage: .github/scripts/play-templates.sh <path to the cpb binary>
set -eu
cpb=$1
names=$(grep -ohE 'cpb play [a-z0-9][a-z0-9-]*( |$)' README.md docs/guides/*.md docs/reference/*.md examples/*/README.md \
  | awk '{print $3}' | sort -u)
[ -n "$names" ] || { echo "FAIL: no template name found in the docs (has the README example moved?)"; exit 1; }
fail=0
for n in $names; do
  # The URL cpb resolves the name to, through a proxy that is not there:
  # no network, and the error names it.
  url=$(HTTPS_PROXY=http://127.0.0.1:9 https_proxy=http://127.0.0.1:9 NO_PROXY= no_proxy= \
    "$cpb" play --check "$n" 2>&1 | grep -oE 'https://raw\.githubusercontent\.com/[^ "]*\.cpb' | head -1 || true)
  path=$(printf '%s\n' "$url" | sed -E 's#^https://raw\.githubusercontent\.com/[^/]+/[^/]+/[^/]+/##')
  if [ "$path" != "site/p/$n.cpb" ]; then
    echo "FAIL: $n resolves to '$url', not site/p/$n.cpb at a tag"; fail=1; continue
  fi
  if [ ! -f "$path" ]; then
    echo "FAIL: the docs play the template $n, and $path is not in this tree"; fail=1; continue
  fi
  if ! "$cpb" play --check "./$path" > /dev/null; then
    echo "FAIL: $path does not pass cpb play --check"; fail=1; continue
  fi
  echo "ok   $n ($path)"
done
if [ -d site/p ]; then
  "$cpb" play --check site/p || fail=1
fi
exit $fail
