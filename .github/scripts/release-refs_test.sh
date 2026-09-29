#!/bin/sh
# Tests release-refs.sh against a throwaway repository: which tag commits
# may be released (gate), and what the npx check says as tags appear
# (npx-check). Run by CI on both OSes: sh .github/scripts/release-refs_test.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
refs="$here/release-refs.sh"
repo=$(mktemp -d)
trap 'rm -rf "$repo"' EXIT
cd "$repo"
git init -q -b main
git config user.email t@t && git config user.name t
c() { git commit -q --allow-empty -m "$1" && git rev-parse HEAD; }

# main: base - m1 ; release/v3.24 and release/v3.23 fork at base; a feature
# branch forks at base too. origin/* are what release.yml fetches.
base=$(c base)
m1=$(c m1)
git checkout -q -b release/v3.24 "$base"; r1=$(c r1)
git checkout -q -b release/v3.23 "$base"; q1=$(c q1)
git checkout -q -b feature "$base"; f1=$(c f1)
git checkout -q main
git update-ref refs/remotes/origin/main "$m1"
git update-ref refs/remotes/origin/release/v3.24 "$r1"
git update-ref refs/remotes/origin/release/v3.23 "$q1"

fail=0
want() { # want <exit> <expected stdout|-> <cmd...>
  code=$1 out=$2; shift 2
  set +e; got=$("$@" 2>/dev/null); rc=$?; set -e
  if [ "$rc" != "$code" ] || { [ "$out" != - ] && [ "$got" != "$out" ]; }; then
    echo "FAIL: $* -> exit $rc, '$got' (want exit $code, '$out')"; fail=1
  else
    echo "ok:   $* -> exit $rc${got:+, $got}"
  fi
}

# gate
want 0 main          sh "$refs" gate v3.25.0 "$m1"        # a tag on main
want 0 main          sh "$refs" gate v3.24.0 "$base"      # on main and on the branch: main
want 0 release/v3.24 sh "$refs" gate v3.24.0 "$r1"        # a tag on its own release branch
want 0 release/v3.24 sh "$refs" gate v3.24.1 "$r1"        # a patch on it
want 0 release/v3.24 sh "$refs" gate v3.24.0-rc1 "$r1"    # an rc on it
want 1 -             sh "$refs" gate v3.23.2 "$r1"        # another minor's branch: refused
want 1 -             sh "$refs" gate v3.25.0 "$r1"        # a minor with no branch: refused
want 1 -             sh "$refs" gate v3.24.0 "$f1"        # a random branch: refused
want 1 -             sh "$refs" gate v3.24.0 "$q1"        # a different release branch: refused
want 1 -             sh "$refs" gate arena "$m1"          # not a release tag: refused
want 0 release/v3.24 sh "$refs" gate v3.24.0-rc12 "$r1"   # rcN, any number of digits
want 1 -             sh "$refs" gate v3.24.0-beta "$r1"   # only -rcN is a prerelease: refused
want 1 -             sh "$refs" gate v3.24.0- "$r1"       # a bare dash: refused
want 1 -             sh "$refs" gate v3.24.0-rc "$r1"     # -rc with no number: refused
want 1 -             sh "$refs" gate v3.24.0-rc1x "$r1"   # trailing junk: refused
# a refusal says why, on stderr, as a GitHub error
set +e; why=$(sh "$refs" gate v3.24.0 "$f1" 2>&1 >/dev/null); set -e
case "$why" in *"::error::"*"neither origin/main nor origin/release/v3.24"*) echo "ok:   the refusal names both refs" ;; *) echo "FAIL: refusal: $why"; fail=1 ;; esac

# npx-check, as tags appear (tags on any commit: git tag -l sees them all)
want 0 -  sh "$refs" npx-check 3.23.1                     # no tags yet: skipped
git tag v3.9.0 "$base"; git tag v3.10.0 "$base"           # text order would put v3.9.0 first
want 0 -  sh "$refs" npx-check 3.10.0                     # the newest is v3.10.0, by number
want 1 -  sh "$refs" npx-check 3.9.0                      # behind it: refused
git tag v3.23.1 "$base"
want 0 -  sh "$refs" npx-check 3.23.1                     # main today
want 1 -  sh "$refs" npx-check 3.22.0                     # behind: refused
git tag v3.24.0-rc1 "$r1"
want 0 -  sh "$refs" npx-check 3.23.1                     # an rc is not a release
want 0 -  sh "$refs" npx-check 3.24.0                     # the bump before its tag: a warning
git tag v3.24.0 "$r1"                                     # v3.24.0 tagged on release/v3.24
want 1 -  sh "$refs" npx-check 3.23.1                     # main still on 3.23.1: refused (npx stuck)
want 0 -  sh "$refs" npx-check 3.24.0                     # main bumped to it
want 0 -  sh "$refs" npx-check 3.25.0                     # v3.25.0's bump, before its tag
# the warning says what npx does meanwhile: v3.24.0 within the major, nothing across one
case "$(sh "$refs" npx-check 3.25.0)" in *"npx runs v3.24.0, with a notice"*) echo "ok:   the pending-tag warning names the stand-in" ;; *) echo "FAIL: pending-tag warning (same major)"; fail=1 ;; esac
want 0 -  sh "$refs" npx-check 4.0.0                      # a new major's bump, before its tag
case "$(sh "$refs" npx-check 4.0.0)" in *"npx serves nothing until v4.0.0"*"never falls back across one"*) echo "ok:   a new major's warning says npx serves nothing" ;; *) echo "FAIL: pending-tag warning (new major)"; fail=1 ;; esac
git tag v3.25.0 "$m1"
want 0 -  sh "$refs" npx-check 3.25.0
want 1 -  sh "$refs" npx-check 3.24.0                     # behind the newest: refused
git tag v3.24.1 "$r1"                                     # a patch after a newer minor
want 0 -  sh "$refs" npx-check 3.25.0                     # the newest is still v3.25.0 (version sort, not date)

exit $fail
