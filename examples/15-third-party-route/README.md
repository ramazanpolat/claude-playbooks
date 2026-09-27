# 15 — a playbook for a non-Anthropic route

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK routed   # ANTHROPIC_BASE_URL from router
```

The `CLAUDE.md` that `CREATE PLAYBOOK` writes ends with four
`@~/.pilot-profile/…` imports. Without a profile they do nothing. With one,
Claude Code sends the profile with every request: the pilot's name, emails,
host index and secret reference names. For a playbook routed to another
provider (a local router, EVREN, GLM, DeepSeek), those requests leave
Anthropic, so the profile would go with them.

- `CREATE PLAYBOOK routed … NO PILOT PROFILE` writes the same `CLAUDE.md`
  without those imports. The hidden command takes `--no-pilot-profile`. It
  applies at create time only: after that, `CLAUDE.md` is yours, and taking
  the imports out of an existing playbook means deleting the lines.
- When a statement routes a playbook that *does* import the profile away
  from Anthropic, cpb warns once, naming the playbook and the host:

  ```
  $ cpb ALTER PLAYBOOK plain USE ENV router
  Warning: PLAYBOOK plain (localhost) imports ~/.pilot-profile/ and now sends requests to a non-Anthropic ANTHROPIC_BASE_URL, …
  ```

  The same holds for an env set changed under it, `ALTER DEFAULTS`, and a
  playbook created under such `DEFAULTS`. `APPLY --json` gives the warning the
  code `pilot_profile_third_party_endpoint`. It is a warning, never a refusal.
  `localhost` counts as non-Anthropic, since a local router forwards
  elsewhere.

Reference: [The pilot profile and non-Anthropic routes](../../docs/reference/cli-grammar.md#the-pilot-profile-and-non-anthropic-routes-v3230).
