# The terminal UI

`cpb tui` shows your playbooks, their sessions, env sets and defaults on
one screen, and resumes a session in the right playbook. It is a view of
the grammar: every screen is a `cpb … --json` statement, named on its last
line. v1 only reads.

```
cpb tui
```

## Playbooks

```
 cpb  [1 Playbooks]  2 Sessions   3 Env sets   4 Defaults   5 Log        ? help
────────────────────────────────────────────────────────────────────────────────
  NAME           LAUNCHER  ENV SETS  LOGIN     SESSIONS  MODEL
▸ kommander-dev  kd        -         shared    1         claude-opus-5-5
  router         k9        9router   isolated  1         glm-5.3

────────────────────────────────────────────────────────────────────────────────
 2 playbooks · 2 live sessions · read 0s ago
 enter open  s sessions  c SHOW CREATE  e export  y copy  / filter  q quit
reads: cpb SHOW PLAYBOOKS --json
```

- **LOGIN** is the kind of login: `shared` with `~/.claude`, `isolated`, or
  `sandbox`. It is never a value.
- **SESSIONS** counts the live Claude Code sessions.
- **Opening a playbook.** `enter` opens it, `s` shows its sessions, and `/`
  filters the list.
- **Narrow terminals.** Columns drop in order (version, model, launcher),
  so the name stays.

## A playbook

`enter` opens the tabs: Overview, Env, Vars, Plugins, MCP, Skills, Status
line, Model and Sessions. `←→` or `1`–`9` switch between them.

```
 cpb   1 Playbooks   2 Sessions   3 Env sets   4 Defaults   5 Log        ? help
────────────────────────────────────────────────────────────────────────────────
 router  (launcher k9 · ~/.claude-playbooks/router)
  Overview  Env [Vars] Plugins  MCP  Skills  Status line  Model  Sessions

  effective at launch, from EXPLAIN
  KEY                   VALUE / REF               LAYER
  X                     1                         defaults base
  ANTHROPIC_BASE_URL    http://tr0:20128/v1       env 9router
  ANTHROPIC_AUTH_TOKEN  FROM 'keychain:pilot/9r'  env 9router
  OPENAI_API_KEY        (redacted, plaintext)     playbook
  MY_FLAG               1                         playbook

────────────────────────────────────────────────────────────────────────────────
 2 playbooks · 2 live sessions · read 0s ago
 ←→/1-9 tab  c SHOW CREATE  e export .cpb  y copy  r refresh  esc back  q quit
reads: cpb EXPLAIN PLAYBOOK router --json
```

- **Vars** shows the variables in effect at launch, with the layer each
  comes from (`EXPLAIN PLAYBOOK --json`).
- **A reference is shown as the reference.** A plaintext credential shows
  as `(redacted, plaintext)`. No screen ever holds a secret value, since
  cpb has withheld it before the TUI reads it.
- **Overview** says whether the playbook's `CLAUDE.md` imports your pilot
  profile.

## Sessions

```
 cpb   1 Playbooks  [2 Sessions]  3 Env sets   4 Defaults   5 Log        ? help
────────────────────────────────────────────────────────────────────────────────
  PLAYBOOK       PID    TTY      AGE  ACTIVE  MODEL            CWD
▸ kommander-dev  47904  ttys039  9h   54s     claude-opus-5-5  ~/DEV/claude-pla…
  router         43627  -        2d   28m     glm-5.3          ~/DEV/claude-pla…

────────────────────────────────────────────────────────────────────────────────
 2 playbooks · 2 live sessions · read 0s ago
 R recent here (to resume)  y copy RESUME  / filter  r refresh  q quit
reads: cpb SHOW SESSIONS --json
```

- **Where each one is running:** the pid, the terminal (TTY), how long it
  has run, when it last did something, its model and its folder.
- **The list refreshes** every 5 seconds while it is on screen.
- **`R` switches to the recent sessions of the current folder**, live or
  not. `enter` on one that is not running resumes it: the TUI hands the
  terminal to `cpb RESUME SESSION '<id>' FOR PLAYBOOK <name>`, and comes
  back when you leave Claude.
- **A running session** is not resumed. The TUI names the pid that holds
  it, since two processes on one session corrupt it.

## Take it with you

| Key | What |
|---|---|
| `y` | copy the statement behind the selection (`SHOW PLAYBOOK router`, `RESUME SESSION '…' FOR PLAYBOOK …`) |
| `c` | show the selection's `SHOW CREATE`, without secrets; `y` there copies the text |
| `e` | write that text as `<name>.cpb` in this folder, a recipe `cpb APPLY` re-applies; an existing file is replaced only after a typed `y` |

## Env sets and defaults

- **`3` lists the env sets.** They are also called env profiles, and are
  stored in `~/.claude-playbooks/.env-profiles/`. The list shows their
  variables, which playbooks use them, and which one is a default.
- **`4` shows `DEFAULTS`:** the env sets layered under every playbook, and
  the secret helper.

## Good to know

- **It needs a terminal.** In a script, use the statements it names:
  `cpb SHOW … --json`, `cpb SELECT …`.
- **It gives the terminal back** however it ends: `q`, Ctrl-C, a kill, or
  a crash.
- **`NO_COLOR`** turns off its only attributes: reverse video and dim.
- **Plain `cpb`** in a terminal ends with the hint `Browse and manage them:
  cpb tui`.
- **v2 will change things.** Attaching an env set, setting a variable and
  the rest will each be shown as the statement it runs, planned with
  `APPLY --dry-run --json`, and confirmed.

Reference: [cpb tui](../reference/cli-grammar.md#cpb-tui-v3250).
