# 17 — status line panels

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK barred    # Panels: 3 (local.beat, local.clock, local.model)
```

Claude Code runs one status line command per session. A **host**,
[statusmux](https://github.com/agent-realm/statusmux), takes that one slot
and composes the bar from **panels**: small manifest files that playbooks,
plugins and you contribute. The contract between them is SPC/1.

- `SET STATUSLINE '"$HOME/.local/bin/statusmux" render' REFRESH 10` puts the
  host in the slot. `REFRESH` keeps it rendering while the session is idle,
  which observers need.
- `ADD PANEL <ns>.<id> <type> …` writes one manifest at
  `statusline.d/<ns>/<id>.toml`, one per type:
  - `EXEC '<command>'` runs a command on every render;
  - `TEMPLATE '<text>'` builds text from Claude Code's JSON;
  - `RECORDS '<path>'` reads a file another process keeps;
  - `OBSERVE '<command>' EVERY <ms>` runs a side effect (a heartbeat) and
    shows nothing.
- The options (`ROW`, `PRIORITY`, `ALIGN`, `TIMEOUT`, …) are the manifest's
  fields.
- `DROP PANEL` removes a manifest.
- `ADD PANEL local.bar FROM STATUSLINE` turns the bar you have now into a
  panel, before you put the host in its place.

What the pilot controls stays the pilot's. cpb never writes the layout file
(`statusline.toml`), and a manifest you wrote yourself, one without cpb's
`# Written by cpb` first line, is never changed or removed. `SHOW PLAYBOOK
--json` and `SELECT … FROM PANELS` list the playbook's panels and, read-only,
those of its enabled plugins.

Reference: [Status line panels](../../docs/reference/cli-grammar.md#status-line-panels-v3250).
