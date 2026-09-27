# 14 — the model picker

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK router-agent   # Model picker: only: glm-5.3 (GLM 5.3), glm-5.3-flash (GLM 5.3 Flash)
```

`/model` in a session of this playbook lists these two rows and nothing
else. They are written into the playbook's `settings.json` as
`modelPicker`, which Claude Code reads from 2.1.242 (`behavesAs` from
2.1.257):

- `ADD MODEL '<id>' [LABEL '…'] [DESCRIPTION '…'] [BEHAVES AS '<id>']` adds a row, or updates the one with that id in place. `BEHAVES AS` tells Claude Code which model's behaviour to assume for an id it does not know, such as a router's.
- `DROP MODEL '<id>'` removes a row.
- `SET MODEL PICKER ONLY` shows these rows only; `APPEND` adds them after the built-in ones. `UNSET MODEL PICKER` removes the picker.

Rows and keys cpb did not write are kept. The same recipe works on a plain
config directory (`APPLY … TO '~/.claude'`), since the picker is a user
setting. The model a session starts with is a separate clause:
`SET MODEL '<id>'` ([example 10](../10-tools-statusline-model/)).
