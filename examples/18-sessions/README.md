# 18 — sessions

```
cpb SHOW SESSIONS                   # the live sessions of every playbook
cpb SHOW SESSIONS FOR PLAYBOOK worker --json
cpb "SELECT playbook, pid, model FROM SESSIONS"
cpb run worker --continue           # the newest session here, through its playbook
cpb run worker --resume '<id>'      # one session; a launcher takes the same flags
```

Claude Code keeps a small file for each live session in the config dir it
runs under, `sessions/<pid>.json`. cpb reads those files in its playbooks.
It reads no process's environment, and never changes the files.

- **`SHOW SESSIONS`** lists each live session with:
  - its playbook, pid and working directory;
  - its age and when it was last active (its transcript's time);
  - the model it last answered with;
  - the command that resumes it.
- **Resuming** is Claude Code's own `--resume` and `--continue`, on the
  launcher or `cpb run`, so the launch is the playbook's: its env sets,
  variables, secret references and login.
  - **A live session is never resumed.** Two processes on one session id
    corrupt it. cpb names the pid, so you can close that one first. For
    `--continue`, that is the newest session in this folder.
  - **The folder.** Claude Code finds a session by the folder it ran in.
    From elsewhere, cpb refuses with the command that resumes it, which is
    also the `resume` field of `SHOW SESSIONS --json`:
    `cd '<folder>' && cpb run worker --resume <id>`.
- **The exit line.** When `claude` exits under `cpb run` or a launcher, on a
  terminal, cpb prints `Resume this playbook's session with: <launcher>
  --resume <id>`. Claude Code's own `claude --resume` line would look under
  `~/.claude`.

The `.check` fakes a live session with a `sleep` and a session file shaped
like Claude Code's.

Reference: [Sessions](../../docs/reference/cli-grammar.md#sessions-v3250).
