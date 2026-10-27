# 15 — a playbook for a non-Anthropic route

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK routed   # Account: router (gateway http://localhost:8080/v1, …)
```

A route to a router (a local proxy, or another provider) is an **account**:
its address, its key (here none, for a local router that takes no key) and
its model names in one record ([example 23](../23-accounts/)). Every request
the playbook makes goes there, with whatever its `CLAUDE.md` holds. Four
things keep it to what you meant to send:

- The `CLAUDE.md` that `CREATE PLAYBOOK` writes imports nothing. Anything you
  add to it, `@` imports included, goes to the router with every request.
- `SET login = 'isolated'` gives the playbook a login of its own, so a
  `/login` in it never lands in the machine's shared login
  ([example 16](../16-isolated-login/)). An account that uses no Claude login
  never reaches that login either: on a shared playbook, the launch is
  refused.
- Its `global_instructions` setting is `'excluded'`, a new playbook's default, so the
  machine's `~/.claude/CLAUDE.md` and rules do not go along either
  ([example 22](../22-global-instructions/)).
- The account's variables are the only routing and credentials the launch
  carries: the Anthropic credentials your shell may export are removed.

`CREATE OR ALTER PLAYBOOK … USE ACCOUNT router` creates the playbook on its
account in one statement, and applied again changes nothing.

`localhost` is a route like any other: a local router forwards elsewhere.

Reference: [CREATE / ALTER / DROP ACCOUNT](../../SPEC.md#create--alter--drop-account).
