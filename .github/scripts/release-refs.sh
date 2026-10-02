#!/bin/sh
# Where a release may come from, and whether npx serves the newest release.
# Used by release.yml and ci.yml; tested by release-refs_test.sh.
#
#   release-refs.sh gate <tag> <sha>
#       Passes (exit 0, printing where) when <sha> is on origin/main, or on
#       origin/release/vX.Y for the tag's own minor (v3.24.1 → release/v3.24).
#       Anything else fails (exit 1) with the reason: a release that cannot
#       ship must say so, never pass green while publishing nothing.
#       origin/main and origin/release/* must already be fetched.
#
#   release-refs.sh npx-check <package.json version>
#       Compares package.json with the highest release tag, by version (a
#       prerelease such as v3.24.0-rc1 is not a release). Equal passes. Behind
#       it fails: npx would keep serving the old binary. Ahead of it, with no
#       tag of that version yet, passes with a warning: the bump commit of a
#       release that is about to be tagged. The warning says what npx does
#       meanwhile: runs the newest release within the same major, or nothing
#       for a new major (bin/npx-shim.sh never falls back across one). A
#       release candidate (X.Y.Z-rcN) that is tagged and newer than the
#       highest release passes: npx serves that pre-release, which is right.
set -eu

# minor_of v3.24.1 → v3.24; v3.24.0-rc1 → v3.24; anything else ("-beta",
# a bare "-", "-rc" with no number) → "".
minor_of() {
  printf '%s\n' "$1" | sed -n 's/^\(v[0-9][0-9]*\.[0-9][0-9]*\)\.[0-9][0-9]*\(-rc[0-9][0-9]*\)\{0,1\}$/\1/p'
}

# newer A B: exit 0 when version A is greater than version B (X.Y.Z).
newer() {
  awk -v a="$1" -v b="$2" 'BEGIN {
    n = split(a, x, "."); m = split(b, y, ".")
    for (i = 1; i <= 3; i++) { if ((x[i] + 0) > (y[i] + 0)) exit 0; if ((x[i] + 0) < (y[i] + 0)) exit 1 }
    exit 1 }'
}

case "${1:-}" in
gate)
  tag=${2:?usage: release-refs.sh gate <tag> <sha>}
  sha=${3:?usage: release-refs.sh gate <tag> <sha>}
  minor=$(minor_of "$tag")
  if [ -z "$minor" ]; then
    echo "::error::$tag is not a release tag (vX.Y.Z, or vX.Y.Z-rcN); refusing to release it." >&2
    exit 1
  fi
  if git merge-base --is-ancestor "$sha" refs/remotes/origin/main 2>/dev/null; then
    echo main
    exit 0
  fi
  branch="release/$minor"
  if git rev-parse --verify --quiet "refs/remotes/origin/$branch" >/dev/null &&
    git merge-base --is-ancestor "$sha" "refs/remotes/origin/$branch" 2>/dev/null; then
    echo "$branch"
    exit 0
  fi
  echo "::error::Tag $tag points to $sha, which is on neither origin/main nor origin/$branch. A release comes from main or from its own minor's release branch; tag a commit on one of them." >&2
  exit 1
  ;;
npx-check)
  pkg=${2:?usage: release-refs.sh npx-check <package.json version>}
  latest=$(git tag -l 'v[0-9]*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)
  if [ -z "$latest" ]; then
    echo "no release tags yet; skipping"
    exit 0
  fi
  if [ "v$pkg" = "$latest" ]; then
    echo "package.json version ($pkg) matches the latest release ($latest)"
    exit 0
  fi
  case "$pkg" in
  *-rc*)
    if [ -n "$(minor_of "v$pkg")" ] && [ -n "$(git tag -l "v$pkg")" ] && newer "${pkg%%-rc*}" "${latest#v}"; then
      echo "package.json names v$pkg, a tagged release candidate newer than the latest release ($latest): npx serves it"
      exit 0
    fi
    ;;
  esac
  if newer "$pkg" "${latest#v}" && [ -z "$(git tag -l "v$pkg")" ]; then
    # The shim stands in the newest release only within one major.
    if [ "${pkg%%.*}" = "$(printf '%s' "${latest#v}" | cut -d. -f1)" ]; then
      npx="npx runs $latest, with a notice, until v$pkg is published."
    else
      npx="npx serves nothing until v$pkg is published: a new major, and the shim never falls back across one."
    fi
    echo "::warning::package.json version ($pkg) is ahead of the latest release ($latest) and v$pkg is not tagged yet: a release pending its tag. $npx"
    exit 0
  fi
  echo "::error::package.json version ($pkg) != the latest release ($latest). npx would serve the wrong binary: bump package.json to ${latest#v} (or to the release this commit prepares)." >&2
  exit 1
  ;;
*)
  echo "usage: release-refs.sh gate <tag> <sha> | npx-check <version>" >&2
  exit 2
  ;;
esac
