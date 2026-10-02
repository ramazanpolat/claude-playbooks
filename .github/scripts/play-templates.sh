#!/bin/sh
# Every template name the docs say to play must exist in this tree.
# Run in the site's tree (.github/scripts/site-tree.sh: the pinned ref's docs
# and cpb, this checkout's site/), by templates-verify.yml.
# `cpb play <name>` reads site/p/<name>.cpb at the release's own tag, so a
# template missing here is a 404 for everyone on that release, for good.
# It runs with the site's checks, and in the release PR's site pass (the pin at
# HEAD), so it fails before a tag rather than after.
# Usage: .github/scripts/play-templates.sh <path to the cpb binary>
set -eu
cpb=$1
# Each file is read as one line, so `cpb play` wrapped before the name still
# counts; the name ends at a space, a backtick (`cpb play x` inline) or the end.
played='cpb play [a-z0-9][a-z0-9-]*([ `]|$)'
# The pattern is checked on a sample, not on the docs: docs that play no
# template (a release before play's) are fine, a pattern that finds none is not.
[ "$(printf 'see `cpb play code-reviewer` and\n' | grep -oE "$played" | tr -d '`' | awk '{print $3}')" = code-reviewer ] \
  || { echo "FAIL: the template-name pattern no longer finds a name"; exit 1; }
# A glob that matches nothing stays literal in sh: skip it, or set -e ends the
# loop there and the files after it go unread.
names=$(for f in README.md SPEC.md docs/guides/*.md docs/reference/*.md examples/*/README.md; do [ -f "$f" ] || continue; tr '\n' ' ' < "$f"; echo; done \
  | grep -oE "$played" | tr -d '`' | awk '{print $3}' | sort -u || true)
[ -n "$names" ] || echo "no template is played in these docs"
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
