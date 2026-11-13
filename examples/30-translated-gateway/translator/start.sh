#!/bin/sh
# Starts the local translator in the foreground; Ctrl-C stops it.
#
# - It listens on 127.0.0.1 only.
# - The provider's key is lent by your secret helper to this one process for
#   its life (the helper's exec form, the one cpb uses at launch). It is never
#   written to a file.
# - The local port takes a new random token at every start, written where
#   your helper reads file: references and removed when the translator stops.
#   The account reads it as file:translator-token, so a launch with the
#   translator down stops before claude starts.
#
# Adjust: HELPER (a helper that resolves both references below), PROVIDER_KEY
# (the reference of the provider's key in your secret store) and TOKENS (the
# folder your helper reads file: references from; this one is the sample
# helper's, examples/secret-helper/).
set -eu

DIR="$HOME/.local/share/translator"           # the translator's own folder
HELPER=$(command -v my-secret-helper) || { echo "my-secret-helper is not on PATH" >&2; exit 1; }
PROVIDER_KEY='keychain:venice-key'
TOKENS="${CPB_SECRET_FILES:-$HOME/.config/cpb-secrets}"
PORT=4100

umask 077
mkdir -p "$TOKENS"
token="sk-$(LC_ALL=C tr -dc 'a-f0-9' </dev/urandom | head -c 40)"
printf '%s' "$token" > "$TOKENS/translator-token"
trap 'rm -f "$TOKENS/translator-token"' EXIT

# Nothing from the calling shell reaches the translator but HOME and PATH.
# LITELLM_LOCAL_MODEL_COST_MAP keeps LiteLLM from fetching its price table.
# The translator is called by its full path: a command of the same name on
# your PATH may be something else (a cpb launcher, say).
env -i HOME="$HOME" PATH=/usr/bin:/bin \
  TRANSLATOR_TOKEN="$token" LITELLM_LOCAL_MODEL_COST_MAP=True \
  "$HELPER" PROVIDER_API_KEY="$PROVIDER_KEY" -- \
  "$DIR/venv/bin/litellm" --config "$DIR/litellm.yaml" --host 127.0.0.1 --port "$PORT" &
child=$!
trap 'kill -TERM "$child" 2>/dev/null' INT TERM
status=0
wait "$child" || status=$?
# A trapped signal interrupts wait: wait until the translator has ended.
while kill -0 "$child" 2>/dev/null; do wait "$child" || status=$?; done
exit "$status"
