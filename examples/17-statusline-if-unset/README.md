# 17 — a status line offered with IF UNSET

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK barred    # Status line: date +%H:%M …
```

Claude Code runs one status line command per session, and a playbook has
one slot for it.

- `SET STATUSLINE '<command>'` always applies: it replaces whatever the slot
  holds, and cpb keeps the replaced one in a short history
  (`SET STATUSLINE PREVIOUS` puts it back; [example 10](../10-tools-statusline-model/)).
- `SET STATUSLINE '<command>' IF UNSET` applies only where no status line is
  set yet. A recipe that offers a bar uses it, so applying the recipe to a
  playbook whose status line you chose leaves yours in place. `REFRESH <n>`
  goes before `IF UNSET` and applies with it.
- `SHOW CREATE` writes the status line the playbook has, never the condition.

Reference: [Status line and model](../../SPEC.md#status-line-and-model).
