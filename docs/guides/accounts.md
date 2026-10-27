# Accounts

An **account** says where a playbook's requests go and how they
authenticate: the provider, its address, who you are there, one credential,
and that provider's model names. A playbook names the accounts it may run
on. At every launch cpb derives the routing, credential and model variables
from the account the launch runs on, so an address can never change while
its key stays behind, and your Claude login never follows a playbook to
another provider.

The contract is [SPEC, CREATE / ALTER / DROP ACCOUNT](../../SPEC.md#create--alter--drop-account);
[example 23](../../examples/23-accounts/) runs everything below.

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
  flag, the playbook's `settings.json` or the project's `.claude/settings.json`
  that sets one refuses the launch, naming where. `EXPLAIN` says the same in
  `route.refused`.
- **Moving an account moves its key.** `ALTER ACCOUNT glm SET base_url = …`
  is refused unless the same statement states the credential.
- **Your login stays home.** An account that uses no Claude login (a router's
  key, a Console key) never reaches yours: a playbook that shares the
  machine's login is refused on it (give it `SET login = 'isolated'`), and an
  isolated playbook's own login is set aside for the run on Linux, refused on
  macOS.
- **A gateway you trust** with your Claude credential (a company proxy in
  front of Anthropic) says so: `claude_login_trusted = true`, over `https`
  only. Without it such an account is refused when written.
- **Models follow the account.** `model` is the default a new session starts
  on; `opus_model`, `sonnet_model`, `haiku_model` and `fable_model` are what
  those aliases mean on this provider. Give a playbook an alias (`SET model =
  'opus'`) and it stays right on every account.
- **No Anthropic profile sneaks in.** Claude Code also reads profiles from
  `~/.config/anthropic`; every launch points it at an empty directory of its
  own.

## Who may use which

Both sides are allow-lists:

- A playbook with a `USE ACCOUNT` list runs only on those accounts.
- A playbook with no list may run on any account open to every playbook.
- `ALTER ACCOUNT work SET access = 'listed'` makes `work` run only for a
  playbook that lists it; it cannot be the `DEFAULTS` account.

A playbook installed from a source, or played from a recipe, never brings an
account list: accounts are this machine's, and a recipe that tries to route
is refused (play it on your own with `cpb play --account <account>`).

## Coming from v4.1

An env set holding `ANTHROPIC_BASE_URL` and a key, or a playbook variable that
sets `CLAUDE_CODE_OAUTH_TOKEN`, keeps its file, but a launch that layers it is
refused, naming the variable and the layer, and for such an env set the
`CREATE ACCOUNT` statement that replaces it. Make the account, attach it with
`USE ACCOUNT`, then remove the variables (`ALTER ENV <set> UNSET …`).
