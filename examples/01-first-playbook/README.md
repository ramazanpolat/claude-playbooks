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

Later, on the command line: `cpb ALTER PLAYBOOK scratch RENAME TO sandbox-lab`
renames it and its command, `cpb ALTER PLAYBOOK scratch SET launcher = sc`
gives it another command (`SET launcher = ''` none, `DELETE launcher` its
name again), `cpb CREATE PLAYBOOK boxed SET sandbox.always = true, login = isolated`
makes one
that always runs inside a Docker Sandbox
([Sandboxed sessions](../../docs/guides/sandbox.md)), and
`cpb CREATE PLAYBOOK dev LINK <dir>` registers a directory you develop in
place (it needs a `.playbook` there). `.check` runs them all.
