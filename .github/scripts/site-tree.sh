#!/bin/sh
# Builds the tree the site's checks run in: a worktree at the ref
# site/cpb-ref names, with this checkout's site/ and site-tools/ laid over it,
# and that ref's cpb built at its root. The site documents a release, not
# the work in flight: what it is checked against (cpb, README, docs,
# examples, the TUI goldens) is the pinned ref's, so a change to cpb reaches
# the site's checks only when a site pass moves the pin.
# Usage: .github/scripts/site-tree.sh <out dir>   (needs the history: fetch-depth 0)
set -eu
out=$1
ref=$(sed -e 's/#.*//' -e 's/[[:space:]]//g' site/cpb-ref | grep . | head -1 || true)
[ -n "$ref" ] || { echo "site/cpb-ref names no ref" >&2; exit 1; }
git worktree add -q --detach "$out" "$ref"
rm -rf "$out/site" "$out/site-tools"
cp -R site site-tools "$out/"
v=$(git -C "$out" describe --tags --always --match 'v*' 2>/dev/null || echo dev)
(cd "$out" && go build -ldflags "-X github.com/ramazanpolat/claude-playbooks/cmd.Version=$v" -o cpb .)
echo "site tree: $ref ($v) at $out"
