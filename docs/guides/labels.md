# Label your sessions

With many sessions open, an id says little. A label says what a session is
for: `name=review-auth`, `task=triage`. cpb shows labels where you look at
sessions: in the playbook's status line, in `SHOW SESSIONS` and in the usage
log.

```
$ cpb run --label name=review-auth agent
...
$ cpb SHOW SESSIONS
PLAYBOOK  ACCOUNT      PID    TTY    KIND         STATUS  AGE  ACTIVE  MODEL            SESSION                               CWD             LABELS
--------  -------      ---    ---    ----         ------  ---  ------  -----            -------                               ---             ------
agent     claude-work  41870  pts/3  interactive  busy    12m  1m      claude-opus-5-5  0e5c1a2b-0000-4000-8000-0000000000b1  /home/user/app  launch name=review-auth
```

## Two sources, kept apart

- **Launch labels** are yours, given at launch: `--label KEY=VALUE` on `cpb
  run`, a launcher or `cpb start`: up to 16, 4 KiB together. A later one
  for the same key wins. They are fixed for the launch, and travel with it
  like `--env`: an account failover relaunch, the line cpb prints to resume
  by hand, and a launch on a sandbox host carry them. cpb checks them before
  anything starts, and before it forwards a launch to a sandbox host.
- **Session labels** are set from inside a live session, by a hook or a
  tool, with `cpb session label`:

```
cpb session label task=triage 'name=fixing ci'
cpb session label --unset name
cpb session label --list              # key=value, one per line
cpb session label --list --json       # {"session_id": "...", "labels": {...}}
```

  `cpb session label` finds the session it runs in from its own process
  ancestry: the nearest ancestor that is a Claude Code session of a
  playbook. Inside a session, that is the only session it labels:
  `--session` may name only that one. Outside any session, `--session <id>`
  labels a live session by its id, one cpb can confirm is running.

The two never mix: a session label does not change a launch label of the
same key, and every place that shows labels shows both, apart.

## From the environment

`label_vars` copies the values of environment variables you name into
launch labels, each as `env.<name in lower case>`:

```
cpb "ALTER DEFAULTS SET label_vars = ['TERM_PROGRAM']"            # every launch
cpb "ALTER PLAYBOOK reviewer SET label_vars = ['HC_TERMINAL_ID']"  # this playbook's, after DEFAULTS'
cpb "ALTER PLAYBOOK reviewer RESET label_vars"
```

- **Only the names you list.** Exact names, no patterns, at most 8 in each
  list. When you set the list, cpb refuses, whatever the value:
  - its own variables and an account's;
  - any name that may hold a credential (`GITHUB_TOKEN`, `API_KEY`,
    `DB_PASSWORD`, `SLACK_WEBHOOK`, `DATABASE_DSN`, a name with `KEY`,
    `COOKIE`, `SALT`, `SIGNING` or a `SESSION` part, or shaped like a key);
  - a name that says who or where you are: `USER`, `LOGNAME`, `USERNAME`,
    `HOSTNAME`, `HOST`, the `GIT_AUTHOR_*` and `GIT_COMMITTER_*` names, and
    `GITHUB_ACTOR`.
- **Read once per run,** from the environment you launched cpb in. A
  `--label` of the same key wins. When failover moves the session to
  another account, the relaunch keeps the labels the launch read.
- **Checked more than a typed label.** Nobody typed these values, so
  besides what any label refuses (an e-mail address, a credential,
  invisible characters), a URL, a path and an address are refused too.
  The launch says so in one line, by key and kind, never by value:
  `cpb: label_vars left out: env.hc_terminal_id (the value is a URL)`. An
  unset or empty variable is left out quietly.
- **Yours, on this machine.** An installed or played playbook never brings
  its own `label_vars`, and `cpb update` keeps yours.
- `EXPLAIN PLAYBOOK <name>` shows, for each name, the label it gives and
  what a launch from here would do with it: copy it, or leave it out
  (unset, a refused name, a refused value and its kind).

## From a hook

A `SessionStart` hook can label every session, here with its folder's name.
In a playbook's `settings.json`, or a folder's `.claude/settings.json`:

```json
{"hooks": {"SessionStart": [{"hooks": [{"type": "command",
  "command": "cpb session label task=\"$(basename \"$PWD\")\" || [ $? -eq 3 ]"}]}]}}
```

`cpb session label` exits:

| Code | Meaning |
|---|---|
| 0 | done |
| 1 | refused: a label it does not take, a `--session` id of no live session (or of another session, from inside one), or another write of the same session's labels held the lock for 400 ms |
| 3 | not in a session cpb launched |

`|| [ $? -eq 3 ]` ignores exactly the last, so the same hook works under
plain `claude`. A hook never waits long: a write that cannot get the
session's lock within 400 ms gives up.

## Where they show

- **The status line.** The `cpb` key a playbook's status line reads has
  `labels`: `{"launch": {...}, "session": {...}}`, each left out when
  empty. A label set during the session shows from the next render. This
  needs the usage step, so a playbook status line of its own ([See how much
  of each account is used](usage.md)).
- **`SHOW SESSIONS`.** `--json` has `labels`, and the table's last column,
  `LABELS`, shows them (`launch k=v, …; session k=v, …`). The `SESSION`
  column stays the session's id. `cpb "SELECT session_id, labels FROM
  SESSIONS"` queries them.
- **The usage log.** Each line under `.state/usage/sessions/` has
  `launch_labels` and `session_labels`, and a change of either writes a new
  line.

## What a label may hold

- A key: `a-z`, `0-9`, `.`, `_`, `-`, starting with a letter or a digit, at
  most 32 characters.
- A value: one line of plain visible text, not empty, at most 200 bytes.
  `k=` is refused; `--unset k` removes a label.
- At most 16 labels from each source, 4 KiB together.

A label is plain visible text. cpb refuses any character a screen does not
show as written: control and format characters (zero-width spaces and
joiners, the soft hyphen, direction marks), separators, private-use
characters and variation selectors. So a single emoji is fine, but not an
emoji joined from several.

A label is printed on shared screens and written to the log, so cpb also
refuses a value that looks like an e-mail address, and a credential, judged
on key and value together: `password=…`, `token=…`, `api_key=…`,
`auth=Bearer …`, even `key=main`, since `key` names a secret. A hash after a
key that names one, such as `commit=<40 hex>`, is fine. The refusal names a
valid key, never a value, and an invalid key not at all. What cpb cannot
recognise is yours to keep out: no secrets, no login identity, no home
paths, host names or addresses, no prompt text. cpb takes a label from the
environment only through `label_vars`, for the names you list.

## Another playbooks root

A session launched with `--playbooks-dir` gets `CPB_PLAYBOOKS_DIR` set to
that root, replacing one your shell exported, so `cpb session label` and
`SHOW SESSIONS` inside the session use the same one. A root you set only in
the environment is passed on as it is.

## Files

Session labels live in `.state/usage/labels/<session id>.json` under the
playbooks root (mode 600). The file goes when its last label does, and with
the other usage files nobody wrote for 30 days, unless its session is still
running. Launch labels live in the
launch's record, which goes when the launch ends.

Labels say what a session is for; they are not a boundary. Any process of
yours can write a session's labels file.

Reference: [Session labels](../../SPEC.md#session-labels). Example:
[29-labels](../../examples/29-labels/README.md).
