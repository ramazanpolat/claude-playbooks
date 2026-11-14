#!/bin/sh
# One command for a session on the provider: starts the translator when none
# runs, waits until it answers, runs the playbook with every argument passed
# on, and stops the translator again when the session ends.
#
#   session.sh                              # cpb run venice-agent
#   session.sh --model claude-haiku-5-5     # arguments pass on to cpb run
#
# - A translator that was already running (started by hand with start.sh in
#   another terminal) is used and left running: only one this script started
#   is stopped.
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

# Up: the per-run token is there and the translator answers.
up() { [ -s "$TOKENS/translator-token" ] && curl -fsS -m 2 -o /dev/null "$HEALTH" 2>/dev/null; }

started=""
stop() {
  [ -n "$started" ] || return 0
  kill -TERM "$started" 2>/dev/null
  wait "$started" 2>/dev/null
  started=""
}
trap stop EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

if ! up; then
  pid=$(cat "$DIR/run.lock/pid" 2>/dev/null || true)
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    echo "a translator is starting (pid $pid); waiting for it" >&2
  else
    [ -x "$START" ] || { echo "no translator start script at $START" >&2; exit 1; }
    umask 077
    mkdir -p "$DIR"
    echo "starting the translator (log: $DIR/translator.log)" >&2
    "$START" >>"$DIR/translator.log" 2>&1 &
    started=$!
  fi
  i=0
  until up; do
    if [ -n "$started" ] && ! kill -0 "$started" 2>/dev/null; then
      started=""
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

# The session handles Ctrl-C itself; this script waits for it to end, then
# stops what it started.
trap ':' INT
cpb run "$PLAYBOOK" "$@"
status=$?
stop
exit "$status"
