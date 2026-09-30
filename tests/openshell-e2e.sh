#!/usr/bin/env bash
# openshell-e2e.sh -- end-to-end check of the experimental OpenShell sandbox
# backend (--sandbox=openshell) against a real OpenShell gateway.
#
# Needs: Linux, Docker Engine 28+, OpenShell 0.1.x with host mounts enabled
# (docs/guides/sandbox.md, "OpenShell backend"), tmux, python3, and the
# claude-playbook binary to test. Not run in CI. Run it on a disposable host.
#
#   tests/openshell-e2e.sh /path/to/claude-playbook
#
# Everything is dummy: a throwaway HOME (the gateway registration is borrowed
# from ~/.config/openshell), a fake Anthropic endpoint on this host that
# answers every message with E2E-OK and logs the auth headers it received,
# and made-up tokens. It restarts the gateway twice (the refusal checks) and
# restores its config. It leaves no sandbox, provider or profile behind; the
# built image (cpb-openshell/claude:*) stays for the next run.
set -u
unset CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN
CPB=${1:?usage: $0 /path/to/claude-playbook}
REAL_HOME=$HOME
E=$(mktemp -d /tmp/cpb-openshell-e2e.XXXXXX)
GW=$REAL_HOME/.config/openshell/gateway.toml
export HOME=$E/home
mkdir -p "$HOME/.config" "$E/bin" "$E/work" "$E/ro"
ln -s "$REAL_HOME/.config/openshell" "$HOME/.config/openshell"
ln -s "$(readlink -f "$CPB")" "$E/bin/claude-playbook"
export PATH="$E/bin:$PATH"
W=$(cd "$E/work" && pwd -P); RO=$(cd "$E/ro" && pwd -P)
echo work > "$W/README"; echo ro > "$RO/NOTE"
SB=cpb-e2e; ID=$SB-anthropic-auth-token
pass=0; fail=0
ok() { echo "PASS $1"; pass=$((pass+1)); }
bad() { echo "FAIL $1${2:+ -- $2}"; fail=$((fail+1)); }
O() { openshell "$@" </dev/null 2>&1; }
X() { openshell sandbox exec -n "$SB" --no-tty -- sh -c "$1" </dev/null 2>&1; }
gateway_up() { for _ in $(seq 60); do O status | grep -q Connected && return 0; sleep 2; done; return 1; }

# The fake Anthropic endpoint: SSE for streamed messages, JSON otherwise;
# GETs echo the auth headers. Port 18081 is a second host the credential is
# not bound to.
cat > "$E/api.py" <<'PY'
import http.server, json, sys, threading
LOG = sys.argv[1]
class H(http.server.BaseHTTPRequestHandler):
    def rec(self):
        r = {"port": self.server.server_port, "method": self.command, "path": self.path,
             "authorization": self.headers.get("Authorization"), "x-api-key": self.headers.get("x-api-key")}
        with open(LOG, "a") as f: f.write(json.dumps(r) + "\n")
        return r
    def send(self, code, body, ctype="application/json"):
        b = body.encode(); self.send_response(code); self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self): self.send(200, json.dumps(self.rec()))
    def do_POST(self):
        self.rec(); n = int(self.headers.get("Content-Length") or 0)
        try: req = json.loads(self.rfile.read(n) or b"{}")
        except Exception: req = {}
        if not self.path.startswith("/v1/messages") or self.path.startswith("/v1/messages/count_tokens"):
            return self.send(200, json.dumps({"input_tokens": 1}))
        msg = {"id": "msg_e2e", "type": "message", "role": "assistant", "model": req.get("model", "e2e"),
               "content": [], "stop_reason": None, "stop_sequence": None, "usage": {"input_tokens": 1, "output_tokens": 1}}
        if not req.get("stream"):
            msg["content"] = [{"type": "text", "text": "E2E-OK"}]; msg["stop_reason"] = "end_turn"
            return self.send(200, json.dumps(msg))
        ev = [("message_start", {"type": "message_start", "message": msg}),
              ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}),
              ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "E2E-OK"}}),
              ("content_block_stop", {"type": "content_block_stop", "index": 0}),
              ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": 1}}),
              ("message_stop", {"type": "message_stop"})]
        self.send(200, "".join("event: %s\ndata: %s\n\n" % (k, json.dumps(v)) for k, v in ev), "text/event-stream")
    def log_message(self, *a): pass
for p in (18080, 18081):
    s = http.server.ThreadingHTTPServer(("0.0.0.0", p), H); threading.Thread(target=s.serve_forever, daemon=True).start()
threading.Event().wait()
PY
setsid python3 "$E/api.py" "$E/received.jsonl" >/dev/null 2>&1 &
API=$!
cleanup() {
  kill "$API" 2>/dev/null; tmux -L cpbe2e kill-server 2>/dev/null
  [ -f "$E/gateway.toml.bak" ] && cp "$E/gateway.toml.bak" "$GW" && systemctl --user restart openshell-gateway && gateway_up
  O sandbox delete "$SB" >/dev/null
  # The sandbox's deletion is asynchronous; a provider attached to it cannot
  # be deleted until it is gone.
  for _ in $(seq 15); do O provider get "$ID" >/dev/null || break; O provider delete "$ID" >/dev/null && break; sleep 2; done
  O profile delete --global "$ID" >/dev/null
  rm -rf "$E"
}
trap cleanup EXIT

claude-playbook CREATE PLAYBOOK e2e NO ALIAS NO PILOT PROFILE >/dev/null
P=$HOME/.claude-playbooks/e2e
claude-playbook env-profile r set ANTHROPIC_BASE_URL=http://localhost:18080 ANTHROPIC_AUTH_TOKEN=dummy-e2e-token-1 >/dev/null
claude-playbook env e2e use r >/dev/null
# Test fixture: a second host the sandbox may reach (with no credential).
printf '\n[sandbox]\nallow_net = ["host.openshell.internal:18081"]\n' >> "$P/.playbook"
L() { n=$1; shift; timeout 900 claude-playbook run --sandbox=openshell --workdir "$W" --mount "$RO:ro" e2e "$@" </dev/null >"$E/out.$n" 2>&1; echo $? >"$E/rc.$n"; }
has() { grep -qF -- "$2" "$E/out.$1"; }
got() { grep -F "\"port\": $1" "$E/received.jsonl" 2>/dev/null | grep -qF -- "$2"; }

echo "== 1 create: image, policy, mounts, secret, a real claude -p, stop"
L 1 -p "say hi"
if [ "$(cat "$E/rc.1")" = 0 ] && has 1 E2E-OK; then ok "claude -p answered through the sandbox"; else bad "claude -p" "rc $(cat "$E/rc.1"): $(tail -3 "$E/out.1" | tr '\n' ' ')"; fi
has 1 "Sandbox $SB created (openshell)" && ok "sandbox created" || bad "sandbox created"
has 1 "Secret ANTHROPIC_AUTH_TOKEN stays on the host" && ok "secret registered" || bad "secret registered"
got 18080 "Bearer dummy-e2e-token-1" && ok "the endpoint got the real token, injected on the way out" || bad "token injected"
has 1 "Sandbox $SB stopped" && O sandbox get "$SB" | grep -q Stopped && ok "stopped after the session" || bad "stopped after the session"

echo "== 2 inside: placeholder, mounts, identity, a host the key is not bound to"
O sandbox start "$SB" >/dev/null
X 'env' > "$E/env.in"
grep -q '^ANTHROPIC_AUTH_TOKEN=openshell:resolve:env:' "$E/env.in" && ! grep -q dummy-e2e-token "$E/env.in" && ok "the sandbox env holds only OpenShell's placeholder" || bad "placeholder in env"
grep -q CLAUDE_CODE_OAUTH_TOKEN "$E/env.in" && bad "no machine token inside" || ok "no machine token inside"
X "echo x > '$W/from-inside'" >/dev/null; [ "$(stat -c %u "$W/from-inside" 2>/dev/null)" = "$(id -u)" ] && ok "workdir writable, the file owned by the host user" || bad "workdir writable"
X "cat '$RO/NOTE'" | grep -qx ro && ok "read-only mount readable" || bad "ro readable"
X "echo x > '$RO/nope'" | grep -q "Read-only file system" && ok "read-only mount refuses a write" || bad "ro write refused"
X "cat '$REAL_HOME/.claude/.credentials.json' 2>&1; ls /home /Users 2>&1" | grep -qiE "denied|no such" && ok "the rest of the host is not there" || bad "host isolation"
X 'curl -sS -m 10 -H "Authorization: Bearer $ANTHROPIC_AUTH_TOKEN" http://host.openshell.internal:18081/other' | grep -q credential_endpoint_mismatch && ok "the key sent to another host is refused (403 credential_endpoint_mismatch)" || bad "mismatch refused"
got 18081 dummy-e2e-token && bad "nothing reached the other host with the key" || ok "nothing reached the other host with the key"
O sandbox stop "$SB" >/dev/null

echo "== 3 rotation: a new token in the profile, the stopped sandbox started"
claude-playbook env-profile r set ANTHROPIC_AUTH_TOKEN=dummy-e2e-token-2 >/dev/null
L 3 -p "say hi"
has 3 "Starting sandbox $SB" && ok "stopped sandbox started on reuse" || bad "started on reuse"
[ "$(cat "$E/rc.3")" = 0 ] && got 18080 "Bearer dummy-e2e-token-2" && ok "rotated token injected" || bad "rotation"

echo "== 4 the claude TUI under the generated hard_requirement policy"
T="tmux -L cpbe2e"
$T new-session -d -s tui -x 160 -y 45 "claude-playbook run --sandbox=openshell --workdir '$W' --mount '$RO:ro' e2e; echo EXIT_RC=\$?; sleep 600"
main=""; for _ in $(seq 40); do
  sleep 3; s=$($T capture-pane -p -t tui)
  case "$s" in
    *"trust this folder"*) $T send-keys -t tui Down; sleep 1; $T send-keys -t tui Enter;;
    *"Choose the text style"*|*"Press Enter to continue"*|*"Enter to confirm"*) $T send-keys -t tui Enter;;
    *"shortcuts"*|*"auto mode"*|*"for agents"*) main=1; break;;
  esac
done
[ -n "$main" ] && ok "TUI reached the main prompt" || bad "TUI main prompt" "$($T capture-pane -p -t tui | grep -v '^\s*$' | tail -4 | tr '\n' ' ')"
$T send-keys -t tui -l "say hi"; sleep 1; $T send-keys -t tui Enter
for _ in $(seq 20); do sleep 2; $T capture-pane -p -t tui | grep -q E2E-OK && break; done
$T capture-pane -p -t tui | grep -q E2E-OK && ok "TUI prompt answered" || bad "TUI answer"
$T send-keys -t tui -l "/exit"; sleep 1; $T send-keys -t tui Enter
for _ in $(seq 30); do sleep 2; $T capture-pane -p -t tui | grep -q EXIT_RC= && break; done
$T capture-pane -p -t tui | grep -q "EXIT_RC=0" && ok "/exit, exit status 0" || bad "TUI exit" "$($T capture-pane -p -t tui | grep -v '^\s*$' | tail -3 | tr '\n' ' ')"
$T capture-pane -p -t tui | grep -q "Sandbox $SB stopped" && ok "stopped after the TUI session" || bad "stopped after TUI"
$T kill-server 2>/dev/null

echo "== 5 revoke: the token gone from the profile"
claude-playbook env-profile r unset ANTHROPIC_AUTH_TOKEN >/dev/null
L 5 -p "say hi"
has 5 "Secret ANTHROPIC_AUTH_TOKEN revoked at the proxy" && ok "revoked" || bad "revoked"
O provider list | grep -q "$ID" && bad "provider deleted" || ok "provider deleted"
O profile list | grep -q "$ID" && bad "profile deleted" || ok "profile deleted"

echo "== 6 refusals"
mkdir -p "$E/shim"; printf '#!/bin/sh\n[ "$1" = --version ] && { echo "openshell 0.1.1"; exit 0; }\nexec %s "$@"\n' "$(command -v openshell)" > "$E/shim/openshell"; chmod +x "$E/shim/openshell"
( PATH="$E/shim:$PATH"; L 6a -p hi ); has 6a "OpenShell 0.1.1 is not supported" && ok "an unsupported OpenShell refuses" || bad "version refusal" "$(tail -1 "$E/out.6a")"
systemctl --user stop openshell-gateway; L 6b -p hi
has 6b "the OpenShell gateway is not running" && ok "a stopped gateway refuses" || bad "gateway refusal" "$(tail -1 "$E/out.6b")"
systemctl --user start openshell-gateway; gateway_up || bad "gateway back up"
# Host mounts off: the create fails and names the fix. --sandbox-fresh first
# removes the sandbox and cpb's providers and profiles (token set again, so
# there is one to remove).
claude-playbook env-profile r set ANTHROPIC_AUTH_TOKEN=dummy-e2e-token-3 >/dev/null
L 6c -p hi; O provider list | grep -q "$ID" || bad "provider back for the removal check"
cp "$GW" "$E/gateway.toml.bak"; sed -i 's/^\([[:space:]]*\)enable_bind_mounts[[:space:]]*=[[:space:]]*true/\1enable_bind_mounts = false/' "$GW"
grep -q 'enable_bind_mounts = false' "$GW" || bad "host mounts switched off for the check" "no enable_bind_mounts = true in $GW"
systemctl --user restart openshell-gateway; gateway_up
L 6d --sandbox-fresh -p hi
has 6d "must allow host mounts" && ok "host mounts off refuses and names the fix" || bad "bind-mount refusal"
echo "   create error was: $(grep -A4 -F 'openshell sandbox create' "$E/out.6d" | tr '\n' ' ' | cut -c1-700)"
O provider list | grep -q "$ID" && bad "--sandbox-fresh deleted cpb's provider" || ok "--sandbox-fresh deleted cpb's provider"
O profile list | grep -q "$ID" && bad "--sandbox-fresh deleted cpb's profile" || ok "--sandbox-fresh deleted cpb's profile"
cp "$E/gateway.toml.bak" "$GW" && systemctl --user restart openshell-gateway && gateway_up && rm "$E/gateway.toml.bak"

echo "== $pass passed, $fail failed"
[ "$fail" = 0 ]
