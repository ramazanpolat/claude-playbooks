# 24 — the ask history

```
cpb SHOW ASK HISTORY                          # what cpb ask answered, newest first
cpb SHOW ASK HISTORY FOR PLAYBOOK reviewer --json
cpb SHOW ASK HISTORY LIMIT 1                  # one page; its last line lists the next
cpb SHOW ASK RECORD r-20261109-091500-3c4d    # one record in full
```

`cpb ask` keeps a record of every answered ask in
`~/.claude-playbooks/.ask/history.md`, one markdown record each, appended and
never rewritten. Every ask sends the newest index lines and the last record,
so a follow-up such as "I meant the reviewer playbook" can revise the last
answer. `SHOW ASK` reads the same file from the command line.

- **`SHOW ASK HISTORY`** lists the records newest first: the time, the
  playbook the ask was addressed to (`-` for none), the id and what was
  asked. It lists 20 by default, `LIMIT <n>` up to 200. When older records
  match, its last line is the statement that lists them (`BEFORE <id>`).
  `FOR PLAYBOOK <name>` keeps the asks addressed to one playbook.
- **`SHOW ASK RECORD <id>`** prints one record as it was written: what was
  asked, the answer, the steps and the notes.
- **To forget,** delete `history.md`; cpb starts a new one. `cpb ask
  --no-history` keeps one ask out of it.
- **`.ask/CLAUDE.md`** is your standing context, in your own words. cpb only
  reads it, and sends it with every ask.

`cpb ask` needs a Claude login, so the `.check` writes two records shaped as
cpb ask writes them, then reads them back.

Reference: [Follow-ups and your own context](../../docs/guides/assistant.md#follow-ups-and-your-own-context),
and [SPEC.md](../../SPEC.md), `cpb ask`, "The ask store".
