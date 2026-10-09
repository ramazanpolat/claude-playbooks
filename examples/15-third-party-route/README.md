# 15 — a playbook for a non-Anthropic route

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK routed   # ANTHROPIC_BASE_URL from router
```

With `ANTHROPIC_BASE_URL` pointing at a router (a local proxy, or another
provider), every request the playbook makes goes there, with whatever its
`CLAUDE.md` holds. Four things keep it to what you meant to send:

- The `CLAUDE.md` that `CREATE PLAYBOOK` writes imports nothing. Anything you
  add to it, `@` imports included, goes to the router with every request.
- `SETTINGS login = 'isolated'` gives the playbook a login of its own, so a
  `/login` in it never lands in the machine's shared login
  ([example 16](../16-isolated-login/)).
- Its `memory` setting is `'isolated'`, a new playbook's default, so the
  machine's `~/.claude/CLAUDE.md` and rules do not go along either
  ([example 22](../22-isolated-memory/)).
- `BLOCK VAR ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN` keeps the Anthropic
  credentials your shell may export out of its launches. The router's own key
  belongs in the env set, by reference ([example 04](../04-secret-references/)).

`localhost` is a route like any other: a local router forwards elsewhere.

Reference: [Objects](../../SPEC.md#objects) and [Layers at launch](../../SPEC.md#layers-at-launch).
