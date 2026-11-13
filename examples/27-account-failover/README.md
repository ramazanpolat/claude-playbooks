# 27 — account failover

```
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK agent        # Failover: on (DEFAULTS); … moves to acct-work, acct-personal
cpb SHOW DEFAULTS                 # Failover: on; accounts acct-work, acct-personal
cpb run --account acct-work agent # at a usage limit, the session moves to acct-personal
cpb run --no-failover agent       # one launch that never moves
```

When a session stops at a usage limit, a launch with failover on moves it to
the next listed account by itself:

- **It acts only when nothing runs.** The session is idle (no turn, no
  background shell, agent or monitor), its transcript has not changed for two
  minutes, and the limit resets more than 30 minutes from now. A sooner reset
  is left to Claude Code, which continues by itself then.
- **The same session, the same terminal.** cpb ends that claude and runs
  the same launch again on the next account, with `--resume <id>` and Claude
  Code's own continue prompt. The prompt's first line says what moved and how
  much context the new account reads again, uncached.
- **The order is yours.** `failover_accounts` lists the accounts, in order.
  Each is tried at most once, and only one the playbook may run on. Another
  kind of account (a Claude login moving to a gateway) only when it is listed
  as `'<name> (any provider)'`.
- **Off unless turned on.** DEFAULTS turns it on for every playbook that
  inherits it. A playbook says `failover = 'on'` or `'off'` for itself, and
  `--failover` / `--no-failover` decide for one launch.

The `.check` runs a real switch with a stand-in claude. The stand-in stops
at a weekly limit and waits, as Claude Code does. cpb ends it and resumes the
same session on acct-personal, with acct-personal's token.

Text typed in the input box and not yet sent is lost in a switch. The whole
context is read again on the new account, with no cache.
`failover_max_context` sets a cap on that when you want one.

Reference: [Account failover](../../SPEC.md#account-failover). Guide:
[Account failover](../../docs/guides/account-failover.md).
