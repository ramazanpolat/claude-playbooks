# 09 — MCP servers, a credential by reference

```
cpb APPLY playbook.cpb --dry-run      # the `claude mcp add-json` commands it would run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK researcher       # the servers, and the variable the launch supplies
cpb SHOW CREATE PLAYBOOK researcher   # the clauses back, the reference as FROM '<ref>'
```

`ADD MCP SERVER` declares one server: stdio (`COMMAND … ARGS …`) or remote
(`URL …`, HTTP unless `TRANSPORT SSE`). cpb builds the JSON and runs Claude
Code's own `claude mcp add-json <name> '<config>' --scope user` for the
playbook, and reads the playbook's `.claude.json` to see what already holds,
so applying again runs nothing. A server whose declaration changed is removed
and added again.

A credential (an `Authorization` header, or an env key that looks like a
secret) must come `FROM '<ref>'`. The reference goes to the playbook's own
layer; Claude's config gets only `${CPB_MCP_SENTRY_H_AUTHORIZATION_<h8>}`,
which the secret helper resolves into the session at launch and Claude Code
expands when it starts the server. A header's reference resolves to the
whole value, so store `Bearer <token>`, not the bare token. A literal
credential is refused, and `AS PLAINTEXT` is not accepted here.

`file:sentry-auth` (read by the sample helper, [`cpb-secret-file`](../secret-helper/))
and `/srv/notes` are placeholders: point them at your own. `DROP MCP SERVER sentry` removes the server and forgets its
reference.
