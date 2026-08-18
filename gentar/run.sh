#!/usr/bin/env bash
# Kick this repo's arena: stage the working tree as the subject, run a
# scenario, land the report in gentar/reports/.
#
#   gentar/run.sh <scenario>          # e.g. cli-head-build
#   GENTAR_REF=main gentar/run.sh …   # pin the arena version
#
# First run: clones gentar into gentar/.arena and copies .env.example
# to .env — edit .env if your bench-host differs from the default.
# Reports: gentar/reports/report-<run_id>.md — on failure, feed one to
# an agent; it states everything needed to fix.
set -euo pipefail

SCENARIO=${1:?usage: gentar/run.sh <scenario> [more scenarios...]}
shift || true
HERE=$(cd "$(dirname "$0")" && pwd)          # <repo>/gentar
REPO=$(dirname "$HERE")
ARENA=${GENTAR_DIR:-$HERE/.arena}
REF=${GENTAR_REF:-main}

if [ ! -d "$ARENA" ]; then
  git clone -q --depth 1 --branch "$REF" https://github.com/agent-realm/gentar "$ARENA" \
    || git clone -q https://github.com/agent-realm/gentar "$ARENA"
fi
cd "$ARENA"
[ -f .env ] || cp .env.example .env

# Stage THIS checkout (working tree, uncommitted changes included) as
# the subject. Plain copy: symlinks don't resolve through the bind.
# Exclude the arena itself — it holds .env and the bench key, which
# must never ride into the subject.
mkdir -p subjects out
rm -rf "subjects/claude-playbooks"
mkdir "subjects/claude-playbooks"
(cd "$REPO" && tar \
  --exclude=./gentar/.arena --exclude=./gentar/reports \
  -cf - .) | tar -xf - -C "subjects/claude-playbooks"

mkdir -p "$HERE/reports"
for s in "$SCENARIO" "$@"; do
  # Exit code is the verdict: 0 pass · 1 fail · 2 usage/config refusal.
  MARKER=$(mktemp)
  set +e
  GENTAR_SUBJECTS_DIR="$PWD/subjects" \
    docker compose -p "arena-$(basename "$REPO")" run --rm \
      -e GENTAR_SCENARIOS_DIR=/extra \
      -v "$HERE/scenarios:/extra:ro" \
      coordinator run "$s"
  rc=$?
  set -e
  # Copy every report this run produced into the repo.
  found=0
  while IFS= read -r f; do
    cp -v "$f" "$HERE/reports/" && found=1
  done < <(find out -name 'report-*.md' -newer "$MARKER" 2>/dev/null)
  rm -f "$MARKER"
  [ "$found" -eq 1 ] || echo "no report written for $s"
  [ "$rc" -eq 0 ] || exit "$rc"
done
