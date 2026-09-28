# 18 — sessions

```
cpb sessions                        # = cpb SHOW SESSIONS: the live sessions of every playbook
cpb SHOW SESSIONS FOR PLAYBOOK worker --json
cpb "SELECT playbook, pid, model FROM SESSIONS"
cpb RESUME --list                   # this folder's recent sessions, live ones marked
cpb RESUME                          # the newest one here that is not live, through its playbook
cpb RESUME SESSION '<id>'
```

Claude Code keeps a small file for each live session in the config dir it
runs under, `sessions/<pid>.json`. cpb reads those files in its playbooks.
It reads no process's environment, and never changes the files.

- **`SHOW SESSIONS`** lists each live session with:
  - its playbook, pid and working directory;
  - its age and when it was last active (its transcript's time);
  - the model it last answered with;
  - the command that resumes it.
- **`RESUME`** starts `claude --resume <id>` the way the playbook's
  launcher does: its env sets, variables, secret references and login. It
  runs in the directory the session ran in.
  - With no id, it takes the newest session in this folder that is not
    live, and says so. A newer live session is named, not taken.
  - **A live session is never resumed.** Two processes on one session id
    corrupt it. RESUME names the pid, so you can close that one first.
- **The exit line.** When `claude` exits under `cpb run` or a launcher, on a
  terminal, cpb prints `Resume this playbook's session with: <launcher>
  --resume <id>`. Claude Code's own `claude --resume` line would look under
  `~/.claude`.

The `.check` fakes a live session with a `sleep` and a session file shaped
like Claude Code's.

Reference: [Sessions](../../docs/reference/cli-grammar.md#sessions-v3250).
