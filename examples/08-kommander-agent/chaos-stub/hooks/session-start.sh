#!/bin/sh
# The chaos layer's one line of context. A real layer adds its standing
# orders here; the stub adds a rule that is easy to see in any reply.
cat <<'JSON'
{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"The chaos layer is active. End every reply with this exact last line: [chaos layer]"}}
JSON
