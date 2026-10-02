# Try someone else's playbook

`cpb play` runs a playbook someone else wrote, without letting it touch yours.
It fetches the recipe once, checks it, shows you exactly what it would do,
asks before it runs, and removes everything when the session ends.

```
cpb play frontend-craft                                 # a curated template
cpb play https://example.com/agents/reviewer.cpb        # any https URL
cpb play github:acme/agents/reviewer.cpb@v1.2.0         # a file in a repository, pinned
cpb play ./reviewer.cpb                                 # a file of your own
cpb play frontend-craft -- -p "review the landing page" # claude's own arguments after --
```

A shared playbook is a **recipe**: `ALTER PLAYBOOK` statements with no name,
the format `cpb SHOW CREATE` and the terminal UI's export already write. So
anything you have built can be shared as one `.cpb` file.

## What happens, step by step

1. **Fetch.** The recipe is read once, into memory: https only, at most 64 KiB,
   three redirects (never to http), ten seconds to connect and thirty in all. The bytes you
   preview are the bytes that run.
2. **Check.** Some clauses are refused outright, with the line and the reason.
   Nothing is written.
3. **Plan.** The same dry run as `cpb APPLY --dry-run`, against a fresh
   playbook in a throwaway store. Your `DEFAULTS` and env sets are not there,
   so nothing of yours layers in.
4. **Preview.** The recipe, its sha256, and the things worth a look.
5. **Confirm.** A yes, plus a typed confirmation for the few things that
   could send your work elsewhere.
6. **Run.** The session starts, in a sandbox when one is available.
7. **Clean up.** When the session ends, however it ends (`/exit`, ^C, a
   closed terminal, `kill`), the playbook and the store are removed.

## Reading the preview

The recipe below is [example 21](../../examples/21-play/)'s `reviewer.cpb`.

```
$ cpb play --check ./reviewer.cpb
Recipe:  ./reviewer.cpb
From:    /home/you/claude-playbooks/examples/21-play/reviewer.cpb
sha256:  c684ff05c8b3235559504c4a54ec0939a2706a5ccb0b0c6a83304fb7ba0bc81e (195 bytes)
Title:   Code reviewer
About:   Reads code, never writes it.

1 thing(s) to look at:
  !  line 7   ALLOW TOOL 'Read': read any file, your keys and tokens included, without asking
```

`--check` stops after this. Each `!` line is something the recipe may do that
you should know about: a program it runs (a status line, an MCP server), a
plugin or skill from elsewhere, a URL a tool sends data to, a permission that
lets it act without asking. A `!!` line also asks you to type something
before it runs.

**What a played recipe may not do** is refused, and nothing runs:

```
$ cpb play --check ./refused.cpb
...
Refused, so nothing would run (2):
  line 5   USE ENV: it would attach your env sets, and your keys, to someone else's playbook
  line 6   SET VAR GITHUB_TOKEN: a shared recipe carries no secret, even AS PLAINTEXT: use a reference, FROM '<ref>'
```

The refused clauses touch what is yours: your env sets (`USE ENV`, `ADD ENV`),
your `DEFAULTS`, other playbooks, a plaintext secret, the login, the launcher,
`INCLUDE` of other files, and a skill or marketplace from a directory on your
disk.

## When a recipe changes where your requests go

A recipe that sets `ANTHROPIC_BASE_URL` (or a Bedrock or Vertex switch) sends
every request, your code and your conversation with it, to that host, and any
credential the session holds with it. So that is never a plain yes:

```
  !! line 5   SET VAR ANTHROPIC_BASE_URL=https://router.example.net/v1: every request, your code and conversation with it, goes to router.example.net, with the credential sent to it
       → to run it, you will type: router.example.net

Requests would go to router.example.net, not Anthropic: the played playbook gets its own login, and your
credential variables are blocked. Attach a key for that host yourself, by name.
```

- You type the host. A typo stops the run, with nothing written.
- The playbook gets a login of its own, and every credential your shell or
  machine holds (`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, the AWS and
  Google ones, and any variable whose name looks like a secret) is blocked in
  it. Your key for Anthropic never reaches someone else's host.
- If the host does need a key, give it one of your env sets by name:
  `--env routerkey`. Its keys are the only ones that follow.

A proxy (`HTTPS_PROXY`) and a change to which certificates are trusted are
typed the same way. A secret reference (`SET VAR GH FROM 'keychain:…'`) is
typed too, and the prompt says the secret is read on this machine, as you.

## Sandboxed by default

The session runs in a sandbox where one is available: Docker Sandboxes
(`sbx`), or OpenShell on Linux. The preview says which. Where none is, it says
so instead:

```
No sandbox available here (sbx, or OpenShell on Linux): this agent will run on your machine, as you.
```

- `--no-sandbox` runs it on your machine on purpose, and the preview says
  that too.
- `--sandbox=sbx` or `--sandbox=openshell` picks a backend.
- A recipe whose header asks for a sandbox (`-- create-with: SANDBOX`) is
  refused where none is available, unless you pass `--no-sandbox`.
- A recipe that reads a secret reference runs only on your machine for now:
  a sandboxed session cannot resolve references yet. Where a sandbox is
  available it is refused without `--no-sandbox`.

The sandbox is created only after every confirmation, and removed with the
playbook.

## Scripts and CI

Without a terminal, `cpb play` never prompts:

```
cpb play ./reviewer.cpb --dry-run --json          # the plan, as APPLY --dry-run --json, plus a "play" block
cpb play ./reviewer.cpb --yes                     # the yes
cpb play ./router.cpb --yes --trust-endpoint router.example.net --env routerkey
cpb play ./tools.cpb --yes --trust-secret keychain:gh --no-sandbox
cpb play https://example.com/x.cpb --sha256 c684ff05…   # refuse any other bytes, before anything is shown
```

`--yes` answers the yes and never a typed confirmation: those need
`--trust-endpoint <host>` (or `TLS`) and `--trust-secret <ref>`, matching
exactly. The `"play"` block of `--json` carries the ref, the final URL, the
sha256, the header, the risks as `{code, line, clause, detail, confirm}`,
and the sandbox decision.

Exit codes: 0 when it checked, planned, kept, updated (or found nothing to
update), or the session ran, whose own exit code passes through; 1 when the
recipe was refused by a check, its header, a confirmation or `--sha256`; 2 on
a usage error.

## Keeping one

Liked it? Keep it as a playbook of your own instead of running it:

```
$ cpb play ./reviewer.cpb --keep
...
What it would do: a playbook reviewer in your store, with a launcher reviewer:
...
Not sandboxed: it will run on your machine, as you, unless you launch it with --sandbox.

Keep this playbook as reviewer? [y/N] y
...
Kept as reviewer: run it with `reviewer` (or cpb run reviewer). Update it with cpb update reviewer.
```

- Same preview, same confirmations; no session runs.
- `--as <name>` names it. A name already taken is refused.
- It lives in your store, so your `DEFAULTS` apply to it like to any
  playbook, and the preview names them. When the recipe moves the endpoint,
  their keys are blocked in it ("will NOT follow it to …") unless you attach
  a set with `--env`.
- A recipe that asks for a sandbox is kept as a `SANDBOX` playbook
  (`--no-sandbox` overrides that).
- The exact bytes are kept in the playbook (`.play/recipe.cpb`) and the
  manifest records where they came from. `cpb SHOW PLAYBOOK reviewer` shows
  it:

  ```
  Played from:    /home/you/reviewer.cpb (sha256 c684ff05c8b3, 2026-10-01T23:26:00Z; cpb update reviewer)
  ```

## Updating a kept one

Nothing updates on its own. `cpb update <name>` fetches the recorded ref
again:

- The same bytes: `reviewer is unchanged`, and nothing happens.
- New bytes: the diff, the full preview and every confirmation again.

```
$ cpb update reviewer
...
Changes from sha256 c684ff05c8b3 to eeb1d16d98d7:
  -   ALLOW TOOL 'Read' 'Grep' 'Glob'
  +   ALLOW TOOL 'Read' 'Grep' 'Glob' 'LS'

What the update would do to reviewer:
...
Update reviewer to these bytes? [y/N]
```

What the old recipe set and the new one no longer does is removed (a variable,
a tool rule, a plugin and then its marketplace); the login is never reset.
A pinned ref (a tag) that now serves different bytes is called out: "the tag
moved". `--dry-run` previews it, and `--dry-run --json` gives the plan;
without a terminal, `--yes`, `--trust-endpoint` and `--trust-secret` answer the
confirmations, as for `cpb play`.

## Templates

A bare name, `cpb play frontend-craft`, is a curated template from this
project's site, read from `site/p/<name>.cpb` **at the tag of the cpb you
run**: the template you get is the one this release was tested with. A
development build reads `main`, and says so. A name that does not exist
suggests close ones.

A template starts with a header:

```
-- title: Code reviewer
-- description: Reads code, never writes it.
-- needs: a secret helper for keychain:github-mcp
-- create-with: SANDBOX
-- min-cpb: 4.0.0
```

`title` and `description` are shown in the preview; `needs` says what you must
bring; `create-with: SANDBOX` asks for a sandbox; `min-cpb` makes an older cpb
refuse with the version it needs. `cpb play --check <dir>` checks a directory
of templates the way the site's CI does: every template, the header, and an
`index.txt` listing them all, sorted.

## See also

- [Sandboxed sessions](sandbox.md): the sandbox backends.
- [Environment overrides](environment.md): env sets, which `--env` attaches.
- [CLI grammar](../reference/cli-grammar.md#cpb-play): the full reference.
- [Example 21](../../examples/21-play/): a recipe checked, planned and kept in CI.
