#!/usr/bin/env bash
# release-gate: may this commit be released?
#
#   gentar/release-gate.sh <sha>
#
# Yes when the arena workflow (.github/workflows/gentar-arena.yml) has a
# SUCCESSFUL `arena / phase2` job on exactly that commit — the full
# regression, however it was triggered: a dispatch, the `arena` tag, a
# `v*-rc*` tag. A narrowed or targeted run is a different job and never
# counts. Call it as the first job of your release workflow and make every
# publishing job `needs:` it; a release is gated BEFORE it ships, not
# tested after.
#
# Refusals are specific. A phase 2 that was cancelled — including a run
# GitHub cancelled before any job started, which is what a concurrency
# group does to a PENDING run when a newer one queues — is named, with
# the run, so "no successful phase 2" never has to be guessed at.
#
# Policy (gentar/policy.toml, [phase2]): release_gate = false turns this
# into a report that always passes; max_age_days > 0 refuses a phase 2
# older than that (the bench image, agent CLI or provider may have moved
# since). Needs `gh` with GH_TOKEN (actions: read) and GITHUB_REPOSITORY.
#
# Exit: 0 releasable · 1 not releasable (reasons printed) · 2 usage.

set -euo pipefail

SHA=${1:-}
case "$SHA" in
  ''|*[!0-9a-f]*) echo "usage: gentar/release-gate.sh <full commit sha>" >&2; exit 2 ;;
esac
[ "${#SHA}" = 40 ] || { echo "release-gate: need the full 40-character sha" >&2; exit 2; }
REPO=${GITHUB_REPOSITORY:?release-gate: GITHUB_REPOSITORY is not set}
HERE=$(cd "$(dirname "$0")" && pwd)
WF=gentar-arena.yml
JOB="arena / phase2"

# Policy knobs, read by the one reader of policy.toml.
cfg=$(python3 "$HERE/plan.py" gate-config) || exit 2
GATE=$(printf '%s\n' "$cfg" | sed -n 's/^release_gate=//p')
MAXAGE=$(printf '%s\n' "$cfg" | sed -n 's/^max_age_days=//p')

runs=$(gh api "repos/$REPO/actions/workflows/$WF/runs?head_sha=$SHA&per_page=100" \
  --jq '.workflow_runs[] | "\(.id)\t\(.status)\t\(.conclusion // "-")\t\(.event)\t\(.head_branch // "-")"') || {
  echo "release-gate: could not list $WF runs for $SHA" >&2; exit 1; }

ok="" notes=""
while IFS=$'\t' read -r id status concl event branch; do
  [ -n "$id" ] || continue
  jobs=$(gh api "repos/$REPO/actions/runs/$id/jobs?per_page=100" \
    --jq ".jobs[] | select(.name == \"$JOB\") | \"\(.conclusion // \"-\")\t\(if (.completed_at // .started_at) then (((now - ((.completed_at // .started_at) | fromdateiso8601)) / 86400) | floor) else 0 end)\"") || jobs=""
  if [ -z "$jobs" ]; then
    # No phase 2 job in this run. Only worth saying when the RUN died
    # before deciding anything: that is the eviction case.
    if [ "$concl" = cancelled ]; then
      notes="${notes}  run $id ($event, $branch) was cancelled before a phase 2 job ran — evicted from the concurrency queue, or cancelled by hand; re-trigger it\n"
    fi
    continue
  fi
  while IFS=$'\t' read -r jc age; do
    case "$jc" in
      success)
        if [ "${MAXAGE:-0}" -gt 0 ] && [ "$age" -gt "$MAXAGE" ]; then
          notes="${notes}  phase 2 in run $id passed, but $age day(s) ago (max_age_days = $MAXAGE)\n"
        else
          ok="run $id ($event, $branch), $age day(s) ago"
        fi ;;
      -) notes="${notes}  phase 2 in run $id is still $status\n" ;;
      *) notes="${notes}  phase 2 in run $id: $jc\n" ;;
    esac
  done <<EOF
$jobs
EOF
done <<EOF
$runs
EOF

if [ -n "$ok" ]; then
  echo "release-gate: $SHA passed phase 2 — $ok"
  [ -z "$notes" ] || printf 'also on this commit:\n%b' "$notes"
  exit 0
fi
if [ "$GATE" = false ]; then
  echo "release-gate: no green phase 2 on $SHA — NOT refusing ([phase2] release_gate = false)"
  [ -z "$notes" ] || printf '%b' "$notes"
  exit 0
fi
echo "release-gate: refusing $SHA — no successful '$JOB' job on this commit" >&2
[ -z "$notes" ] || printf '%b' "$notes" >&2
echo "run phase 2 on it first: push the \`arena\` tag at $SHA, a \`v*-rc*\` tag, or dispatch the arena workflow" >&2
exit 1
