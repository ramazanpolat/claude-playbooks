# 30 — a provider that speaks only the OpenAI API
```
translator/start.sh                    # in its own terminal; Ctrl-C stops it
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK venice-agent      # Account: venice (gateway http://127.0.0.1:4100, …)
cpb run venice-agent
```
Claude Code speaks Anthropic's Messages API. A provider that serves its
models only through OpenAI's chat completions API (Venice.ai here) needs a
**translator** between them, on this machine. The account points at the
translator, never at the provider:
```
claude ── Messages ──▶ translator, 127.0.0.1:4100 ── chat completions ──▶ provider
          (account venice)   (holds the provider's key)
```
- **The provider's key stays with the translator.** `translator/start.sh`
  starts LiteLLM with the key lent by your secret helper for the life of the
  process; it is never written to a file. `translator/litellm.yaml` names
  the variables, never a value.
- **The account holds a per-run token.** Each start writes a new random
  token where the helper reads `file:` references, and removes it on exit.
  The account sends it as `auth_token FROM 'file:translator-token'`, so with
  the translator stopped a launch stops before claude starts.
- **On request only.** `access = 'listed'`: only `venice-agent` runs on it.
  `failover = 'off'` on the playbook, and the account stays out of DEFAULTS'
  `failover_accounts`: it is pay per token.
- **Everything goes to the provider**: the whole context, with what the
  playbook's `CLAUDE.md` holds. The one cpb writes imports nothing, the login
  is the playbook's own, and `~/.claude`'s instructions stay out
  ([example 15](../15-third-party-route/)).
- **The provider's own model names**: each tier (`opus_model`,
  `sonnet_model`, `haiku_model`) is an id the translator serves. Claude Code's
  background requests use the haiku tier.
- **One command.** `translator/session.sh` starts the translator when none
  runs, runs `venice-agent` with every argument passed on, and stops the
  translator it started when the session ends; one started by hand is left
  running.

In this check no translator runs: `.setup` writes a placeholder token, the
stand-in claude records what a launch would send, and `translator/session.sh`
runs against a stand-in translator (a local port and the token).

Reference: [CREATE / ALTER / DROP ACCOUNT](../../SPEC.md#create--alter--drop-account);
guide: [A provider that speaks only the OpenAI API](../../docs/guides/openai-compatible-provider.md).
