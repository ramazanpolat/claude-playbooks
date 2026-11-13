# See how much of each account is used

Claude Code tells each session how much of its account's 5-hour and weekly
allowance is used. cpb gathers that from every session it launched, so one
statement shows every account.

```
$ cpb SHOW USAGE
ACCOUNT       SESSIONS  AT LIMIT  5H USED  5H RESETS  7D USED  7D RESETS  SEEN
claude-home   4         0         12%      in 3h10m   40%      in 5d 2h   1m ago
claude-work   9         2         100%     in 1h20m   100%     in 2d 21h  40s ago
router        2         0         not reported
```

- **SESSIONS** counts the live sessions on that account.
- **AT LIMIT** counts the ones whose own request was refused.
- **SEEN** is how old the figure is.
- **`not reported`**: a gateway, an API key or a cloud account; their
  providers report no allowance.
- **`not seen`**: a Claude account no session has reported for yet.
- `--json` gives the same rows, and `cpb "SELECT account, seven_day FROM
  USAGE"` queries them.

## Where the figures come from

Claude Code passes the figures to a playbook's status line. A launch through
cpb puts a small step of cpb's in front of the playbook's own status line
command:
1. the step records what the figures say;
2. it adds a `cpb` key to the input;
3. it runs the original command, which sees what it always saw, plus that key.

`cpb EXPLAIN PLAYBOOK <name>` says whether a launch records usage:

```
Usage at launch: recorded by cpb's status line step (the playbook's own status line runs after it)
```

- **A playbook needs a status line of its own**, set with `SET
  statusline.command = '…'`. A playbook without one gets none; cpb does not
  add one.
- **A folder's own status line runs as it is.** A status line set in the
  working folder's `.claude/settings.json` (or its repository's) runs
  untouched, and that session records nothing. Claude Code runs a folder's
  command only once you trust the folder, and cpb never runs it earlier.
- **No timer.** A figure is as fresh as the last reply any session on that
  account got.

## In your own status line

The `cpb` key carries every account's figure, by name, never an e-mail:

```
input=$(cat)
echo "$input" | jq -r '.cpb.usage[] | "\(.account) \(.five_hour.used_percentage // "-")% / \(.seven_day.used_percentage // "-")%"'
```

- **`account.name`** is the session's own account.
- **`usage`** has its own entry first, then every other account, at most 16.
- **`state`** says `not_reported` or `not_seen` where there is no figure.
- **A reset window** keeps its last figure, with a past `resets_at`.

The full shape is in [the SPEC](../../SPEC.md#the-usage-step).

## What is kept, and where

- **The log.** `.state/usage/sessions/<YYYY-MM>/<session id>.jsonl` under
  the playbooks root holds one line per change, each describing itself:
  - the time, the session, its config directory, playbook, account and
    identity;
  - the figures, cost, context and model.
- **The latest figure** per account is in `.state/usage/accounts/`.
- Everything is mode 600. Nothing in it is a credential.
- A session's log rotates at 1 MiB, and a launch removes files nobody wrote
  for 30 days.

Reference: [SHOW USAGE](../../SPEC.md#show-usage).
