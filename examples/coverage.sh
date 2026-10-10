#!/bin/sh
# Every grammar clause is documented and exercised: each clause kind in
# internal/grammar/ast.go (its value is the clause as written) must appear in
# the reference and in something CI runs: a .cpb file, or a command in an
# example's .check or .setup script. Comments and README prose do not count.
# A new clause without both fails CI; there is no exemption list.
# Usage: examples/coverage.sh   (from anywhere in the repository)
set -eu
cd "$(dirname "$0")/.."
run=$(mktemp)
trap 'rm -f "$run"' EXIT
# What CI runs, without comments: `--` lines in .cpb files, `#` lines in scripts.
for f in $(find examples -name '*.cpb'); do sed 's/--.*$//' "$f"; done > "$run"
for f in $(find examples -name .check -o -name .setup); do sed 's/#.*$//' "$f"; done >> "$run"
kinds=$(sed -n 's/^[[:space:]]*[A-Za-z]* *Kind = "\([^"]*\)".*/\1/p' internal/grammar/ast.go)
[ -n "$kinds" ] || { echo "no clause kinds found in internal/grammar/ast.go"; exit 1; }
fail=0
# One kind per line, read whole: a kind holds spaces and underscores.
while IFS= read -r k; do
  case "$k" in
    # The kinds whose value is not their keywords: SET [VAR] K FROM is split
    # by an operand, the sandbox.<key> forms take the table's own keys, and
    # the property forms take a playbook property's key.
    "SET FROM") re='(^|[^A-Z])SET[[:space:]]+(VAR[[:space:]]+)?[A-Za-z_][A-Za-z0-9_]*[[:space:]]+FROM([^A-Z]|$)' ;;
    "SET sandbox.<key>") re='(^|[^a-z_.])sandbox\.[a-z_]+[[:space:]]*=' ;;
    "DELETE sandbox.<key>") re='(^|[^A-Z])DELETE[[:space:]]+sandbox\.[a-z_]+' ;;
    "SET <key> = <value>") re='(^|[^A-Z])SET[[:space:]]+[a-z_]+[[:space:]]*=' ;;
    "DELETE <key>") re='(^|[^A-Z])DELETE[[:space:]]+[a-z_]+([^A-Za-z_=]|$)' ;;
    *) re="(^|[^A-Z])$(printf '%s' "$k" | sed 's/ /[[:space:]]+/g')([^A-Z]|$)" ;;
  esac
  grep -Eq "$re" SPEC.md || { echo "no reference entry: $k"; fail=1; }
  grep -Eq "$re" "$run" || { echo "no example CI runs: $k"; fail=1; }
done <<EOF
$kinds
EOF
[ $fail = 0 ] && echo "every clause has a reference entry and an example CI runs"
exit $fail
