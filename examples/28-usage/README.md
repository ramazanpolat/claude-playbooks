# 28 — usage

```
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK agent             # Usage at launch: recorded by cpb's status line step
cpb run agent                          # its status line now sees a cpb key
cpb SHOW USAGE                         # every account: used, when it resets, how old the figure is
cpb "SELECT account, seven_day FROM USAGE"
```

Claude Code tells a session's status line how much of its account's 5-hour
and weekly allowance is used. A launch through cpb puts a small step of
cpb's in front of the playbook's own status line:

- the step **records** the figures, per account, under the playbooks root
  (`.state/usage/`, mode 600);
- it **adds a `cpb` key** to the input: the session's account, and every
  account's latest figure, by name, never an e-mail;
- it **runs the original command**, which sees what it always did, plus
  that key. The status line here prints the account's name from it.

`cpb SHOW USAGE` shows each account:
- how many live sessions it has, and how many are stopped at a limit;
- the 5-hour and weekly use, and when each resets;
- how old the figure is.

A gateway or API key shows `not reported`, and an account no session has
reported for yet shows `not seen`. Neither is ever 0.

The playbook needs a status line of its own for the step. A status line set
in a working folder's own settings runs as it is, and records nothing.

The `.check` uses a stand-in claude that runs the launch's status line the
way Claude Code does, with a status line input that carries figures.

Reference: [SHOW USAGE](../../SPEC.md#show-usage), [The usage
step](../../SPEC.md#the-usage-step). Guide: [See how much of each account
is used](../../docs/guides/usage.md).
