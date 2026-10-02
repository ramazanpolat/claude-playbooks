# 19 — the terminal UI

```
cpb APPLY playbook.cpb
cpb tui
```

`cpb tui` shows what the grammar reads, on one screen per object:

| Key | Screen | The statement behind it |
|---|---|---|
| `1` | Playbooks | `cpb SHOW PLAYBOOKS --json` (and `SHOW SESSIONS` for the session counts) |
| `enter` | a playbook's tabs | `cpb SHOW PLAYBOOK browsed --json`; the Vars tab: `cpb EXPLAIN PLAYBOOK browsed --json` |
| `2` | Sessions | `cpb SHOW SESSIONS --json` |
| `3` | Env sets | `cpb SHOW ENVS --json` |
| `4` | Defaults | `cpb SHOW DEFAULTS --json` |
| `c` | the selection's text | `cpb SHOW CREATE PLAYBOOK browsed --skip-secrets` |

- **Nothing new.** The TUI adds nothing the statements do not print. Each
  screen names its statement on its last line.
- **Take what you see with you.**
  - `y` copies the statement, or on Sessions the command that resumes the
    session once it ends.
  - `e` writes the SHOW CREATE text as `browsed.cpb` in this folder. It
    asks before replacing a file.
- **Past sessions** are in Claude Code's own picker, `<launcher> --resume`,
  as the Sessions footer says.

**v1 only reads.** Changing a playbook is still a statement, for example:

```
cpb ALTER PLAYBOOK browsed DROP ENV proxy
```

v2 will build such statements from the screens, show them, and run them
through `APPLY --dry-run --json` before applying.

The `.check` cannot open a terminal in CI. It checks the refusal off a
terminal, and each screen's statement against this recipe.

Reference: [cpb tui](../../docs/reference/cli-grammar.md#cpb-tui-v3250).
