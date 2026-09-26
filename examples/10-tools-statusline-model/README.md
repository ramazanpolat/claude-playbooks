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
- `SET STATUSLINE '<command>'` sets `statusLine` to that command, keeping any
  other field of an existing one (such as `padding`). `UNSET STATUSLINE`
  removes it.
- `SET MODEL '<model>'` is the playbook's default model and the weakest
  choice: `ANTHROPIC_MODEL` from an env set, `--model` at launch and `/model`
  in a session all win over it. `EXPLAIN PLAYBOOK` says which one decides.
  `UNSET MODEL` removes it.
