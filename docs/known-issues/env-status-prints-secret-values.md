# `claude-playbook env <name>` prints resolved secret values, not just presence

**Status:** fixed (task `cpb-env-redact-secrets`, 2026-09-22). `env`,
`env-profile`, and `info` now mask a `set` value rather than printing it
resolved; `--reveal` opts back in for one invocation.

**`SPEC-v4.md`'s "Redaction (v3.15.0, `--reveal`)" amendment is the
contract** -- read it rather than this paragraph for the current rules, which
three rounds of review widened well past what the fix first shipped. In
outline: strong words (`TOKEN`, `SECRET`, `PASSWORD`, ...) match anywhere in
the key, `AUTH` and the abbreviations (`PWD`, `PASS`, `PAT`) match a whole
`_`-separated segment, a segment ending in `KEY`/`KEYS` matches unless a
`PUBLIC`/`PUB` segment says the material is public, and a credential embedded
in a connection URL is masked from the *value* since no key-based rule can
see it. A masked value keeps up to 4 characters at each end with at least 8
always hidden, so anything under 12 characters is redacted whole.
`docs/environment.md` has the same thing for readers rather than
implementers. The rest of this writeup is left as-is for the root-cause
record.

Found live 2026-09-21 while attaching the
`claude-opus-5` env profile to `kommander-metu` for an unrelated task (a code
review run through that playbook). Running `claude-playbook env kommander-metu`
to check which profiles were attached printed the fully resolved effective
environment, including `ANTHROPIC_AUTH_TOKEN=sk-...` in plain text, into an
active Claude Code session transcript.

## Root cause

`cmd/env.go`'s no-verb status path (`claude-playbook env <name>`, and the
`use`/`unuse` confirmation path that falls through to it) calls
`printEnvBlock` to show the playbook's *effective* environment — the
profile-expanded result, not just the raw manifest entries:

```go
// cmd/env.go:120 and :128
printEnvBlock("  ", block)      // the playbook's own overrides
...
printEnvBlock("  ", effective)  // profiles expanded + overrides applied
```

`printEnvBlock` (`cmd/env.go:277-294`) prints every `set` entry's value
unconditionally:

```go
// cmd/env.go:287
fmt.Printf("%sset    %s=%s\n", indent, key, e.Set[key])
```

There's no distinction between an ordinary override (`ANTHROPIC_BASE_URL`,
safe to show) and a credential (`ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`,
anything an env profile's TOML marks as secret per `docs/environment.md`'s own
"values may be secrets" note about `.env-profiles/*.toml`) — both print the
same way.

## Why this matters here specifically

The rest of this project's secret handling is presence-only by convention —
`with-secret --check` (the chaos-secret / pilot-profile pattern this repo's own
docs point to) deliberately never prints a resolved value, only whether one is
set. `claude-playbook env <name>`'s status display is the one place that
breaks that pattern: it's the *intended, everyday* way to check which profiles
are attached to a playbook, and it silently leaks whatever those profiles
resolve to.

Practical impact: any Claude Code session (or a human) that runs this
completely ordinary status command gets the credential in its scrollback/
transcript. For an agent session in particular, that transcript is not
ephemeral — it may get logged, shipped to a memory/observability system, or
simply persist in session history far longer than the human would expect from
"I just wanted to see which profile is attached."

## Suggested fix

Redact values for keys that look like credentials before printing, or add a
`--reveal`/`--show-secrets` flag that defaults off. A reasonable heuristic:
treat any key matching `*_TOKEN`, `*_KEY`, `*_SECRET`, `*_AUTH*` (case-
insensitive) as sensitive and print `set    KEY=<redacted, N chars>` instead
of the value, unless the flag is passed. Cross-check against whatever env
profile TOML files already mark a field as secret (`docs/environment.md`
mentions "values may be secrets" for `.env-profiles/*.toml` — if that's a real
per-field flag rather than just a comment, prefer it over a key-name
heuristic).

## Not filed as a GitHub issue (yet)

This is a personal dev environment finding, not triaged into the repo's
public issue tracker. Left here so the next agent working in this repo picks
it up automatically (see `CLAUDE.local.md`) and can decide whether/how to fix
it or turn it into a tracked task.
