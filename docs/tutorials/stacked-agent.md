# Stack layers into an agent

A playbook file can `INCLUDE` another, so a setup can be built in layers,
each one a small file that adds to the one below. This tutorial walks
[example 08](../../examples/08-kommander-agent/), which builds a Kommander
agent in three.

## The layers

**bare.cpb** makes the playbook and gives it a route:

```
CREATE PLAYBOOK IF NOT EXISTS kommander-agent;
ALTER PLAYBOOK kommander-agent USE ENV glm-5.3-flash;
```

**kommander.cpb** includes bare and makes Kommander the main thread:

```
INCLUDE 'bare.cpb';
ALTER PLAYBOOK kommander-agent
  ADD MARKETPLACE kommander FROM 'github:ramazanpolat/kommander-playbook'
  ADD PLUGIN kommander@kommander
  SET AGENT 'kommander';
```

`ADD MARKETPLACE` and `ADD PLUGIN` run `claude plugin marketplace add` and
`claude plugin install` with the playbook as `CLAUDE_CONFIG_DIR`: Claude Code
installs the plugin for this playbook only. `SET AGENT` pins the agent in the
playbook's `settings.json`, so its prompt is the session's system prompt.

**chaos.cpb** includes kommander and adds a layer on top:

```
INCLUDE 'kommander.cpb';
ALTER PLAYBOOK kommander-agent
  ADD MARKETPLACE chaos-stub FROM './chaos-stub'
  ADD PLUGIN chaos@chaos-stub;
```

`'./chaos-stub'` is a directory beside the file. Its plugin adds session
context through a hook. It sets no agent, so Kommander stays the main thread
and the layer's context stacks on top.

## Apply the top layer

```bash
cpb APPLY chaos.cpb --dry-run   # every statement, and every `claude plugin` command
cpb APPLY chaos.cpb             # bare, then kommander, then chaos
kommander-agent                 # run it
cpb EXPLAIN PLAYBOOK kommander-agent
```

A file included twice runs once, and applying again changes nothing: cpb reads
the playbook's plugin state first and runs only what is missing. A plugin that
wants to run a command its marketplace declares is never accepted for you; the
statement fails and shows the command to review.

## Your own layer

Copy chaos.cpb, point `ADD MARKETPLACE` at your plugin (a directory while you
develop it, `github:<owner>/<repo>` once published), and apply your file. The
layers below it come along.
