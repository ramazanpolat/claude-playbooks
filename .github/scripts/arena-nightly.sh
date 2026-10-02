#!/bin/sh
# What arena-nightly runs phase 2 on, and whether each dispatched run really
# runs it. Used by arena-nightly.yml; tested by arena-nightly_test.sh.
#
#   arena-nightly.sh verdict <ref> [<plan's reason>]
#       The run's jobs on stdin, as `gh run view --json jobs` prints them.
#       0: the run has an `arena / phase2` job that was not skipped (queued,
#          running or finished; its result is watched separately).
#       1: that job is skipped or absent, said as ::error:: with the ref and
#          plan's reason. A skip must never read as green: on 2026-09-30 a
#          dispatch on the v3.25.0 tag was skipped by the kit's plan and the
#          run reported success.
#       2: the plan job has not finished yet; ask again.
#
#   arena-nightly.sh check <run id> <ref>
#       Reads the run with gh, waits (up to about 10 minutes) for its plan
#       job, and gives the verdict, with plan's reason. Read-only, so it also
#       works as a dry run on any past run.
set -eu

case "${1:-}" in
verdict)
  ref=${2:?usage: arena-nightly.sh verdict <ref> [<reason>]}
  reason=${3:-}
  jobs=$(cat)
  plan=$(printf '%s' "$jobs" | jq -r '[.jobs[] | select(.name == "plan")][0].status // "absent"')
  if [ "$plan" != completed ]; then
    exit 2
  fi
  # The arena job: a skipped one never gets its name evaluated
  # ("arena / ${{ needs.plan.outputs.bench }}"), so match the prefix.
  arena=$(printf '%s' "$jobs" | jq -r '[.jobs[] | select((.name // "") | startswith("arena / "))][0] // empty | "\(.name)\t\(.status)\t\(.conclusion // "")"')
  name=$(printf '%s' "$arena" | cut -f1)
  status=$(printf '%s' "$arena" | cut -f2)
  conclusion=$(printf '%s' "$arena" | cut -f3)
  if [ -z "$arena" ]; then
    why="it has no arena job"
  elif [ "$conclusion" = skipped ]; then
    why="its arena job was skipped"
  elif [ "$name" != "arena / phase2" ]; then
    why="it ran \`$name\`, not phase 2"
  else
    echo "phase 2 on $ref: $status${conclusion:+, $conclusion}"
    exit 0
  fi
  echo "::error::phase 2 did not run on $ref: $why${reason:+ (plan: $reason)}. Drift on $ref is not monitored tonight."
  exit 1
  ;;
check)
  id=${2:?usage: arena-nightly.sh check <run id> <ref>}
  ref=${3:?usage: arena-nightly.sh check <run id> <ref>}
  i=0
  while :; do
    jobs=$(gh run view "$id" --json jobs)
    rc=0
    printf '%s' "$jobs" | sh "$0" verdict "$ref" > /dev/null 2>&1 || rc=$?
    if [ "$rc" != 2 ]; then
      break
    fi
    i=$((i + 1))
    if [ "$i" -ge 60 ]; then
      echo "::error::phase 2 on $ref: run $id's plan did not finish in 10 minutes"
      exit 1
    fi
    sleep 10
  done
  # The jobs that depend on plan are created just after it finishes: read
  # the run once more before calling one absent.
  sleep 20
  jobs=$(gh run view "$id" --json jobs)
  planjob=$(printf '%s' "$jobs" | jq -r '[.jobs[] | select(.name == "plan")][0].databaseId // empty')
  reason=""
  if [ -n "$planjob" ]; then
    reason=$(gh run view --job "$planjob" --log 2>/dev/null | sed -n '/reason=/{s/.*reason=//;p;q;}' || true)
  fi
  printf '%s' "$jobs" | sh "$0" verdict "$ref" "$reason"
  ;;
*)
  echo "usage: arena-nightly.sh verdict <ref> [<reason>] | check <run id> <ref>" >&2
  exit 2
  ;;
esac
