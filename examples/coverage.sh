#!/bin/sh
# Every grammar clause is documented and shown: each clause kind in
# internal/grammar/ast.go (its value is the clause as written) must appear in
# the reference and in an example (a .cpb file, a README or a .check). A new
# clause without both fails CI; there is no exemption list.
# Usage: examples/coverage.sh   (from anywhere in the repository)
set -eu
cd "$(dirname "$0")/.."
examples=$(find examples -name '*.cpb' -o -name README.md -o -name .check -o -name .setup)
fail=0
kinds=$(sed -n 's/^[[:space:]]*[A-Za-z]* *Kind = "\([^"]*\)".*/\1/p' internal/grammar/ast.go)
[ -n "$kinds" ] || { echo "no clause kinds found in internal/grammar/ast.go"; exit 1; }
for k in $(printf '%s\n' "$kinds" | tr ' ' '_'); do
  k=$(printf '%s' "$k" | tr '_' ' ')
  # Keywords in order; one operand may sit between two (SET K FROM).
  re="(^|[^A-Z])$(printf '%s' "$k" | sed 's/ /[[:space:]]+([^[:space:]]+[[:space:]]+)?/g')([^A-Z]|$)"
  grep -Eq "$re" docs/reference/cli-grammar.md || { echo "no reference entry: $k"; fail=1; }
  # shellcheck disable=SC2086
  cat $examples | grep -Eq "$re" || { echo "no example: $k"; fail=1; }
done
[ $fail = 0 ] && echo "every clause has a reference entry and an example"
exit $fail
