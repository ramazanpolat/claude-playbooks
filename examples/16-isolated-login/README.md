# 16 — an isolated login

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK other-account   # Login: isolated, shares nothing …
cpb auth status                      # other-account  isolated  …
```

By default a playbook shares the machine's login: its `.credentials.json`
is a link to `~/.claude/.credentials.json`, so `/login` in any playbook
logs in all of them. An **isolated login** shares nothing: no link, no
machine-wide token, no leftover account record. The playbook is logged in
only if you run `/login` in it.

- `CREATE PLAYBOOK … SET login = 'isolated'` makes a new one isolated.
  `ALTER PLAYBOOK … SET login = 'isolated'` isolates an existing
  one and removes its link to the shared login at once.
- `SET login = 'shared'` shares the machine's login again from the
  next launch. It is refused while the playbook holds a login of its own, because
  a shared launch would take it out of use: another account's login is set
  aside, and the same account's is copied over the machine's. Run
  `/logout` in it first. It is also
  refused on a sandboxed playbook (`sandbox.always = true`), which is always
  isolated.
- Use it for a second account, and for a throwaway or a third-party route
  whose `/login` must not land in the machine's shared login
  ([example 15](../15-third-party-route/)).

It is `isolated_login = true` in the playbook's `.playbook`
([authentication guide](../../docs/guides/authentication.md)).
Reference: [Playbook properties](../../SPEC.md#playbook-properties).
