# 22 — keeping ~/.claude's instructions out

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK focused     # Global instructions: excluded …
cpb SHOW PLAYBOOK familiar       # Global instructions  inherited: ~/.claude's CLAUDE.md and rules load into it …
```

Claude Code loads the CLAUDE.md of every ancestor of the directory it
starts in, and `~/.claude`, the machine's own configuration, is one of them.
So without this setting, `~/.claude/CLAUDE.md` and `~/.claude/rules/` load
into every playbook you run anywhere under your home directory.

- A new playbook keeps them out: its `global_instructions` setting is
  `'excluded'`, the default. That is one entry in its `settings.json`,
  `claudeMdExcludes: ["<your home>/.claude/**"]`, which every launch reads,
  `cpb run` and a plain `CLAUDE_CONFIG_DIR=… claude` alike.
- `SET global_instructions = 'inherited'` at `CREATE`, or later, lets them
  load. `RESET global_instructions` goes back to the default. Only cpb's own
  entry moves; other `claudeMdExcludes` entries are yours and stay.
- The project's own `CLAUDE.md` loads either way: "global" means the
  machine-wide instructions in `~/.claude`.
- It stops the loading, not reading: an agent can still open those files
  with its Read tool. Add `DENY TOOL 'Read(<your home>/.claude/**)'` where that
  matters.
- A playbook created before this setting existed keeps loading them until
  you run `SET global_instructions = 'excluded'` in it: nothing is migrated.

Reference: [Playbook properties](../../SPEC.md#playbook-properties).
