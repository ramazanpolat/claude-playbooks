# Environment overrides

Variables set or unset for every launch of one playbook and no other — so one
playbook talks to a proxy or keeps its own login while the rest of your shell
does not. Available since v3.5.0.

A fresh install has none. This manifest is complete and normal:

```toml
version = "3.11.4"
name = "kommander"
alias = "k"

[source]
repository = "https://github.com/ramazanpolat/kommander-playbook"
```

Launching `k` runs `claude` with your shell's environment plus
`CLAUDE_CONFIG_DIR`, exactly as before. Nothing changes until you add an
override.

## What happens at launch

Every launch of a playbook (its launcher command, `run`, or `start` at its
directory) builds the child `claude` process's environment in layers, later
layers winning:

```text
your shell's environment
  + the registry default env profile, if one is set             (cpb env-profile <name> default)
  + each env profile the playbook uses, in the order listed     (~/.claude-playbooks/.env-profiles/<name>.toml)
  + the playbook's own [env.set]                                 (in its .playbook)
  - the playbook's own [env] unset
  + one-off launch flags (--env-profile, --env, --unset, --env-file)
  + CLAUDE_CONFIG_DIR, bound by the tool, cannot be overridden
  = what claude sees
```

A playbook with no `[env]` block still gets the registry default, when one is
set; without one it inherits only your shell's environment.

`set` overrides whatever the shell exported; `unset` removes a variable even when
the shell exports it. Raw `claude` launches bypass all of this. Claude Code's own
`env` block in `settings.json` is applied later, inside the `claude` process, and
wins over these layers; it can set variables but cannot unset one the shell
exported, which is what the manifest block is for.

## One playbook, its own overrides

```bash
cpb env kommander set ANTHROPIC_MODEL=claude-opus-5
cpb env kommander unset CLAUDE_CODE_OAUTH_TOKEN
```

The manifest above now ends with:

```toml
[env]
unset = ["CLAUDE_CODE_OAUTH_TOKEN"]

[env.set]
ANTHROPIC_MODEL = "claude-opus-5"
```

Inspect and undo:

```bash
cpb env kommander                        # show this playbook's block
cpb env                                  # every playbook that declares overrides
cpb env kommander clear ANTHROPIC_MODEL  # forget the entry; the shell's value applies again
cpb info kommander                       # "Env:" lines appear when a block exists
```

```text
Environment overrides for "kommander":
  set    ANTHROPIC_MODEL=claude-opus-5
  unset  CLAUDE_CODE_OAUTH_TOKEN
```

## Env profiles: define once, attach to many

When several playbooks want the same overrides, put them in a **profile**: a
named file under `~/.claude-playbooks/.env-profiles/`, managed with
`env-profile`, attached to playbooks by name with `env <playbook> use`.

```bash
cpb env-profile glm set ANTHROPIC_BASE_URL=http://proxy:1/v1 ANTHROPIC_DEFAULT_OPUS_MODEL=glm/glm-5.3
cpb env-profile glm unset CLAUDE_CODE_OAUTH_TOKEN
cpb env-profile glm describe "GLM 5.3 through the local router, own /login"
```

That wrote `~/.claude-playbooks/.env-profiles/glm.toml` (mode `0600`, values may
be secrets):

```toml
description = "GLM 5.3 through the local router, own /login"
unset = ["CLAUDE_CODE_OAUTH_TOKEN"]

[set]
ANTHROPIC_BASE_URL = "http://proxy:1/v1"
ANTHROPIC_DEFAULT_OPUS_MODEL = "glm/glm-5.3"
```

Attach it. The playbook's manifest records only the name:

```bash
cpb env router use glm
cpb env router set ANTHROPIC_DEFAULT_OPUS_MODEL=glm/glm-5.4   # local entry on top of the profile
cpb env router
```

```text
Environment overrides for "router":
  profiles  glm
  set    ANTHROPIC_DEFAULT_OPUS_MODEL=glm/glm-5.4
Effective at launch:
  set    ANTHROPIC_BASE_URL=http://proxy:1/v1
  set    ANTHROPIC_DEFAULT_OPUS_MODEL=glm/glm-5.4
  unset  CLAUDE_CODE_OAUTH_TOKEN
```

```toml
[env]
profiles = ["glm"]

[env.set]
ANTHROPIC_DEFAULT_OPUS_MODEL = "glm/glm-5.4"
```

Profiles apply in the order listed, later ones overriding earlier, and the
playbook's own entries apply last.

One profile can be the **registry default**, applied under every playbook's own
block, manifest or not, `start` included:

```bash
cpb env-profile personal default      # every launch starts from this layer
cpb env-profile personal undefault    # back to no default
```

An empty `[env]` block and a missing one are the same thing: the identity layer.
So a launch is a stack — shell environment, registry default, the playbook's
profiles, the playbook's own entries, one-off flags — and every layer that says
nothing changes nothing.

Manage them:

```bash
cpb env-profile                 # list profiles, descriptions, which playbooks use each
cpb env-profile glm             # show one
cpb env router unuse glm        # detach
cpb env-profile glm delete      # refused while any playbook still uses it
```

A profile that a playbook names but that is missing, unreadable, or invalid
**refuses the launch** rather than silently running without it: a dropped layer
could send traffic to the wrong endpoint with the wrong credentials.

## One launch only

The same layers can be added for a single launch without touching any file.
Launch flags go before the playbook name, or right after it, and stop at the
first argument that is not one of them; everything after that is `claude`'s:

```bash
cpb run --env-profile work kommander                     # an existing profile, this launch only
cpb run kommander --env ANTHROPIC_MODEL=claude-opus-5 -p "..."
cpb run --unset CLAUDE_CODE_OAUTH_TOKEN kommander        # this launch uses the stored login
cpb run --env-file ./work-account.env kommander          # KEY=VALUE lines, dotenv style
cpb start --env-profile glm /tmp/scratch
kommander --env-profile work -p "..."                    # launchers take them too, at the start
```

They apply on top of the playbook's own block, in command-line order, and obey
the same rules: `CLAUDE_CONFIG_DIR` refused, a missing profile refuses the
launch, an unset of the token switches this launch to the stored login.
`cpb run --help` lists them.

## The authentication case

Unsetting or setting `CLAUDE_CODE_OAUTH_TOKEN` through an env block or profile
changes which account a playbook runs as. The full decision and every mode are in
[Authentication](authentication.md).

## What stays yours

The block and the profiles are **install-local**, like `alias`. `update` keeps
your block and ignores one the source ships, and the source's block is never
live, not even during the update; `install` drops a source-shipped block with a
note; nothing ships profiles and `update` never touches their directory. A shared
playbook repository cannot redirect your API endpoint or strip your
authentication by publishing a manifest. Manifests holding `set` values are
written `0600`; a file's mode is never loosened by a rewrite.
