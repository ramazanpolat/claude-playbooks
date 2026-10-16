# 08 — an agent built from stacked recipes

Three layer files and one entry file build one playbook, `reviewer-agent`.
The layers are **recipes**: their `ALTER PLAYBOOK` names no playbook, so the
same files build the agent under any name.

| File | Layer | What it adds |
|---|---|---|
| `base.cpb` | base | the model and two tool permissions |
| `agent.cpb` | agent | `INCLUDE 'base.cpb'`, the local `reviewers` marketplace and its plugin, the `reviewer` agent as the main thread, and a status line offered `IF UNSET` |
| `team.cpb` | team | `INCLUDE 'agent.cpb'`, and the local `team-rules` marketplace, whose plugin adds one line of session context |
| `playbook.cpb` | entry | `USE PLAYBOOK reviewer-agent` and `INCLUDE 'team.cpb'` |

```
cpb APPLY playbook.cpb --dry-run      # what would run, including every `claude plugin` command
cpb APPLY playbook.cpb                # build it (reviewer-agent is created bare first)
reviewer-agent                        # run it (or: cpb run reviewer-agent)
cpb EXPLAIN PLAYBOOK reviewer-agent   # the plugins and where the agent comes from

cpb APPLY team.cpb TO reviewer-lab    # the same layers, another playbook
```

Applying a layer applies everything under it, and applying again changes
nothing. Both marketplaces are directories beside the files:
`'./reviewer-marketplace'` resolves against the directory of `agent.cpb`.

## What the layers do at launch

- The playbook's `settings.json` pins `agent: reviewer` (`SET AGENT`), so the
  reviewer's prompt is the main thread's system prompt.
- `ALLOW TOOL` lets the agent run `git diff` and `git log` without asking.
  A plugin cannot grant permissions, so the playbook does.
- `SET IF UNSET statusline.command = '…'` offers a status line without replacing one you
  set yourself ([example 17](../17-statusline-if-unset/)).
- The team plugin's SessionStart hook adds its line of context. It sets no
  agent, so the reviewer stays the main thread and both layers reach the
  model: a reply starts with `Review:` and ends with `[team layer]`.

## Your own layer

Copy `team.cpb`, point `ADD MARKETPLACE` at your plugin (a directory while you
develop it, `github:<owner>/<repo>` once published), and apply it `TO` a
playbook, or from a file with a `USE PLAYBOOK` line. The layers below it come
along. The [stacking tutorial](../../docs/tutorials/stacked-agent.md) walks
these files one by one.
