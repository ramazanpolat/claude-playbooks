# 08 — the Kommander agent, built from stacked playbook files

Three playbook files build one playbook, `kommander-agent`, layer by layer:

| File | Layer | What it adds |
|---|---|---|
| `bare.cpb` | bare | the playbook and its model route |
| `kommander.cpb` | kommander | `INCLUDE 'bare.cpb'`, the kommander marketplace and plugin, and the `kommander` agent as the main thread |
| `chaos.cpb` | chaos (stub) | `INCLUDE 'kommander.cpb'`, and `chaos-stub/`: a local marketplace whose plugin adds one line of session context |

```
cpb APPLY chaos.cpb --dry-run     # what would run, including every `claude plugin` command
cpb APPLY chaos.cpb               # build it
kommander-agent                   # run it (or: cpb run kommander-agent)
cpb EXPLAIN PLAYBOOK kommander-agent
```

Applying a layer applies everything under it. Applying again changes nothing.

## Before you apply

- **The route.** `bare.cpb` attaches the env set `glm-5.3-flash`. Use one you
  have (`cpb SHOW ENVS`), or remove that line to run on the machine's
  `DEFAULTS`.
- **The kommander plugin.** `kommander.cpb` installs it from
  `github:ramazanpolat/kommander-playbook`, whose root is both the
  marketplace and the plugin (so `APPLY` fetches it). To run a local checkout
  instead, a branch under development, write
  `ADD MARKETPLACE kommander FROM '~/DEV/kommander-playbook'`. A playbook
  that already has the marketplace from another source keeps it until you
  `DROP PLUGIN kommander@kommander DROP MARKETPLACE kommander`.
- **The chaos layer** is a stub. `'./chaos-stub'` resolves against the
  directory of `chaos.cpb`. The real layer adds its own marketplace and
  plugin in the same two clauses.

## What the layers do at launch

- The playbook's `settings.json` pins `agent: kommander` (`SET AGENT`), so the
  kommander agent's prompt is the main thread's system prompt.
- The chaos plugin's SessionStart hook adds its line of context on top. It
  does not set an agent, so Kommander stays the main thread.
- `EXPLAIN PLAYBOOK kommander-agent` shows both plugins and where the agent
  comes from.

## Proof run (2026-09-26, macminim, GLM via 9router)

A cpb build of this branch, against the machine's real registry:

```
$ cpb APPLY chaos.cpb --dry-run
…/bare.cpb:5      created   CREATE PLAYBOOK kommander-agent
…/bare.cpb:6      changed   ALTER PLAYBOOK kommander-agent
…/kommander.cpb:7 changed   ALTER PLAYBOOK kommander-agent  (would run: claude plugin marketplace add ramazanpolat/kommander-playbook --scope user; claude plugin install kommander@kommander --scope user --json)
chaos.cpb:9       changed   ALTER PLAYBOOK kommander-agent  (would run: claude plugin marketplace add …/08-kommander-agent/chaos-stub --scope user; claude plugin install chaos@chaos-stub --scope user --json)
Would apply chaos.cpb: 1 created, 3 changed, 0 unchanged, 0 dropped

$ cpb APPLY chaos.cpb
…
Altered PLAYBOOK kommander-agent
  marketplace kommander from github:ramazanpolat/kommander-playbook
  plugin    kommander@kommander
  agent     kommander
Altered PLAYBOOK kommander-agent
  marketplace chaos-stub from …/08-kommander-agent/chaos-stub
  plugin    chaos@chaos-stub
Applied chaos.cpb: 1 created, 3 changed, 0 unchanged, 0 dropped

$ kommander-agent -p "Hello. Who are you, and what do you need from me to start? Keep it short."
Kommander — the main-thread agent of the `kommander-agent` playbook, running on GLM (glm-5.3-flash via 9router). I handle engineering work with task tracking: task folders, locks, session logs.

All I need from you: either describe a task you want to work on (I'll create and lock it), or say "no task" for a quick untracked session.

[chaos layer]

$ cpb APPLY chaos.cpb
Applied chaos.cpb: 0 created, 0 changed, 4 unchanged, 0 dropped

$ cpb EXPLAIN PLAYBOOK kommander-agent      (variables omitted)
Secret helper: (none)
Plugins: chaos@chaos-stub, kommander@kommander
Agent: kommander (playbook settings)
```

The last line of the answer, `[chaos layer]`, is the chaos stub's instruction
being followed: both layers reached the model. Claude Code also prints a
`[claude-code:unrecognized_model]` notice for the GLM model name; it is
harmless.

## Known gaps (first cut)

- **statusLine.** Kommander's status line is part of a full Kommander install
  (`settings.json` `statusLine`), and cpb has no clause that sets it yet.
- **The `kommander-helper` permission.** The Kommander agent runs its helper
  through Bash. A full install allows it in `settings.json`
  (`permissions.allow`); cpb has no clause for permissions yet, so a session
  asks the first time, or it is added by hand.
- **Updates.** The plugin loads from the checkout in place: `git pull` there
  and start a new session. A published marketplace updates through
  `claude plugin marketplace update`.
