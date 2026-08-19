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

# Refs are branch/tag/SHA only — reject anything hostile before it
# reaches git (the CI workflow passes a dispatch input through here).
case "$REF" in
  ''|*[!A-Za-z0-9._/-]*) echo "bad GENTAR_REF: $REF" >&2; exit 2 ;;
esac

if [ ! -d "$ARENA" ]; then
  git clone -q https://github.com/agent-realm/gentar "$ARENA"
fi
# Honor GENTAR_REF on EVERY run: fetch, resolve, detach. A cached
# .arena must never pin the engine to whatever was checked out first.
# Plain fetch covers branches/tags; a second fetch of the ref itself
# covers bare SHAs (GitHub allows want-sha).
git -C "$ARENA" fetch -q --tags origin
git -C "$ARENA" fetch -q origin "$REF" 2>/dev/null || true
sha=$(git -C "$ARENA" rev-parse -q --verify "$REF^{commit}") || {
  echo "GENTAR_REF $REF not found in agent-realm/gentar" >&2; exit 2
}
git -C "$ARENA" checkout -q --detach "$sha"
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
# A worktree's .git is a pointer file with a host-absolute path — dead
# on the bench — so `git describe` there finds nothing. Freeze the
# version HERE, where git works; scenarios read it instead of trusting
# the bench's git.
(cd "$REPO" && git describe --tags --always --dirty 2>/dev/null || echo dev) \
  > "subjects/claude-playbooks/.gentar-version"

# Run every requested scenario; report all, fail if any failed.
mkdir -p "$HERE/reports"
status=0
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
  # Copy every report this run produced into the repo, with a
  # reproduce command that works for own-arena scenarios (the
  # engine's default assumes the central arena's invocation).
  found=0
  while IFS= read -r f; do
    sed "s|^Reproduce: \`.*\`|Reproduce: \`gentar/run.sh $s\`|" "$f" \
      > "$HERE/reports/$(basename "$f")" && found=1
  done < <(find out -name 'report-*.md' -newer "$MARKER" 2>/dev/null)
  rm -f "$MARKER"
  if [ "$found" -ne 1 ]; then
    # Every terminal outcome (pass/fail/refuse) writes one; a missing
    # report means reporting itself broke — fail loudly, don't pass
    # silently without the fix-loop artifact.
    echo "no report written for $s" >&2
    rc=1
  fi
  [ "$rc" -eq 0 ] || status=$rc
done
exit "$status"
