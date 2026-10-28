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

Say you reach a model through a local router. Every request a playbook routed
there makes leaves Anthropic, with what its `CLAUDE.md` holds, so give the
route a playbook of its own, with its own login, and keep your Claude
credentials out of it.

First the route itself, an **account**: the router's address, its key, and
its model names, in one record any playbook can run on. The router's key does
not belong in it as text. Store it where a secret helper can read it, then
refer to it. With the sample helper,
[`cpb-secret-file`](../../examples/secret-helper/), copied onto your `PATH`:

```bash
mkdir -p ~/.config/cpb-secrets && chmod 700 ~/.config/cpb-secrets
(umask 077 && read -rs v && printf '%s' "$v" > ~/.config/cpb-secrets/router-token)
cpb ALTER DEFAULTS SET secret_helper = cpb-secret-file
cpb CREATE ACCOUNT router SET provider = 'gateway', base_url = 'http://localhost:8080/v1', auth_token FROM 'file:router-token', model = 'glm-5.3', description = 'my model router'
```

`read -rs` waits for you to paste the token and does not echo it. cpb stores
the reference, checks it with the helper, and resolves it only at launch. Any
helper with the same interface works, a wrapper around your keychain or
password manager included ([Secrets](../../SPEC.md#secrets-optional)).

Then the playbook:

```bash
cpb CREATE PLAYBOOK routed SET login = 'isolated' USE ACCOUNT router
cpb EXPLAIN PLAYBOOK routed
routed
```

- The account decides where `routed` sends its requests and with which key.
  At launch cpb sets those variables from it and removes the ones your shell
  exports, so your Anthropic credentials never follow it to the router.
- `SET login = 'isolated'` gives `routed` a login of its own: your Claude
  login and long-lived token never reach the router (on a shared playbook,
  cpb refuses the launch), and a `/login` in it never lands in the machine's
  login.
- The `CLAUDE.md` that `CREATE PLAYBOOK` wrote imports nothing, and
  `~/.claude`'s `CLAUDE.md` stays out (`global_instructions = 'excluded'`, the
  default). Whatever you add to its own `CLAUDE.md` goes to the router
  ([example 15](../../examples/15-third-party-route/)).

`EXPLAIN` names the account and lists every variable a launch of `routed`
sets and the layer that decided it, with no credential shown. Variables that
are not an account's (a tool's settings, `MAX_THINKING_TOKENS`) go in **env
sets**, named groups any playbook can use: the playbook's own `SET VAR` wins
over its env sets, a later set wins over an earlier one, and `ALTER DEFAULTS
USE ENV …` puts sets under every playbook. `ALTER DEFAULTS USE ACCOUNT …`
names the account a playbook with none of its own runs on.

## 3. The file

```bash
cpb SHOW CREATE ALL > playbook.cpb
```

`playbook.cpb` now holds your machine as statements: env sets, accounts,
`DEFAULTS`, playbooks. Every statement is safe to repeat, and no secret value is in it.
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
- [The grammar](../../SPEC.md): every statement and rule.
