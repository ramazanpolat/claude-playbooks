# Resume a session

Every playbook is its own Claude Code config dir, so `claude --resume` in a
plain shell looks in the wrong place: `~/.claude`. Resume through the
playbook instead. Its launcher, or `cpb run <name>`, hands `--resume` to
Claude Code under the playbook's own config dir.

## See what is running

```
$ cpb SHOW SESSIONS
PLAYBOOK  ACCOUNT      PID    TTY      KIND         STATUS    AGE  ACTIVE  MODEL            SESSION                               CWD
work      claude-work  59735  ttys039  interactive  busy      9h   54s     claude-opus-5-5  bfb2d226-403f-46da-8216-e39b2a71dae9  /Users/me/DEV/app
review    claude-home  30641  ttys012  interactive  limit 7d  39m  35m     claude-opus-5-5  15247cc4-c3e1-4b98-9c53-54402e029bce  /Users/me/other-app
```

- **ACCOUNT** is the account the session runs on, for a session cpb
  launched: cpb keeps a record of each launch while it runs, since Claude
  Code's own file does not say. `-` for a session started some other way.
- **STATUS** reads `limit 5h` or `limit 7d` while the session is stopped at
  a usage limit (from its transcript), until the limit resets.

- `--json` gives every field, `resume` included: the exact command that
  resumes that session.
- `SHOW SESSIONS FOR PLAYBOOK <name>` shows one playbook.
- `cpb "SELECT playbook, count() FROM SESSIONS GROUP BY playbook"` counts
  sessions per playbook, in ClickHouse's `clickhouse local`.

The list comes from files Claude Code keeps itself, one per live process
(`<config dir>/sessions/<pid>.json`).
- A session counts as live only while that process runs and is the same
  process (a reused pid is not).
- cpb reads nothing from other processes, and never changes those files.
  Its own launch records live in `.state/launches/` under the playbooks
  root, one per running launch, removed when the launch ends.

## Resume

In the folder the session ran in:

```
kd --resume                                        # Claude Code's picker: this playbook's sessions here
kd --resume bfb2d226-403f-46da-8216-e39b2a71dae9   # one session
kd --continue                                      # the newest session here
```

- **Without a launcher**, `cpb run <name> --resume …` does the same.
- **The same launch as always.** The launch is the playbook's own: the
  account, env sets, variables, secret references, the login.
- **From another folder.** Claude Code finds a session by the folder it ran
  in. The `resume` field of `SHOW SESSIONS --json` is the whole command:
  `cd '<folder>' && kd --resume <id>`. A `--resume <id>` typed in another
  folder is refused with that command.

**A live session is refused.** If the session is still open in another
terminal, cpb says so with its pid, the folder it runs in and its name, and
launches nothing. For `--continue`, that is the newest session in this
folder. Two processes on one session id corrupt it. Close the other one
first, or pick another session in the picker. To go on with it while the
other stays open, open a copy under a new id: the refusal names the command,
`kd --resume <id> --fork-session` from the session's folder.
`--fork-session` starts a new id, so it is never refused.

**Another playbook's session is refused.** Each playbook keeps its own
sessions. If you give one playbook an id another playbook holds, cpb names
that playbook and the command that resumes it there, instead of letting
Claude Code say it found no conversation. From `/Users/me/DEV/app`, with
`work`'s session given to `review`:

```
$ cpb run --account work-claude review --resume bfb2d226-403f-46da-8216-e39b2a71dae9
Error: session bfb2d226-403f-46da-8216-e39b2a71dae9 is not playbook review's: it is playbook work's, and Claude Code resumes a session only in the config dir that holds it. Resume it there: kd --account work-claude --resume bfb2d226-403f-46da-8216-e39b2a71dae9
```

From another folder, the command starts with the `cd` into the session's
folder. When `work` cannot run on the account you named (it lists others),
the command goes without `--account`, and the error says why.

**On another account.** `--account` goes before the session flags:
`kd --account work-claude --resume <id>`, or `cpb run --account work-claude
work --resume <id>`. Every command cpb names for the launch, the exit line
included, keeps the account you gave it. That is how to carry on a session
whose Claude subscription has hit its limit: see [Accounts](accounts.md).

## The line after a session

When a session ends under a launcher or `cpb run`, in a terminal, cpb
prints the command that brings it back:

```
Resume this playbook's session with: kd --resume bfb2d226-403f-46da-8216-e39b2a71dae9
```

Claude Code's own `claude --resume …` line still appears above it. Use
cpb's: it opens the session in the playbook, where the transcript lives.

## Limits

- **Sandboxed playbooks** (`--sandbox`, `sandbox.always = true`) keep their sessions
  inside the sandbox. `--resume` and `--continue` go to the claude in there.
  cpb cannot see those sessions, so it does not check them, and says so.
- **The format.** The session files are Claude Code's, and undocumented.
  cpb reads them defensively. If a Claude Code version stops writing them,
  `SHOW SESSIONS` shows nothing rather than guessing.

Reference: [Sessions](../../SPEC.md#show-sessions).
