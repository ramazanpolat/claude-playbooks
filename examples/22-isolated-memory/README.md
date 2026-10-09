# 22 — keeping ~/.claude's memory out

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK focused     # Memory: isolated …
cpb SHOW PLAYBOOK familiar       # Memory  shared: ~/.claude's CLAUDE.md and rules load into it …
```

Claude Code loads project memory from every ancestor of the directory it
starts in, and `~/.claude`, the machine's own configuration, is one of them.
So without this setting, `~/.claude/CLAUDE.md` and `~/.claude/rules/` load
into every playbook you run anywhere under your home directory.

- A new playbook keeps them out: its `memory` setting is `'isolated'`, the
  default. That is one entry in its `settings.json`, `claudeMdExcludes:
  ["<your home>/.claude/**"]`, which every launch reads, `cpb run` and a
  plain `CLAUDE_CONFIG_DIR=… claude` alike.
- `SETTINGS memory = 'shared'` at `CREATE`, or `MODIFY SETTING memory =
  'shared'` later, lets them load. `RESET SETTING memory` goes back to the
  default. Only cpb's own entry moves; other `claudeMdExcludes` entries are
  yours and stay.
- It stops the loading, not reading: an agent can still open those files
  with its Read tool. Add `DENY TOOL 'Read(<your home>/.claude/**)'` where that
  matters.
- A playbook created before this setting existed keeps loading them until
  you run `MODIFY SETTING memory = 'isolated'` in it: nothing is migrated.

Reference: [Playbook settings](../../SPEC.md#playbook-settings).
