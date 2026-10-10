# 10 — tool permissions, the status line and the model

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK reviewer     # Tools: allow …; deny …, and which model decides
```

These are `settings.json` keys Claude Code has no command for, so cpb writes
them into the playbook's own `settings.json` and keeps every key it did not
write:

- `ALLOW TOOL '<rule>'` / `DENY TOOL '<rule>'` add Claude Code permission
  rules (`permissions.allow` / `permissions.deny`), as typed. A rule is in at
  most one list: allowing a denied rule moves it. `UNSET TOOL '<rule>'`
  removes it from either.
- `SET statusline.command = '<command>'` sets `statusLine` to that command, keeping any
  other field of an existing one (such as `padding`). `DELETE statusline`
  removes it. `REFRESH 10` also re-renders it every 10 seconds while the
  session is idle, which Claude Code does not do without it. `SET STATUSLINE
  REFRESH <n>` and `DELETE statusline.refresh` change only the interval.
  `REVERT STATUSLINE` puts back the status line a statement replaced
  last (cpb keeps a short history of them).
- `SET model = '<model>'` is the playbook's default model and the weakest
  choice: `ANTHROPIC_MODEL` from an env set, `--model` at launch and `/model`
  in a session all win over it. `EXPLAIN PLAYBOOK` says which one decides.
  `DELETE model` removes it.
