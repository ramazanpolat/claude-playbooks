#!/usr/bin/env bash
# Kick this repo's arena: stage the working tree as the subject, run a
# scenario, land the report in gentar/reports/.
#
#   gentar/run.sh <scenario>            # e.g. first-suite
#   GENTAR_REF=v0.1.1 gentar/run.sh …   # run against another engine version
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
#     gentar/run.sh first-suite
#
# The only thing to edit below is SUBJECT, if your repo's directory name
# is not the subject name your scenarios declare.
set -euo pipefail

# --stage-engine clones/fetches/checks out the engine and stops there.
# dryrun.py needs the engine's scenario parser but no bench, so without
# this the only way to get one was a full bench run — the cheap check
# would have required the expensive one first.
#
# --sweep runs every suite this environment CAN run: a suite declaring
# `credentials` is included only when one of its groups is fully present.
# --down tears this subject's arena down — the one teardown CI needs,
# with the project name derived in exactly one place (here).
#
# --plan prints what this CI event should run under gentar/policy.toml (the
# kit's plan job; see plan.py). --check is phase 1's bench-free half:
# stage the engine, lint the adaptation, dry-run every suite. Neither
# needs Docker or a bench.
STAGE_ONLY=0
REVIEW_ONLY=0
DOWN_ONLY=0
SWEEP=0
CHECK_ONLY=0
case "${1:-}" in
  --stage-engine) STAGE_ONLY=1; shift ;;
  --review)       REVIEW_ONLY=1; shift ;;
  --down)         DOWN_ONLY=1; shift ;;
  --sweep)        SWEEP=1; shift ;;
  --check)        CHECK_ONLY=1; shift ;;
  --plan)         exec python3 "$(cd "$(dirname "$0")" && pwd)/plan.py" plan ;;
esac

if [ "$STAGE_ONLY$REVIEW_ONLY$DOWN_ONLY$SWEEP$CHECK_ONLY" = 00000 ]; then
  SCENARIO=${1:?usage: gentar/run.sh [--stage-engine|--review|--sweep|--down|--check|--plan] <scenario> [more scenarios...]}
  shift || true
else
  SCENARIO=""
fi
HERE=$(cd "$(dirname "$0")" && pwd)          # <repo>/gentar
REPO=$(dirname "$HERE")

# The [scenario] table only — the one place the engine reads `subject`,
# `credentials` and `pass_env`. Reading keys anywhere in the file let a
# `subject` or `credentials` under another table (a review found both)
# masquerade as the scenario's. A header is a WHOLE line holding only
# `[name]` / `[[name]]`, so `[ -f x ]` inside a step never counts.
scenario_table() {
  awk '
    # Inside a multi-line string (a step written as triple-quoted text), a
    # line reading `[scenario]` is TEXT, not a header — without this, it
    # re-entered the table and its `subject` was read.
    function toggles(line, delim,   n) { n = gsub(delim, "", line); return n % 2 }
    !inml && /^[[:space:]]*\[\[?[A-Za-z_][A-Za-z0-9_.-]*\]\]?[[:space:]]*(#.*)?$/ {
      h = $0; gsub(/[][[:space:]]|#.*/, "", h); insc = (h == "scenario"); next
    }
    { if (insc) print
      if (toggles($0, "\047\047\047")) inml = !inml
      if (toggles($0, "\"\"\"")) inml = !inml }' "$1"
}

# The subject name is read FROM THE SCENARIOS, which must declare it
# anyway. It used to be $(basename "$REPO") — which is wrong in a git
# worktree, whose directory is named after the BRANCH: a checkout at
# .worktrees/<repo>/<agent>/<branch> staged itself under a subject no
# scenario declared. (Found by the first real adopter, claude-playbooks,
# which is only ever worked on in worktrees.) One source of truth, and it
# refuses rather than guesses: the template's REPLACE-ME, or scenarios
# that disagree, stop here with exit 2 before anything is staged.
# GENTAR_SUBJECT overrides, for a repo that genuinely wants otherwise.
if [ -n "${GENTAR_SUBJECT:-}" ]; then
  SUBJECT=$GENTAR_SUBJECT
else
  # Either TOML string style: "basic" or 'literal'. Missing the second
  # made a single-quoted subject read as NO subject.
  declared=$(for f in "$HERE"/scenarios/*.toml; do
      [ -f "$f" ] && scenario_table "$f" \
        | sed -n "s/^[[:space:]]*subject[[:space:]]*=[[:space:]]*[\"']\([^\"']*\)[\"'].*/\1/p"
    done | sort -u)
  nscen=$(ls "$HERE"/scenarios/*.toml 2>/dev/null | wc -l | tr -d ' ')
  case "$(printf '%s\n' "$declared" | grep -c .)" in
    0) if [ "$nscen" -gt 0 ]; then
         # Scenarios exist but none declared a subject this could read. Never
         # fall back to the directory: in a worktree that is the branch name,
         # which is the bug this block exists to prevent.
         echo "could not read subject = \"…\" from gentar/scenarios/*.toml" >&2
         echo "declare it in each scenario, or set GENTAR_SUBJECT" >&2
         exit 2
       fi
       SUBJECT=$(basename "$REPO") ;;          # no scenarios yet (--review says so)
    1) SUBJECT=$declared ;;
    *) echo "scenarios disagree on subject: $(echo $declared)" >&2
       echo "every gentar/scenarios/*.toml must declare the same subject = \"…\"" >&2
       exit 2 ;;
  esac
fi
case "$SUBJECT" in
  REPLACE-ME|'')
    echo "subject is still the template placeholder (REPLACE-ME)" >&2
    echo "set subject = \"<your repo name>\" in gentar/scenarios/*.toml" >&2
    exit 2 ;;
  *[!A-Za-z0-9._-]*)
    echo "bad subject name: $SUBJECT (letters, digits, dot, dash, underscore)" >&2
    exit 2 ;;
esac

# One arena per subject per Docker host, at a time. Every run of this
# subject uses the same compose project (arena-<subject>), image and host
# ports, so two at once stop each other's containers or fail to bind. CI
# used to serialise them with one GitHub concurrency group per repository
# — which CANCELS a pending run when a newer one queues, so keyword and
# phase 2 runs were silently dropped behind main pushes. Here a later run
# WAITS, and says for whom. flock where it exists (Linux runners); a
# mkdir lock with a pid and stale-holder detection where it does not
# (stock macOS). /tmp, not TMPDIR: two runner users on one host share one
# Docker daemon, so they must share the lock.
LOCK_BASE="${GENTAR_LOCK_DIR:-/tmp}/gentar-arena-$SUBJECT"
_lock_mode=""
_lock_holder() {
  cat "$LOCK_BASE.lock.holder" "$LOCK_BASE.lock.d/holder" 2>/dev/null | head -1
}
_lock_note() {
  { printf '%s' "pid $$ on $(hostname 2>/dev/null || echo ?) since $(date '+%Y-%m-%d %H:%M:%S')"
    [ -n "${GITHUB_RUN_ID:-}" ] && printf ' (%s run %s)' "${GITHUB_REPOSITORY:-}" "$GITHUB_RUN_ID"
    echo; } > "$1" 2>/dev/null || true
}
arena_lock() {            # wait|try -> 0 once this process holds the lock
  local lock="$LOCK_BASE.lock" d="$LOCK_BASE.lock.d" pid said=0
  if command -v flock >/dev/null 2>&1; then
    [ -e "$lock" ] || (umask 000; : > "$lock") 2>/dev/null || true
    [ -r "$lock" ] || { echo "cannot read arena lock $lock" >&2; return 1; }
    exec 9<"$lock"
    if ! flock -n 9; then
      [ "$1" = try ] && { exec 9<&-; return 1; }
      echo "arena-$SUBJECT is in use on this host by: $(_lock_holder || true) — waiting" >&2
      flock 9
    fi
    _lock_mode=flock
    ( umask 000; _lock_note "$lock.holder" )
    return 0
  fi
  while :; do
    if mkdir "$d" 2>/dev/null; then
      _lock_mode=mkdir; _lock_note "$d/holder"; return 0
    fi
    pid=$(awk '{print $2; exit}' "$d/holder" 2>/dev/null || true)
    # Stale: a holder pid that no longer exists. kill -0 also fails for a
    # live process of ANOTHER user, so ask ps before deciding it is gone.
    if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null && ! ps -p "$pid" >/dev/null 2>&1; then
      rm -rf "$d"; continue
    fi
    [ "$1" = try ] && return 1
    [ "$said" = 1 ] || echo "arena-$SUBJECT is in use on this host by: $(_lock_holder || true) — waiting" >&2
    said=1; sleep 3
  done
}
arena_unlock() {
  case "$_lock_mode" in
    mkdir) rm -rf "$LOCK_BASE.lock.d" ;;
    flock) exec 9<&- ;;
  esac
  _lock_mode=""
}

# --check: phase 1's bench-free half — everything that can be proven about
# this adaptation without Docker or a bench, so it runs on a GitHub-hosted
# runner and a pull request's code never reaches the self-hosted one.
#   1. the engine stages at the pin (and the subject resolves, above);
#   2. plan.py lint: the policy parses, credential grouping, kit drift;
#   3. every suite dry-runs (UNVERIFIED-only suites need the arena; they
#      are reported, not failed).
# A fork's pull request gets no secrets, so a private engine cannot be
# cloned there: that is a NAMED skip, never a silent pass.
if [ "$CHECK_ONLY" = 1 ]; then
  if ! "$HERE/$(basename "$0")" --stage-engine; then
    if [ "${GENTAR_FORK_PR:-false}" = true ]; then
      echo "::notice::gentar check SKIPPED: the engine could not be staged from a fork's pull request (no secrets reach forks); a maintainer's run checks it"
      exit 0
    fi
    exit 2
  fi
  rc=0
  python3 "$HERE/plan.py" lint "${GENTAR_DIR:-$HERE/.arena}" || rc=1
  (cd "$REPO" && GENTAR_DRYRUN_UNVERIFIED=ok python3 "$HERE/dryrun.py") || rc=1
  [ "$rc" = 0 ] && echo "check: clean" || echo "check: FAILED (see above)" >&2
  exit "$rc"
fi

# --down: tear down THIS subject's arena and nothing else. Run-created
# containers are one-offs that `compose down` does not stop, so they are
# stopped by label first (stopping is what fires their AutoRemove), then
# the project's network and volumes go. Needs no engine checkout.
#
# With GENTAR_NAME_PREFIX set (the kit workflow sets one per CI job), it
# also removes the bench-host sandboxes that prefix names: a cancelled job
# never reaches the coordinator's own rm, and the sandbox would otherwise
# stay on a bench-host shared with every other arena. The engine's
# bin/bench-reap matches whole names only, so it cannot remove another
# run's bench. Unset (a local run), the bench-host is not touched.
#
# It never tears down an arena another live run of this subject holds the
# host lock for: with runs waiting on each other rather than cancelling,
# a cancelled job's teardown must not stop the run it was queued behind.
if [ "$DOWN_ONLY" = 1 ]; then
  if arena_lock try; then
    cids=$(docker ps -q --filter "label=com.docker.compose.project=arena-$SUBJECT" 2>/dev/null || true)
    [ -n "$cids" ] && docker stop $cids >/dev/null 2>&1 || true
    docker compose -p "arena-$SUBJECT" down -v --remove-orphans >/dev/null 2>&1 || true
    arena_unlock
    echo "arena-$SUBJECT torn down"
  else
    echo "arena-$SUBJECT is in use by another run ($(_lock_holder || true)); left alone"
  fi
  if [ -n "${GENTAR_NAME_PREFIX:-}" ]; then
    reap="${GENTAR_DIR:-$HERE/.arena}/bin/bench-reap"
    if [ -x "$reap" ]; then
      "$reap" || echo "bench sandboxes of $GENTAR_NAME_PREFIX may remain (see above)" >&2
    else
      echo "no engine staged at ${reap%/bin/bench-reap}; bench-host not checked" >&2
    fi
  fi
  exit 0
fi

# credentials = [...] -> one group per line, names space-separated. A
# top-level string is a group of one; a nested list is ONE all-of group.
# Mirrors the engine's satisfied_group() grammar exactly (cross-checked
# against its parser on flat, nested, multi-line and commented shapes).
# Both TOML string styles ("basic", 'literal'). awk, not python: this runs
# on a stock CI runner.
credential_groups() {
  scenario_table "$1" | awk '
    /^[[:space:]]*#/ { next }
    !inarr && /^[[:space:]]*credentials[[:space:]]*=/ { inarr = 1; buf = ""; sub(/^[^=]*=/, "") }
    inarr {
      line = $0; sub(/#.*/, "", line); buf = buf " " line
      t = buf; o = gsub(/\[/, "", t); t = buf; c = gsub(/\]/, "", t)
      if (o > 0 && o == c) { inarr = 0; emit(buf) }
    }
    function emit(b,   i, ch, depth, instr, cur, grp, q) {
      depth = 0; instr = 0; cur = ""; grp = ""
      for (i = 1; i <= length(b); i++) {
        ch = substr(b, i, 1)
        if (instr) {
          if (ch == q) { instr = 0
            if (depth == 1) print cur; else grp = grp (grp == "" ? "" : " ") cur
          } else cur = cur ch
          continue
        }
        if (ch == "\"" || ch == "\047") { instr = 1; q = ch; cur = ""; continue }
        if (ch == "[") { depth++; if (depth == 2) grp = ""; continue }
        if (ch == "]") { if (depth == 2 && grp != "") print grp; depth--; continue }
      }
    }'
}

# 0 when the suite declares no credentials or one group is fully set.
credentials_present() {
  scenario_table "$1" | grep -q '^[[:space:]]*credentials[[:space:]]*=' || return 0
  local g n full
  while IFS= read -r g; do
    [ -n "$g" ] || continue
    full=1
    for n in $g; do [ -n "${!n:-}" ] || { full=0; break; }; done
    [ "$full" = 1 ] && return 0
  done <<EOF
$(credential_groups "$1")
EOF
  return 1
}

# --sweep: every suite this environment can run. A suite whose
# credentials are absent is SKIPPED with its reason, not run into an
# exit-2 refusal that would turn the whole sweep red for a key nobody
# promised. The check is the engine's own grouping, not a hardcoded
# provider list: `["TOKEN", "URL"]` is two ALTERNATIVES, so a token with
# no URL counts as present here exactly as it does in the engine — and
# the coordinator's forwarding warning then says the URL was dropped.
if [ "$SWEEP" = 1 ]; then
  runnable=""
  for f in "$HERE"/scenarios/*.toml; do
    [ -f "$f" ] || continue
    s=$(basename "$f" .toml)
    if credentials_present "$f"; then
      runnable="$runnable $s"
    else
      echo "skipping $s — no credential group of it is fully set" >&2
    fi
  done
  [ -n "$runnable" ] || { echo "no runnable suites in gentar/scenarios" >&2; exit 2; }
  echo "sweep:$runnable" >&2
  set -- $runnable
  SCENARIO=$1; shift
fi
ARENA=${GENTAR_DIR:-$HERE/.arena}
# Engine version. A RELEASE TAG by default, never a moving branch: the
# engine is a separate repo on its own release cycle, so `main` means
# every adopter's suites can change behaviour on a day nobody touched
# this repo. That already happened once — the kit advertised a scenario
# feature the engine's tip did not parse yet, and adopters saw a load
# error they had not caused. Bump this deliberately: change the default,
# run your suites, commit the bump as its own change. `main` stays
# available for anyone tracking the engine on purpose.
REF=${GENTAR_REF:-v0.4.0}

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
  # A candidate is something a fresh machine can RUN: a tracked file with
  # the executable bit, plus anything in bin/. Not "every file under cmd/"
  # — for a Go CLI that listed root.go, table.go and the rest of the
  # implementation, 27 names no suite should ever mention burying the one
  # real gap (claude-playbooks). Not every .py either: a library module is
  # not an entry point. The executable bit is the one signal that means
  # "this is run, not imported", in any language.
  cands=$( { git ls-files -s 2>/dev/null | awk '$1 == "100755" { print $4 }' || true
             git ls-files 2>/dev/null | grep -E '^bin/' || true
           } | grep -vE '^(gentar|test|tests|\.github)/' | sort -u)

  gaps=0
  for c in $cands; do
    base=$(basename "$c"); stem=${base%.*}
    if ! printf '%s\n' "$mentions" | grep -qxF "$base" \
       && ! printf '%s\n' "$mentions" | grep -qxF "$stem"; then
      if [ "$gaps" = 0 ]; then echo "the repo ships these, and no suite mentions them:"; fi
      last=$(git log -1 --format='%ad' --date=short -- "$c" 2>/dev/null || echo '?')
      printf '  %-40s last changed %s\n' "$c" "$last"
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
# --force: .arena is a cache this script owns, and switching
# GENTAR_REPO_URL to a fork that has its OWN tag of the same name made a
# plain fetch refuse ("would clobber existing tag") — reported below as
# "GENTAR_REF not found", for a tag that exists. The fork's tag is what
# was asked for.
if ! git -C "$ARENA" fetch -q --force --tags origin "$REF"; then
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
# --match 'v*': the workflow's keyword tags (`arena`, `arena-*`) are
# floating triggers, and a bare `git describe --tags` returns whichever
# tag is NEAREST — so moving `arena` onto a commit made the frozen version
# read "arena-3-g…" instead of the release it came from (claude-playbooks,
# where the `arena` tag once shadowed a real v3.13.0).
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
  # Never repeat a value that is not plainly a path. Setting
  # GENTAR_BENCH_KEY_FILE to the key's CONTENTS instead of its path is an
  # easy mistake (in CI, pointing it at the secret instead of the staged
  # file), and the "not found at <value>" line below then printed the whole
  # private key — to a terminal, or to a CI log where masking a multi-line
  # secret is not something to rely on. Found in review; true since 0.1.0.
  case "$key" in
    *"
"*|*BEGIN*|*PRIVATE*|*KEY-----*)
      echo "GENTAR_BENCH_KEY_FILE holds what looks like KEY MATERIAL, not a path —" \
           "it must be the PATH to the key file (value not shown)" >&2
      exit 2 ;;
  esac
  if [ "${#key}" -gt 1024 ]; then
    echo "GENTAR_BENCH_KEY_FILE is ${#key} characters — not a path (value not shown)" >&2
    exit 2
  fi
  # Readable AND non-blank. CI stages the key with printf '%s\n', so an
  # UNSET secret becomes a one-byte file holding only the newline — which
  # passes -r and -s alike and then fails every suite at ssh, reading as a
  # red arena rather than a missing secret (claude-playbooks). grep -q
  # inspects without printing: the key's contents never reach a log.
  if [ -r "$key" ] && grep -q '[^[:space:]]' "$key" 2>/dev/null; then return 0; fi
  if [ -r "$key" ]; then
    echo "bench ssh key at $key is EMPTY — the secret that should fill it is" \
         "probably unset (BENCH_SSH_KEY in CI)" >&2
    exit 2
  fi
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
    # Point at --down, not raw docker: it knows the two steps (`compose
    # down` alone refuses the network with "Resource is still in use",
    # because it does not stop one-off containers). (claude-playbooks.)
    #
    # The ClickHouse port is the one compose PUBLISHED, not the shell's:
    # a port moved only in the arena's .env would otherwise print 8123,
    # and on a host where another arena holds 8123 the dashboard renders
    # that arena. Line-oriented read of `config --format json`, as for the
    # bench key above (the "published" line follows "target": 8123).
    local chport
    chport=$({ arena config --format json 2>/dev/null || true; } \
      | sed -E -n '/"target": *8123[,]?$/,/"published"/{ s/.*"published": *"?([0-9]+).*/\1/p; }' \
      | head -1)
    chport=${chport:-${GENTAR_CLICKHOUSE_HOST_PORT:-8123}}
    echo "arena kept up (GENTAR_KEEP_ARENA=1). Watch it, then tear it down:" >&2
    echo "  GENTAR_CLICKHOUSE_HOST_PORT=$chport python3 $ARENA/dashboard/generate.py --watch --out $HERE/reports/dashboard.html" >&2
    echo "  gentar/run.sh --down" >&2
    arena_unlock
    return "$rc"
  fi
  arena_stop_all
  arena_unlock
  return "$rc"
}

# EXIT tears down on any normal end. INT and TERM need their OWN handlers
# that tear down AND exit: a signal trap that merely returns lets bash
# RESUME the script after the interrupted command, so the next suite starts
# against an arena that was just torn down and the run can finish with
# status 0. A CI cancel is a SIGTERM to this shell, so a cancelled run read
# as a PASS. Exiting 130/143 keeps a cancel a failure; EXIT then sees
# _torn_down and does nothing, so teardown still happens exactly once.
# Take the host lock before anything touches arena-<subject>: the image
# build and every service start below come after it.
arena_lock wait
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
  for _f in "$@"; do scenario_table "$_f"; done | awk '
    /^[[:space:]]*#/ { next }
    /^[[:space:]]*(credentials|pass_env)[[:space:]]*=/ { inarr = 1; depth = 0 }
    inarr {
      line = $0
      # either TOML quote style — a 'literal' name used to forward nothing,
      # and the coordinator then refused credentials the caller had set
      while (match(line, /["\047][A-Za-z_][A-Za-z0-9_]*["\047]/)) {
        print substr(line, RSTART + 1, RLENGTH - 2)
        line = substr(line, RSTART + RLENGTH)
      }
      depth += gsub(/\[/, "[") - gsub(/\]/, "]")
      if (depth <= 0) inarr = 0
    }
  '
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
