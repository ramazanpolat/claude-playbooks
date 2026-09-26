# Your first playbook.cpb

Ten minutes, from nothing to a setup you can rebuild on any machine with one
command. You need `cpb` ([install](../guides/installation.md)) and Claude Code.

## 1. A playbook

```bash
cpb CREATE PLAYBOOK scratch
scratch
```

`scratch` is a directory under `~/.claude-playbooks/` and a command on your
PATH. Everything that session changes (settings, memory, history, MCP servers)
stays in that directory; your `~/.claude` is untouched. `cpb SHOW` lists your
playbooks; `cpb SHOW PLAYBOOK scratch` shows one.

## 2. A route

Say you reach a model through a local router. Put its variables in an env set,
a named group that any playbook can use:

```bash
cpb CREATE ENV router DESCRIBE 'my model router' SET ANTHROPIC_BASE_URL=http://localhost:20128/v1 ANTHROPIC_MODEL=glm-5.3
cpb ALTER PLAYBOOK scratch USE ENV router
cpb EXPLAIN PLAYBOOK scratch
```

`EXPLAIN` lists every variable a launch of `scratch` sets and the layer that
decided it. The playbook's own `SET VAR` wins over its env sets, a later set
wins over an earlier one, and `ALTER DEFAULTS USE ENV …` puts sets under every
playbook.

The router's token does not belong in the set as text. Store it with a secret
helper, then refer to it:

```bash
cpb ALTER DEFAULTS SET SECRET HELPER with-secret
cpb ALTER ENV router SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:pilot/router-token'
```

cpb stores the reference, checks it with the helper, and resolves it only at
launch. Any helper with the same interface works
([Secrets](../reference/cli-grammar.md#secrets-optional)).

## 3. The file

```bash
cpb SHOW CREATE ALL > playbook.cpb
```

`playbook.cpb` now holds your machine as statements: env sets, `DEFAULTS`,
playbooks. Every statement is safe to repeat, and no secret value is in it.
Commit it to your dotfiles.

## 4. Another machine

```bash
cpb APPLY playbook.cpb --dry-run   # what would change
cpb APPLY playbook.cpb             # do it
cpb APPLY playbook.cpb             # again: 0 created, 0 changed
```

`APPLY` checks every statement before writing anything, then runs them in
order. If one fails, it stops and says what already ran; fix the file and apply
it again.

## Next

- [Stack layers into an agent](stacked-agent.md): plugins, the agent, `INCLUDE`.
- [Examples](../../examples/): one small file per idea.
- [The grammar](../reference/cli-grammar.md): every statement and rule.
