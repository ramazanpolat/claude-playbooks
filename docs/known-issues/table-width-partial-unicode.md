# Known issue: table column widths use partial Unicode data

**Status:** known limitation, shipped deliberately in v3.15.0. Follow-up: https://github.com/ramazanpolat/claude-playbooks/issues/51
**Affects:** the aligned tables (`claude-playbook env-profile` and other tabular
output), only when stdout is a terminal.
**Severity:** cosmetic. No data is lost or hidden.

## What happens

Column widths are measured in terminal cells by `displayWidth` in
`cmd/table.go`. It uses a hand-maintained list of double-width Unicode ranges
rather than the full `East_Asian_Width` table.

The list covers what realistically appears in a playbook name or description:
CJK ideographs, Hangul, Kana, fullwidth forms, the emoji planes, common
double-width symbols, and emoji variation selectors (`U+FE0F`). It does **not**
cover every unconditional wide character. Characters it misses are counted as
one cell although a terminal draws them in two — for example:

- `U+2329` / `U+232A` (angle brackets)
- supplementary Kana such as `U+1B000`

A description built from such characters can be wider than the table
calculated, so that row overflows the terminal width instead of being
truncated with an ellipsis.

## What is not affected

- **Piped or redirected output** (`| grep`, `> file`, CI). Tables are never
  truncated when stdout is not a terminal, so every character is always
  printed in full.
- **Credential masking.** Masking is decided by `looksLikeSecretKey` and
  `displayEnvValue` in `cmd/env.go`, which do not depend on display width.
- Alignment of every other row in the same table.

## Why it shipped this way

The hand-maintained list went through four review rounds, and each round found
the next missing range: cells versus runes, then symbols below `U+2E80`, then
emoji variation selectors, then `U+2329` and supplementary Kana. That pattern
is the evidence that extending the list by hand does not converge.

The remaining gap is limited to characters that rarely appear in a playbook
description, and the table output is still a large improvement on the
unaligned output it replaced, so v3.15.0 ships with it rather than holding the
release.

## The fix

Derive widths from real Unicode data instead of a hand-written list —
`golang.org/x/text/width` (same `golang.org/x` family as the `x/term` already
in use), or an equivalent implementation. Keep the `go` directive at 1.21 when
adding it: the arena builds in `golang:1.21`, and `go get <pkg>@latest` bumps
the directive silently.

The existing tests in `cmd/table_test.go` —
`TestDisplayWidthCountsCellsNotRunes`,
`TestDisplayWidthCoversWideSymbolsBelowCJK`,
`TestDisplayWidthHandlesVariationSelectors`,
`TestClipNeverExceedsTheCellBudget` and `TestClipAndDisplayWidthAgree` — should
keep passing, and a case for `U+2329` and `U+1B000` should be added.
