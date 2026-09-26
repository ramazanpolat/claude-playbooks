# Stack layers into an agent

A playbook file can `INCLUDE` another, so a setup can be built in layers,
each one a small file that adds to the one below. Written as **recipes**
(an `ALTER PLAYBOOK` that names no playbook), the same layers build the agent
under any name. This tutorial walks
[example 08](../../examples/08-kommander-agent/), which builds a Kommander
agent in three layers and one entry file.

## The layers

**bare.cpb** gives the playbook a route:

```
ALTER PLAYBOOK USE ENV glm-5.3-flash;
```

`ALTER PLAYBOOK` followed straight by a clause names no playbook: its target
is decided when the file is applied.

**kommander.cpb** includes bare and makes Kommander the main thread:

```
INCLUDE 'bare.cpb';
ALTER PLAYBOOK
  ADD MARKETPLACE kommander FROM '~/path/to/kommander-playbook'
  ADD PLUGIN kommander@kommander
  SET AGENT 'kommander'
  ALLOW TOOL 'Bash(kommander-helper *)'
  SET STATUSLINE 'bash ~/path/to/kommander-playbook/hooks/statusline.sh';
```

The kommander repository is private: the path is your checkout of it, and
with access the same clause can read
`FROM 'github:ramazanpolat/kommander-playbook'`. Agent Kommander is moving to
its own repository, `agent-kommander`, and the example will follow.

`ADD MARKETPLACE` and `ADD PLUGIN` run `claude plugin marketplace add` and
`claude plugin install` with the playbook as `CLAUDE_CONFIG_DIR`: Claude Code
installs the plugin for this playbook only. `SET AGENT` pins the agent in the
playbook's `settings.json`, so its prompt is the session's system prompt. A
plugin can set neither permissions nor a status line, so the playbook does:
`ALLOW TOOL` lets the agent run its helper without asking, and
`SET STATUSLINE` shows Kommander's task and lock state.

**chaos.cpb** includes kommander and adds a layer on top:

```
INCLUDE 'kommander.cpb';
ALTER PLAYBOOK
  ADD MARKETPLACE chaos-stub FROM './chaos-stub'
  ADD PLUGIN chaos@chaos-stub;
```

`'./chaos-stub'` is a directory beside the file. Its plugin adds session
context through a hook. It sets no agent, so Kommander stays the main thread
and the layer's context stacks on top.

## Pick the target

**kommander-agent.cpb** names the playbook the layers build:

```
USE PLAYBOOK kommander-agent;
INCLUDE 'chaos.cpb';
```

`USE PLAYBOOK` sets the target for the name-less statements after it,
including those in the files it includes. A target that does not exist is
created bare first, with its name as its launcher.

## Apply it

```bash
cpb APPLY kommander-agent.cpb --dry-run   # every statement, and every `claude plugin` command
cpb APPLY kommander-agent.cpb             # bare, then kommander, then chaos
kommander-agent                           # run it
cpb EXPLAIN PLAYBOOK kommander-agent
cpb APPLY chaos.cpb TO kommander-lab      # the same agent, under another name
```

A file included twice runs once per target, and applying again changes
nothing: cpb reads the playbook's plugin, settings and skill state first and
runs only what is missing. A plugin that wants to run a command its
marketplace declares is never accepted for you; the statement fails and shows
the command to review.

## Your own layer

Copy chaos.cpb, point `ADD MARKETPLACE` at your plugin (a directory while you
develop it, `github:<owner>/<repo>` once published), and apply it with
`TO <playbook>` or from a file with a `USE PLAYBOOK` line. The layers below it
come along. A layer can also add MCP servers, skills, tool rules and a model:
see [examples 09 to 11](../../examples/).
