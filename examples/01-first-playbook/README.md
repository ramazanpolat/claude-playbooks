# 01 — your first playbook

```
cpb APPLY playbook.cpb          # creates ~/.claude-playbooks/scratch and the `scratch` command
scratch                         # Claude Code, bound to that directory
cpb SHOW PLAYBOOK scratch       # what it is
cpb DROP PLAYBOOK scratch --yes # gone, launcher and all
```

The playbook is a directory with its own `CLAUDE.md`, `settings.json`, hooks,
history and MCP servers; your `~/.claude` is untouched. Applying the file again
changes nothing: `CREATE PLAYBOOK IF NOT EXISTS` never re-creates.
The same statement works on the command line: `cpb CREATE PLAYBOOK scratch`.
