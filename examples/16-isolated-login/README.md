# 16 — an isolated login

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK other-account   # Login: isolated: …
cpb auth status                      # other-account  isolated  …
```

By default a playbook shares the machine's login: its `.credentials.json`
is a link to `~/.claude/.credentials.json`, so `/login` in any playbook
logs in all of them. An **isolated login** shares nothing: no link, no
machine-wide token, no leftover account record. The playbook is logged in
only if you run `/login` in it.

- `CREATE PLAYBOOK … ISOLATED LOGIN` (the hidden command:
  `create --isolated-login`) makes a new one isolated. `ALTER PLAYBOOK …
  SET ISOLATED LOGIN` isolates an existing one and removes its link to the
  shared login at once.
- `UNSET ISOLATED LOGIN` shares the machine's login again from the next
  launch. It is refused while the playbook holds a login of its own, because
  a shared launch would copy that login over the machine's and switch every
  shared playbook to that account. Run `/logout` in it first. It is also
  refused on a `SANDBOX` playbook, which is always isolated.
- Use it for a second account, and for a throwaway or a third-party route
  whose `/login` must not land in the machine's shared login. With a
  non-Anthropic route, also create it with `NO PILOT PROFILE`
  ([example 15](../15-third-party-route/)).

It is `isolate_auth = true` in the playbook's `.playbook`
([authentication guide](../../docs/guides/authentication.md)).
Reference: [Isolated login](../../docs/reference/cli-grammar.md#isolated-login-v3230).
