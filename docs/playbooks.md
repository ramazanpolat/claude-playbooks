# Managing playbooks

Creating, installing, linking, launching, renaming, updating and deleting them.

Most workflows start with either `create`, `install`, or `link`.

`cpb` reuses your existing Claude Code authentication for newly created,
installed, and linked playbooks, so a new playbook normally opens Claude Code
directly instead of asking you to log in again. How it does that depends on
whether you use a long-lived token; see [Authentication](authentication.md).

## Create your own playbook

Use `create` when you want a fresh isolated Claude Code setup.

```bash
cpb create experiment
experiment
```

This creates `~/.claude-playbooks/experiment`, drops in a starter `CLAUDE.md`
that introduces the playbook concept to the Claude Code session opened inside it,
syncs Claude auth metadata, and registers a launcher command named `experiment` —
a symlink to the `claude-playbook` binary on your PATH. It works immediately, in
every shell, with no rc-file edit and no reload. A `.playbook` manifest is only
written when you pick a custom command name with `--alias`.

You can also run it without the launcher:

```bash
cpb run experiment
```

Pass Claude Code flags after the playbook name:

```bash
cpb run experiment --model claude-opus-5 --permission-mode auto
cpb run --env-profile work experiment      # one launch with an env profile
```

Use a custom command name, or skip launcher creation:

```bash
cpb create backend --alias be
cpb create scratch --no-alias
```

## See what is installed

```bash
cpb list
```

```
NAME           PATH                                            COMMAND  LAST USED
experiment     ~/.claude-playbooks/experiment                  exp      2 days ago
awesome        ~/.claude-playbooks/awesome                     ap       2 hours ago
```

`cpb info <name>` prints one playbook's path, alias, env block and update source.

## Install a shared playbook repo

Use `install` when the playbook is in a Git repo or local directory and you want
a copied install under `~/.claude-playbooks`.

```bash
cpb install https://github.com/ramazanpolat/awesome-playbooks
```

Override the install name or launcher command:

```bash
cpb install https://github.com/user/awesome --name team-tools --alias tt
```

Install a local directory by copying it:

```bash
cpb install ~/dev/my-playbook
```

### Install one playbook from a larger repo

Use a GitHub tree URL when you want only one subdirectory:

```bash
cpb install https://github.com/user/awesome/tree/main/playbooks/dba
```

Or pass the subdirectory explicitly:

```bash
cpb install https://github.com/user/awesome --subdir playbooks/dba
```

Cherry-picked installs are flat top-level playbooks.

Branch names containing `/` are resolved against the repository's remote refs.
You can also make the boundary explicit with `--branch feature/name`.

Customize the name and alias:

```bash
cpb install https://github.com/user/awesome --subdir playbooks/dba --name dba --alias ap-dba
```

### Example: role-focused playbooks

To install a specific role configuration from a repository containing multiple
playbooks (like
[awesome-playbooks](https://github.com/ramazanpolat/awesome-playbooks)):

```bash
# Install the DBA playbook flat under your playbooks root:
cpb install https://github.com/ramazanpolat/awesome-playbooks --subdir playbooks/dba --name dba --alias ap-dba

# Or install the SRE playbook:
cpb install https://github.com/ramazanpolat/awesome-playbooks --subdir playbooks/sre --name sre --alias ap-sre
```

## Develop a playbook in place

Use `link` when you are actively editing a playbook outside
`~/.claude-playbooks` and want live changes.

```bash
cpb link ~/dev/my-playbook
cpb link ~/dev/my-playbook --name scratch --alias sc
cpb link ~/dev/my-playbook --no-alias
```

`link` creates a symlink under the playbooks root. Deleting a linked playbook
removes only the symlink. The source directory is preserved.

## Launcher commands

`create`, `install`, and `link` register each playbook as a **launcher
command**: a symlink to the `claude-playbook` binary placed next to it (falling
back to `~/.local/bin` when that directory is not writable):

```text
~/.local/bin/experiment -> /usr/local/bin/claude-playbook
```

When invoked through the link, the binary sees the link's name in `argv[0]` and
behaves as `cpb run <name>` — the multicall pattern used by busybox and git. The
name resolves against the live playbook registry (directory name first, then the
`.playbook` manifest's `alias`) **at invocation time**, so the launcher carries
no state that can go stale. Unlike shell aliases, launchers work identically from
any shell, are available immediately with no rc-file edit or reload, and are
visible to scripts and cron.

`delete` removes the launchers named for the playbook it is deleting and prints
`Removed command <name>`. It never removes a launcher another playbook still
claims, by spelling or, on a case-insensitive filesystem, by being the same
directory entry under another spelling; that one is kept and named. When the
registry cannot be scanned, the launcher is kept with a warning rather than
guessed about. Launchers are only ever written for the default playbooks root, so
a name nobody there claims serves nothing `cpb` made.

## Manage aliases

A playbook is addressed by its directory name and, optionally, one **alias** — an
alternate command name recorded in its `.playbook` manifest and materialized as a
launcher, so `cpb alias experiment exp` makes both `experiment` and `exp` work as
commands:

```bash
cpb alias                    # list every playbook's alias
cpb alias experiment         # show one
cpb alias experiment exp     # set (replaces any previous alias + launcher)
cpb alias experiment --remove
cpb dealias experiment       # same as --remove
```

Renaming with `cpb rename` keeps names, aliases, and launchers consistent
automatically; a launcher named by the alias keeps working across renames
untouched.

## Temporary sessions

Use `start` for a one-off Claude Code config directory without registering a
playbook:

```bash
cpb start /tmp/scratch
cpb start /tmp/scratch --model claude-opus-5
cpb start /tmp/scratch --delete
```

`--delete` removes the directory when the session ends, which is useful for
disposable experiments. Like the launch flags, it counts only before the path or
right after it; a `--delete` later in the line, after `--` or as a value for one
of `claude`'s own flags, goes to `claude` untouched.

## Rename and delete

```bash
cpb rename experiment lab
cpb rename lab experiment --alias exp
```

```bash
cpb delete experiment      # prompts for confirmation
cpb delete awesome -y      # skip confirmation
```

`uninstall` and `unlink` are command aliases for `delete`:

```bash
cpb uninstall awesome
cpb unlink my-linked-playbook
```

## Update

Update pulls the playbook from the source recorded in its `.playbook`:

```bash
cpb update awesome
cpb update awesome --check    # report the available version only
```

Git installs record their repository, branch, and selected subdirectory in
`.playbook`, and a flat, non-linked install updates natively from that source.
There is no delegated update script: the CLI owns the update.

The update replaces only the top-level entries the source itself ships, in place.
Runtime state the source knows nothing about — `data/`, `projects/`, `sessions/`,
`history.jsonl` — is never read, moved, or copied, so a session writing to it
during the update cannot lose work. Replaced entries are moved to a timestamped
`.<name>.bak.<stamp>` beside the install first, and rolled back if the overlay
fails.

Local configuration survives even when the source ships its own copy.
`settings.json`, `settings.local.json`, `.credentials.json` and `.claude.json`
are always restored over the incoming files; a playbook names anything further in
its manifest:

```toml
[update]
preserve = ["settings.json", "config/local.toml"]
```

New stock settings still arrive alongside (playbooks conventionally ship
`settings.json.template`) for you to merge by hand.

Afterwards, if the playbook ships an executable `migrations/apply.sh`, it runs as
`migrations/apply.sh <from-version> <to-version> <install-dir>` with the versions
taken from the old and new `.playbook`. Runners are expected to be idempotent.

Linked playbooks and manifests that select their config through a top-level
`subdir` cannot be updated this way.

### Update every playbook at once

Several installs of one playbook is the ordinary way to run one configuration
under different environments — and updating them one command at a time gets old
fast:

```bash
cpb update --all            # update them all
cpb update --all --check    # report what is available for each
```

```text
kommander            3.11.4 -> 3.13.0  ok
kommander-9router    3.11.4 -> 3.13.0  ok
kommander-dev        3.11.4 -> 3.13.0  ok
kommander-personal   3.13.0  up to date
lifeos               no [source] metadata

3 updated, 1 up to date, 1 skipped.
```

Each row is exactly what `cpb update <name>` would have done, with two
differences worth knowing:

- **A playbook already at the source's version is left alone.** `cpb update
  <name>` re-applies regardless, which is how you repair an install that drifted;
  doing that across every playbook would leave a backup directory per playbook
  per run for no change. Use the single-playbook form when you want a re-apply.
- **A failure doesn't stop the run.** The rest still update, the failing
  playbook's output is printed after the table, and the exit status is non-zero.

Playbooks that can't update natively — no `[source]`, linked, or a manifest
`subdir` layout — are listed as skipped with the reason rather than treated as
errors.

Running `cpb update` with no name self-updates the binary instead; see
[Installation](installation.md#updating-the-tool).

## Use temporary config locations

For tests or demos, keep playbooks away from your real files:

```bash
CLAUDE_PLAYBOOKS_DIR=/tmp/playbooks cpb create demo
```

The equivalent flag is:

```bash
cpb --playbooks-dir /tmp/playbooks create demo
```

Launcher commands are only managed for the default playbooks root
(`~/.claude-playbooks`), so a temporary root never touches your PATH or shell
files — the command prints how to run the playbook with an explicit
`--playbooks-dir` instead.

## Add a playbook's bin directory to PATH

Some playbooks ship CLI tools in a `bin/` directory. Add them to your PATH
manually:

```bash
# In ~/.zshrc
export PATH="$HOME/.claude-playbooks/experiment/bin:$PATH"
```

## Relationship to CLAUDE.md

Every playbook can have a `CLAUDE.md` file in its root directory. Claude Code
loads this file as standing instructions at the start of every session — your
rules, protocols, and context that apply to every conversation in that playbook.

This is separate from project-level `CLAUDE.md` files (which live in your project
directories and describe the project itself). Both are loaded simultaneously; the
playbook's `CLAUDE.md` defines *how you work*, the project's `CLAUDE.md` defines
*what you're working on*.
