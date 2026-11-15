#!/bin/sh
# One command for a session on the provider: starts the translator when none
# runs, waits until it answers, runs the playbook with every argument passed
# on, and stops the translator again when the last session using it ends.
#
#   session.sh                              # cpb run venice-agent
#   session.sh --model claude-haiku-5-5     # arguments pass on to cpb run
#
# - Sessions share one translator: a session started while another runs uses
#   the same translator, and only the last one to end stops it.
# - A translator that was already running when the first session started
#   (started by hand with start.sh in another terminal) is used and left
#   running.
# - The translator's output goes to translator.log in its folder.
#
# Adjust: PLAYBOOK, TOKENS as in start.sh, and HEALTH for a translator other
# than LiteLLM (an address that answers once it is ready). START and HEALTH
# may also come from the environment.
set -u

DIR="$HOME/.local/share/translator"           # the translator's own folder
PLAYBOOK=venice-agent
TOKENS="${CPB_SECRET_FILES:-$HOME/.config/cpb-secrets}"
START="${TRANSLATOR_START:-$DIR/start.sh}"
HEALTH="${TRANSLATOR_HEALTH:-http://127.0.0.1:4100/health/liveliness}"
SESSIONS="$DIR/sessions"        # one empty file per running session, named by its pid
OURS="$DIR/started-by-session"  # the pid of a translator a session started
MUTEX="$DIR/session.lock"       # held while a session joins or leaves

# Up: the per-run token is there and the translator answers.
up() { [ -s "$TOKENS/translator-token" ] && curl -fsS -m 2 -o /dev/null "$HEALTH" 2>/dev/null; }
# Alive: the process exists and has not ended (a zombie has ended).
alive() { [ -n "$1" ] && kill -0 "$1" 2>/dev/null && ! ps -o stat= -p "$1" 2>/dev/null | grep -q Z; }

# Joining and leaving take turns, so a session that leaves and one that
# arrives never both decide alone. A lock whose holder died is taken over.
locked=""
lock() {
  n=0
  while ! mkdir "$MUTEX" 2>/dev/null; do
    holder=$(cat "$MUTEX/pid" 2>/dev/null || true)
    if [ -n "$holder" ] && ! alive "$holder"; then rm -rf "$MUTEX"; continue; fi
    n=$((n + 1))
    if [ "$n" -gt 900 ]; then echo "$MUTEX is held by pid ${holder:-?} for over 3 minutes" >&2; return 1; fi
    sleep 0.2
  done
  echo $$ > "$MUTEX/pid"
  locked=1
}
unlock() { [ -z "$locked" ] || rm -rf "$MUTEX"; locked=""; }

joined=""
leave() {
  [ -n "$joined" ] || { unlock; return 0; }
  lock
  rm -f "$SESSIONS/$$"
  joined=""
  # A session still running keeps the translator. The file of a session that
  # ended without leaving (killed) is removed.
  others=""
  for f in "$SESSIONS"/*; do
    [ -e "$f" ] || continue
    if alive "${f##*/}"; then others=1; else rm -f "$f"; fi
  done
  if [ -z "$others" ]; then
    # The last session out stops the translator if a session started it: its
    # pid is the one holding start.sh's lock. One started by hand stays.
    pid=$(cat "$OURS" 2>/dev/null || true)
    if [ -n "$pid" ] && [ "$pid" = "$(cat "$DIR/run.lock/pid" 2>/dev/null)" ] && alive "$pid"; then
      kill -TERM "$pid" 2>/dev/null
      i=0
      while alive "$pid" && [ "$i" -lt 100 ]; do sleep 0.2; i=$((i + 1)); done
    fi
    rm -f "$OURS"
  fi
  unlock
}
trap leave EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

(umask 077 && mkdir -p "$SESSIONS") || exit 1
lock || exit 1
: > "$SESSIONS/$$"
joined=1
if ! up; then
  pid=$(cat "$DIR/run.lock/pid" 2>/dev/null || true)
  started=""
  if alive "$pid"; then
    echo "a translator is starting (pid $pid); waiting for it" >&2
  else
    [ -x "$START" ] || { echo "no translator start script at $START" >&2; exit 1; }
    echo "starting the translator (log: $DIR/translator.log)" >&2
    # Owner-only files for the translator and its log; the session keeps the
    # caller's umask. nohup: closing this terminal does not stop a translator
    # another session still uses.
    (umask 077 && exec nohup "$START" >>"$DIR/translator.log" 2>&1) &
    started=$!
    echo "$started" > "$OURS"
  fi
  i=0
  until up; do
    if [ -n "$started" ] && ! alive "$started"; then
      echo "the translator did not start; the end of $DIR/translator.log:" >&2
      tail -n 15 "$DIR/translator.log" >&2
      exit 1
    fi
    i=$((i + 1))
    if [ "$i" -gt 60 ]; then
      echo "the translator did not answer at $HEALTH within 60 seconds" >&2
      exit 1
    fi
    sleep 1
  done
fi
unlock

# The session handles Ctrl-C itself; this script waits for it to end, then
# leaves (and stops the translator if it was the last session).
trap ':' INT
cpb run "$PLAYBOOK" "$@"
exit $?
