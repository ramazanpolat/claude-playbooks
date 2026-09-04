# Claude Playbooks

[![CI](https://github.com/ramazanpolat/claude-playbooks/actions/workflows/ci.yml/badge.svg)](https://github.com/ramazanpolat/claude-playbooks/actions/workflows/ci.yml)

![claude-playbook demo](docs/demo.gif)

A **Claude Playbook** is an isolated instance of Claude Code.

That's it. Each playbook has its own configuration, settings, hooks, memory, and task history -- completely separate from your default `~/.claude` installation and from every other playbook.

## Why would I need this?

Claude Code stores everything in `~/.claude/`: your settings, conversation history, permissions, hooks, MCP servers. If you want to try something -- a different model, a custom hook, a new CLAUDE.md behavior -- you have to touch your main setup. One wrong change and your daily workflow breaks.

Playbooks solve this by giving each experiment (or workflow) its own isolated directory.

Common use cases:

- **Test a new hook or setting** without risking your main `~/.claude`
- **Separate work and personal** configurations that don't interfere
- **Run two Claude Code instances concurrently** on different tasks with different personalities
- **Authenticate with different accounts concurrently** (e.g., keep one playbook authenticated with your corporate account and another with your personal account)
- **Share a configuration** with your team by putting the playbook in a Git repo
- **Consume a repository** containing one or more playbook configurations (e.g. via subdirectories)

## How isolation works

Claude Code reads its configuration from the directory set in `CLAUDE_CONFIG_DIR` (defaults to `~/.claude`). Change that variable, and you get a completely fresh, independent instance:

```bash
# Your normal Claude Code (uses ~/.claude)
claude

# An isolated playbook (uses ~/.claude-playbooks/experiment)
CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude
```

That's all a playbook is under the hood. `claude-playbook` just makes creating, sharing, and managing them easy.

Since v3.5.0 a playbook can also isolate its **environment**: variables to set or unset for every launch of that playbook and no other, so one playbook talks to a proxy or keeps its own login while the rest of your shell does not. See [Environment overrides](#environment-overrides).

```
~/.claude-playbooks/                Launcher commands (on PATH):

├── experiment/                     ◄── ~/.local/bin/experiment -> claude-playbook
│   ├── CLAUDE.md                       (typing `experiment` runs this playbook)
│   └── settings.json
│
└── awesome/                        ◄── ~/.local/bin/ap -> claude-playbook
    ├── .playbook                       (marker + metadata; `alias = "ap"` names the command)
    └── CLAUDE.md

Each playbook directory is a completely isolated Claude Code instance.
```

A directory is a playbook if it exists under the playbooks root. A `.playbook` manifest file is optional and used for storing metadata (like version, author, description).

## Installation

```bash
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/install.sh | sh
```

The script detects your OS and architecture, downloads the right binary from the latest GitHub Release, verifies it against the release's `SHA256SUMS`, and installs it to `/usr/local/bin` (or `~/.local/bin` if that's not writable). Linux and macOS, amd64/arm64 (no native Windows — WSL works).

Verify:

```bash
claude-playbook --version
```

The installer also creates a `cpb` symlink -- a shorter name for the same
binary:

```bash
cpb --version
```

Want a different command name? Use a shell alias (`alias pb=claude-playbook`)
or a hard link (`ln "$(command -v claude-playbook)" ~/.local/bin/pb` — works
for both install locations). Do not use a symlink: a symlink to the binary
under any other name is treated as a playbook launcher and dispatched
accordingly.

The installer never edits your shell rc files. To enable completions
(optional), add one line to your rc file yourself:

```bash
echo 'source <(cpb completion zsh)'  >> ~/.zshrc     # zsh
echo 'source <(cpb completion bash)' >> ~/.bashrc    # bash
```

You can also clone the repo and run the installer locally:

```bash
git clone https://github.com/ramazanpolat/claude-playbooks.git
cd claude-playbooks
./install.sh
```

Uninstall only the binary:

```bash
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/uninstall.sh | sh
```

Or run the local uninstaller from a clone:

```bash
./uninstall.sh
```

The script delegates to `claude-playbook self-uninstall --binary-only`, so
one implementation owns all cleanup: the binary, its `cpb` sibling, launcher
symlinks, and any completion lines you added. Every launcher the tool creates
is recorded in a registry (`~/.local/state/claude-playbook/launchers`), so
launchers are removed wherever they were created — including custom
`--launcher-dir` locations — while a link you renamed or repointed yourself
is left alone. Playbooks are untouched, and `~/.claude-playbooks` is never
deleted.

### Uninstalling claude-playbook itself

To remove the tool, all its installed playbooks, their launcher commands,
the completion lines in your rc files, and the binary in one step:

```bash
claude-playbook self-uninstall          # prompts for confirmation
claude-playbook self-uninstall -y       # skip prompt
claude-playbook self-uninstall -y --keep-data     # keep ~/.claude-playbooks
claude-playbook self-uninstall -y --keep-binary   # keep the binary
claude-playbook self-uninstall --dry-run          # preview without removing
```

If the binary can't be removed (e.g. installed to `/usr/local/bin` and you're
not root), the command prints a `sudo rm <path>` hint and continues cleaning up
everything else.

**Manual fallback** (if you can't run the binary):

```bash
# 1. Remove launcher symlinks pointing at the binary
#    (in the binary's directory and ~/.local/bin: ls -l | grep claude-playbook)
# 2. Remove any `source <(claude-playbook completion ...)` lines from your
#    shell config (~/.zshrc or ~/.bashrc)
# 3. rm -rf ~/.claude-playbooks
# 4. sudo rm /usr/local/bin/claude-playbook   # or wherever the binary lives
```

**Build from source** (requires [Go](https://go.dev/dl/) 1.21+):

```bash
git clone https://github.com/ramazanpolat/claude-playbooks.git
cd claude-playbooks
./build.sh
mv claude-playbook /usr/local/bin/
```

## Usage

Most workflows start with either `create`, `install`, or `link`.

`claude-playbook` reuses your existing Claude Code authentication for newly created, installed, and linked playbooks, so a new playbook normally opens Claude Code directly instead of asking you to log in again. How it does that depends on whether you use a long-lived token; see [Authentication](#authentication) for the decision and the per-playbook choices.

Driving `claude-playbook` from an agent rather than a shell? Read [docs/AGENT-GUIDE.md](docs/AGENT-GUIDE.md).

### Authentication

At every launch `claude-playbook` first decides whether a **long-lived token** is active for that playbook, then prepares the config directory accordingly. Nothing else in the tool touches credentials.

```text
token active for this playbook?
  = the playbook's [env] (or a profile it uses) sets CLAUDE_CODE_OAUTH_TOKEN
    or the shell exports CLAUDE_CODE_OAUTH_TOKEN
    or ~/.config/claude-code/oauth-token is non-empty
  and the playbook's [env] does not unset it

yes  ->  inject the token; remove the playbook's own stored login (claudeAiOauth only,
         MCP logins survive); sync non-secret account metadata so the dir presents as logged in
no   ->  link the playbook's .credentials.json to ~/.claude/.credentials.json; remove nothing;
         Claude Code refreshes the shared login itself
isolate_auth = true  ->  neither: detach from the shared store, strip the global token,
         keep only what this playbook logs in itself
```

The removal on the token path exists for one reason: under token auth Claude Code never refreshes a stored login, and its 401-recovery path adopts a stored login over the token. A stale stored login would therefore replace a working year-long token with a dead one on the first transient 401. Removing it leaves nothing to adopt. It is never done on the no-token path, where that stored login *is* the session.

The modes this gives you, per playbook:

| You want | Do | At launch |
|---|---|---|
| Everything shares one login, no token | nothing (no `oauth-token` file) | credentials symlinked to `~/.claude`; `/login` anywhere logs in everywhere |
| Everything shares one long-lived token | `claude setup-token` once | token injected everywhere; each playbook's own login removed |
| One playbook keeps its own `/login` while the others use the token | `claude-playbook env <name> unset CLAUDE_CODE_OAUTH_TOKEN` | that playbook takes the no-token path; the rest unchanged |
| One playbook uses its own token | `claude-playbook env <name> set CLAUDE_CODE_OAUTH_TOKEN=...` | that token wins over the file; its own login removed |
| One playbook is a different account, sharing nothing | `isolate_auth = true` in its `.playbook`, or `CLAUDE_PLAYBOOKS_ISOLATE_AUTH=true` | detached; log in there once; add `set CLAUDE_CODE_OAUTH_TOKEN` for a per-account token |

The unset and set forms can come from an [env profile](#env-profiles-define-once-attach-to-many) shared by several playbooks.

Two things to know about the shared-login mode. Claude Code namespaces its macOS Keychain entry per config directory and refreshes the OAuth grant from whichever directory hits expiry first; with many playbooks sharing one symlinked file, two concurrent refreshes can race and the loser's `invalid_grant` empties the shared file, logging every playbook out at once. That race is why the long-lived token path exists. And raw `claude` launches bypass all of the above: only launchers, `run`, and `start` prepare authentication.

### Create and run your own playbook

Use `create` when you want a fresh isolated Claude Code setup.

```bash
claude-playbook create experiment
experiment
```

This creates `~/.claude-playbooks/experiment`, drops in a starter `CLAUDE.md` that introduces the playbook concept to the Claude Code session opened inside it, syncs Claude auth metadata, and registers a launcher command named `experiment` — a symlink to the `claude-playbook` binary on your PATH. It works immediately, in every shell, with no rc-file edit and no reload. A `.playbook` manifest is only written when you pick a custom command name with `--alias`.

You can also run it without the launcher:

```bash
claude-playbook run experiment
```

Pass Claude Code flags after the playbook name:

```bash
claude-playbook run experiment --model claude-opus-5 --permission-mode auto
```

Use a custom command name, or skip launcher creation:

```bash
claude-playbook create backend --alias be
claude-playbook create scratch --no-alias
```

### See what is installed

```bash
claude-playbook list
```

```
NAME           PATH                                            COMMAND  LAST USED
experiment     ~/.claude-playbooks/experiment                  exp      2 days ago
awesome        ~/.claude-playbooks/awesome                     ap       2 hours ago
```

### Install a shared playbook repo

Use `install` when the playbook is in a Git repo or local directory and you want a copied install under `~/.claude-playbooks`.

Install a repo:

```bash
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks
```

Override the install name or launcher command:

```bash
claude-playbook install https://github.com/user/awesome --name team-tools --alias tt
```

Install a local directory by copying it:

```bash
claude-playbook install ~/dev/my-playbook
```

### Install one playbook from a larger repo

Use a GitHub tree URL when you want only one subdirectory:

```bash
claude-playbook install https://github.com/user/awesome/tree/main/playbooks/dba
```

Or pass the subdirectory explicitly:

```bash
claude-playbook install https://github.com/user/awesome --subdir playbooks/dba
```

Cherry-picked installs are flat top-level playbooks.

Branch names containing `/` are resolved against the repository's remote refs. You can also make the boundary explicit with `--branch feature/name`.

Customize the name and alias:

```bash
claude-playbook install https://github.com/user/awesome --subdir playbooks/dba --name dba --alias ap-dba
```

### Develop a playbook in place

Use `link` when you are actively editing a playbook outside `~/.claude-playbooks` and want live changes.

```bash
claude-playbook link ~/dev/my-playbook
```

`link` creates a symlink under the playbooks root.

```bash
claude-playbook link ~/dev/my-playbook --name scratch --alias sc
claude-playbook link ~/dev/my-playbook --no-alias
```

Deleting a linked playbook removes only the symlink. The source directory is preserved.

### Launcher commands

`create`, `install`, and `link` register each playbook as a **launcher command**: a symlink to the `claude-playbook` binary placed next to it (falling back to `~/.local/bin` when that directory is not writable):

```text
~/.local/bin/experiment -> /usr/local/bin/claude-playbook
```

When invoked through the link, the binary sees the link's name in `argv[0]` and behaves as `claude-playbook run <name>` — the multicall pattern used by busybox and git. The name resolves against the live playbook registry (directory name first, then the `.playbook` manifest's `alias`) **at invocation time**, so the launcher carries no state that can go stale. Unlike shell aliases, launchers work identically from any shell, are available immediately with no rc-file edit or reload, and are visible to scripts and cron.

`delete` and `rename` never silently remove a launcher another playbook (or another playbooks root) might still be using: a name that no longer resolves is kept with an explicit `rm <path>` hint, and invoking a stale launcher fails loudly with "unknown playbook". `rename` registers the new name; a launcher named by a manifest alias keeps working across renames untouched.

### Manage aliases

A playbook is addressed by its directory name and, optionally, one **alias** — an alternate command name recorded in its `.playbook` manifest and materialized as a launcher, so `cpb alias experiment exp` makes both `experiment` and `exp` work as commands:

```bash
claude-playbook alias                    # list every playbook's alias
claude-playbook alias experiment         # show one
claude-playbook alias experiment exp     # set (replaces any previous alias + launcher)
claude-playbook alias experiment --remove
claude-playbook dealias experiment       # same as --remove
```

Renaming with `cpb rename` keeps names, aliases, and launchers consistent automatically; a launcher named by the alias keeps working across renames untouched.

### Environment overrides

A fresh install has none. This manifest is complete and normal:

```toml
version = "3.11.4"
name = "kommander"
alias = "k"

[source]
repository = "https://github.com/ramazanpolat/kommander-playbook"
```

Launching `k` runs `claude` with your shell's environment plus `CLAUDE_CONFIG_DIR`, exactly as before. Nothing changes until you add an override.

#### What happens at launch

Every launch of a playbook (its launcher command, `run`, or `start` at its directory) builds the child `claude` process's environment in layers, later layers winning:

```text
your shell's environment
  + each env profile the playbook uses, in the order listed     (~/.claude-playbooks/.env-profiles/<name>.toml)
  + the playbook's own [env.set]                                 (in its .playbook)
  - the playbook's own [env] unset
  + CLAUDE_CONFIG_DIR, bound by the tool, cannot be overridden
  = what claude sees
```

`set` overrides whatever the shell exported; `unset` removes a variable even when the shell exports it. Raw `claude` launches bypass all of this. Claude Code's own `env` block in `settings.json` is applied later, inside the `claude` process, and wins over these layers; it can set variables but cannot unset one the shell exported, which is what the manifest block is for.

#### One playbook, its own overrides

```bash
claude-playbook env kommander set ANTHROPIC_MODEL=claude-opus-5
claude-playbook env kommander unset CLAUDE_CODE_OAUTH_TOKEN
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
claude-playbook env kommander                        # show this playbook's block
claude-playbook env                                  # every playbook that declares overrides
claude-playbook env kommander clear ANTHROPIC_MODEL  # forget the entry; the shell's value applies again
claude-playbook info kommander                       # "Env:" lines appear when a block exists
```

```text
Environment overrides for "kommander":
  set    ANTHROPIC_MODEL=claude-opus-5
  unset  CLAUDE_CODE_OAUTH_TOKEN
```

#### Env profiles: define once, attach to many

When several playbooks want the same overrides, put them in a **profile**: a named file under `~/.claude-playbooks/.env-profiles/`, managed with `env-profile`, attached to playbooks by name with `env <playbook> use`.

```bash
claude-playbook env-profile glm set ANTHROPIC_BASE_URL=http://proxy:1/v1 ANTHROPIC_DEFAULT_OPUS_MODEL=glm/glm-5.3
claude-playbook env-profile glm unset CLAUDE_CODE_OAUTH_TOKEN
claude-playbook env-profile glm describe "GLM 5.3 through the local router, own /login"
```

That wrote `~/.claude-playbooks/.env-profiles/glm.toml` (mode `0600`, values may be secrets):

```toml
description = "GLM 5.3 through the local router, own /login"
unset = ["CLAUDE_CODE_OAUTH_TOKEN"]

[set]
ANTHROPIC_BASE_URL = "http://proxy:1/v1"
ANTHROPIC_DEFAULT_OPUS_MODEL = "glm/glm-5.3"
```

Attach it. The playbook's manifest records only the name:

```bash
claude-playbook env router use glm
claude-playbook env router set ANTHROPIC_DEFAULT_OPUS_MODEL=glm/glm-5.4   # local entry on top of the profile
claude-playbook env router
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

Profiles apply in the order listed, later ones overriding earlier, and the playbook's own entries apply last. Manage them:

```bash
claude-playbook env-profile                 # list profiles, descriptions, which playbooks use each
claude-playbook env-profile glm             # show one
claude-playbook env router unuse glm        # detach
claude-playbook env-profile glm delete      # refused while any playbook still uses it
```

A profile that a playbook names but that is missing, unreadable, or invalid **refuses the launch** rather than silently running without it: a dropped layer could send traffic to the wrong endpoint with the wrong credentials.

#### The authentication case

If you use a long-lived token (`claude setup-token`, stored at `~/.config/claude-code/oauth-token`), every playbook launch injects it as `CLAUDE_CODE_OAUTH_TOKEN` and removes the playbook's own stored login so a transient 401 cannot swap the working token for a dead one. That is right for most playbooks and wrong for one that must use a different account or a proxy that does not want the token.

Unsetting `CLAUDE_CODE_OAUTH_TOKEN` for a playbook, directly or through a profile, does more than drop the variable: the token is treated as inactive for that playbook, so the launch takes the stored-credentials path. No token is injected, the playbook's own login is left alone, and the shared credentials are synced. `/login` once there and it sticks, while every other playbook keeps using the token. Setting the variable instead supplies a per-playbook token that wins over the machine-global file. This is a middle ground between sharing the token and `isolate_auth` (the playbook shares nothing). The full decision and every mode are in [Authentication](#authentication).

#### What stays yours

The block and the profiles are **install-local**, like `alias`. `update` keeps your block and ignores one the source ships, and the source's block is never live, not even during the update; `install` drops a source-shipped block with a note; nothing ships profiles and `update` never touches their directory. A shared playbook repository cannot redirect your API endpoint or strip your authentication by publishing a manifest. Manifests holding `set` values are written `0600`; a file's mode is never loosened by a rewrite.

### Temporary sessions

Use `start` for a one-off Claude Code config directory without registering a playbook:

```bash
claude-playbook start /tmp/scratch
claude-playbook start /tmp/scratch --model claude-opus-5
claude-playbook start /tmp/scratch --delete
```

`--delete` removes the directory when the session ends, which is useful for disposable experiments.

### Rename, delete, and update

Rename a playbook:

```bash
claude-playbook rename experiment lab
claude-playbook rename lab experiment --alias exp
```

Delete a playbook:

```bash
claude-playbook delete experiment      # prompts for confirmation
claude-playbook delete awesome -y      # skip confirmation
```

`uninstall` and `unlink` are command aliases for `delete`:

```bash
claude-playbook uninstall awesome
claude-playbook unlink my-linked-playbook
```

Update pulls the playbook from the source recorded in its `.playbook`:

```bash
claude-playbook update awesome
claude-playbook update awesome --check    # report the available version only
```

Git installs record their repository, branch, and selected subdirectory in `.playbook`, and a flat, non-linked install updates natively from that source. There is no delegated update script: the CLI owns the update.

The update replaces only the top-level entries the source itself ships, in place. Runtime state the source knows nothing about — `data/`, `projects/`, `sessions/`, `history.jsonl` — is never read, moved, or copied, so a session writing to it during the update cannot lose work. Replaced entries are moved to a timestamped `.<name>.bak.<stamp>` beside the install first, and rolled back if the overlay fails.

Local configuration survives even when the source ships its own copy. `settings.json`, `settings.local.json`, `.credentials.json` and `.claude.json` are always restored over the incoming files; a playbook names anything further in its manifest:

```toml
[update]
preserve = ["settings.json", "config/local.toml"]
```

New stock settings still arrive alongside (playbooks conventionally ship `settings.json.template`) for you to merge by hand.

Afterwards, if the playbook ships an executable `migrations/apply.sh`, it runs as `migrations/apply.sh <from-version> <to-version> <install-dir>` with the versions taken from the old and new `.playbook`. Runners are expected to be idempotent.

Linked playbooks and manifests that select their config through a top-level `subdir` cannot be updated this way.

With **no** name, `update` self-updates the `claude-playbook` binary itself to the latest GitHub release:

```bash
claude-playbook update            # download + install the latest release
claude-playbook update --check    # report the latest version without installing
claude-playbook update --force    # reinstall even if already on the latest
```

It downloads the release asset for your OS/architecture, verifies it, and atomically replaces the running binary (resolving the `cpb` symlink so the real binary is updated). If the install directory needs elevated privileges to write, it says so.

### Use temporary config locations

For tests or demos, keep playbooks away from your real files:

```bash
CLAUDE_PLAYBOOKS_DIR=/tmp/playbooks claude-playbook create demo
```

The equivalent flag is:

```bash
claude-playbook --playbooks-dir /tmp/playbooks create demo
```

Launcher commands are only managed for the default playbooks root
(`~/.claude-playbooks`), so a temporary root never touches your PATH or shell
files — the command prints how to run the playbook with an explicit
`--playbooks-dir` instead.

### Add a playbook's bin directory to PATH

Some playbooks ship CLI tools in a `bin/` directory. Add them to your PATH manually:

```bash
# In ~/.zshrc
export PATH="$HOME/.claude-playbooks/experiment/bin:$PATH"
```

## Relationship to CLAUDE.md

Every playbook can have a `CLAUDE.md` file in its root directory. Claude Code loads this file as standing instructions at the start of every session -- your rules, protocols, and context that apply to every conversation in that playbook.

This is separate from project-level `CLAUDE.md` files (which live in your project directories and describe the project itself). Both are loaded simultaneously; the playbook's `CLAUDE.md` defines *how you work*, the project's `CLAUDE.md` defines *what you're working on*.

## Example: role-focused playbooks

To install a specific role configuration from a repository containing multiple playbooks (like [awesome-playbooks](https://github.com/ramazanpolat/awesome-playbooks)):

```bash
# Install the DBA playbook flat under your playbooks root:
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks --subdir playbooks/dba --name dba --alias ap-dba

# Or install the SRE playbook:
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks --subdir playbooks/sre --name sre --alias ap-sre
```

## Release process

GitHub releases are created from `v*` tags only when the tagged commit is already on `main`. Tags pushed from feature branches are ignored by the release workflow.

```bash
git checkout main
git pull --ff-only
git tag -a vX.Y.Z -m vX.Y.Z
git push origin vX.Y.Z
```

## License

MIT
