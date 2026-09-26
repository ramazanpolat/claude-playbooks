# Configure an agent

A playbook is a Claude Code agent. Since v3.21.0 a playbook file can describe
all of it: route, plugins, main-thread agent, MCP servers, tool permissions,
status line, model and skills. Each clause below goes on `ALTER PLAYBOOK`,
on the command line or in a file, and applying it again changes nothing.

```
ALTER PLAYBOOK reviewer
  ADD MCP SERVER files COMMAND 'npx' ARGS '-y' '@modelcontextprotocol/server-filesystem' '/srv/notes'
  ALLOW TOOL 'Bash(git diff *)'
  DENY TOOL 'Read(~/.ssh/**)'
  SET STATUSLINE 'bash ~/bin/statusline.sh'
  SET MODEL 'claude-opus-5-5'
  ADD SKILL release-notes FROM '~/src/skills/release-notes';
```

## MCP servers

```
ADD MCP SERVER <name> COMMAND '<cmd>' [ARGS '<arg>' ...] [ENV K=V | ENV K FROM '<ref>' ...]
ADD MCP SERVER <name> URL '<url>' [TRANSPORT SSE] [HEADER '<name>' '<value>' | HEADER '<name>' FROM '<ref>' ...]
DROP MCP SERVER <name>
```

cpb runs Claude Code's own `claude mcp add-json … --scope user` with the
playbook as `CLAUDE_CONFIG_DIR`, so the server belongs to that playbook
alone. A changed declaration is removed and added again.

Credentials take a reference, always: an `Authorization` header or a
secret-looking env key must use `FROM '<ref>'`. Claude's config gets a
`${CPB_MCP_…}` placeholder, and the secret helper resolves it into the
session at launch. A header's reference resolves to the whole value
(`Bearer <token>`). Example: [09](../../examples/09-mcp-servers/).

## Tools, status line, model

```
ALLOW TOOL '<rule>' ...     DENY TOOL '<rule>' ...     UNSET TOOL '<rule>' ...
SET STATUSLINE '<command>'  UNSET STATUSLINE
SET MODEL '<model>'         UNSET MODEL
```

These are `settings.json` keys with no CLI, so cpb writes them into the
playbook's `settings.json` and keeps every key it did not write. A rule is
Claude Code's permission syntax, as typed, and sits in one list at most. The
model is the playbook's default and the weakest choice: `ANTHROPIC_MODEL`,
`--model` and `/model` all win over it, and `EXPLAIN PLAYBOOK` says which
one decides. Example: [10](../../examples/10-tools-statusline-model/).

## Skills

```
ADD SKILL <name> FROM '<dir>'                                   -- linked
ADD SKILL <name> FROM '<git source>' [BRANCH <ref>] [SUBDIR <dir>]   -- copied
DROP SKILL <name>
```

A directory is linked, so edits reach the next session; a git source
(`github:<owner>/<repo>`, `https://…`, `git@…`) is cloned and copied, and
`cpb update <playbook>` refreshes it. cpb records each skill it adds, and
`DROP SKILL` removes only those. Example: [11](../../examples/11-skills/).

## Apply one configuration to many places

Leave the playbook name out and the file becomes a recipe; choose the target
when you apply it:

```bash
cpb APPLY agent.cpb TO reviewer                # a playbook, created bare if missing
cpb APPLY agent.cpb TO '~/.claude' --dry-run   # the plain Claude Code config directory
cpb APPLY agent.cpb TO '~/.claude' --yes
```

A plain config directory takes Claude Code's own configuration only: plugins,
the agent, tools, status line, model, skills, MCP servers without
credentials, and `SET VAR` into its `settings.json` `env`. cpb backs up
`settings.json` (and `.claude.json` before an MCP change) once per run and
asks before writing. Example: [12](../../examples/12-recipes-and-targets/).

## See what you have

```bash
cpb EXPLAIN PLAYBOOK reviewer              # plugins, agent, servers, tools, model and where each comes from
cpb SHOW CREATE PLAYBOOK reviewer          # the clauses back, references as FROM '<ref>'
cpb "SELECT name, model, tools FROM PLAYBOOKS"
```

Every clause, rule and limit is in the reference:
[An agent's configuration](../reference/cli-grammar.md#an-agents-configuration-v3210).
