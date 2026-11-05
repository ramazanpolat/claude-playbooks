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
playbook on an Anthropic account that signs in with a Claude token asks with
that token. A playbook on an account that is not Anthropic's, or on one that
signs in with a key or a token of another kind, is refused: ask without
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
  cpb CREATE PLAYBOOK route-a-tmp SET login = 'isolated' USE ACCOUNT route-a
      Creates a playbook with a login of its own that runs on account route-a.
  (cd /tmp && cpb run route-a-tmp)
      Starts it from /tmp.
      note: This launch sends its requests away from Anthropic, with the CLAUDE.md files ... Run it from a directory outside home, as shown, so none of yours go along.
  cpb DROP PLAYBOOK route-a-tmp
      ! deletes the playbook when you are done
```

`route-a` is an account (`cpb SHOW ACCOUNTS`) holding the provider's address
and key. A playbook on an account that uses no Claude login needs a login of
its own, so the answer creates one rather than a `start` directory, which
shares this machine's login and would be refused on that account.

When a launch would send its requests away from Anthropic, cpb shows it run
from `/tmp`. A Claude Code session takes the `CLAUDE.md` files of the
directory it starts in and of every directory above it, so a session started
in a project under your home would send that project's `CLAUDE.md`,
`~/CLAUDE.md` and `~/.claude/CLAUDE.md` to that endpoint. When you ask from
such a directory, the answer names the files.

A `start` directory shares this machine's login. Do not run `/login` inside
it: that replaces the machine's login. For a route you use often, keep the
playbook: `CREATE PLAYBOOK <name> SET login = 'isolated' USE ACCOUNT <account>`
([example 15](../../examples/15-third-party-route/)).

## What is sent, and to whom

The question goes to Anthropic's API through your own Claude Code (`claude
-p`) and its login, whatever your shell exports and whichever accounts your
playbooks run on. cpb holds no key and reads no credential.

It sends the request and the names of what is installed: playbooks,
launchers, env sets, `DEFAULTS`, and for each playbook (by the account it
runs on) and each env set whether it routes to Anthropic or away. It never sends a variable's value, a secret
reference, an env set's description, or a credential.

The model has one tool, and it is cpb, read-only: it can run `SHOW` and
`EXPLAIN` to look things up, such as how many sessions run or which plugins a
playbook has. What the tool returns is cut down to names, routes, versions,
and for sessions their count, kind, status, age and model. It never returns a
value, a path, a session's folder or title, or a credential, and it cannot
change anything. It does not even look at a credential: it never reads, or
checks for, the token file, a login store or the Keychain. So it cannot say
whether a playbook signs in with a token or a stored login, only whether its
`login` is `shared` or `isolated`. Claude Code is started
with no user settings, no proxy, no `~/.claude/CLAUDE.md`, no MCP server and
no tools, from a fresh directory outside your home, so the model sees nothing
else and can read nothing itself. Claude Code adds its own environment block:
that directory's path, the platform and the OS version.

If Claude Code is not logged in, or its login has hit a usage limit, `cpb
ask` says so and stops: run `claude` and `/login`, or set
`ANTHROPIC_API_KEY`. When you have Claude token accounts, it names them
too. It never asks another endpoint unless you say so with
`--allow-other-provider` ([below](#asking-another-provider)).

## Asking on another account

When your Claude login has hit its limit, ask on one of your accounts:

```bash
cpb ask --account work-claude "list my env sets"
```

The account must be an Anthropic one that signs in with a Claude token
(`CREATE ACCOUNT work-claude SET provider = anthropic, claude_token FROM
keychain:work-claude-token`) or a Claude login. Its token goes to that one
`claude` call, in its environment, with a new, empty config directory, as
`CPB_ASK_LOGIN=token` does. A token stored by reference is resolved by your
secret helper, which starts `claude` with it, so cpb never sees the value.
The helper runs with the same short environment as that `claude` call, so
an `env:` reference, or a backend that needs a variable of its own, does not
resolve here, though it does at launch.
With a playbook named, `cpb ask work --account work-claude "…"`, the question
signs in as `cpb run --account work-claude work` would: the account must be
one that playbook may run on, and a Claude login account uses the
playbook's own login, or the machine's for a playbook that shares it. An account on another provider, or
with request headers of its own, is refused before anything is sent; for a
gateway account, the refusal says how to ask there anyway.

## Asking another provider

A gateway account, such as one for a model behind your own gateway, is
refused by default, because the question would leave Anthropic:

```bash
cpb ask --account glm "list my env sets"
# Error: account glm sends its requests away from Anthropic (provider gateway),
# and cpb ask asks Anthropic only: nothing was sent. To ask there anyway, add
# --allow-other-provider: ...
```

Add `--allow-other-provider` to send it there anyway:

```bash
cpb ask --account glm --allow-other-provider "list my env sets"
cpb ask glm-work --allow-other-provider "how many of your sessions are live?"
```

The question then goes to the account's address, signed in with the
account's own key or token and on its own `model`, as a launch on it would.
What goes there is what every ask sends: the request, your `.ask/CLAUDE.md`
and history, and the names of what is installed. Before sending, cpb says
where on stderr (`asking account glm's gateway at gw.example.net, not
Anthropic`). Your Claude login and tokens never go along: `claude` starts
with a new, empty config directory and no Anthropic key or Claude token, and
an account that would send your Claude credential to the gateway
(`claude_login_trusted`) is refused, with the flag or without. Bedrock,
Vertex and Foundry accounts are refused too.

How well another model answers is up to it. Its answer is checked like any
other: a step cpb would refuse is marked as rejected, with the reason.

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

## Leave secrets out of the request

The request goes to the model as you type it, so `cpb ask` checks it first.
A request that holds something shaped like a credential, such as
`ANTHROPIC_AUTH_TOKEN=sk-...`, a GitHub token or a URL with a password, is
refused before anything is sent, naming what was found and never the value:

```
Error: the request looks like it holds a credential (ANTHROPIC_AUTH_TOKEN=...): nothing was sent. Leave the value out and name it instead, e.g. <token>
```

Name a key instead (`set the router account's key from my keychain`): the
answer then uses a reference, `ALTER ACCOUNT router SET auth_token FROM
'<secret-ref>'`.

## Follow-ups and your own context

`cpb ask` remembers what it answered (since v4.5.0), so you can correct it
or point back:

```
cpb ask "list my env sets"
cpb ask "I was asking for accounts"
cpb ask "the router one from yesterday, again"
```

Everything it keeps is under `~/.claude-playbooks/.ask/`:

- `history.md`: one record per answered ask, newest at the bottom, each
  headed by its time, the playbook you asked (`-` for none) and an id:

  ```
  ## 2026-11-02T11:39:12+03:00 · - · r-20261102-113912-3f2a

  **Asked:** list my env sets
  ```

  It is appended to and never rewritten. Read it with `cpb SHOW ASK
  HISTORY` (below) or any tool; delete it to forget, and cpb starts a new
  one. Each ask sends the last 20 headings
  and the last record; the model reads further back only when your request
  points there. A refused ask is never recorded, and a part that looks like
  a credential is written as withheld.
- `CLAUDE.md`: write it yourself, in your own words: what you call things,
  how you like answers. It goes with every ask. cpb never writes it, and one
  that holds a credential refuses the ask.

`--no-history` keeps one ask out: it reads no history and adds nothing.

To look back yourself:

```
cpb SHOW ASK HISTORY                                   # the last 20, newest first
cpb SHOW ASK HISTORY FOR PLAYBOOK work LIMIT 50        # the asks addressed to work
cpb SHOW ASK RECORD r-20261102-113912-3f2a             # one in full
```

`SHOW ASK HISTORY` prints `TIME PLAYBOOK ID ASKED`, and when there is more
a last line with the statement that lists the older ones (`BEFORE <id>`).
Both take `--json`. The model reads the history with the same two
statements, through its read-only tool.

## Choosing the model

`--model <model>`, or `CPB_ASK_MODEL`, else `sonnet`. Any Claude model your
login can use works; each question is one call. On a gateway account
(`--allow-other-provider`), the account's own `model` comes before
`CPB_ASK_MODEL`.

## For scripts

`--json` prints the checked plan, every step with its `status`, `guards` and
notes. Exit codes: 0 shown, 1 refused or no answer, 2 a usage error.

An agent that drives cpb should use the grammar directly ([Agent
guide](agent-guide.md)): `cpb ask` spends a model call on your login.
