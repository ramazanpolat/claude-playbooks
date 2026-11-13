# A provider that speaks only the OpenAI API

Claude Code speaks Anthropic's Messages API. Some providers serve Claude
models, and others, only through OpenAI's chat completions API. cpb cannot
point an account straight at such a provider. A **translator** on your
machine takes Claude Code's requests, rewrites them for the provider and
rewrites the answers back. The account points at the translator:

```
claude ── Messages ──▶ translator, 127.0.0.1:4100 ── chat completions ──▶ provider
          (a gateway account)   (holds the provider's key)
```

This guide runs [Venice.ai](https://docs.venice.ai/) through
[LiteLLM](https://docs.litellm.ai/). Another provider of the same kind, or
another translator, works the same way.
[Example 30](../../examples/30-translated-gateway/) holds every file it uses.

**First, check for a Messages address.** If the provider has one, make a
`gateway` account on it directly ([Accounts](accounts.md)); you need no
translator. A request without a key costs nothing and tells: `404` means the
address does not exist.

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST https://api.venice.ai/api/v1/messages
```

## Before you start

- **Everything goes to the provider**: the whole context, what the
  playbook's `CLAUDE.md` holds, the files a session reads, tool output. Give
  the provider a playbook of its own: the `CLAUDE.md` cpb writes imports
  nothing, `login = 'isolated'` keeps your Claude login out, and a new
  playbook keeps `~/.claude`'s instructions out
  ([example 15](../../examples/15-third-party-route/)).
- **It is pay per token.** Claude Code's background requests use the haiku
  tier: map it to a cheap model.
- **On request only.** Make the account `access = 'listed'` and set the
  playbook's `failover = 'off'`; never put the account in DEFAULTS'
  `failover_accounts`.
- **Usage figures.** `SHOW USAGE` shows such an account as `not reported`:
  the provider reports no allowance to Claude Code. The cost a status line
  or `claude -p --output-format json` shows is Claude Code's own estimate
  from its price table. The provider's billing has the real figure.

## 1. Install the translator in a folder of its own

```bash
mkdir -p ~/.local/share/translator && cd ~/.local/share/translator
uv venv --python 3.13 venv
uv pip install --python venv/bin/python 'litellm[proxy]==1.105.7'
cp <this repository>/examples/30-translated-gateway/translator/* .
```

Pin the version. Call the translator by its full path
(`~/.local/share/translator/venv/bin/litellm`): a command of the same name
on your `PATH` may be something else, a cpb launcher for instance.

## 2. Its configuration

[`litellm.yaml`](../../examples/30-translated-gateway/translator/litellm.yaml)
holds no secret: `os.environ/<NAME>` names the variables `start.sh` sets.

- **`model_name`** is the provider's own id (`claude-sonnet-5-5`), so Claude
  Code sees an id it knows: its context window, its price.
- **`use_chat_completions_url_for_anthropic_messages: true`**: requests go
  to `/chat/completions` (LiteLLM's default for an `openai/` model would be
  `/responses`).
- **`turn_off_message_logging: true`**: no prompt or answer text in
  LiteLLM's logs.
- **`drop_params: true`**: Anthropic-only options with no place in chat
  completions are dropped, not refused.
- **`include_venice_system_prompt: false`**, under `extra_body` →
  `venice_parameters`: Venice adds a system prompt of its own to every
  request unless told not to. A sign of it: cached input far larger than
  what you sent.
- **`master_key`**: the token a client must present.

## 3. Start it with the key lent for its life

[`start.sh`](../../examples/30-translated-gateway/translator/start.sh) runs
in the foreground; Ctrl-C stops it. Set its three names first: your helper,
the reference of the provider's key, and the folder your helper reads `file:`
references from.

- The provider's key goes through your secret helper's exec form
  (`<helper> KEY=REF -- command`, the one cpb uses at launch) into the
  translator's environment only. It is never written to a file.
- Each start makes a new random token for the local port, writes it as
  `translator-token` in that folder (mode `600`) and removes it on exit.
- It listens on `127.0.0.1` only, with nothing of your shell's environment
  but `HOME` and `PATH`.
- One translator at a time: a second start while one runs refuses, and
  leaves the running one's token alone.

## 4. The playbook and its account

cpb resolves the account's `file:translator-token` through the same secret
helper, so cpb must use it: cpb's built-in helper resolves only `cpb:`
references. `cpb SHOW DEFAULTS` shows the one cpb uses; it is one per
machine, so if you already have one, use it in `start.sh` too. Otherwise set
it once (or set `CPB_SECRET_HELPER` in the shell you run cpb from);
[example 30](../../examples/30-translated-gateway/playbook.cpb) uses the
sample helper.

```bash
cpb ALTER DEFAULTS SET secret_helper = 'my-secret-helper'
cpb CREATE PLAYBOOK venice-agent SET launcher = '', login = 'isolated', failover = 'off'
cpb CREATE ACCOUNT venice SET provider = 'gateway', base_url = 'http://127.0.0.1:4100', auth_token FROM 'file:translator-token', model = 'claude-sonnet-5-5', opus_model = 'claude-opus-5-5', sonnet_model = 'claude-sonnet-5-5', haiku_model = 'claude-haiku-5-5', access = 'listed', description = 'Venice.ai, pay per token, through the local translator'
cpb ALTER PLAYBOOK venice-agent USE ACCOUNT venice
```

`CREATE ACCOUNT` checks the reference, so run it while the translator runs.

## 5. Run

```bash
~/.local/share/translator/start.sh     # one terminal
cpb run venice-agent                   # another
```

With the translator stopped, its token is gone and the launch stops before
claude starts: the helper cannot resolve `file:translator-token`. cpb's last
line names the account and says the local server behind it may not be
running; whatever the helper itself suggests, the token is the script's to
write.

## What to expect

Seen with LiteLLM 1.105.7, Claude Code 2.1.323 and Venice's Claude models:

- **Works**: text, streaming, tool use (a Bash round trip), and the system
  messages Claude Code sends in the middle of a conversation.
- **Prompt caching works**: the second turn of a session read most of its
  prompt from the cache. Venice places Claude's cache breakpoints itself.
- **Effort** reaches the provider as `reasoning_effort`. A thinking budget
  set on its own does not: the translator drops it.
- **Context window**: Claude Code knows the window of a model id it
  recognises; for an id it does not (an older Claude Code, a prefixed id) it
  assumes 200K.

Not tried against Venice, or expected to fail:

- **Images** passed the translator in a test against a stand-in provider;
  they were not sent to Venice.
- **Web search**: Claude Code's WebSearch tool runs on Anthropic's servers.
  Expect it to fail on this route.

## Variants

- **Another translator.** Anything that serves the Messages API on
  `127.0.0.1` fits the same account. Ask where it keeps the provider's key:
  claude-code-router, which Venice's own guide uses, keeps it in its data
  folder.
- **A fixed local token.** Store one in your secret store, lend it to the
  translator through the helper as well, and make the account `auth_token
  FROM` that reference. A launch with the translator down then reaches
  claude, which reports connection errors.
- **No local token** (`auth = 'none'`) works too; any process on the
  machine can then send requests through the port while the translator runs.

Reference: [CREATE / ALTER / DROP ACCOUNT](../../SPEC.md#create--alter--drop-account).
