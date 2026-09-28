# Resume a session

Every playbook is its own Claude Code config dir, so `claude --resume` in a
plain shell looks in the wrong place: `~/.claude`. cpb knows which playbook
a session belongs to and resumes it there.

## See what is running

```
$ cpb sessions
PLAYBOOK       PID    KIND         STATUS  AGE  ACTIVE  MODEL            SESSION                               CWD
kommander-dev  47904  interactive  busy    9h   54s     claude-opus-5-5  08c4811b-3867-4f18-b08f-de6d1e07395f  /Users/me/DEV/app
kommander-san  26218  interactive  idle    39m  35m     claude-opus-5-5  8ba14a71-15a8-45b9-bc3e-9db6c209f318  /Users/me/santiment
```

`cpb sessions` is short for `cpb SHOW SESSIONS`.
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

## Resume

In the project folder:

```
$ cpb RESUME
1 newer session is live (pid 47904); resuming d0a04774-d6ed-49f7-bb32-3c57962348fa of kommander-dev (last active 4 days ago)
```

- **A bare `RESUME`** takes the newest session of this folder that is not
  running, across all playbooks. It says what it took, and which newer ones
  it skipped because they are live.
- **`RESUME --list`** shows the ten newest sessions, with their titles.
  Resume one of them with `RESUME SESSION '<id>'`.
- **From anywhere.** `RESUME SESSION '<id>'` works from any folder. cpb
  moves to the folder the session ran in.
- **The same launch as the launcher.** The launch is the playbook's own, as
  its launcher or `cpb run <name>` would do it: env sets, variables, secret
  references, the login.

**A live session is refused.** If the session is still open in another
terminal, `RESUME` says so with its pid and launches nothing. Two processes
on one session id corrupt it. Close the other one first, or pick another
session.

## The line after a session

When a session ends under a launcher or `cpb run`, in a terminal, cpb
prints the command that brings it back:

```
Resume this playbook's session with: kd --resume 08c4811b-3867-4f18-b08f-de6d1e07395f
```

Claude Code's own `claude --resume …` line still appears above it. Use
cpb's: it opens the session in the playbook, where the transcript lives.

## Limits

- **Sandboxed playbooks** (`--sandbox`, `[sandbox] always`) keep their
  sessions inside the sandbox. `RESUME` refuses them for now.
- **The format.** The session files are Claude Code's, and undocumented.
  cpb reads them defensively. If a Claude Code version stops writing them,
  `cpb sessions` shows nothing rather than guessing.

Reference: [Sessions](../reference/cli-grammar.md#sessions-v3250).
