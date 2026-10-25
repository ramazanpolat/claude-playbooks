# Ask in plain words

`cpb ask` turns a request in plain words into the cpb command that does it.
You do not need to remember the grammar or read `cpb --help`, and you do not
need to start a separate agent to drive cpb.

```bash
cpb ask "list the env profiles"
```

```
Your env sets.

  cpb SHOW ENVS
      Lists the env sets (v3 called them env profiles).
```

It shows the command and stops: `cpb ask` never runs anything. Copy the line
you want and run it yourself. The contract is [SPEC.md, "cpb ask"](../../SPEC.md).

## Ask a playbook

Name a playbook, or its launcher, first, and the question is about it:

```bash
cpb ask work "how many of your sessions are live?"
```

```
work has 2 live sessions: one interactive, one in the background.

  cpb SHOW SESSIONS FOR PLAYBOOK work
      Lists them with their folders and the command that resumes each.
```

"You" in the request means that playbook. The question signs in the way a
launch of it would: with the machine's token, with the machine's login, or
with its own login when it has one (`SET login = 'isolated'`). Only its login
is used; its `CLAUDE.md`, settings, hooks and MCP servers stay out. A
playbook that sends its requests away from Anthropic is refused: ask without
naming it. Quote the request when you name a playbook.

## Reading the answer

Each step is one cpb command and one line saying what it does. cpb checks
every step before it shows it, and adds what you should know before you run
it, marked `!`:

- **it removes, forgets or replaces something:** `DROP PLAYBOOK`, `DROP ENV`,
  `UNSET`, `BLOCK`, `RESET`, `RENAME TO`, `CREATE OR REPLACE ENV`, `APPLY`,
  `start --delete`;
- **it touches a credential or the login:** a token or key, a secret
  reference, `login`, the secret helper;
- **it fetches code:** `CREATE PLAYBOOK … FROM`, `ADD MARKETPLACE`,
  `ADD PLUGIN`, `ADD SKILL`, `ADD MCP SERVER`, `play`, `update`. The source is
  shown as written: check it, since a model can propose a repository that only
  looks right.

A step with a placeholder such as `<secret-ref>` needs you to fill it in. A
step marked `not proposed` is one cpb would not suggest: another program, a
statement that does not parse, a playbook or env set that does not exist.

## A session on another model

```bash
cpb ask "create an ephemeral playbook configured to run with route-a's keys"
```

```
  (cd /tmp && cpb start --env-set route-a --delete /tmp/cpb-route-a)
      Starts Claude Code in a new config directory with env set route-a, deleted when it ends.
      ! deletes /tmp/cpb-route-a when the session ends
      note: This launch sends its requests away from Anthropic, with the CLAUDE.md files ... Run it from a directory outside home, as shown, so none of yours go along.
      note: The new directory shares this machine's login: a /login inside it replaces the machine's login.
```

When a launch would send its requests away from Anthropic, cpb shows it run
from `/tmp`. A Claude Code session takes the `CLAUDE.md` files of the
directory it starts in and of every directory above it, so a session started
in a project under your home would send that project's `CLAUDE.md`,
`~/CLAUDE.md` and `~/.claude/CLAUDE.md` to that endpoint. When you ask from
such a directory, the answer names the files.

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
reference, an env set's description, or a credential.

The model has one tool, and it is cpb, read-only: it can run `SHOW` and
`EXPLAIN` to look things up, such as how many sessions run or which plugins a
playbook has. What the tool returns is cut down to names, routes, versions,
and for sessions their count, kind, status, age and model. It never returns a
value, a path, a session's folder or title, or a credential, and it cannot
change anything. Claude Code is started
with no user settings, no proxy, no `~/.claude/CLAUDE.md`, no MCP server and
no tools, from a fresh directory outside your home, so the model sees nothing
else and can read nothing itself. Claude Code adds its own environment block:
that directory's path, the platform and the OS version.

If Claude Code is not logged in, `cpb ask` says so and stops: run `claude`
and `/login`, or set `ANTHROPIC_API_KEY`. It never asks another endpoint.

## Signing in with cpb's token

If your playbooks sign in with a long-lived token (`claude setup-token`, see
[Authentication](authentication.md)) and plain `claude` is not logged in,
let `cpb ask` use that token:

```bash
export CPB_ASK_LOGIN=token
```

cpb then reads its machine token when you ask, and gives it to that one
`claude` call in its environment, as every launch does, together with a new,
empty config directory, so a token that is refused cannot fall back to
another login. The token never goes into the command line, the question, a
file or cpb's output. Without a machine token, `cpb ask` stops and says so.
Unset, `cpb ask` uses Claude Code's own login and reads no credential.

## Behind a proxy

`cpb ask` does not run through a proxy yet: a proxy, with a certificate
authority added for it, could read the question and the credential sent
with it. When `HTTP_PROXY`, `HTTPS_PROXY` or `ALL_PROXY` is set, it stops
before anything is sent:

```
Error: cpb ask does not run through a proxy yet, and HTTPS_PROXY is set here: nothing was sent. If Anthropic's API is reachable without it, run: env -u HTTPS_PROXY cpb ask ...
```

If your network reaches Anthropic's API directly, run it without the
variable, as the message says. If it does not, `cpb ask` cannot be used
from that network for now; the grammar and `cpb --help` work as always.

## Choosing the model

`--model <model>`, or `CPB_ASK_MODEL`, else `sonnet`. Any Claude model your
login can use works; each question is one call.

## For scripts

`--json` prints the checked plan, every step with its `status`, `guards` and
notes. Exit codes: 0 shown, 1 refused or no answer, 2 a usage error.

An agent that drives cpb should use the grammar directly ([Agent
guide](agent-guide.md)): `cpb ask` spends a model call on your login.
