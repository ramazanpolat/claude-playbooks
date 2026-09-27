# 12 — recipes and targets

A **recipe** is a playbook file whose `ALTER PLAYBOOK` names no playbook
(`recipe.cpb`). Where it goes is decided when it is applied:

```
cpb APPLY playbook.cpb --dry-run          # creates frontend and backend, applies the recipe to both
cpb APPLY playbook.cpb
cpb APPLY recipe.cpb TO frontend          # one playbook (created bare if missing)
cpb APPLY recipe.cpb TO '~/.claude' --dry-run   # a plain Claude Code config directory
cpb APPLY recipe.cpb TO frontend --dry-run --json   # the plan as JSON, for a program
```

- **`USE PLAYBOOK <name>;`** in a file sets the target for the name-less
  statements after it, as `USE <db>` does in SQL. An included file starts
  with the includer's target, so `playbook.cpb` reuses one recipe for two
  playbooks.
- **`APPLY <file> TO <playbook>`** sends every name-less statement to that
  playbook and ignores the files' `USE PLAYBOOK` lines (with a warning). A
  statement that names its playbook always goes to that playbook.
- **`TO '<dir>'`** targets a config directory that is not a playbook, such as
  `~/.claude`. Only Claude Code's own configuration applies there: plugins,
  the agent, tools, status line, model, MCP servers without credentials,
  skills, and `SET VAR` into its `settings.json` `env`. cpb backs up
  `settings.json` (and `.claude.json` before an MCP change) once per run, as
  `<file>.cpb-backup-<timestamp>`, and asks before it writes; `--yes`
  answers for a script. Launcher-only clauses (env sets, references,
  `BLOCK VAR`, aliases) are refused there, each with its reason.

A file with name-less statements and no target is refused before anything
is written.
