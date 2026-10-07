#!/bin/sh
# cpb depends on no integration (docs/design/integrations.md), so its code,
# tests and docs name none. This fails on a tracked file that does. Skipped:
# CHANGELOG.md (it records history), this script (it holds the list), and the
# page that states the rule. "pilot" is not on the list: it is cpb's word for
# the human who drives it.
# Run by CI on both OSes: sh .github/scripts/no-integration-names.sh
set -eu
cd "$(dirname "$0")/../.."

# The names, as extended regular expressions matched without case.
names='pilot[-_.]?profile
oi[-_]costume
oi_wear
statusmux
kommander'

pattern=$(printf '%s\n' "$names" | paste -sd '|' -)
if git grep -n -I -i -E "$pattern" -- . \
  ':(exclude)CHANGELOG.md' \
  ':(exclude).github/scripts/no-integration-names.sh' \
  ':(exclude)docs/design/integrations.md'; then
  echo "" >&2
  echo "error: the lines above name an integration. cpb depends on none" >&2
  echo "(docs/design/integrations.md): describe the case without the name." >&2
  exit 1
fi
echo "no integration named"
