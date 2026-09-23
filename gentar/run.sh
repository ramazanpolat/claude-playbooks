#!/usr/bin/env bash
# Kick this repo's arena: stage the working tree as the subject, run a
# scenario, land the report in gentar/reports/.
#
#   gentar/run.sh <scenario>            # e.g. cli-head-build
#   GENTAR_REF=v0.2.0 gentar/run.sh …   # run against another engine version
#   GENTAR_REF=main gentar/run.sh …     # …or the engine's tip, unpinned
#
# First run: clones gentar into gentar/.arena and copies .env.example
# to .env. Set GENTAR_BENCH_HOST and GENTAR_BENCH_USER there — there is
# no default bench-host, and an unset one is a refusal (exit 2), not a
# run against somebody else's machine.
# Reports: gentar/reports/report-<run_id>.md — on failure, feed one to
# an agent; it states everything needed to fix.
#
# Two arenas on one Docker host collide on the published ClickHouse and
# OTLP ports. Both are env knobs the engine's compose file reads, and
# docker compose takes them from this script's environment, so they need
# no file edit:
#
#   GENTAR_CLICKHOUSE_HOST_PORT=8124 GENTAR_OTLP_HOST_PORT=14320 \
#     gentar/run.sh cli-head-build
#
# The only thing to edit below is SUBJECT, if your repo's directory name
# is not the subject name your scenarios declare.
set -euo pipefail

# --stage-engine clones/fetches/checks out the engine and stops there.
# dryrun.py needs the engine's scenario parser but no bench, so without
# this the only way to get one was a full bench run — the cheap check
# would have required the expensive one first.
STAGE_ONLY=0
REVIEW_ONLY=0
case "${1:-}" in
  --stage-engine) STAGE_ONLY=1; shift ;;
  --review)       REVIEW_ONLY=1; shift ;;
esac

if [ "$STAGE_ONLY" = 0 ] && [ "$REVIEW_ONLY" = 0 ]; then
  SCENARIO=${1:?usage: gentar/run.sh [--stage-engine|--review] <scenario> [more scenarios...]}
  shift || true
else
  SCENARIO=""
fi
HERE=$(cd "$(dirname "$0")" && pwd)          # <repo>/gentar
REPO=$(dirname "$HERE")
# Set explicitly rather than taken from the directory name: this repo is
# worked on in git worktrees (~/DEV/.worktrees/claude-playbooks/<agent>/<branch>),
# whose basename is the branch, not the repo -- so the kit's default,
# $(basename "$REPO"), names a subject no scenario declares.
SUBJECT=claude-playbooks      # dir under the subjects root — MUST match
                              # `subject = "…"` in your scenario TOMLs
ARENA=${GENTAR_DIR:-$HERE/.arena}
# Engine version. A RELEASE TAG by default, never a moving branch: the
# engine is a separate repo on its own release cycle, so `main` means
# every adopter's suites can change behaviour on a day nobody touched
# this repo. That already happened once — the kit advertised a scenario
# feature the engine's tip did not parse yet, and adopters saw a load
# error they had not caused. Bump this deliberately: change the default,
# run your suites, commit the bump as its own change. `main` stays
# available for anyone tracking the engine on purpose.
REF=${GENTAR_REF:-v0.2.0}

# --review: has this repo outgrown its suites?
#
# The arena is rebuilt from scratch every run, so it cannot go stale. The
# ADAPTATION can: a repo grows a command, a flag, an install step, and
# the existing suites still pass because they never mentioned it. Nothing
# fails, the board stays green, and coverage decays quietly — which is
# the failure mode worth naming, because it is the one nobody notices.
#
# This reports and stops. It does not fail, it does not write, and it
# does not decide what a suite should assert: matching a repo's real
# behaviour to an assertion needs reading comprehension, so the report
# is the input to that judgement, not a substitute for it. Needs no
# engine, no Docker and no bench.
if [ "$REVIEW_ONLY" = 1 ]; then
  HERE=$(cd "$(dirname "$0")" && pwd)
  REPO=$(dirname "$HERE")
  cd "$REPO"

  printf 'adaptation review — %s\n\n' "$SUBJECT"

  sc=$(ls "$HERE"/scenarios/*.toml 2>/dev/null || true)
  if [ -z "$sc" ]; then
    echo "no scenarios in gentar/scenarios — this repo is not adapted yet"
    exit 0
  fi

  # What the suites talk about: every quoted/bare token in oracle steps
  # and verify blocks. Crude on purpose — a name that appears anywhere in
  # a suite counts as mentioned, so this errs toward saying "covered",
  # and a gap it reports is unlikely to be a false alarm.
  # Split on path separators too: a suite writes `./install.sh` or
  # `bin/tool`, and comparing those whole against a basename never
  # matches — the first version of this check reported install.sh as
  # unasserted for a suite whose very first step runs it.
  mentions=$(cat $sc | grep -vE '^[[:space:]]*#' \
    | tr -c 'A-Za-z0-9_.-' '\n' | sort -u)

  echo "suites:"
  for f in $sc; do
    printf '  %-28s %s asserted\n' "$(basename "$f")" \
      "$(grep -c '^\[\[verify' "$f" 2>/dev/null || echo 0)"
  done
  echo

  # Candidates the repo exposes: executables it ships, and the scripts a
  # README tells a person to run. Both are things a fresh machine would
  # encounter, which is what a scenario is for.
  # Candidates are the surface a fresh machine exposes. For scripts and
  # bin/, that is the file itself. For this Go CLI it is the REGISTERED
  # SUBCOMMANDS -- cobra's Use: fields -- not cmd/'s source files, which are
  # implementation and helpers a scenario can never invoke (root.go,
  # table.go, ...). Listing files buried the real gaps under names no suite
  # should ever mention. A subcommand candidate is printed as `cmd <name>`
  # and dated by the file that defines it.
  cands=$( { git ls-files 2>/dev/null | grep -E '^(bin|scripts)/' || true
             git ls-files 2>/dev/null | grep -E '\.(sh|py)$' | grep -vE '^(gentar|test|tests)/' || true
             git grep -hoE 'Use:[[:space:]]*"[a-z][a-z0-9-]*' -- 'cmd/*.go' 2>/dev/null \
               | sed -E 's/Use:[[:space:]]*"/cmd:/' || true
           } | sort -u)

  gaps=0
  for c in $cands; do
    case "$c" in
      cmd:*) name=${c#cmd:}; base=$name; stem=$name; label="cmd $name"
             src=$(git grep -lE "Use:[[:space:]]*\"$name([ \"])" -- 'cmd/*.go' 2>/dev/null | head -1) ;;
      *)     base=$(basename "$c"); stem=${base%.*}; label=$c; src=$c ;;
    esac
    if ! printf '%s\n' "$mentions" | grep -qxF "$base" \
       && ! printf '%s\n' "$mentions" | grep -qxF "$stem"; then
      if [ "$gaps" = 0 ]; then echo "the repo ships these, and no suite mentions them:"; fi
      last=$(git log -1 --format='%ad' --date=short -- "${src:-$c}" 2>/dev/null || echo '?')
      printf '  %-40s last changed %s\n' "$label" "${last:-?}"
      gaps=$((gaps + 1))
    fi
  done

  echo
  if [ "$gaps" = 0 ]; then
    echo "no gaps found by this check. It only looks at shipped executables and"
    echo "scripts — a behaviour change inside a file it already knows about will"
    echo "not show up here. Read the diff since the scenarios last changed:"
  else
    echo "$gaps unasserted. A gap is a question, not a defect: some of these"
    echo "should have a suite, some never will. Deciding which is the work, and"
    echo "it needs someone who has read the repo. Start from the diff:"
  fi
  # The commit that last touched a SCENARIO FILE, not the gentar dir:
  # staging the runner or a report would otherwise read as "the suites
  # were just updated" and the suggested diff would come back empty,
  # which is exactly the reassuring-but-wrong answer this check exists
  # to avoid.
  since=$(git log -1 --format='%h %ad' --date=short -- "$HERE"/scenarios/'*.toml' 2>/dev/null || true)
  if [ -n "$since" ]; then
    printf '  scenarios last changed at %s\n' "$since"
    printf '  git diff %s..HEAD -- . ":(exclude)gentar"\n' "${since%% *}"
  fi
  exit 0
fi

# Refs are branch/tag/SHA only — reject anything hostile before it
# reaches git (the CI workflow passes a dispatch input through here).
case "$REF" in
  ''|*[!A-Za-z0-9._/-]*) echo "bad GENTAR_REF: $REF" >&2; exit 2 ;;
esac

# Where the engine comes from. The default is a plain anonymous https
# clone of the upstream repo, which is all a public engine needs.
#
#   GENTAR_REPO_URL      any git URL — your fork, an internal mirror, or
#                        a local path. Use it and nothing below applies.
#   GENTAR_CLONE_SSH_KEY path to a read-only deploy key, for the case
#                        where the engine repo is PRIVATE: https then
#                        fails on a CI runner (a laptop may still pass
#                        via a credential helper, which is how the
#                        difference hides until CI). Setting it switches
#                        the default URL to ssh and pins the identity.
GENTAR_REPO_URL=${GENTAR_REPO_URL:-}
if [ -n "$GENTAR_REPO_URL" ]; then
  CLONE_URL=$GENTAR_REPO_URL
elif [ -n "${GENTAR_CLONE_SSH_KEY:-}" ]; then
  CLONE_URL=git@github.com:agent-realm/gentar.git
else
  CLONE_URL=https://github.com/agent-realm/gentar
fi
if [ -n "${GENTAR_CLONE_SSH_KEY:-}" ]; then
  export GIT_SSH_COMMAND="ssh -i $GENTAR_CLONE_SSH_KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
fi

if [ ! -d "$ARENA" ]; then
  git clone -q "$CLONE_URL" "$ARENA"
else
  # A cached .arena remembers the URL it was first cloned from, so
  # switching GENTAR_REPO_URL (to a fork, a mirror) would otherwise keep
  # fetching the old engine and look like the switch did nothing.
  git -C "$ARENA" remote set-url origin "$CLONE_URL"
fi
# Honor GENTAR_REF on EVERY run: fetch, resolve, detach. A cached
# .arena must never pin the engine to whatever was checked out first.
# Fetching the ref itself covers branches, tags, and bare SHAs (GitHub
# allows want-sha) — but resolves via FETCH_HEAD, NOT the ref name: a
# plain `git fetch origin main` writes FETCH_HEAD only and never moves
# the local branch, so rev-parse main would answer with the stale tip.
if ! git -C "$ARENA" fetch -q --tags origin "$REF"; then
  echo "GENTAR_REF $REF not found in $CLONE_URL" >&2; exit 2
fi
sha=$(git -C "$ARENA" rev-parse -q --verify FETCH_HEAD^{commit}) || {
  echo "GENTAR_REF $REF not resolvable in $CLONE_URL" >&2; exit 2
}
git -C "$ARENA" checkout -q --detach "$sha"
cd "$ARENA"
[ -f .env ] || cp .env.example .env

if [ "$STAGE_ONLY" = 1 ]; then
  echo "engine staged: $ARENA @ $REF ($sha)" >&2
  echo "arena env: $ARENA/.env" >&2
  exit 0
fi

# Stage THIS checkout (working tree, uncommitted changes included) as
# the subject. Plain copy: symlinks don't resolve through the bind.
#
# Three exclusions, each for a reason:
#   gentar/.arena    holds .env and the bench key — secrets must never
#                    ride into a bench the subject's own agent can read
#   gentar/reports   previous verdicts; dead weight
#   .git             a CREDENTIAL on CI. actions/checkout leaves its
#                    auth header in .git/config, so shipping .git hands
#                    the job token to anything running in the bench
#                    (PR #27 review). It is also useless there — a
#                    worktree's .git is a host-absolute pointer file —
#                    which is why .gentar-version is frozen below.
# A subject whose scenarios genuinely need git history must ship it
# deliberately, from a checkout that persists no credentials.
mkdir -p subjects out
rm -rf "subjects/$SUBJECT"
mkdir "subjects/$SUBJECT"
(cd "$REPO" && tar \
  --exclude=./.git --exclude=./gentar/.arena --exclude=./gentar/reports \
  -cf - .) | tar -xf - -C "subjects/$SUBJECT"
# A worktree's .git is a pointer file with a host-absolute path — dead
# on the bench — so `git describe` there finds nothing. Freeze the
# version HERE, where git works; scenarios read it instead of trusting
# the bench's git.
# --match 'v*': the workflow's keyword tags (`arena`, `arena-*`) are floating
# triggers, and a bare `git describe --tags` returns whichever tag is NEAREST,
# of any kind -- so moving `arena` onto a commit would make the built binary
# report "arena-3-g..." as its version. That exact trap broke this repo's CI
# before (the `arena` tag shadowed v3.13.0).
(cd "$REPO" && git describe --tags --always --dirty --match 'v*' 2>/dev/null || echo dev) \
  > "subjects/$SUBJECT/.gentar-version"

# Rebuild the coordinator image from the checkout we just detached.
#
# This is the difference between a fresh ENGINE and a fresh CHECKOUT. The
# compose service is `build: ./coordinator`, so `docker compose run` happily
# reuses a cached image -- and on a long-lived self-hosted runner that image
# can be months older than the source above, silently. Symptom when it bit:
# the credential guard refused a run for a variable it was given, because the
# cached image predated the guard becoming "any of these" rather than "all".
# GENTAR_REF was honoured perfectly the whole time; the code that ran was not
# the code that was fetched. gentar's own gate builds before every run for
# this reason.
# Nothing this script starts may outlive it. Two independent guarantees,
# because each covers what the other cannot.
#
# `docker compose run --rm` removes only the coordinator: clickhouse and
# otelcol come up via depends_on as ordinary `up` containers
# (AutoRemove=false) and survive, and clickhouse owns a named volume. Each
# project then strands two containers and a volume per run -- invisible
# until a laptop is full of them.
#
#   1. EVERY container is --rm. The compose spec has no per-service
#      auto-remove key and `compose up` has no `--rm`, so the arena's
#      services are started as one-off `compose run -d --rm` containers --
#      the only way to get daemon-level AutoRemove. A stopped container is
#      then removed by the Docker daemon itself, whatever stopped it: ctrl-c,
#      SIGKILL, OOM, or this script dying before its trap can run. The
#      engine's compose.rm.yml overlay turns clickhouse's named volume
#      anonymous so --rm reclaims that too.
#   2. The trap tears the project down anyway, on a pass, a failing verdict,
#      a refusal and ctrl-c alike -- and never changes the verdict: teardown
#      failure is swallowed, the scenario's exit code is not.
#
# The overlay ships with the engine, so an older GENTAR_REF may not have it;
# guarantee 1 still holds for the containers, only the named volume survives
# until the trap's `down -v`.
ARENA_FILES=(-f docker-compose.yml)
[ -f "$ARENA/compose.rm.yml" ] && ARENA_FILES+=(-f compose.rm.yml)

arena() { docker compose "${ARENA_FILES[@]}" -p "arena-$SUBJECT" "$@"; }

# The bench key must EXIST before compose is asked to mount it. The
# engine's compose file declares it as a SECRET with a bind source
# (`file: ${GENTAR_BENCH_KEY_FILE:-~/.ssh/id_ed25519}`), so a missing
# file is rejected by the daemon at container-create ("bind source path
# does not exist") with exit 1 — before the coordinator's own exit-2
# refusal can say what is missing. An adopter with no ed25519 key would
# see a Docker mount error naming neither the variable to set nor the
# path tried.
#
# Ask compose for the path it actually RESOLVED rather than re-deriving
# it here: compose honours the arena's .env as well as this shell, and a
# check reading only the shell would pass while the run mounts a
# different, missing file. Same shape as the engine's own bin/arena, so
# the two say the same thing. The path is printed, never the contents.
require_bench_key() {
  local key
  # `compose config --format json` PRETTY-PRINTS, so "bench_ssh_key" and
  # its "file" land on different lines and a single-line sed match finds
  # nothing — then falls back to the shell var and passes while the run
  # mounts a different, missing file. Verified: a bad path set only in
  # the arena's .env slipped straight through to the daemon's mount
  # error. Take the first "file" line AFTER the bench_ssh_key key
  # instead, which is line-oriented and needs no JSON parser (this must
  # work on a stock runner with no python dependency).
  key=$({ arena config --format json 2>/dev/null || true; } \
    | sed -n '/"bench_ssh_key"/,/}/{ s/.*"file": *"\([^"]*\)".*/\1/p; }' \
    | head -1)
  [ -n "$key" ] || key="${GENTAR_BENCH_KEY_FILE:-$HOME/.ssh/id_ed25519}"
  case "$key" in "~/"*) key="$HOME/${key#\~/}" ;; esac
  [ -r "$key" ] && return 0
  echo "bench ssh key not found at $key — set GENTAR_BENCH_KEY_FILE" \
       "(in gentar/.arena/.env or the shell) to the key the coordinator" \
       "uses to reach the bench-host" >&2
  exit 2
}

# `compose down` does not stop `run`-created containers (they are one-offs,
# not services), and stopping is what triggers AutoRemove -- so stop by
# label first, then let `down -v` clear the network.
arena_stop_all() {
  local cids
  cids=$(docker ps -q --filter "label=com.docker.compose.project=arena-$SUBJECT" 2>/dev/null || true)
  [ -n "$cids" ] && docker stop $cids >/dev/null 2>&1 || true
  arena down -v --remove-orphans >/dev/null 2>&1 || true
}

_torn_down=0
teardown_arena() {
  local rc=$?
  # INT/TERM fire the trap and EXIT fires it again -- tear down once.
  [ "$_torn_down" = "1" ] && return "$rc"
  _torn_down=1
  if [ "${GENTAR_KEEP_ARENA:-0}" = "1" ]; then
    # Two steps, not one: `compose down` alone refuses the network with
    # "Resource is still in use", because it does not stop one-off
    # containers. Stopping them is also what triggers their AutoRemove.
    echo "arena kept up (GENTAR_KEEP_ARENA=1) -- tear down with:" >&2
    echo "  docker stop \$(docker ps -q --filter label=com.docker.compose.project=arena-$SUBJECT)" >&2
    echo "  docker compose -p arena-$SUBJECT down -v --remove-orphans" >&2
    return "$rc"
  fi
  arena_stop_all
  return "$rc"
}

# EXIT tears down on any normal end. INT/TERM need their OWN handlers that
# tear down AND exit: a signal trap that merely returns lets bash resume the
# script after the interrupted command -- with _torn_down already set, so a
# resumed run could start new services after a cancel, the final EXIT
# would skip cleanup, and the run would end with status 0. Exiting 130/143
# keeps the cancel a failure; the EXIT trap then sees _torn_down and does
# nothing, so teardown still happens exactly once.
trap teardown_arena EXIT
trap 'teardown_arena; exit 130' INT
trap 'teardown_arena; exit 143' TERM

arena_container() {   # service -> container id, empty if not up
  docker ps -q --filter "label=com.docker.compose.project=arena-$SUBJECT" \
    --filter "label=com.docker.compose.service=$1" 2>/dev/null | head -1
}

# --no-deps because this script owns dependency order: letting depends_on do
# it would start plain `up` containers, which are exactly what leaks.
arena_start() {
  [ -n "$(arena_container "$1")" ] && return 0
  arena run -d --rm --use-aliases --service-ports --no-deps "$1" >/dev/null
}

arena_wait_healthy() {
  local cid=$1 name=$2 i h
  for i in $(seq 1 90); do
    h=$(docker inspect "$cid" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' 2>/dev/null || echo gone)
    case "$h" in
      healthy|none) return 0 ;;
      gone) echo "$name died before becoming healthy" >&2; return 1 ;;
    esac
    sleep 1
  done
  echo "$name never became healthy (90s)" >&2
  return 1
}

require_bench_key

echo "building the coordinator image from $sha..." >&2
arena build coordinator

# clickhouse healthy, then otelcol -- the order depends_on declares.
arena_start clickhouse
arena_wait_healthy "$(arena_container clickhouse)" clickhouse
arena_start otelcol

# The coordinator runs in a CONTAINER, so nothing in this script's
# environment reaches it unless it is forwarded. Agent-in-the-loop suites
# declare `credentials`, and the coordinator refuses before creating a bench
# when none of the named variables is present -- which is what happens to a
# correctly-configured run whose variables simply stopped at the container
# boundary. Forward only the ones actually set, so an unset variable stays
# unset inside rather than arriving empty-but-present.
#
# The names are READ FROM THE SCENARIOS about to run, not from a fixed
# list: `credentials` and `pass_env` are arbitrary env var names in the
# schema, so a hardcoded ANTHROPIC_* allowlist silently drops
# OPENAI_API_KEY or any subject's own knob — the coordinator then
# refuses (exit 2) for a variable the caller did set, or quietly runs
# without an optional one (PR #27 review). Parsed with awk rather than a
# TOML library: this must work on a stock runner with no python
# dependency. The two shapes the schema allows are
# `credentials = ["A", ["B", "C"]]` (groups, possibly spanning lines)
# and `pass_env = ["D"]`; tracking bracket depth reads both and stops at
# the array's real end rather than at the first `]` on a later line.
# Commented-out examples are skipped — a `#` line is not a declaration.
#
# GENTAR_BUDGET_CAP is the one fixed addition: it configures the budget
# guard itself, so no scenario declares it.
extract_env_names() {         # files... -> one env var name per line
  awk '
    /^[[:space:]]*#/ { next }
    /^[[:space:]]*(credentials|pass_env)[[:space:]]*=/ { inarr = 1; depth = 0 }
    inarr {
      line = $0
      while (match(line, /"[A-Za-z_][A-Za-z0-9_]*"/)) {
        print substr(line, RSTART + 1, RLENGTH - 2)
        line = substr(line, RSTART + RLENGTH)
      }
      depth += gsub(/\[/, "[") - gsub(/\]/, "]")
      if (depth <= 0) inarr = 0
    }
  ' "$@"
}
# ${FORWARD[@]+"..."} rather than "${FORWARD[@]}": under `set -u`, bash 3.2
# (still the system bash on macOS) treats an EMPTY array expansion as an
# unbound variable and aborts. Credential-less is the normal case on a dev
# machine, so the plain form would break every local run.
SCENARIO_FILES=()
for s in "$SCENARIO" "$@"; do
  [ -f "$HERE/scenarios/$s.toml" ] && SCENARIO_FILES+=("$HERE/scenarios/$s.toml")
done
declared=$(
  { [ ${#SCENARIO_FILES[@]} -eq 0 ] \
      || extract_env_names ${SCENARIO_FILES[@]+"${SCENARIO_FILES[@]}"}
    echo GENTAR_BUDGET_CAP
  } | sort -u)
FORWARD=()
for var in $declared; do
  [ -n "${!var:-}" ] && FORWARD+=(-e "$var")
done

# Run every requested scenario; report all, fail if any failed.
mkdir -p "$HERE/reports"
status=0
for s in "$SCENARIO" "$@"; do
  # Exit code is the verdict: 0 pass · 1 fail · 2 usage/config refusal.
  MARKER=$(mktemp)
  set +e
  GENTAR_SUBJECTS_DIR="$PWD/subjects" \
    arena run --rm --no-deps \
      -e GENTAR_SCENARIOS_DIR=/extra \
      ${FORWARD[@]+"${FORWARD[@]}"} \
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
