# Accounts

An **account** says where a playbook's requests go and how they
authenticate: the provider, its address, who you are there, one credential,
and that provider's model names. A playbook names the accounts it may run
on. At every launch cpb derives the routing, credential and model variables
from the account the launch runs on, so an address can never change while
its key stays behind, and your Claude login never follows a playbook to
another provider.

The contract is [SPEC, CREATE / ALTER / DROP ACCOUNT](../../SPEC.md#create--alter--drop-account);
[example 23](../../examples/23-accounts/) runs two Claude accounts on one
playbook; [example 15](../../examples/15-third-party-route/) runs a gateway
account.

## Make one

```bash
# Two Claude subscriptions, each a token from `claude setup-token`, by reference.
cpb CREATE ACCOUNT work SET provider = 'anthropic', identity = 'me@work.example', claude_token FROM 'keychain:claude-work'
cpb CREATE ACCOUNT personal SET provider = 'anthropic', claude_token FROM 'keychain:claude-personal'

# A model router on this machine, with its own key and its model names.
cpb CREATE ACCOUNT glm SET provider = 'gateway', base_url = 'http://localhost:4000/v1', auth_token FROM 'keychain:router-key', model = 'glm-5.3', haiku_model = 'glm-5.3-flash'

# The Anthropic API with a Console key; Bedrock through your AWS credentials.
cpb CREATE ACCOUNT console SET provider = 'anthropic', api_key FROM 'keychain:anthropic-api'
cpb CREATE ACCOUNT aws SET provider = 'bedrock', auth = 'cloud'
```

References are resolved by your secret helper at launch, never by cpb
([Secrets](../../SPEC.md#secrets-optional)); without one, write the value
with `AS PLAINTEXT` after the list. The credential is exactly one of:

| key | is sent as |
|---|---|
| `auth_token FROM '<ref>'` | `Authorization: Bearer` (`ANTHROPIC_AUTH_TOKEN`), what most routers take |
| `api_key FROM '<ref>'` | `X-Api-Key` (`ANTHROPIC_API_KEY`), a Console key |
| `claude_token FROM '<ref>'` | a Claude subscription token (`CLAUDE_CODE_OAUTH_TOKEN`) |
| `helper = '<command>'` | an `apiKeyHelper` that prints the key, passed to Claude Code for that launch only |
| `auth = 'claude_login'` | the playbook's own `/login` (shared or isolated, per its `login`) |
| `auth = 'cloud'` | the cloud's own credential chain (Bedrock, Vertex, Foundry) |
| `auth = 'none'` | nothing: a local router that takes no key |

### Headers a gateway asks for

A gateway behind an access proxy may ask every request for headers of its
own, a client id and secret, say. They go on the account, each by
reference, never as a value:

```bash
cpb ALTER ACCOUNT edge SET header.X-Access-Id FROM 'keychain:edge-id', header.X-Access-Secret FROM 'keychain:edge-secret'
cpb ALTER ACCOUNT edge RESET header.X-Access-Id
```

At launch your secret helper resolves each one and cpb joins them into
`ANTHROPIC_CUSTOM_HEADERS` in that launch's `claude` environment, as for
the account's key (what claude starts, hooks and tools, may inherit it): no
value is on a command line, in a file or in cpb's output. `SHOW ACCOUNT edge` lists each header's
name and reference. At most 8 headers, on an `anthropic` or `gateway`
account; the headers that belong to the credential (`Authorization`,
`X-Api-Key`, `Proxy-Authorization`), to Anthropic's API (`anthropic-*`) or to
the request itself (`Host`, `Content-Length`, …) are refused. Moving the
account (`base_url`, `provider`) removes every header the same statement
does not set again. A sandboxed launch with headers is refused, as for any
reference.

## Run a playbook on it

```bash
cpb ALTER PLAYBOOK sre USE ACCOUNT work, personal     # may run on these; the first by default
cpb run sre                                           # on work
cpb run --account personal sre                        # one launch on personal
cpb ALTER PLAYBOOK sre USE ACCOUNT personal, work     # personal by default from now on
cpb ALTER DEFAULTS USE ACCOUNT work                   # for playbooks that name none
```

A launch runs on `--account`, else the playbook's first account, else the
`DEFAULTS` account, else `machine`: your machine's own login, exactly as
before accounts existed.

```bash
cpb SHOW ACCOUNTS                            # every account, never a credential
cpb "SELECT name, accounts, account FROM PLAYBOOKS"
cpb EXPLAIN PLAYBOOK sre                     # Account: work (anthropic, claude_token from a reference; the playbook's first)
```

## What an account guarantees

- **One source.** The address, the key, the Claude token and the model
  variables come from the account and nowhere else. Anything your shell
  exports for them is removed; an env set, a playbook variable, a launch
  flag, the playbook's `settings.json`, the project's `.claude/settings.json`
  or a `--settings` you pass to claude that sets one (or names an
  `apiKeyHelper`) refuses the launch, naming where. `EXPLAIN` says the same in
  `route.refused`.
- **Moving an account moves its key.** `ALTER ACCOUNT glm SET base_url = …`
  is refused unless the same statement states the credential.
- **Your login stays home.** An account that uses no Claude login (a router's
  key, a Console key) never reaches yours: a playbook that shares the
  machine's login is refused on it (give it `SET login = 'isolated'`), and an
  isolated playbook's own login is set aside for the run on Linux and, on
  macOS, shadowed: the run gets a placeholder Claude token that Claude Code
  uses before any stored login, so your Keychain login stays in place and
  never reaches the account's address. So `cpb run --account <router>
  <playbook>` works for a playbook you are logged into, for one launch. On
  Linux, a login that cannot be set aside, a store that cannot be
  read, or a login still reachable afterwards refuses the launch: never a
  warning and a run.
- **Sandboxes too.** A sandboxed launch renders the same account and starts
  claude from a plain shell: the sandbox's login shell is read for its `PATH`
  only, and every variable the account does not set is removed, so a profile
  inside cannot re-route the run. A sandbox is bound to the account it was
  created for (recorded on the host, outside any mount): a launch on another
  account is refused, and `--sandbox-fresh` recreates it for the new one. On
  an account that uses no Claude login, a sandboxed launch is refused while
  the playbook holds a login of its own, a copy beside its store included:
  its directory is mounted inside. `/logout` in it, run it unsandboxed, or use
  another playbook.
- **One account per run of a playbook.** Two runs of one isolated playbook
  may not mix its login with an account that keeps it out: a launch that uses
  the playbook's own login is refused while a run that keeps that login aside
  is live, and a launch on an account that uses no Claude login is refused
  while the playbook holds its login and another session of it is live.
- **One `--settings`.** An account with `helper` passes its key helper to
  claude as a `--settings` of its own, so a `--settings` you pass along is
  refused for it.
- **A gateway you trust** with your Claude credential (a company proxy in
  front of Anthropic) says so: `claude_login_trusted = true`, over `https`
  only. Without it such an account is refused when written.
- **Models follow the account.** `model` is the default a new session starts
  on; `opus_model`, `sonnet_model`, `haiku_model` and `fable_model` are what
  those aliases mean on this provider. Give a playbook an alias (`SET model =
  'opus'`) and it stays right on every account.
- **No Anthropic profile sneaks in.** Claude Code also reads profiles from
  `~/.config/anthropic`; every launch points it at a directory of its own,
  recreated empty each time (anything in it, or a link in its place, is
  removed).
- **Names are lowercase** (`a-z`, digits, `.`, `_`, `-`): the store may be on
  a case-insensitive filesystem. A name that looks like a credential is
  refused without being printed.
- **A playbook is one trust zone.** Every account it lists runs with the
  same config directory, settings and hooks, so a session on one account can
  change what a later run on another account uses. Keep a model you would not
  trust with your Claude credential in a playbook of its own.

## Who may use which

Both sides are allow-lists:

- A playbook with a `USE ACCOUNT` list runs only on those accounts.
- A playbook with no list may run on any account open to every playbook.
- `ALTER ACCOUNT work SET access = 'listed'` makes `work` run only for a
  playbook that lists it; it cannot be the `DEFAULTS` account.

A playbook installed from a source, or played from a recipe, never brings an
account list: accounts are this machine's, and a recipe that tries to route
is refused (play it on your own with `cpb play --account <account>`).
`cpb play --account` takes an account open to every playbook only: for a
`listed` one, keep the recipe, then list the account on the kept playbook
(`ALTER PLAYBOOK <name> USE ACCOUNT <account>`).

## A route stated in one place

A playbook that names no account can still say where its requests go in one
env set, its own `SET VAR` block, or one launch's `--env` flags, as long as
the address and its key come together from that one place:

```bash
cpb CREATE ENV router SET ANTHROPIC_BASE_URL=http://localhost:4000/v1 ANTHROPIC_DEFAULT_OPUS_MODEL=glm-5.3 SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:router-key'
cpb CREATE PLAYBOOK glm SET login = 'isolated' USE ENV router
cpb EXPLAIN PLAYBOOK glm     # Account: env:router (gateway http://localhost:4000/v1, auth_token from a reference; a route stated in ENV router)
```

cpb runs it as an account derived for that launch, so the account rules
hold: http only to this machine, your Claude login kept out (a playbook that
shares it is refused), nothing your shell exports standing in. Model names
may come from any of the playbook's layers. Refused: an address in one place
and its key in another, an address with no key, a route in `DEFAULTS` (it
would reach every playbook: give `DEFAULTS` an account instead), a route
beside an account the playbook or the launch names, and a Claude token or a
cloud provider's variables in a layer, which only an account says.

## Coming from v4.1

An env set holding `ANTHROPIC_BASE_URL` with its key runs as it is, as a
route stated in one place (above), on a playbook that names no account; a
playbook that shares your login needs `SET login = 'isolated'` for it. A
route split across layers, a playbook variable that sets
`CLAUDE_CODE_OAUTH_TOKEN`, or a route beside an account is refused, naming
the variable and the layer, and for an env set holding an address and its
key the refusal also prints the `CREATE ACCOUNT` statement that makes it an
account of its own. Make the account, attach it with `USE ACCOUNT`, then
remove the variables (`ALTER ENV <set> UNSET …`).

Which of your playbooks this refuses:

```bash
cpb auth status                                   # MODE error, the reason in NOTE
cpb "SELECT name, auth_mode FROM PLAYBOOKS"       # auth_mode = error
cpb EXPLAIN PLAYBOOK <name> --json                # route.refused says why
```

- **An address is `https`, or `http` to this machine only** (`localhost`,
  `127.0.0.1`, `::1`). A v4.1 env set that routes to plain `http` on another
  host has no account it can become, and its refusal prints no `CREATE
  ACCOUNT` line. Put `https` in front of the gateway, or reach it through a
  tunnel to `localhost` (`ssh -L 4000:gateway:4000 …`), then make the account
  on that address.
- **A model only.** A layer that sets `ANTHROPIC_MODEL` (v4.1's example 02
  did `SET VAR ANTHROPIC_MODEL`) moves to the playbook's own `SET model =
  '<model>'`; a provider's model names go on its account (`model`,
  `opus_model`, …). `ANTHROPIC_SMALL_FAST_MODEL` is `haiku_model` now.
- **A repository's `.claude/settings.json`** that sets `ANTHROPIC_MODEL` (or
  any variable below) in `env`, or names an `apiKeyHelper`, refuses every
  launch from that folder: use its `model` key instead, and an account's
  `helper` for the key helper.
- **No account key yet.** These are refused with nothing to move them to;
  a setup that needs one stays on v4.1 until an account can say it (custom
  headers can: `header.<Name> FROM '<ref>'`, above): client certificates
  (`CLAUDE_CODE_CLIENT_CERT`, `CLAUDE_CODE_CLIENT_KEY`,
  `CLAUDE_CODE_CLIENT_KEY_PASSPHRASE`); `ANTHROPIC_UNIX_SOCKET`;
  `CLAUDE_CODE_SKIP_BEDROCK_AUTH` and the other `CLAUDE_CODE_SKIP_*_AUTH`;
  Bedrock Mantle and Anthropic on AWS (`CLAUDE_CODE_USE_MANTLE`,
  `CLAUDE_CODE_USE_ANTHROPIC_AWS`, their base URLs and `ANTHROPIC_AWS_API_KEY`);
  `AWS_BEARER_TOKEN_BEDROCK`, `ANTHROPIC_FOUNDRY_API_KEY` and
  `ANTHROPIC_FOUNDRY_AUTH_TOKEN` (a cloud account authenticates with
  `auth = 'cloud'`); Anthropic profiles and federation (`ANTHROPIC_PROFILE`,
  `ANTHROPIC_FEDERATION_RULE_ID` and the other identity variables);
  `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` and `CLAUDE_CODE_OAUTH_SCOPES`;
  `CLAUDE_CODE_SUBAGENT_MODEL`.
