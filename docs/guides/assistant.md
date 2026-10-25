# Ask in plain words

`cpb agent` turns a request in plain words into the cpb command that does it.
You do not need to remember the grammar or read `cpb --help`, and you do not
need to start a separate agent to drive cpb.

```bash
cpb agent "list the env profiles"
```

```
Your env sets.

  cpb SHOW ENVS
      Lists the env sets (v3 called them env profiles).
```

It shows the plan and stops. Copy the line, or let cpb run it for you with
`--run`. The contract is [SPEC.md, "cpb agent"](../../SPEC.md).

## Show, or run

```bash
cpb agent "attach env set router to playbook work"          # shows the plan
cpb agent --run "attach env set router to playbook work"    # shows it, asks, runs it
```

`--run` prints the plan exactly as it will run and asks `Run it? [y/N]`.
Steps run in order, each as `cpb` with its arguments, never through a shell;
the first that fails stops the rest.

Some steps always ask, even with `--yes`, and never run without a terminal.
cpb marks them with `!` and says what they do:

- **they remove, forget or replace something:** `DROP PLAYBOOK`, `DROP ENV`,
  `UNSET`, `BLOCK`, `RESET`, `RENAME TO`, `CREATE OR REPLACE ENV`, `APPLY`,
  `start --delete`;
- **they touch a credential or the login:** a token or key, a secret
  reference, `login`, the secret helper;
- **they fetch code:** `CREATE PLAYBOOK … FROM`, `ADD MARKETPLACE`,
  `ADD PLUGIN`, `ADD SKILL`, `ADD MCP SERVER`, `play`, `update`. The source is
  shown as written: check it, since a model can propose a repository that only
  looks right.

`--yes` skips the question for a plan with no `!` step, for scripts.

A step cpb cannot run as proposed is shown and never runs: a placeholder such
as `<secret-ref>` you must fill in first, or a step marked `not proposed`
(another program, a statement that does not parse, a playbook that does not
exist). With such a step in the plan, `--run` runs nothing.

## A session on another model

```bash
cpb agent "start a throwaway session on route-a"
```

```
  (cd /tmp && cpb start --env-set route-a --delete /tmp/cpb-route-a)
      Starts Claude Code in a new config directory with env set route-a, deleted when it ends.
      ! deletes /tmp/cpb-route-a when the session ends
      note: This launch sends its requests away from Anthropic. ...
      note: The new directory shares this machine's login: a /login inside it replaces the machine's login.
```

When a launch would send its requests away from Anthropic, cpb runs it from a
new directory outside your home. A Claude Code session takes the `CLAUDE.md`
files of the directory it starts in and of every directory above it, so a
session started in a project under your home would send that project's
`CLAUDE.md`, `~/CLAUDE.md` and `~/.claude/CLAUDE.md` to that endpoint. When you
ask from such a directory, the plan names the files.

A `start` directory shares this machine's login. Do not run `/login` inside
it: that replaces the machine's login. For a route you use often, make a
playbook with its own login: `CREATE PLAYBOOK <name> SET login = 'isolated'`
([example 15](../../examples/15-third-party-route/)).

## What is sent, and to whom

The question goes to Anthropic's API through your own Claude Code (`claude
-p`) and its login, whatever your shell, your env sets or your `DEFAULTS`
route other launches to. cpb holds no key and reads no credential.

It sends the request and the names of what is installed: playbooks,
launchers, env sets, `DEFAULTS`, and for each playbook and env set whether it
routes to Anthropic or away. It never sends a variable's value, a secret
reference, an env set's description, or a credential. Claude Code is started
with no user settings, no proxy, no `~/.claude/CLAUDE.md`, no MCP server and no tools,
from a fresh directory outside your home, so the model sees nothing else and
can read nothing itself. Claude Code adds its own environment block: that
directory's path, the platform and the OS version.

If Claude Code is not logged in, `cpb agent` says so and stops: run `claude`
and `/login`, or set `ANTHROPIC_API_KEY`. It never asks another endpoint.

## Signing in with cpb's token

If your playbooks sign in with a long-lived token (`claude setup-token`, see
[Authentication](authentication.md)) and plain `claude` is not logged in,
let `cpb agent` use that token:

```bash
export CPB_AGENT_LOGIN=token
```

cpb then reads its machine token when you ask, and gives it to that one
`claude` call in its environment, as every launch does, together with a new,
empty config directory, so a token that is refused cannot fall back to
another login. The token never goes into the command line, the question, a
file or cpb's output. Without a machine token, `cpb agent` stops and says so.
Unset, `cpb agent` uses Claude Code's own login and reads no credential.

## Behind a proxy

`cpb agent` does not run through a proxy yet: a proxy, with a certificate
authority added for it, could read the question and the credential sent
with it. When `HTTP_PROXY`, `HTTPS_PROXY` or `ALL_PROXY` is set, it stops
before anything is sent:

```
Error: cpb agent does not run through a proxy yet, and HTTPS_PROXY is set here: nothing was sent. If Anthropic's API is reachable without it, run: env -u HTTPS_PROXY cpb agent ...
```

If your network reaches Anthropic's API directly, run it without the
variable, as the message says. If it does not, `cpb agent` cannot be used
from that network for now; the grammar and `cpb --help` work as always.

## Choosing the model

`--model <model>`, or `CPB_AGENT_MODEL`, else `sonnet`. Any Claude model your
login can use works; each question is one call.

## For scripts

`--json` prints the checked plan, every step with its `status`, `guards` and
notes, and runs nothing. Exit codes: 0 shown or run, 1 refused or declined or
no answer, 2 a usage error; a failing step passes its own code.

An agent that drives cpb should use the grammar directly ([Agent
guide](agent-guide.md)): `cpb agent` spends a model call on your login.

## Two things called agent

`cpb agent` is this assistant. The `agent` **property**
(`ALTER PLAYBOOK work SET agent = 'reviewer'`) is something else: the agent a
playbook's main session runs as ([Stack layers into an agent](../tutorials/stacked-agent.md)).
