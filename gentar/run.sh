#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Ramazan Polat
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
# Nothing below is yours to edit: this is a kit file, and --check fails
# when it differs from the pinned engine's copy. The subject name comes
# from your scenarios (GENTAR_SUBJECT overrides it), the engine pin is
# GENTAR_REF, and the rest is policy.toml, hooks.py and the environment.
set -euo pipefail

# Subject secrets routed by name ([secrets] route in policy.toml) arrive in
# numbered slots, GENTAR_ROUTE_1..8, set by the kit's workflow. Their values
# are taken into this shell and the slots are removed from the environment
# FIRST, before any mode runs or execs anything, so no process this script
# starts ever inherits a slot. Each value gets its own name further down
# (after the policy is read), and leaves this shell only for a suite that
# declares that name. Never printed, never written to disk.
ROUTE_SLOTS=8
ROUTE_VALS=()
_i=1
while [ "$_i" -le "$ROUTE_SLOTS" ]; do
  _v="GENTAR_ROUTE_$_i"
  ROUTE_VALS[$_i]=${!_v:-}
  unset "$_v"
  _i=$((_i + 1))
done

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
# --route says which secrets [secrets] route names, whether each arrived and
# which suites ask for it -- names only, never a value.
#
# --plan prints what this CI event should run under gentar/policy.toml (the
# kit's plan job; see plan.py). --check is phase 1's bench-free half:
# stage the engine, lint the adaptation, dry-run every suite. Neither
# needs Docker or a bench.
STAGE_ONLY=0
REVIEW_ONLY=0
REVIEW_SINCE=""
DOWN_ONLY=0
SWEEP=0
CHECK_ONLY=0
ROUTE_ONLY=0
case "${1:-}" in
  --stage-engine) STAGE_ONLY=1; shift ;;
  --review)       REVIEW_ONLY=1; shift
                  # --since <ref>: diff from there instead of from the commit
                  # that last changed a scenario (the adoption, usually).
                  if [ "${1:-}" = --since ]; then
                    [ -n "${2:-}" ] || { echo "--since needs a ref" >&2; exit 2; }
                    REVIEW_SINCE=$2; shift 2
                  fi ;;
  --down)         DOWN_ONLY=1; shift ;;
  --sweep)        SWEEP=1; shift ;;
  --check)        CHECK_ONLY=1; shift ;;
  --route)        ROUTE_ONLY=1; shift ;;
  --plan)         exec python3 "$(cd "$(dirname "$0")" && pwd)/plan.py" plan ;;
esac

if [ "$STAGE_ONLY$REVIEW_ONLY$DOWN_ONLY$SWEEP$CHECK_ONLY$ROUTE_ONLY" = 000000 ]; then
  # A usage error is a refusal: exit 2. (`${1:?…}` exits 1 or 127.)
  if [ $# -lt 1 ]; then
    echo "usage: gentar/run.sh [--stage-engine|--review [--since <ref>]|--sweep|--down|--check|--route|--plan] <scenario> [more scenarios...]" >&2
    exit 2
  fi
  SCENARIO=$1
  shift
else
  SCENARIO=""
fi
HERE=$(cd "$(dirname "$0")" && pwd)          # <repo>/gentar
REPO=$(dirname "$HERE")

# Every suite's facts come from `plan.py scenarios`, which reads the TOML
# with a TOML parser. The shell's own reading (awk and grep) diverged from
# the engine's (ground-up review A7): a step printing `goal=1` made a
# suite judged, a comment holding triple quotes hid its [scenario] table,
# and `credentials = []` read as a credential never set. One line per
# suite, tab-separated: file stem, subject, judged (1/0), credential
# groups (`;` between groups, a space between names), every forwarded
# name. A suite that does not read, or whose `name` differs from its file
# name, is refused (exit 2) by every mode that uses these facts.
# --stage-engine, --check and --review use none: --check reports each
# broken suite through lint and the dry-run instead.
if [ "$STAGE_ONLY" = 1 ]; then         # quiet: --check, its caller, reports it
  SCAN=$(python3 "$HERE/plan.py" scenarios 2>/dev/null) && SCAN_OK=1 || { SCAN_OK=0; SCAN=""; }
elif SCAN=$(python3 "$HERE/plan.py" scenarios); then SCAN_OK=1; else SCAN_OK=0; SCAN=""; fi
if [ "$SCAN_OK" = 0 ] && [ "$STAGE_ONLY$CHECK_ONLY$REVIEW_ONLY" = 000 ]; then
  echo "the scenarios do not read (above); gentar/run.sh --check lists each problem" >&2
  exit 2
fi
scan_field() {                # stem, field number -> that field, "" when absent
  # ("" x) compares as strings: awk compares two numeric-looking values as
  # numbers, and suites `01` and `1` got each other's facts (Codex on #96)
  printf '%s\n' "$SCAN" | awk -F '\t' -v s="$1" -v n="$2" '($1 "") == (s "") { print $n; exit }'
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
  declared=$(printf '%s\n' "$SCAN" | awk -F '\t' 'NF && $2 != "" { print $2 }' | sort -u)
  nscen=$(printf '%s\n' "$SCAN" | grep -c . || true)
  # --stage-engine, --check and --review with suites that do not read:
  # they stage no subject, so the directory name stands in.
  [ "$SCAN_OK" = 1 ] || nscen=0
  case "$(printf '%s\n' "$declared" | grep -c .)" in
    0) if [ "$nscen" -gt 0 ]; then
         # Scenarios exist but none declared a subject this could read. Never
         # fall back to the directory: in a worktree that is the branch name,
         # which is the bug this block exists to prevent.
         echo "no scenario declares subject = \"…\" in gentar/scenarios/*.toml" >&2
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
  .*|-*)
    # the engine refuses these too (C3): `..` would stage into subjects/..
    echo "bad subject name: $SUBJECT (may not start with a dot or a dash)" >&2
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
# (stock macOS). /tmp, not TMPDIR: runner users who share one Docker daemon
# must share the lock.
#
# A shared root is a world-writable, NON-sticky directory: in sticky /tmp, a
# lock left by one runner user's dead run could not be removed by another's,
# and one unclean exit blocked the host.
#
# Shared and world-writable makes the lock advisory between local users —
# fine on a single-tenant runner host — but it must not become a way to make
# this user write somewhere else: a symlinked root or lock file is refused,
# and notes are written to a fresh temp file and renamed into place, which
# replaces a planted symlink instead of following it (claude-playbooks).
# --- lock root
# Who shares a lock is who shares a Docker daemon. On rootless Docker each
# account has a daemon of its own, so its lock is private: /tmp/gentar-locks-<user>,
# 0700, and no other account can hold or remove it. On rootful Docker, or when
# this cannot tell, accounts may share the daemon and must share the lock:
# /tmp/gentar-locks. Only a clear answer goes private, since a wrong one would
# split a shared daemon's locks and let two arenas collide. An explicit
# GENTAR_LOCK_DIR wins. Same text in bin/arena and the kit's run.sh (a test
# holds them equal): the kit decides before any engine is staged.
lock_daemon_rootless() {
  local tmo info
  tmo=()
  command -v docker >/dev/null 2>&1 || return 1
  command -v timeout >/dev/null 2>&1 && tmo=(timeout -k 2 5)
  info=$(${tmo[@]+"${tmo[@]}"} docker info --format '{{json .SecurityOptions}}' 2>/dev/null) || return 1
  # A JSON array with an entry whose name field is exactly name=rootless, as
  # bin/docker-identity reads it.
  case "$info" in
    \[*\"name=rootless\"*\]|\[*\"name=rootless,*\]) return 0 ;;
  esac
  return 1
}
lock_root_choose() {
  LOCK_PRIVATE=0
  if [ -n "${GENTAR_LOCK_DIR:-}" ]; then
    LOCK_ROOT="$GENTAR_LOCK_DIR/gentar-locks"
  elif lock_daemon_rootless; then
    LOCK_ROOT="/tmp/gentar-locks-$(id -un)"
    LOCK_PRIVATE=1
  else
    LOCK_ROOT=/tmp/gentar-locks
  fi
}
lock_root_make() {
  [ ! -e "$LOCK_ROOT" ] && [ ! -L "$LOCK_ROOT" ] || return 0
  if [ "$LOCK_PRIVATE" = 1 ]; then
    (umask 077; mkdir "$LOCK_ROOT") 2>/dev/null || true
  else
    (umask 000; mkdir "$LOCK_ROOT") 2>/dev/null || true
  fi
}
# /tmp is anyone's: a private root another account made first is not private.
lock_root_is_mine() {
  [ "$LOCK_PRIVATE" = 1 ] || return 0
  [ -O "$LOCK_ROOT" ] || return 1
  chmod 700 "$LOCK_ROOT"
}
# --- end lock root
# --- lock reap
# Reclaiming a dead holder's lock directory, in the no-flock fallback. Two
# waiters can read the same dead holder's note; if both then removed the
# directory, the second would remove the lock the first had just taken, and
# both would run (#100). So a reclaim takes a reap mutex, "$d.reap", re-reads
# the note under it, and removes the directory only if the note is still the
# one judged dead; a directory with no note only if it is still over a minute
# old. A reap mutex is held for milliseconds; one left by a waiter killed
# inside it is cleared once it is a minute old. (That clearing is itself
# check-then-remove: it can misfire only after a waiter was killed inside the
# reap, with three more reaping within milliseconds.) A lock root this user
# cannot write holds nothing it can reclaim: that is said, never waited on.
# Waiters on an older pin take no mutex, so the fix holds between waiters
# that have it. Same text in bin/arena and the kit's run.sh (a test holds
# them equal).
_lock_reap() {           # dir, note judged dead -> 0 removed · 1 busy or changed · 2 cannot remove
  local d=$1 seen=$2 m="$1.reap" now rc=0
  if ! (umask 000; mkdir "$m") 2>/dev/null; then
    [ -w "${m%/*}" ] && [ -x "${m%/*}" ] || return 2
    if [ -n "$(find "$m" -maxdepth 0 -mmin +1 2>/dev/null)" ] && ! rm -rf "$m" 2>/dev/null; then
      echo "stale reap mutex $m cannot be removed by $(id -un 2>/dev/null || echo this user); remove it: rm -rf '$m'" >&2
      return 2
    fi
    return 1
  fi
  now=$(head -n 1 "$d/holder" 2>/dev/null || true)
  if [ "$now" != "$seen" ]; then
    rc=1
  elif [ -z "$seen" ] && [ -z "$(find "$d" -maxdepth 0 -mmin +1 2>/dev/null)" ]; then
    rc=1
  elif ! rm -rf "$d" 2>/dev/null; then
    rc=2
  fi
  rmdir "$m" 2>/dev/null || rm -rf "$m" 2>/dev/null || true
  return "$rc"
}
# --- end lock reap
lock_root_choose
lock_root_make
LOCK_BASE="$LOCK_ROOT/gentar-arena-$SUBJECT"
_lock_mode=""
_lock_holder() {
  cat "$LOCK_BASE.lock.holder" "$LOCK_BASE.lock.d/holder" 2>/dev/null | head -1
}
_lock_note() {           # write a holder note via temp + rename, never through a link
  local tmp
  tmp=$(mktemp "$(dirname "$1")/.note.XXXXXX" 2>/dev/null) || return 0
  { printf '%s' "pid $$ on $(hostname 2>/dev/null || echo ?) since $(date '+%Y-%m-%d %H:%M:%S')"
    [ -n "${GITHUB_RUN_ID:-}" ] && printf ' (%s run %s)' "${GITHUB_REPOSITORY:-}" "$GITHUB_RUN_ID"
    echo; } > "$tmp" 2>/dev/null
  chmod 644 "$tmp" 2>/dev/null
  mv -f "$tmp" "$1" 2>/dev/null || rm -f "$tmp"
}
arena_lock() {            # wait|try -> 0 held · 1 held by another · 2 refused
  local lock="$LOCK_BASE.lock" d="$LOCK_BASE.lock.d" pid note rrc said=0
  if [ -L "$LOCK_ROOT" ] || [ ! -d "$LOCK_ROOT" ] || [ -L "$lock" ] || [ -L "$d" ]; then
    echo "arena lock refused: $LOCK_ROOT (or a lock in it) is a symlink or not a directory — remove it, or set GENTAR_LOCK_DIR" >&2
    return 2
  fi
  if ! lock_root_is_mine; then
    echo "arena lock refused: $LOCK_ROOT belongs to another user — remove it, or set GENTAR_LOCK_DIR" >&2
    return 2
  fi
  if command -v flock >/dev/null 2>&1; then
    # noclobber: O_EXCL create, so a link planted after the check above
    # makes this fail rather than creating the link's target
    [ -e "$lock" ] || (set -C; umask 000; : > "$lock") 2>/dev/null || true
    [ -r "$lock" ] || { echo "cannot read arena lock $lock" >&2; return 1; }
    exec 9<"$lock"
    if ! flock -n 9; then
      [ "$1" = try ] && { exec 9<&-; return 1; }
      echo "arena-$SUBJECT is in use on this host by: $(_lock_holder || true) — waiting" >&2
      flock 9
    fi
    _lock_mode=flock
    _lock_note "$lock.holder"
    return 0
  fi
  while :; do
    if (umask 000; mkdir "$d") 2>/dev/null; then
      _lock_mode=mkdir; _lock_note "$d/holder"; return 0
    fi
    # No lock there, yet none could be made: a root this user cannot write.
    # Say so; waiting would never end.
    if [ ! -e "$d" ] && [ ! -L "$d" ] && { [ ! -w "$LOCK_ROOT" ] || [ ! -x "$LOCK_ROOT" ]; }; then
      echo "arena lock refused: cannot create a lock in $LOCK_ROOT as $(id -un 2>/dev/null || echo this user) — fix its permissions, or set GENTAR_LOCK_DIR" >&2
      return 2
    fi
    note=$(head -n 1 "$d/holder" 2>/dev/null || true)
    pid=${note#pid }; pid=${pid%% *}
    case "$pid" in *[!0-9]*) pid="" ;; esac
    # Stale: a holder pid that no longer exists. kill -0 also fails for a
    # live process of ANOTHER user, so ask ps before deciding it is gone.
    # Or no readable holder at all in a lock dir more than a minute old: a
    # run killed between mkdir and writing its note (a live one writes it
    # in milliseconds) — without this, every later run waited forever.
    if { [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null && ! ps -p "$pid" >/dev/null 2>&1; } \
       || { [ -z "$pid" ] && [ -n "$(find "$d" -maxdepth 0 -mmin +1 2>/dev/null)" ]; }; then
      rrc=0; _lock_reap "$d" "$note" || rrc=$?
      [ "$rrc" = 0 ] && continue
      if [ "$rrc" = 1 ]; then       # another waiter is reaping, or took it first
        [ "$1" = try ] && return 1
        sleep 1; continue
      fi
      # Not ours to remove (a lock from before 0.4.0's shared directory, or
      # permissions changed by hand). Say exactly what to do rather than
      # hang or die on `set -e`.
      echo "stale arena lock $d (holder ${pid:+pid $pid }is gone) cannot be removed by $(id -un 2>/dev/null || echo this user); remove it: rm -rf '$d'" >&2
      return 2
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
  # One snapshot for the whole check: the host lock is held from staging
  # through lint and the dry-run, which read the engine --stage-engine
  # fetched; released, a run could move it in between (agy on #97). The
  # child --stage-engine sees its parent holds it and does not wait on it.
  arena_lock wait || exit 2
  trap arena_unlock EXIT
  export GENTAR_LOCK_HOLDER_PID=$$
  if ! "$HERE/$(basename "$0")" --stage-engine; then
    if [ "${GENTAR_FORK_PR:-false}" = true ]; then
      echo "::notice::gentar check SKIPPED: the engine could not be staged from a fork's pull request (no secrets reach forks); a maintainer's run checks it"
      exit 0
    fi
    exit 2
  fi
  # 0 clean · 1 a check failed · 2 a refusal (bad policy, a host the
  # dry-run will not trust) — a refusal wins, so a caller can tell a
  # misconfiguration from a failing assertion.
  rc=0
  worst() { [ "$1" = 2 ] && rc=2 || { [ "$rc" = 2 ] || rc=1; }; }
  python3 "$HERE/plan.py" lint "${GENTAR_DIR:-$HERE/.arena}" || worst $?
  # In CI the dry-run refuses a host whose system install dirs are
  # writable (the kit's checks job makes the runner bench-like first);
  # locally it warns.
  strict=0; [ "${CI:-}" = true ] && strict=1
  (cd "$REPO" && GENTAR_DRYRUN_UNVERIFIED=ok GENTAR_DRYRUN_STRICT=$strict \
      python3 "$HERE/dryrun.py") || worst $?
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
  lrc=0; arena_lock try || lrc=$?
  if [ "$lrc" = 2 ]; then
    echo "arena-$SUBJECT not torn down: its lock was refused (see above)"
  elif [ "$lrc" = 0 ]; then
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
# top-level string is a group of one; a nested list is ONE all-of group
# (the engine's satisfied_group() grammar, read by plan.py scenarios).
credential_groups() {
  scan_field "$(basename "$1" .toml)" 4 | tr ';' '\n' | grep . || true
}

# 0 when the suite declares no credentials (`credentials = []` included)
# or one group is fully set.
credentials_present() {
  local g n full groups
  groups=$(credential_groups "$1")
  [ -n "$groups" ] || return 0
  while IFS= read -r g; do
    full=1
    for n in $g; do [ -n "${!n:-}" ] || { full=0; break; }; done
    [ "$full" = 1 ] && return 0
  done <<EOF
$groups
EOF
  return 1
}

# Every env var name the given suites declare in `credentials` or `pass_env`
# (what reaches the coordinator; see FORWARD below for why it is read from
# the scenarios, and how).
extract_env_names() {         # files... -> one env var name per line
  local _f
  for _f in "$@"; do scan_field "$(basename "$_f" .toml)" 5 | tr ' ' '\n'; done | grep . || true
}

# 0 when the suite has a judged turn (a semantic expect).
# A pull request, whichever event carries it (pull_request_target too —
# claude-playbooks: a repo on that trigger bypassed the guard).
is_pr_event() {
  case "${GITHUB_EVENT_NAME:-}" in pull_request|pull_request_target) return 0 ;; esac
  return 1
}

judged() {
  # The engine's rule (TomlScenario.uses_judge), read by plan.py scenarios:
  # a judged turn, a goal pilot or a [[verify.judge]] check. A step that
  # merely prints `goal=1` is not one (ground-up review A7, GB6).
  [ "$(scan_field "$(basename "$1" .toml)" 3)" = 1 ]
}
# 0 when the suite has no judged turn, or the judge key is set. A judged
# suite without it would only refuse (exit 2).
judge_ready() {
  judged "$1" || return 0
  [ -n "${TYPESAFE_API_KEY:-}" ]
}

# [secrets] route: each slot captured at the top gets the name the policy
# gives it, as a variable of THIS shell -- not exported, so nothing started
# from here inherits it. It is exported just before the coordinator starts,
# and only when a suite about to run declares that name. Done before the
# sweep, which must see a routed credential to pick the suites needing it.
# Read on every run, slots or not: a routed name the caller set by hand (a
# developer's shell, a runner's own environment) goes the same one way, to
# the suites declaring it, and never in FORWARD to all (agy on #79).
ROUTE_NAMES=()
# --review runs no suite, so it needs no route, and a broken policy must
# not hide its diff (it reports; it never fails a build).
routes=""
if [ "$REVIEW_ONLY" != 1 ]; then
  routes=$(python3 "$HERE/plan.py" route) || exit 2
fi
_i=0
for _n in $routes; do
  _i=$((_i + 1))
  ROUTE_NAMES+=("$_n")
  if [ -n "${ROUTE_VALS[$_i]:-}" ]; then
    printf -v "$_n" '%s' "${ROUTE_VALS[$_i]}"
  fi
  # Already exported by the caller? Then printf -v kept the export, and
  # every child up to the first suite would inherit it: take it back.
  # shellcheck disable=SC2163  # the variable NAMED by $_n
  export -n "$_n"
done
# A value in a slot the policy names no secret for: the workflow and the
# policy disagree, or a slot was set by hand. Dropped, and said by number.
while [ "$_i" -lt "$ROUTE_SLOTS" ]; do
  _i=$((_i + 1))
  [ -z "${ROUTE_VALS[$_i]:-}" ] \
    || echo "secret route: slot $_i holds a value but [secrets] route names no secret for it; dropped" >&2
done
ROUTE_VALS=()

if [ "$ROUTE_ONLY" = 1 ]; then
  if [ ${#ROUTE_NAMES[@]} -eq 0 ]; then
    echo "secret route: none ([secrets] route is empty or absent)"
  fi
  for _n in ${ROUTE_NAMES[@]+"${ROUTE_NAMES[@]}"}; do
    users=""
    for f in "$HERE"/scenarios/*.toml; do
      [ -f "$f" ] || continue
      if extract_env_names "$f" | grep -qx "$_n"; then users="$users $(basename "$f" .toml)"; fi
    done
    state="missing"; [ -z "${!_n:-}" ] || state="set"
    exported=""; if compgen -e | grep -qx "$_n"; then exported=" (in the environment)"; fi
    echo "secret route: $_n $state$exported, declared by:${users:- no suite}"
  done
  if compgen -e | grep -q '^GENTAR_ROUTE_[0-9]'; then
    echo "secret route: a GENTAR_ROUTE_ slot is still in the environment" >&2
    exit 1
  fi
  exit 0
fi

# --sweep: every suite this environment can run. A suite whose
# credentials are absent is SKIPPED with its reason, not run into an
# exit-2 refusal that would turn the whole sweep red for a key nobody
# promised. The check is the engine's own grouping, not a hardcoded
# provider list: `["TOKEN", "URL"]` is two ALTERNATIVES, so a token with
# no URL counts as present here exactly as it does in the engine — and
# the coordinator's forwarding warning then says the URL was dropped.
if [ "$SWEEP" = 1 ]; then
  runnable=""
  judged_skipped=""
  skipped=""
  for f in "$HERE"/scenarios/*.toml; do
    [ -f "$f" ] || continue
    s=$(basename "$f" .toml)
    if ! credentials_present "$f"; then
      why="no credential group of it is fully set"
    elif is_pr_event && judged "$f"; then
      why="judged suites never run on a pull request (phase 2 only)"
    elif ! judge_ready "$f"; then
      why="it has judged turns and TYPESAFE_API_KEY is not set"
      judged_skipped="$judged_skipped $s"
    else
      runnable="$runnable $s"
      continue
    fi
    echo "skipping $s — $why" >&2
    skipped="$skipped$s"$'\t'"$why"$'\n'
  done
  # Every suite this sweep skipped, one per line (name TAB reason), and an
  # empty file when it skipped none. The sweep's exit code covers only the
  # suites it ran; an arena mirror reads this before it calls the sweep a
  # proof of the commit (subject-template/mirror/arena, "phase 2 ran every
  # suite").
  mkdir -p "$HERE/reports"
  printf '%s' "$skipped" > "$HERE/reports/sweep-skipped.txt"
  # Said once more, loudly, at the end: a phase 2 that skipped its judged
  # suites can finish green without having run them (from
  # Codex, on an adopter's re-pin). In Actions it is a warning annotation.
  if [ -n "$judged_skipped" ]; then
    n=$(echo $judged_skipped | wc -w | tr -d ' ')
    msg="$n judged suite(s) skipped, TYPESAFE_API_KEY is not set:$judged_skipped (a green sweep did not run them)"
    echo "sweep summary: $msg" >&2
    if [ "${GITHUB_ACTIONS:-}" = true ]; then
      echo "::warning title=judged suites skipped::$msg"
    fi
  fi
  [ -n "$runnable" ] || { echo "no runnable suites in gentar/scenarios" >&2; exit 2; }
  echo "sweep:$runnable" >&2
  set -- $runnable
  SCENARIO=$1; shift
fi
# A judged suite sends screens to a judge and gates on probabilities: never
# on a pull request, whatever picked it (a policy, no policy, a PR's own
# `gentar:` line). Enforced here, where suites run, not only in plan.py.
if is_pr_event; then
  for s in "$SCENARIO" "$@"; do
    if [ -f "$HERE/scenarios/$s.toml" ] && judged "$HERE/scenarios/$s.toml"; then
      echo "refusing $s — judged suites never run on a pull request (phase 2 only)" >&2
      exit 2
    fi
  done
fi
ARENA=${GENTAR_DIR:-$HERE/.arena}
# Engine version. A RELEASE TAG by default, never a moving branch: the
# engine is a separate repo on its own release cycle, so `main` means
# every adopter's suites can change behaviour on a day nobody touched
# this repo. That already happened once — the kit advertised a scenario
# feature the engine's tip did not parse yet, and adopters saw a load
# error they had not caused. Adopters bump it deliberately by re-copying
# the kit from a newer tag (this default comes with it), running their
# suites, and committing the bump as its own change; the engine's release
# moves the default. GENTAR_REF overrides it for one run, and `main` stays
# available for anyone tracking the engine on purpose.
REF=${GENTAR_REF:-v0.12.0}

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
  mentions=$(cat $sc | { grep -vE '^[[:space:]]*#' || true; } \
    | tr -c 'A-Za-z0-9_.-' '\n' | sort -u)

  echo "suites:"
  for f in $sc; do
    printf '  %-28s %s asserted\n' "$(basename "$f")" \
      "$(grep -c '^\[\[verify' "$f" 2>/dev/null || true)"   # -c prints 0 itself
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
           } | { grep -vE '^(gentar|test|tests|\.github)/' || true; } | sort -u)
  # (|| true: a library-only repo ships no executable, grep then exits 1,
  # and pipefail made --review die here instead of reporting.)

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
  echo
  # The commit that last touched a SCENARIO FILE, not the gentar dir:
  # staging the runner or a report would otherwise read as "the suites
  # were just updated" and the suggested diff would come back empty,
  # which is exactly the reassuring-but-wrong answer this check exists
  # to avoid.
  # The diff itself is plan.py's (review): testable, and deterministic --
  # git objects at the ref and HEAD, never the working tree.
  since=${REVIEW_SINCE:-$(git log -1 --format='%H' -- "$HERE"/scenarios/'*.toml' 2>/dev/null || true)}
  if [ -z "$since" ]; then
    echo "  scenarios are not committed yet — nothing to diff from (or pass --since <ref>)"
    exit 0
  fi
  [ -n "$REVIEW_SINCE" ] || echo "  (from the commit that last changed a scenario; --since <ref> for another)"
  python3 "$HERE/plan.py" review "$since" | sed 's/^./  &/'
  exit "${PIPESTATUS[0]}"
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

# The host lock, before the first write to anything a running arena of
# this subject reads: the engine checkout here, subjects/<subject> below.
# Taken later, a run waiting behind another fetched a new engine and
# replaced the staged subject while the other was still running suites
# from both (ground-up review B3, SB05). One invocation, one snapshot.
# --stage-engine and --check wait for a running arena too (--check holds
# the lock itself, for its whole run). Released on exit; the arena's
# teardown trap takes this over once there is one.
if [ -n "${GENTAR_LOCK_HOLDER_PID:-}" ] && [ "$GENTAR_LOCK_HOLDER_PID" = "$PPID" ]; then
  :      # started by the --check that holds it (above): waiting would deadlock
else
  arena_lock wait || exit 2
  trap arena_unlock EXIT
  trap 'arena_unlock; exit 130' INT
  trap 'arena_unlock; exit 143' TERM
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
# the subject: the files git would ship, tracked plus untracked ones it
# does not ignore, so a gitignored .env, .npmrc or build output never
# rides into a bench (plan.py stage-tree). A checkout with no .git of its
# own is copied whole, as before. Plain copy: symlinks are copied as
# links, and don't resolve through the bind.
#
# Three exclusions either way, each for a reason:
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
python3 "$HERE/plan.py" stage-tree "$REPO" "subjects/$SUBJECT"
# History, when the policy asks for it ([stage] git = true): a fresh .git
# built by a local clone, so no credential and no remote path come along.
python3 "$HERE/plan.py" stage-git "$REPO" "subjects/$SUBJECT"
# A worktree's .git is a pointer file with a host-absolute path — dead
# on the bench — so `git describe` there finds nothing. Freeze the
# version HERE, where git works; scenarios read it instead of trusting
# the bench's git.
# --match 'v*': the workflow's keyword tags (`arena`, `arena-*`) are
# floating triggers, and a describe without a --match pattern returns
# whichever tag is NEAREST — so moving `arena` onto a commit made the frozen version
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
# --- workspace root
# In local mode each bench's workspace, the subject's code with it, lives
# under GENTAR_BENCH_WORKSPACE_ROOT (default /tmp/gentar-workspaces), and
# compose binds that root into the arena. /tmp is anyone's, so the root must
# be this account's own: a symlink, or a directory another user owns, is
# refused (another account could read the benches, or hand the arena a
# directory of its own). A new root is made 0700, and one of ours is kept at
# 0700. A path ending in / or holding a . or .. component is refused too:
# a test on `link/` reads through the link. An explicit root must sit in a
# directory only this account can write to, or a sticky one such as /tmp: its
# parents are not checked. On a host with several accounts give each runner
# its own root. Same text in bin/arena and the kit's run.sh (a test holds them
# equal).
workspace_root_own() {
  local root=$1
  case "$root" in
    */|*/.|*/..|*/./*|*/../*)
      echo "workspace root refused: $root ends in / or holds a . or .. component, so a symlink in it would be followed — give it as a plain path (GENTAR_BENCH_WORKSPACE_ROOT)" >&2
      return 2 ;;
  esac
  case "$root" in
    /*) ;;
    *) echo "workspace root refused: $root is not an absolute path (GENTAR_BENCH_WORKSPACE_ROOT)" >&2; return 2 ;;
  esac
  if [ ! -e "$root" ] && [ ! -L "$root" ]; then
    mkdir -p "$(dirname "$root")"
    (umask 077; mkdir "$root") 2>/dev/null || true   # lost a race: the check below decides
  fi
  if [ -L "$root" ] || [ ! -d "$root" ] || [ ! -O "$root" ]; then
    echo "workspace root refused: $root is a symlink, not a directory, or not $(id -un)'s — remove it, or set GENTAR_BENCH_WORKSPACE_ROOT to a directory of this account's" >&2
    return 2
  fi
  chmod 700 "$root"
}
# --- end workspace root
# Local bench mode (GENTAR_BENCH_HOST=local): the arena runs on the bench
# host itself (a runner on that VM) and calls sbx directly, so there is no
# ssh and no bench key. The engine's overlay mounts the host's sbx into the
# coordinator and runs it as this user; an engine without it refuses.
# The setting: the environment first (CI), else the arena's .env (a local
# run edits gentar/.arena/.env), the way compose resolves it. Spaces and
# quotes are tolerated; anything else is a host name.
bench_host_setting() {
  if [ -n "${GENTAR_BENCH_HOST:-}" ]; then printf '%s' "$GENTAR_BENCH_HOST"
  elif [ -f "$ARENA/.env" ]; then sed -n 's/^GENTAR_BENCH_HOST=//p' "$ARENA/.env" | tail -1
  fi | tr -d "\"' [:space:]"
}
LOCAL_BENCH=0
[ "$(bench_host_setting)" = local ] && LOCAL_BENCH=1
if [ "$LOCAL_BENCH" = 1 ]; then
  export GENTAR_BENCH_HOST=local
  # What compose mounts must exist and belong to this user already: a
  # missing bind source is created by Docker as root, and sbx inside the
  # coordinator could then not write its state. They exist once this user
  # has run `sbx login`; refuse rather than create them.
  SBX_PATH=$(command -v sbx 2>/dev/null || true)
  if [ -z "$SBX_PATH" ]; then
    echo "GENTAR_BENCH_HOST=local: no sbx on this host's PATH (install it and run sbx login as $(id -un))" >&2
    exit 2
  fi
  for d in "$HOME/.local/state/sandboxes" "$HOME/.config/sandboxes" "$HOME/.config/com.docker.sandboxes"; do
    if [ ! -d "$d" ] || [ ! -O "$d" ]; then
      echo "GENTAR_BENCH_HOST=local: $d is missing or not owned by $(id -un) — run sbx login as this user first" >&2
      exit 2
    fi
  done
  # sbx in the coordinator has no session bus: it reads the Docker login
  # from files only. A login held in a desktop keyring (gnome-keyring) is
  # invisible there, and every bench would fail at PREPARE IMAGE with "no
  # default account profile set". sbx 0.39 and 0.45 keep each secret as a
  # folder named by its base64 id; this one is docker/auth/metadata/hub/default.
  SBX_AUTH="$HOME/.config/com.docker.sandboxes/com.docker.sandboxes-auth/sandboxes-auth"
  SBX_PROFILE="$SBX_AUTH/ZG9ja2VyL2F1dGgvbWV0YWRhdGEvaHViL2RlZmF1bHQ="
  if [ "${GENTAR_SBX_AUTH_CHECK:-on}" != off ] && ! ls "$SBX_PROFILE" 2>/dev/null | grep -q .; then
    echo "GENTAR_BENCH_HOST=local: sbx has no login in files (no default account profile in $SBX_AUTH); a keyring login is invisible to the coordinator. Sign in once as $(id -un) with $ARENA/bin/sbx-file-login (GENTAR_SBX_AUTH_CHECK=off skips this check)" >&2
    exit 2
  fi
  export GENTAR_LOCAL_SBX_BIN
  GENTAR_LOCAL_SBX_BIN=$(readlink -f "$SBX_PATH" 2>/dev/null || echo "$SBX_PATH")
  if [ ! -f "$ARENA/compose.local-bench.yml" ]; then
    echo "GENTAR_BENCH_HOST=local needs an engine with compose.local-bench.yml (GENTAR_REF $REF has none)" >&2
    exit 2
  fi
  ARENA_FILES+=(-f compose.local-bench.yml)
  export GENTAR_LOCAL_HOME GENTAR_BENCH_KEY_FILE
  GENTAR_LOCAL_HOME=$HOME
  GENTAR_BENCH_KEY_FILE=/dev/null     # the compose secret needs a file; there is no key
  # Create the workspace root as this user before compose binds it: a
  # missing bind source is created by Docker as root, and the coordinator
  # (this user) could then not make a bench's workspace in it.
  export GENTAR_BENCH_WORKSPACE_ROOT="${GENTAR_BENCH_WORKSPACE_ROOT:-/tmp/gentar-workspaces}"
  workspace_root_own "$GENTAR_BENCH_WORKSPACE_ROOT" || exit 2
fi
# One Docker daemon for the whole run (the engine's bin/docker-identity):
# every docker call below, the socket a container mounts (GENTAR_DOCKER_SOCK),
# and in local mode the uid the coordinator runs as -- rootless: 0:0, which
# is this user; rootful: this user; cannot tell: refused. Read as values,
# never eval'd; GENTAR_LOCAL_UID/GID set by a caller are overwritten, not
# used. Local mode refuses an engine without the script (it would guess).
if [ -x "$ARENA/bin/docker-identity" ]; then
  if [ "$LOCAL_BENCH" = 1 ]; then ident=$("$ARENA/bin/docker-identity" --local) || exit 2
  else ident=$("$ARENA/bin/docker-identity") || exit 2; fi
  ident_get() { printf '%s\n' "$ident" | sed -n "s/^$1=//p"; }
  export GENTAR_DOCKER_SOCK; GENTAR_DOCKER_SOCK=$(ident_get sock)
  if [ -n "$(ident_get host)" ]; then export DOCKER_HOST; DOCKER_HOST=$(ident_get host); fi
  if [ "$LOCAL_BENCH" = 1 ]; then
    export GENTAR_LOCAL_UID GENTAR_LOCAL_GID
    GENTAR_LOCAL_UID=$(ident_get uid); GENTAR_LOCAL_GID=$(ident_get gid)
  fi
elif [ "$LOCAL_BENCH" = 1 ]; then
  echo "GENTAR_BENCH_HOST=local needs an engine with bin/docker-identity (GENTAR_REF $REF has none): it decides the coordinator's uid on rootless Docker" >&2
  exit 2
fi
# Telemetry destination (optional; AGENTS.md decision 6): the arena's
# collector forwards what the coordinator scrubbed to GENTAR_OTLP_EXPORT
# with GENTAR_OTLP_KEY. Both or neither — half a destination is a refusal, not a
# run whose telemetry silently goes nowhere.
if [ -n "${GENTAR_OTLP_EXPORT:-}" ] || [ -n "${GENTAR_OTLP_KEY:-}" ]; then
  if [ -z "${GENTAR_OTLP_EXPORT:-}" ] || [ -z "${GENTAR_OTLP_KEY:-}" ]; then
    echo "telemetry destination needs BOTH GENTAR_OTLP_EXPORT and GENTAR_OTLP_KEY (one is unset)" >&2
    exit 2
  fi
  [ -f "$ARENA/compose.export.yml" ] && ARENA_FILES+=(-f compose.export.yml)
fi

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
  [ "${LOCAL_BENCH:-0}" = 1 ] && return 0    # local bench mode: no key
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
# The host lock is held since the engine was staged (above): the image
# build and every service start below come after it.
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
# without an optional one (PR #27 review). Both shapes the schema allows,
# `credentials = ["A", ["B", "C"]]` and `pass_env = ["D"]`, come from
# plan.py scenarios, which parses the TOML (see the top of this file).
#
# GENTAR_BUDGET_CAP is the one fixed addition: it configures the budget
# guard itself, so no scenario declares it.
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
# Routed secrets ([secrets] route) are not in FORWARD: each goes, in the
# run loop below, to the coordinator of the one suite that declares it.
is_routed() { printf '%s\n' ${ROUTE_NAMES[@]+"${ROUTE_NAMES[@]}"} | grep -qx "$1"; }
FORWARD=()
for var in $declared; do
  if is_routed "$var"; then continue; fi
  [ -n "${!var:-}" ] && FORWARD+=(-e "$var")
done

# What this run publishes (reports, dashboard) may be a PUBLIC repository's
# CI artifact, and GitHub masks secrets in logs, not in artifacts. Every
# file is passed through the engine's bin/redact, which replaces the VALUES
# of these variables: the bench-host identity, plus every credential a
# suite here declares.
REDACT_NAMES="GENTAR_BENCH_HOST GENTAR_BENCH_USER GENTAR_BENCH_JUMP GENTAR_TART_HOST GENTAR_TART_USER GENTAR_DAYTONA_API_KEY GENTAR_OSB_API_KEY GENTAR_OTLP_KEY GENTAR_OTLP_EXPORT TYPESAFE_API_KEY"
for f in ${SCENARIO_FILES[@]+"${SCENARIO_FILES[@]}"}; do
  REDACT_NAMES="$REDACT_NAMES $(credential_groups "$f" | tr '\n' ' ')"
done
# Every routed secret too, declared as a credential or only as pass_env.
for _n in ${ROUTE_NAMES[@]+"${ROUTE_NAMES[@]}"}; do REDACT_NAMES="$REDACT_NAMES $_n"; done
# bin/redact finds a value by its name in ITS environment, and a routed
# secret is exported only while its suite's coordinator runs: so redact
# runs in a subshell that exports them for it alone.
redact() (
  for _n in ${ROUTE_NAMES[@]+"${ROUTE_NAMES[@]}"}; do
    # shellcheck disable=SC2163  # the variable NAMED by $_n
    [ -z "${!_n:-}" ] || export "$_n"
  done
  GENTAR_REDACT_NAMES="$REDACT_NAMES" exec "$ARENA/bin/redact" "$@"
)
# Files are prepared and redacted HERE, outside gentar/reports/, and only
# then moved in: a CI cancel between writing and redacting must not leave
# an unredacted file where the always-run upload step would publish it.
STAGE="$ARENA/.publish"      # not under out/, which the report loop scans
mkdir -p "$STAGE"
publish() {             # $1 = staged file, $2 = destination in reports/
  if redact "$1"; then mv -f "$1" "$2"; else rm -f "$1"; return 1; fi
}

# One self-contained dashboard.html per run.sh invocation, rendered from
# this run's ClickHouse before teardown takes it (the spans die with the
# stack; the report alone is a page of text). Every suite of the
# invocation, not the renderer's default ten. It never touches the
# verdict: a render that fails is said and skipped, and a dashboard that
# cannot be redacted is not published at all.
render_dashboard() {
  local out="$ARENA/dashboard/out/dashboard.html"
  mkdir -p "$ARENA/dashboard/out" 2>/dev/null || true
  rm -f "$out" 2>/dev/null || true
  if ! arena run --rm --no-deps dashboard \
       python /dashboard/generate.py --out /out/dashboard.html --runs 500 \
         --since "$RUN_T0" >/dev/null 2>&1 \
     || [ ! -s "$out" ]; then
    echo "dashboard: not rendered (the verdict is unaffected)" >&2
    return 0
  fi
  if cp "$out" "$STAGE/dashboard.html" \
     && publish "$STAGE/dashboard.html" "$HERE/reports/dashboard.html"; then
    echo "dashboard: gentar/reports/dashboard.html"
  else
    echo "dashboard: NOT published — it could not be redacted" >&2
  fi
}

# The verdict a report states, from its own verdict row: pass, fail,
# refuse, or skip (a quarantined suite, which did not run).
report_verdict() {
  sed -n 's/^| verdict | `\([a-z]*\) (exit [0-9]*)` |$/\1/p' "$1" | head -1
}

# Run every requested scenario; report all, fail if any failed.
mkdir -p "$HERE/reports"
status=0
quarantined=""
RUN_T0=$(( $(date +%s) - 5 ))   # the dashboard shows this invocation's runs only
for s in "$SCENARIO" "$@"; do
  # Exit code is the verdict: 0 pass · 1 fail · 2 usage/config refusal.
  MARKER=$(mktemp)
  # Routed secrets ([secrets] route) leave this shell only here: exported
  # for the coordinator of a suite that declares them, and taken back out
  # of the environment the moment it returns, so the next suite's
  # coordinator never sees them.
  ROUTED=()
  scrub=""
  for _n in ${ROUTE_NAMES[@]+"${ROUTE_NAMES[@]}"}; do
    if [ -n "${!_n:-}" ] && [ -f "$HERE/scenarios/$s.toml" ] \
       && extract_env_names "$HERE/scenarios/$s.toml" | grep -qx "$_n"; then
      # shellcheck disable=SC2163  # the variable NAMED by $_n
      export "$_n"
      ROUTED+=(-e "$_n")
      scrub="$scrub $_n"
    fi
  done
  # Names only: the coordinator scrubs the values of what it declares as
  # credentials, and a suite may declare a routed secret in pass_env, so it
  # is told which names carry one (agy on #79).
  [ -z "$scrub" ] || ROUTED+=(-e "GENTAR_REDACT_NAMES=${scrub# }")
  set +e
  GENTAR_SUBJECTS_DIR="$PWD/subjects" \
    arena run --rm --no-deps \
      -e GENTAR_SCENARIOS_DIR=/extra \
      ${FORWARD[@]+"${FORWARD[@]}"} \
      ${ROUTED[@]+"${ROUTED[@]}"} \
      -v "$HERE/scenarios:/extra:ro" \
      coordinator run "$s"
  rc=$?
  # shellcheck disable=SC2163  # the variable NAMED by $_n
  for _n in ${ROUTE_NAMES[@]+"${ROUTE_NAMES[@]}"}; do export -n "$_n" 2>/dev/null; done
  set -e
  # Copy every report this run produced into the repo, with a
  # reproduce command that works for own-arena scenarios (the
  # engine's default assumes the central arena's invocation).
  found=0
  skipped=0
  while IFS= read -r f; do
    [ "$(report_verdict "$f")" = skip ] && skipped=1
    staged="$STAGE/$(basename "$f")"
    # Owner-only, like the engine's original (umask, so no wider moment).
    (umask 077; sed "s|^Reproduce: \`.*\`|Reproduce: \`gentar/run.sh $s\`|" "$f" \
      > "$staged") && found=1
    # gentar/reports/ is what CI publishes. A report that cannot be
    # redacted does not go there — the original stays in the arena's out/
    # for the fix loop, and the run's verdict is unchanged (agy review).
    if ! publish "$staged" "$HERE/reports/$(basename "$f")"; then
      echo "report: $(basename "$f") NOT published — it could not be redacted; the original is $ARENA/out/$(basename "$f")" >&2
    fi
  done < <(find out -name 'report-*.md' -newer "$MARKER" 2>/dev/null)
  rm -f "$MARKER"
  if [ "$found" -ne 1 ]; then
    # Every terminal outcome (pass/fail/refuse/skip) writes one; a missing
    # report means reporting itself broke — fail loudly, don't pass
    # silently without the fix-loop artifact.
    echo "no report written for $s" >&2
    rc=1
  elif [ "$skipped" = 1 ] && [ "$rc" -eq 0 ]; then
    quarantined="$quarantined $s"
  fi
  [ "$rc" -eq 0 ] || status=$rc
done
# A quarantined suite exits 0, so a known flake cannot turn the run red,
# but it ran nothing. Said once more at the end, like the sweep's skipped
# judged suites, so a green run is never read as having checked it.
if [ -n "$quarantined" ]; then
  n=$(echo $quarantined | wc -w | tr -d ' ')
  msg="$n suite(s) quarantined, not run:$quarantined (GENTAR_QUARANTINE; neither a pass nor a failure)"
  echo "run summary: $msg" >&2
  if [ "${GITHUB_ACTIONS:-}" = true ]; then
    echo "::warning title=quarantined suites not run::$msg"
  fi
fi
render_dashboard || true
exit "$status"
