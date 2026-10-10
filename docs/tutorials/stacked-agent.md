# Stack layers into an agent

A playbook file can `INCLUDE` another, so a setup can be built in layers,
each one a small file that adds to the one below. Written as **recipes**
(an `ALTER PLAYBOOK` that names no playbook), the same layers build the agent
under any name. This tutorial walks
[example 08](../../examples/08-stacked-agent/), which builds a reviewer agent
in three layers and one entry file. Everything it uses is in the repository.

## The layers

**base.cpb** gives the playbook a model and two tool permissions:

```
ALTER PLAYBOOK
  SET model = 'claude-sonnet-5-5'
  ALLOW TOOL 'Bash(git diff *)' 'Bash(git log *)';
```

`ALTER PLAYBOOK` followed straight by a clause names no playbook: its target
is decided when the file is applied.

**agent.cpb** includes base and makes the reviewer the main thread:

```
INCLUDE 'base.cpb';
ALTER PLAYBOOK
  ADD MARKETPLACE reviewers FROM './reviewer-marketplace'
  ADD PLUGIN reviewer@reviewers
  SET agent = 'reviewer'
  SET IF UNSET statusline = 'echo reviewer', statusline_refresh = 10;
```

`ADD MARKETPLACE` and `ADD PLUGIN` run `claude plugin marketplace add` and
`claude plugin install` with the playbook as `CLAUDE_CONFIG_DIR`: Claude Code
installs the plugin for this playbook only. `'./reviewer-marketplace'` is a
directory beside the file. `SET AGENT` pins the agent in the playbook's
`settings.json`, so its prompt is the session's system prompt. A plugin can
set neither permissions nor a status line, so the playbook does, and
`IF UNSET` offers the status line without replacing one you chose.

**team.cpb** includes agent and adds a layer on top:

```
INCLUDE 'agent.cpb';
ALTER PLAYBOOK
  ADD MARKETPLACE team-rules FROM './team-marketplace'
  ADD PLUGIN team@team-rules;
```

Its plugin adds session context through a hook. It sets no agent, so the
reviewer stays the main thread and the team's context stacks on top.

## Pick the target

**playbook.cpb** names the playbook the layers build:

```
USE PLAYBOOK reviewer-agent;
INCLUDE 'team.cpb';
```

`USE PLAYBOOK` sets the target for the name-less statements after it,
including those in the files it includes. A target that does not exist is
created bare first, with its name as its launcher.

## Apply it

```bash
cpb APPLY playbook.cpb --dry-run      # every statement, and every `claude plugin` command
cpb APPLY playbook.cpb                # base, then agent, then team
reviewer-agent                        # run it
cpb EXPLAIN PLAYBOOK reviewer-agent
cpb APPLY team.cpb TO reviewer-lab    # the same agent, under another name
```

A file included twice runs once per target, and applying again changes
nothing: cpb reads the playbook's plugin, settings and skill state first and
runs only what is missing. A layer may set a key a layer below it already
set, such as the model or a variable: only the last value is written, and
the plan says which file wins. A plugin that wants to run a command its
marketplace declares is never accepted for you; the statement fails and shows
the command to review.

## When a layer changes

`APPLY` records, in each playbook it gave name-less statements, the files it
applied and what they wrote (`.apply/recipe.cpb`, references only). So the
playbook can follow its layers:

```bash
cpb update reviewer-agent --dry-run   # what changed in the layers, and the plan
cpb update reviewer-agent             # apply the layers again
```

A clause a layer no longer has is removed. Drop `'Bash(git log *)'` from
base.cpb, and each playbook built on it loses the rule at its next
`cpb update`. The status line `IF UNSET` offered is removed only where the
layers wrote it, never one you chose.

## Your own layer

Copy team.cpb, point `ADD MARKETPLACE` at your plugin (a directory while you
develop it, `github:<owner>/<repo>` once published), and apply it with
`TO <playbook>` or from a file with a `USE PLAYBOOK` line. The layers below it
come along. A layer can also add MCP servers, skills, tool rules and a model:
see [examples 09 to 11](../../examples/).
