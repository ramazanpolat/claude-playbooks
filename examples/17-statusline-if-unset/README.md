# 17 — a status line offered with SET IF UNSET

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK barred    # Status line: date +%H:%M …
```

Claude Code runs one status line command per session, and a playbook has
one slot for it.

- `SET statusline.command = '<command>'` always applies: it replaces whatever the slot
  holds, and cpb keeps the replaced one in a short history
  (`REVERT STATUSLINE` puts it back; [example 10](../10-tools-statusline-model/)).
- `SET IF UNSET statusline.command = '<command>', statusline.refresh = <n>`
  applies only where no status line is set yet. A recipe that offers a bar
  uses it, so applying the recipe to a playbook whose status line you chose
  leaves yours in place.
- `SET IF UNSET` takes any property, and applies whole or not at all: only
  when none of the keys it names is set (each at the value `DELETE` gives
  it). Otherwise it changes nothing and says which key was set:
  `SET IF UNSET model = 'claude-opus-5-5', agent = 'reviewer'` leaves both
  alone on a playbook that has a model.
- `SHOW CREATE` writes the status line the playbook has, never the condition.

Reference: [Status line and model](../../SPEC.md#status-line-and-model).
