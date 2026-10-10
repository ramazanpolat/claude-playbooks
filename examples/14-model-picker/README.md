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
- `SET model_picker.mode = 'only'` shows these rows only; `'append'` adds them after the built-in ones. `DELETE model_picker` (or `model_picker.mode`) puts the mode back to Claude Code's default, appending; the rows are a collection, which `DROP MODEL` empties, and a picker left empty goes.

Rows and keys cpb did not write are kept. The same recipe works on a plain
config directory (`APPLY … TO '~/.claude'`), since the picker is a user
setting. The model a session starts with is a separate clause:
`SET model = '<id>'` ([example 10](../10-tools-statusline-model/)).
