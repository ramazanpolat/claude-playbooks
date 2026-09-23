#!/bin/sh
# gentar/bench-template/build-pilot.sh -- the bench template for the
# pilot-wire-real suite: a default shell bench with the REAL `pilot`
# (pilot-profile) installed at the release pinned in ./PILOT_PROFILE.
#
#   gentar/bench-template/build-pilot.sh <bench-host> [pilot-profile checkout]
#
# Runs on a machine that can READ pilot-profile (the pilot's Mac), not on
# the bench-host. pilot-profile is private and this repo is public, so the
# bench-host holds no credential for it and the template is built from a
# `git archive` streamed over the ssh the build already uses. Nothing
# private is fetched at run time; benches only ever see the baked copy.
#
# The archive is taken from the pinned SHA, never from the tag name, and
# only after the tag is confirmed to resolve to that SHA: a moved tag
# refuses the build instead of baking something unreviewed.
#
# Inside the template: /opt/pilot-profile holds the export plus a
# .pinned-sha file, and `install.sh --bin /usr/local/bin --no-init` links
# `pilot` onto PATH without creating a profile (pilot-profile's documented
# bench install). The suite asserts .pinned-sha against PILOT_PROFILE, so a
# stale template fails loudly.
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
HOST=${1:?usage: build-pilot.sh <bench-host> [pilot-profile checkout]}
SRC=${2:-${PILOT_PROFILE_SRC:-$HOME/agentship/pilot-profile}}
. "$HERE/PILOT_PROFILE"
TPL=cpb-pilot-bench-v1
WS=/tmp/cpb-pilot-template-build

git -C "$SRC" fetch -q --tags origin 2>/dev/null || true
got=$(git -C "$SRC" rev-parse -q --verify "$TAG^{commit}") \
  || { echo "tag $TAG not found in $SRC" >&2; exit 1; }
[ "$got" = "$SHA" ] || {
  echo "refusing: $TAG resolves to $got in $SRC, PILOT_PROFILE pins $SHA" >&2
  exit 1
}

git -C "$SRC" archive --format=tar "$SHA" \
  | ssh "$HOST" "rm -rf $WS && mkdir -p $WS && cat > $WS/pilot-profile.tar"

ssh "$HOST" "TPL=$TPL WS=$WS SHA=$SHA sh -s" <<'REMOTE'
set -e
command -v sbx >/dev/null || { echo "sbx not installed on this host" >&2; exit 1; }
B=cpb-pilot-tpl-build
sbx rm "$B" --force >/dev/null 2>&1 || true
sbx create --name "$B" shell "$WS" >/dev/null
sbx exec "$B" sh -lc "
  set -e
  sudo rm -rf /opt/pilot-profile && sudo mkdir -p /opt/pilot-profile
  sudo tar -xf '$WS/pilot-profile.tar' -C /opt/pilot-profile
  echo '$SHA' | sudo tee /opt/pilot-profile/.pinned-sha >/dev/null
  sudo /opt/pilot-profile/install.sh --bin /usr/local/bin --no-init
  test ! -e \"\$HOME/.pilot-profile\"
  command -v pilot && cat /opt/pilot-profile/VERSION
"
sbx stop "$B" >/dev/null
sbx template save "$B" "$TPL" >/dev/null
sbx rm "$B" --force >/dev/null
rm -rf "$WS"
echo "template $TPL built with pilot-profile $SHA:"
sbx template ls | grep "$TPL" || true
REMOTE
