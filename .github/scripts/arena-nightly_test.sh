#!/bin/sh
# Tests arena-nightly.sh: which release branch the nightly picks, and the
# verdict on a dispatched run, against real runs of 2026-09-30 (testdata/).
# Run by CI on both OSes: sh .github/scripts/arena-nightly_test.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
s="$here/arena-nightly.sh"
fail=0
ok() { echo "ok:   $1"; }
bad() { echo "FAIL: $1"; fail=1; }

# newest-release-branch: by version, not by text; nothing when there is none
got=$(printf 'main\nrelease/v3.9\nrelease/v3.24\nrelease/v3.10\nclaude/x\nrelease/v3.24-old\n' | sh "$s" newest-release-branch)
[ "$got" = release/v3.24 ] && ok "newest release branch by version: $got" || bad "newest release branch: '$got'"
got=$(printf 'release/v3.9\nrelease/v3.10\n' | sh "$s" newest-release-branch)
[ "$got" = release/v3.10 ] && ok "v3.10 is newer than v3.9" || bad "v3.10 vs v3.9: '$got'"
got=$(printf 'main\nclaude/x\n' | sh "$s" newest-release-branch)
[ -z "$got" ] && ok "no release branch: nothing" || bad "no release branch: '$got'"

# verdict (rc, stdout)
v() { # v <fixture> <ref> [reason]: sets rc and out
  f=$1; shift
  set +e
  out=$(sh "$s" verdict "$@" 2>&1 < "$f")
  rc=$?
  set -e
}
# today's v3.25.0 tag dispatch: plan gave bench=none, the arena job was
# skipped, and the run reported success
v "$here/testdata/nightly-v3.25.0-skipped.json" v3.25.0 "v3.25.0: a release is gated by release-gate.sh on a green phase 2 of its commit, not run here"
[ $rc = 1 ] && ok "the v3.25.0 skip fails the nightly" || bad "the v3.25.0 skip: rc $rc"
case "$out" in *"::error::phase 2 did not run on v3.25.0: its arena job was skipped (plan: v3.25.0: a release is gated"*) ok "  it names the ref, the skip and plan's reason" ;; *) bad "  message: $out" ;; esac
# today's main run: a real phase 2
v "$here/testdata/nightly-main-phase2.json" main
[ $rc = 0 ] && ok "main's phase 2 passes" || bad "main's phase 2: rc $rc, $out"
# a phase 2 still running passes (its result is watched separately)
t=$(mktemp); trap 'rm -f "$t"' EXIT
printf '{"jobs":[{"name":"plan","status":"completed","conclusion":"success"},{"name":"arena / phase2","status":"in_progress","conclusion":""}]}' > "$t"
v "$t" main
[ $rc = 0 ] && ok "a running phase 2 passes" || bad "running phase 2: rc $rc"
# no arena job at all
printf '{"jobs":[{"name":"plan","status":"completed","conclusion":"failure"},{"name":"checks","status":"completed","conclusion":"skipped"}]}' > "$t"
v "$t" release/v3.24
[ $rc = 1 ] && ok "no arena job fails" || bad "no arena job: rc $rc"
case "$out" in *"release/v3.24: it has no arena job"*) ok "  it says so" ;; *) bad "  message: $out" ;; esac
# plan not finished: ask again
printf '{"jobs":[{"name":"plan","status":"in_progress","conclusion":""}]}' > "$t"
v "$t" main
[ $rc = 2 ] && ok "plan still running: ask again (2)" || bad "plan running: rc $rc"
# a targeted run (another bench) is not phase 2
printf '{"jobs":[{"name":"plan","status":"completed","conclusion":"success"},{"name":"arena / targeted","status":"completed","conclusion":"success"}]}' > "$t"
v "$t" main
[ $rc = 1 ] && ok "a targeted run is not phase 2" || bad "targeted: rc $rc"
case "$out" in *"it ran \`arena / targeted\`, not phase 2"*) ok "  it names the bench it ran" ;; *) bad "  message: $out" ;; esac

exit $fail
