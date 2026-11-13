# Account failover

A session that hits a usage limit stops. You would `/exit` and run it again
on another account: `cpb run --account <other> <playbook> --resume <id>`.
With failover on, the launch that started the session does that itself, in
the same terminal and folder.

## Turn it on

```
cpb ALTER DEFAULTS SET failover = 'on', failover_accounts = ['claude-work', 'claude-home']
cpb EXPLAIN PLAYBOOK reviewer
...
Failover: on (DEFAULTS); at a usage limit, the session moves to claude-work, claude-home
```

- **For every playbook** that does not say otherwise: `ALTER DEFAULTS SET
  failover = 'on'`.
- **For one playbook**: `ALTER PLAYBOOK <name> SET failover = 'on'` or
  `'off'`. `'inherited'`, the default, follows DEFAULTS.
- **For one launch**: `cpb run --failover <name>` or `--no-failover`.
- **The accounts** are DEFAULTS' `failover_accounts`, in the order you list
  them.
  - Only the ones the playbook may run on are used: its `USE ACCOUNT` list,
    or any account with `access = 'any'`.
  - A Claude login or token moves only to another Claude login or token, and
    a gateway only to a gateway at the same address. To cross, list the
    account as
    `'router (any provider)'`.

## When it moves a session

The launch looks at its own session every 10 seconds. It moves it only when
all of these hold:
- Claude Code says the session is idle: no turn running, no background
  shell, agent or monitor;
- its last answer is Claude Code's usage-limit error, hit since this launch
  started, and the limit resets more than 30 minutes from now. A sooner
  reset is left to Claude Code, which continues by itself at the reset;
- nothing has been written to the session for two minutes;
- a resume of it would work: its folder is there and it runs nowhere else.

Just before it acts, cpb checks all of it once more.

Then cpb:
1. ends that claude. Your playbook's SessionEnd hook runs as on any exit;
2. starts the same launch on the next account with `--resume <id>`, from the
   folder the session ran in;
3. sends Claude Code's own continue prompt, after a first line that says
   what moved:

```
[cpb: this session moved from account claude-work (weekly limit, resets Fri 13 Nov 16:00) to claude-home; its context, about 312,000 tokens, is read again on claude-home with no cache.]
```

The prompt the limit refused is still in the conversation, so the model picks
it up.

## What to know

- **Never twice.** Each account is tried once per episode, and at most three
  switches happen in five hours. When none is left, cpb says so and leaves
  the session to wait for its reset.
- **Cost.** The new account reads the whole context again, with no cache.
  `ALTER DEFAULTS SET failover_max_context = 400000` stops a switch above that
  size. There is no cap by default.
- **Unsent text.** Text typed into the input box and not sent is lost when
  the session moves.
- **Flags.** A resume keeps the permission mode, effort, agent, system
  prompts and settings, and the session's model. A launch that gives claude
  any other flag, such as a tool list, `-p` or `--add-dir`, is not moved: cpb
  says so when it starts, and `--failover` with such a flag is refused.
- **If the new account fails.** When the launch on the next account never
  runs the session (its helper fails, say), cpb resumes it on the account
  it came from, and the summary says it did not move. If that fails too,
  cpb says nothing runs the session and prints the command that resumes it
  by hand.
- **Only its own sessions.** A launch watches only the session it started.
  A session started some other way is never touched.
- **What you see.** A line on stderr when a session moves, a summary when the
  launch ends, and the first line of the prompt in the session. `cpb SHOW
  SESSIONS` shows the new account.
- **Without a restart.** An account whose credential is a `helper` already
  gets a fresh key from the helper while a session runs. A helper that
  chooses the key can rotate keys of one provider address on its own. A
  Claude login or token needs the restart above.

Reference: [Account failover](../../SPEC.md#account-failover). Example:
[27 — account failover](../../examples/27-account-failover/).
