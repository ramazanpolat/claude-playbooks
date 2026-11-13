# 29 — labels

```
cpb APPLY playbook.cpb
cpb run --label name=review-auth agent       # a launch label, fixed at launch
cpb session label task=triage                # from inside the session: a hook or a tool
cpb session label --list
cpb SHOW SESSIONS                            # the last column, LABELS, shows them
```

A session carries short `key=value` labels, from two sources kept apart:

- **launch labels** are yours, given at launch with `--label`. A failover
  relaunch, the by-hand line and a sandbox host carry them, like `--env`;
- **session labels** are set from inside a live session, by a hook or a
  tool, with `cpb session label`. It finds the session from its own
  ancestry, and exits 3 when it does not run in one cpb launched.

Both reach the playbook's status line (the `cpb` key's `labels`), `SHOW
SESSIONS` and the usage log. The status line here prints them.

A hook that labels each session with its folder's name, in a playbook's
`settings.json` or a folder's `.claude/settings.json`:

```json
{"hooks": {"SessionStart": [{"hooks": [{"type": "command",
  "command": "cpb session label task=\"$(basename \"$PWD\")\" || [ $? -eq 3 ]"}]}]}}
```

`|| [ $? -eq 3 ]` ignores exactly "not in a session cpb launched", so the
same hook works under plain `claude`.

A label is printed on shared screens, so a value that looks like an e-mail
address or a credential is refused, as is a control or invisible character.

The `.check` uses a stand-in claude that writes its own session file, runs
the hook as its child and renders the launch's status line, as Claude Code
does.

Reference: [Session labels](../../SPEC.md#session-labels). Guide: [Label
your sessions](../../docs/guides/labels.md).
