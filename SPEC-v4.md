# claude-playbook CLI Specification (v4)

## Overview

`claude-playbook` is a CLI tool for creating and managing **Claude Code playbooks**. A playbook is an isolated Claude Code instance — a directory with its own settings, CLAUDE.md, hooks, MCP servers, and history, completely separate from the default `~/.claude/` installation and from every other playbook.

Playbooks solve a simple problem: Claude Code stores everything in a single config directory. If you want to try a new hook, a different model default, or a custom CLAUDE.md without risking your main setup, you need a separate environment. Under the hood, a playbook is just a directory, and Claude Code reads from wherever `CLAUDE_CONFIG_DIR` points. `claude-playbook` makes creating, running, sharing, and maintaining those directories easy.

---

## Concepts

### Isolation

Every playbook is a directory that Claude Code treats as its entire configuration root. Launching Claude Code with `CLAUDE_CONFIG_DIR=<dir>` produces a completely fresh, independent instance.

```bash
# Default Claude Code
claude

# An isolated playbook
CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude
```

`claude-playbook` is a thin convenience layer over this pattern.

### Authentication preparation

Every launch (`run`, `start`, launcher dispatch) prepares the config directory's authentication before exec, in `auth.PrepareLaunchEnv`. "The config directory" is whatever the launch bound: the playbook's install directory, `start`'s path argument, or a caller-supplied `CLAUDE_CONFIG_DIR_OVERRIDE` (see *Environment overrides*). The decision below is identical in every case -- a supplied directory is judged, synced and quarantined exactly as a playbook's own would be, which is what makes it usable as a private state directory. The decision, in order:

1. **Isolation wins.** If the nearest valid manifest walking up from the config directory has `isolate_auth = true`, or `CLAUDE_PLAYBOOKS_ISOLATE_AUTH=true` is set: detach a symlinked `.credentials.json` (the target file survives), sync no account metadata, strip `CLAUDE_CODE_OAUTH_TOKEN` and the plan descriptors (`CLAUDE_CODE_SUBSCRIPTION_TYPE`, `CLAUDE_CODE_RATE_LIMIT_TIER`) from the child environment. A `CLAUDE_CODE_OAUTH_TOKEN` the playbook's own flattened `[env]` sets is honoured as its own token: injected, with the stored `claudeAiOauth` grant quarantined as on the token path. When the isolated playbook holds no login of its own (no grant in its store) and its block sets no token, the Anthropic account state in its `.claude.json` is removed as well: `oauthAccount`, `cachedGrowthBookFeatures`, `cachedGrowthBookFeaturesAt`, `cachedExperimentFeatures`, `cachedExperimentData`, `passesEligibilityCache`, `cachedExtraUsageDisabledReason`. Rationale: Claude Code enables its claude.ai-hosted tools (Artifact and friends) from those cached flags regardless of `ANTHROPIC_BASE_URL`, so a playbook routed to another backend that once ran as the global account keeps sending them; since Claude Code 2.1.265 the Artifact tool's schema is rejected by at least one such backend (GLM, `400` code `1210`) and every interactive turn fails. A playbook that logs in on its own regenerates the state, so nothing of value is lost; an unreadable `.claude.json` leaves it in place and is reported as an advisory warning. `auth status` shows the pending removal as `no login; stale account state, purged at launch`, judged as the launch will see it: a grant reached only through a symlinked shared store does not count, since isolation detaches that link first (`stale account state, purged at launch (the shared login is detached at launch)`). A store that cannot be read or parsed leaves the state in place with an advisory warning, since it may hold a login. The rewrite keeps the file's owner permissions masked to `0600`, never widening a read-only state file.
2. **Token active?** True when the flattened `[env]` sets `CLAUDE_CODE_OAUTH_TOKEN` to a non-empty value, else when the process environment carries it, else when `~/.config/claude-code/oauth-token` (overridable via `CLAUDE_PLAYBOOKS_OAUTH_TOKEN_FILE`) is non-empty; false when the flattened `[env]` unsets it, regardless of the rest.
3. **Token path.** The plan descriptors (`CLAUDE_CODE_SUBSCRIPTION_TYPE`, `CLAUDE_CODE_RATE_LIMIT_TIER`) are appended from the global account's store only when the token IS the machine-global one, read from the token file by this launch, and only for the descriptors the shell did not already export: an explicit export is the operator's deliberate act and outranks what is inferred from disk (a Team seat needs exactly that, its real `subscriptionType` is not a value the picker accepts). A token the flattened `[env]` block supplies (`own-token`) belongs to some other account, so the global descriptors are neither appended nor inherited (the block may set its own); a token inherited from the shell is of unknown provenance, so nothing is appended and whatever descriptors the shell exported beside it pass through unchanged. Then: quarantine the playbook's stored `claudeAiOauth` grant (other keys such as `mcpOAuth` survive; a symlinked store is detached, never written through; the global store is never quarantined — identity is decided with symlinks resolved and `os.SameFile` for the directory and for the credentials file itself, so `start <symlink-to-~/.claude>`, a linked registration of it, or a config directory whose store is the target of a symlinked `~/.claude/.credentials.json` are all recognised as the global store), sync non-secret account metadata into `.claude.json` so an interactive start presents as logged in, and inject the token (replacing any inherited entry); the descriptor rule above then applies. Rationale: under token auth Claude Code never refreshes a stored grant, and its 401-recovery path adopts a differing stored `accessToken` over the environment token; an expired stored grant would replace a working token with a dead one.
4. **No-token path.** `SyncCredentials`: the playbook's `.credentials.json` becomes a symlink to the global `~/.claude/.credentials.json` (an existing regular file with a valid grant is left alone; a grantless store never overwrites the global one; a target that is the global directory by identity, or whose store is the global file itself, is left untouched), so `/login` in any playbook is visible to all. Nothing is removed. Claude Code refreshes the shared grant itself.

Every preparation failure is advisory (a warning, then launch) except a profile that cannot be resolved, which refuses the launch before step 1. Raw `claude` invocations bypass this entirely.

### The filesystem is the source of truth

There is no index file, no database, no registry file. The state the tool reads from or writes to is: the playbooks root directory (including the optional `.playbook` manifests inside it, where a custom command alias is recorded, and the dot-prefixed `.env-profiles/` directory holding shared env profiles), the launcher directory (symlinks to the binary that serve as per-playbook commands), a flock lock file in the user cache dir (`<cache>/claude-playbook/registry.lock`, falling back to `<tmp>/claude-playbook-registry-<uid>.lock` when no cache directory can be resolved or created; used only to serialize concurrent mutations — it holds no data). Any mutation users make with `mv`, `rm`, or a text editor is immediately consistent with what the tool sees on its next invocation.

### The playbooks root

All playbooks live under a single **playbooks root** directory. The default is `~/.claude-playbooks/`. This is configurable via the `--playbooks-dir` flag or the `CLAUDE_PLAYBOOKS_DIR` environment variable, applied globally to every command.

### Playbook discovery

Discovery is a single, flat rule:

- **Each direct child directory of the playbooks root is exactly one playbook.**

That is the whole rule. There is no nesting, no groups, no "container" type, no depth limit to reason about, and no manifest declaration that changes discovery. A directory does not need a `.playbook` file to be a playbook; if one is present it supplies metadata only.

```
~/.claude-playbooks/
    experiment/                 ← playbook (no manifest needed)
        CLAUDE.md
    sre/                        ← playbook (installed from a monorepo subdir)
        .playbook               ← optional metadata
        CLAUDE.md
        settings.json
    dba/                        ← playbook
        CLAUDE.md
```

Directories nested more deeply than the first level are just ordinary files belonging to a playbook — they are never themselves discovered as playbooks. If you drop a whole monorepo into the root by hand, the tool sees one playbook (the top directory), not the playbooks inside it; use `install --subdir` to extract the slice you actually want.

### Playbook names

A playbook's **name** is simply its directory name under the playbooks root:

- `experiment`
- `sre`
- `dba`

Names are used wherever a playbook is referenced: `run`, `delete`, `info`, `rename`, `alias`, `env`, `auth status`, `update`. Env profile names are a separate namespace (`env-profile`).

The charset is enforced for names being **created** (`create`, `install`, `link`, `rename`): a name must match `^[A-Za-z0-9_][A-Za-z0-9_-]*$` — letters, digits, underscores and dashes, starting with an alphanumeric or underscore. A playbook name is interpolated into a launcher command name, a `run <name>` argument, and commands printed for the user to paste, so shell metacharacters are rejected at the front door rather than escaped at each site. Names must not start with `.` (to avoid hidden directories) and must not contain `/` or `\` (names are single directory segments, never paths). Lookup paths (`delete`, discovery) only require a single path segment, so an existing playbook with an odd name can still be listed, run and removed.

---

## Launcher Commands (v2.13.0)

Since v2.13.0, per-playbook commands are **launchers** — symlinks to the `claude-playbook` binary placed in a PATH directory — replacing the shell-alias registration of earlier releases. `create`, `install`, and `link` register one.

- **Multicall dispatch.** Invoked through a launcher, the binary sees the link's name in argv[0] and dispatches as `run <name>` (the busybox/git pattern). The launcher carries no state: the name resolves at invocation time against the live registry — playbook directory names first, then manifest `alias` fields — so nothing goes stale on rename or move.
- **Launcher directory.** `--launcher-dir` / `CLAUDE_LAUNCHER_DIR`, else the directory the binary was invoked from (on PATH by construction), falling back to `~/.local/bin` when that is unwritable.
- **Default root only.** Launcher mutations happen only when operating on the default playbooks root (`~/.claude-playbooks`). A symlink carries no root identity, so managing links on behalf of a custom `--playbooks-dir` root would corrupt the default registry's commands; under a custom root the tool prints a note and the `claude-playbook --playbooks-dir <root> run <name>` form instead.
- **Reserved names.** `claude-playbook` and `cpb` always mean the CLI itself; they never dispatch and may never name a launcher.
- **Collisions and locking.** The registry is the ownership authority: a command name that already addresses another playbook (by directory name or manifest alias) is a hard error before any mutation. Preflight-through-registration is serialized across concurrent processes by a flock in the user cache dir (`<cache>/claude-playbook/registry.lock`).
- **Retirement rule.** `delete` and `rename` remove a launcher named for the playbook going away (its name, its manifest alias, or a name a rename leaves behind), receipt line included, printing `Removed command "x"`, unless another playbook still claims the name: by spelling in the registry, or by directory-entry identity on a case-insensitive filesystem (`cpb create one --alias Foo` and `cpb create two --alias foo` share one entry). A claimed launcher is kept outright (`Kept command "x" (still addresses playbook "y")`); when the registry cannot be scanned the launcher is kept with a warning, since ownership could not be verified. The rule rests on the launcher gate: the tool only ever writes launchers for the default registry root, so a name nobody in that registry claims serves nothing the tool made. A hand-made link named for the playbook goes with it; it would only fail loudly as stale afterwards.
- **Stale launchers fail loudly.** Invoking a launcher whose name no longer resolves errors with `unknown playbook "<name>" — this launcher no longer matches any playbook` and exit code 1, never a silent fall-through to the CLI overview.
- **Foreign files are never touched.** A file occupying a launcher name that is not a symlink to this binary is left alone; attempting to write over it degrades to a warning with manual instructions.

---

## Distribution

The binary reaches a machine by one of four routes. All of them land the same
artifact: a `claude-playbook` executable and a relative `cpb` symlink beside it,
in one directory on `PATH`.

| Route | Entry point | Install directory |
|---|---|---|
| Install script | `install.sh`, piped from the raw repository URL or run from a clone | `$INSTALL_DIR`, else `/usr/local/bin` when writable, else `~/.local/bin` |
| npm / npx | the `cpb-cli` package, whose `bin` entries both point at `bin/npx-shim.sh` | `~/.local/bin` only |
| Source | `build.sh`, then a manual `mv` | wherever the operator puts it |
| devbox / Nix | `flake.nix`, as `git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/<tag>#claude-playbook` | the Nix store, reached through the devbox (or `nix profile`) profile's `bin` |

Neither script edits a shell rc file. Completion lines are printed for the
operator to add, never appended. `cpb` is created as a **relative** symlink to
`claude-playbook`: a symlink to the binary under any other name is dispatched as
a playbook launcher (see *Launcher Commands*), so the short name is the one
exception the binary recognises as itself, together with `claude-playbook`.

### The flake (devbox / Nix)

`flake.nix` builds `claude-playbook` **from source** at the pinned ref with
`buildGoModule` (`CGO_ENABLED=0`, so a static binary whose runtime closure is
data only: tzdata, iana-etc, mailcap), for `x86_64`/`aarch64` × `linux`/`darwin`.
It never fetches release binaries: a tagged commit cannot carry the hashes of
binaries built after it was tagged. The version stamped in is `v` + the
`package.json` version, which `release.yml` requires to equal the tag, so a
**tag** is the ref to pin; a commit between releases reports the previous
release's version. `vendorHash` must be recomputed whenever `go.mod`/`go.sum`
change. The flake's `nixpkgs` input affects only the build, never a user's
profile. `.github/workflows/nix.yml` builds it on Linux and macOS and adds it to
a fresh devbox project by a real `git+https:` reference pinned to the commit.

The documented reference form is `git+https://…?ref=refs/tags/<tag>`, not
`github:…/<tag>`: a `github:` reference is resolved through GitHub's REST API,
which rate-limits unauthenticated callers per IP, and behind a shared IP both
`nix` and `devbox` then fail with HTTP 403 (devbox supplies no token for it).
`git+https` fetches over git, makes no API call, and devbox.lock records the
tag's exact `rev`.

Launchers written from a devbox-installed binary target the path as invoked (the
profile's stable `bin` entry), never the versioned store path, and an unwritable
binary directory falls back to `~/.local/bin` as for any read-only install.

### Release asset naming

Both downloading routes resolve the same asset name, `<prefix>-<os>-<arch>`,
with `os` from `uname -s` lowercased (`darwin` or `linux`; anything else is
refused, native Windows explicitly so in the shim, which names WSL) and `arch`
from `uname -m` mapped `x86_64`→`amd64`, `aarch64`/`arm64`→`arm64`. The asset is
fetched from `<download base>/<tag>/<asset>`.

### Checksum policy

Shared verbatim by `install.sh` and the npx shim. After downloading the asset to
a temporary file, the release's `SHA256SUMS` is fetched from the same base URL
and the asset's line located by name (`hash  name` and `hash *name` both parsed,
comparison lowercased since sums files may carry uppercase hex).

- **A hash that is present, well-formed, and different aborts the install**, printing
  the expected and actual digests. The temporary file is removed by the trap.
- **Every unverifiable case warns and continues**: no `SHA256SUMS` published for
  the tag, the asset not listed in it, a malformed or duplicated entry, no
  `sha256sum` or `shasum` on the machine, and — for `install.sh` — an
  `INSTALL_URL` override in use.

The asymmetry is deliberate and is the policy's whole content: the sums travel
over the same channel as the binary, so they establish that what arrived is what
was published, not that the publisher is honest. They guard against corruption
and truncation, not a compromised host. A malformed entry is therefore treated as
*unverifiable*, never as a mismatch, so a truncated sums file cannot fail a
legitimate binary.

The download is written to a `mktemp` file inside the destination directory (the
shim) or under `$TMPDIR` (`install.sh`), made executable, and moved into place
only after verification, so an aborted install never leaves a partial binary
where the previous one stood.

### `install.sh`

Resolves the release tag from `$VERSION` when set, else the `tag_name` of the
latest release from the GitHub API. Chooses the install directory as in the table
above — the `/usr/local/bin` probe is a writability test, so an unprivileged run
falls back to `~/.local/bin` rather than failing or escalating; the script never
invokes `sudo`. It then reports the install path, warns when that directory is
not on `PATH` (printing the `export` line to add), and prints the optional
completion lines.

### npx shim

`bin/npx-shim.sh` is the `bin` entry for both `claude-playbook` and `cpb` in the
`cpb-cli` package. It has three modes.

1. **Delegate.** An installed `cpb` or `claude-playbook` found on `PATH` is
   `exec`'d with the original arguments. Nothing is downloaded and no version is
   negotiated: the installed binary is the source of truth, updated with `update`.
   The search resolves symlinks on both sides and skips any candidate that is
   this script, because under npx the package's own `bin` directory sits first on
   `PATH` and a naive lookup would find the shim and loop forever.
2. **Bootstrap** (the default when nothing is installed). Downloads and verifies
   as above, installs to `~/.local/bin` — never `/usr/local/bin`, never `sudo` —
   creates the `cpb` link, announces the install and the `self-uninstall
   --keep-data` command that reverses it, warns when the directory is not on
   `PATH`, and `exec`s the binary. Afterwards the machine holds an ordinary
   install and later npx invocations take route 1.
3. **Ephemeral** (`CPB_NPX_BOOTSTRAP=0`). Neither delegates nor installs:
   downloads to `~/.claude-playbooks/bin/<tag>/` (reusing an existing copy) and
   runs from there, which is also how a pinned version is exercised beside an
   installed one.

Tag resolution, first match wins: `$CPB_VERSION`; else the package's own version
as `v<npm_package_version>` (so `npx github:<repo>#v3.9.1` runs that release, and
`package.json`'s `version` is bumped with the release tag); else the latest
release from the GitHub API. A tag that resolves to nothing is an error naming
`CPB_VERSION` as the escape hatch.

The binary is `exec`'d with `argv[0]` set to its own path, so multicall dispatch
behaves exactly as a direct invocation and `cpb` is only a short spelling of the
same root command.

The package declares `os` `darwin`/`linux` and `cpu` `x64`/`arm64`, so npm
refuses to install it on native Windows.

### `uninstall.sh`

Delegates to `claude-playbook self-uninstall --binary-only`, so one
implementation owns all removal (see that command). Playbooks are never touched
by it.

### Environment knobs

Read by the two shell scripts only; the Go binary reads none of them.

| Variable | Scripts | Effect |
|---|---|---|
| `VERSION` | `install.sh` | Release tag to install, skipping the latest-release lookup |
| `CPB_VERSION` | shim | Same, for the shim; highest-priority tag source |
| `CPB_NPX_BOOTSTRAP=0` | shim | Ephemeral mode: no delegation, no install |
| `CPB_NPX_CACHE` | shim | Ephemeral-mode cache root. Default `~/.claude-playbooks/bin` |
| `CPB_NPX_INSTALL_DIR` | shim | Bootstrap install directory. Default `~/.local/bin` |
| `INSTALL_DIR` | `install.sh` | Install directory, overriding the writability probe |
| `DEFAULT_INSTALL_DIR` | `install.sh` | The directory that probe tests. Default `/usr/local/bin` |
| `INSTALL_URL` | `install.sh` | Exact asset URL; suppresses checksum verification with a warning |
| `REPO`, `ASSET_PREFIX`, `DOWNLOAD_BASE_URL` | both | Redirect at another repository, asset name, or asset host |

`REPO`, `ASSET_PREFIX`, `DOWNLOAD_BASE_URL`, `INSTALL_URL` and
`DEFAULT_INSTALL_DIR` exist so the install suites can run against a local fixture
server; they carry no compatibility promise.

---

## Commands

### `claude-playbook` (no arguments)

Prints a one-line description and lists all discovered playbooks with how to run each. The parenthetical shows the playbook's registered command (its launcher, by directory name or manifest alias); a playbook with none shows `(no command registered)`.

```
claude-playbook -- manage isolated Claude Code instances

Playbooks directory: ~/.claude-playbooks

Available playbooks:

  experiment    claude-playbook run experiment    (or: experiment)
  sre           claude-playbook run sre           (or: sre)
  dba           claude-playbook run dba           (no command registered)

Run 'claude-playbook --help' for all commands.
```

Empty state:
```
claude-playbook -- manage isolated Claude Code instances

Playbooks directory: ~/.claude-playbooks
No playbooks installed yet. Get started with one of:

  # Install a single playbook from a Git repo:
  claude-playbook install https://github.com/user/pai

  # Cherry-pick one playbook out of a monorepo (e.g. DBA):
  claude-playbook install https://github.com/ramazanpolat/awesome-playbooks/tree/main/playbooks/dba

  # Create your own from scratch:
  claude-playbook create <name>

Run 'claude-playbook --help' for all commands.
```

---

### `claude-playbook list [prefix]`

Lists all playbooks in a table. If a `prefix` argument is given, only playbooks whose names start with that prefix are shown.

```bash
claude-playbook list
claude-playbook list s
```

**Output:**

```
NAME          PATH                                  COMMAND  LAST USED
----          ----                                  -------  ---------
experiment    ~/.claude-playbooks/experiment        -        2 days ago
sre           ~/.claude-playbooks/sre               sre      1 hour ago
dba           ~/.claude-playbooks/dba               -        never
```

Column widths are computed from the longest NAME, PATH, and COMMAND values, with minimum widths of 4, 4, and 7. `COMMAND` shows the playbook's launcher command name (matching its directory name or manifest alias). `-` means no command is registered. `LAST USED` is derived from the playbook directory's mtime.

---

### `claude-playbook create <name>`

Creates a new, empty playbook.

```bash
claude-playbook create experiment
claude-playbook create experiment --no-alias
claude-playbook create experiment --alias exp
```

**Steps:**
1. Validate the name (single segment; enforced charset; must not start with `.`).
2. Check the target directory does not exist.
3. Preflight the command names against the registry under the registry lock — the directory name, plus the launcher name (`--alias` or the playbook name) unless `--no-alias` — erroring before anything is created if a name already addresses another playbook.
4. Create the directory and write a starter `CLAUDE.md` into it (a short template explaining what a playbook is and how to customize it).
5. Unless `--no-alias`, register a **launcher command**: a symlink to the `claude-playbook` binary in the launcher directory. The command name defaults to the playbook name. Override with `--alias`.

`create` writes **no `.playbook` manifest** in the default case — the directory is a valid playbook simply by living under the playbooks root. The one exception: when `--alias` differs from the playbook name, `create` writes a `.playbook` recording the alias, because multicall dispatch resolves the command name against the registry at invocation time and a custom name is only findable through the manifest `alias` field. If that manifest write fails, the directory is rolled back. Add a `.playbook` yourself only when you want to set metadata (version, description, homepage, author).

**Flags:**

| Flag | Description |
|------|-------------|
| `--alias <alias>` | Use a custom launcher command name (default: the playbook name) |
| `--no-alias` | Skip launcher creation |
| `--sandbox` | Write a `.playbook` with `[sandbox] always = true` and `isolate_auth = true` before the credential sync, so the playbook launches inside its sandbox every time and authenticates on its own (`/login` once inside). Prints `Always sandboxed (sbx); authentication isolated: run /login once inside the sandbox.` |

`--alias` and `--no-alias` cannot be combined.

**Errors:**
- Name already exists → `playbook "experiment" already exists at ~/.claude-playbooks/experiment`
- Name starts with `.` → `playbook name cannot start with '.'`
- Name contains a slash → `playbook name cannot contain '/'`
- Command name taken → `command name "exp" already addresses playbook "other". Pick another name or alias`
- Both `--alias` and `--no-alias` → `--no-alias and --alias cannot be used together`

---

### `claude-playbook run [launch-flags] <name> [launch-flags] [claude-flags...]`

Runs Claude Code using the named playbook. Any flags after the name are forwarded to `claude` unchanged, except a leading run of **launch flags**, which add one-off environment layers for this launch only.

```bash
claude-playbook run experiment
claude-playbook run sre
claude-playbook run sre --model claude-opus-5
claude-playbook run --env-profile work sre                       # one launch with an env profile
claude-playbook run sre --env ANTHROPIC_MODEL=claude-opus-5      # flags may also follow the name
claude-playbook run --unset CLAUDE_CODE_OAUTH_TOKEN sre          # this launch uses the stored login
claude-playbook run --env-file ./work.env sre -p "..."
```

**Launch flags** (each repeatable; value as the next argument or after `=`):

| Flag | Layer |
|------|-------|
| `--env-profile NAME` | an existing env profile (`<playbooks root>/.env-profiles/NAME.toml`); a missing or invalid one refuses the launch |
| `--env KEY=VALUE` | set one variable |
| `--unset KEY` | remove one variable |
| `--env-file PATH` | a dotenv-style file: `KEY=VALUE` per line, blank and `#` lines skipped, a leading `export ` tolerated, one pair of matching surrounding quotes stripped, later lines win; every line validated like a manifest entry |

Scanning: launch flags are recognised only as a **leading run**, before the name and again immediately after it; the first argument that is not a launch flag ends the scan and everything from there on is `claude`'s. Through a launcher (`sre --env K=V -p hi`) the same rule applies to the arguments after the command name. Keys and values follow the manifest rules (`CLAUDE_CONFIG_DIR` reserved, valid UTF-8, no NUL). The layers apply on top of the playbook's flattened `[env]` block in command-line order, are resolved against the same profiles root, drive the token decision exactly as the block would (a one-off `--unset CLAUDE_CODE_OAUTH_TOKEN` takes the stored-credentials path for this launch), and are never written to disk.

Equivalent to:
```bash
CLAUDE_CONFIG_DIR=~/.claude-playbooks/<name> claude [claude-flags...]
```

Flag parsing is disabled so arbitrary `claude` flags pass through. The global `--playbooks-dir` flag is extracted from the argument list before forwarding; launch flags are extracted from the leading positions described above.

**Sandboxed launch (v3.11.0).** With `--sandbox`, the playbook's Claude Code runs inside a Docker Sandbox: a microVM with its own kernel, filesystem, Docker daemon and network stack, driven through the `sbx` CLI (v0.38.0 or newer, installed and logged in by the pilot; `run` refuses with an install hint when it is not on PATH, and surfaces `sbx`'s own "not authenticated" failure). Only what is mounted crosses the boundary: the working directory, the playbook's own root directory (its `CLAUDE_CONFIG_DIR`, so the sandboxed Claude Code reads and writes the same playbook state), the manifest's `[sandbox].mounts`, and `--mount` entries, all at their host absolute paths. The host's `~/.claude`, the rest of the home directory, the shell environment and every other playbook stay outside.

```bash
claude-playbook run --sandbox sre                                  # cwd is the workdir
claude-playbook run --sandbox --workdir ~/proj sre -p "..."
claude-playbook run --sandbox --sandbox-fresh --clone --workdir ~/untrusted-repo sre   # new sandbox on a private clone; host tree untouched
claude-playbook run --sandbox --mount ~/shared-libs:ro sre
claude-playbook run --sandbox --sandbox-fresh sre                  # recreate the sandbox first
```

| Flag | Effect |
|------|--------|
| `--sandbox`, `--sbx`, `--sandbox=BACKEND` | launch inside the playbook's sandbox `cpb-<name>` (characters `sbx` does not accept folded to `-`), created on first use (without the shared skills store) and reused afterwards, so tools installed inside and the sandbox's own state persist between launches. `--sbx` is a synonym; `--sandbox=BACKEND` names the backend (the only value is `sbx`; anything else refuses the launch) |
| `--sandbox-host USER@HOST` | run the sandboxed launch on that machine (v3.13.0, below); implies `--sandbox` |
| `--no-sandbox` | launch on the host although the manifest says `[sandbox] always = true`; prints `Sandbox off for this launch: playbook "<name>" is always sandboxed by its manifest` on stderr so the override never passes silently. Together with `--sandbox` it is an error. Without `always` there is nothing to override: the flag is accepted, nothing is printed, the launch is an ordinary host launch |
| `--sandbox-fresh` | remove the existing sandbox and create it again before launching |
| `--clone` | at creation, mount the working directory read-only and let the agent work on a private git clone inside the sandbox (`sbx --clone`; its commits are reachable from the host through the `sandbox-cpb-<name>` remote). Creation-time only, as in `sbx`: an existing sandbox is reused as it was created, so switching an existing one to clone mode takes `--sandbox-fresh` |
| `--workdir PATH` | the directory mounted and entered; default `[sandbox].workdir`, else the invocation directory. Must exist |
| `--mount PATH[:ro]` | one more host path to mount (repeatable), on top of `[sandbox].mounts` |

The flags belong to the same leading runs as the launch flags, in any order among them; `--sandbox-fresh`, `--clone`, `--workdir` and `--mount` without a sandbox (no flag and no `always`, or `--no-sandbox`) refuse the launch. `--workdir` and `--mount` values may be `~`-prefixed.

**Always sandboxed (v3.12.0).** A manifest with `[sandbox] always = true` sandboxes every launch of the playbook without a flag: `run`, and launcher dispatch (`sre -p "..."`). `--no-sandbox` overrides it for one launch, loudly (above); there is no environment variable or setting that overrides it silently. `create --sandbox` and `install --sandbox` write the key together with `isolate_auth = true`, because the machine login cannot follow a playbook into its sandbox (below). The backend comes from `--sandbox=BACKEND`, else `[sandbox].backend`, else `sbx`; the launch logic talks to it through one seam (list, create, allow network, shell, attach, remove), so a further backend is an implementation, not a redesign. `[sandbox]` is install-local, like `[env]`: `install` never adopts a source-shipped block (it would mount host paths or widen the network), dropping it with a note, and `update` preserves the live one.

Procedure: resolve the environment exactly as an unsandboxed launch would (registry default profile, profiles, block, launch flags, the authentication decision including quarantine and identity purge of the playbook's own store, which the sandbox then reads through the mount; the config directory is made absolute first, so a relative `--playbooks-dir` cannot leak a relative `CLAUDE_CONFIG_DIR`), then reduce it to the variables the sandbox receives: the keys the effective block and launch flags **set**, plus `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CODE_SUBSCRIPTION_TYPE`, `CLAUDE_CODE_RATE_LIMIT_TIER` and `CLAUDE_CONFIG_DIR` when the launch decided them. Nothing else of the host environment enters the sandbox. **A shared login does not enter it either:** when the store is still a symlink after preparation and the environment carries no `CLAUDE_CODE_OAUTH_TOKEN` (the shared-login path; a token launch authenticates with the token, and the link it may leave behind, one to a grantless store, holds nothing to adopt), its target `~/.claude` is not mounted and `sbx` mounts directories only, so the link would dangle inside and Claude Code would be logged out. A missing store on a non-isolated target (a fresh directory or playbook on a machine without a login) is the same shape with nothing to link to yet, and is treated the same way. The link is re-pointed (or created) instead, at a **sandbox-local** file: `<sandbox home>/.claude-playbook-logins/<sandbox name>/.credentials.json` (`/home/agent/...` under `sbx`), whose directory the attach command creates inside before `claude` starts. `/login` inside writes through the link into the sandbox, where the grant persists with the sandbox (and goes with `--sandbox-fresh`) and never reaches the host. The account state the host sync copied in (`oauthAccount`, cached flags) is purged as for an isolated playbook, and `Shared login stays on the host: playbook "<name>" authenticates on its own inside the sandbox (run /login once there; the login lives in the sandbox)` is printed on stderr. When the session returns, the launch runs the credential sync again, which replaces any link that is not the shared one with the shared one and copies nothing, so the host sees the shared link as before; the next sandboxed launch re-points it again and the sandbox login is still there. While a sandboxed session is live (or after a launch that never returned) the host store link dangles, which every host command tolerates: `update` preserves it as it is, `delete` removes it, `auth status` shows `shared-login` with the sandbox target and `no login`, and the next host launch repairs it. A sandboxed **token** launch that finds the store linked to a sandbox login detaches the link first (on the host the quarantine saw a dangling link and nothing to detach, but inside it would resolve to the earlier grant, which Claude Code's 401 recovery adopts over a token), exactly as the quarantine detaches a shared link with a grant. The machine's own config directory (`~/.claude`, by identity) is never sandboxed: it holds the machine login as a regular store, and mounting it would hand that login to the sandbox; `start --sandbox ~/.claude` is refused before any preparation. Nor may any mount contain it: the working directory, the root, and every extra mount (read-only or not) are refused when the machine config directory, the machine credentials store at its resolved location (the store may be a symlink into another directory), the long-lived token file, or the registry's env profiles directory (`<playbooks root>/.env-profiles`, the secret store proxy injection keeps out of the sandbox) exists below them, decided by filesystem identity along the credential's ancestors (so `/`, a differently cased spelling, or any alias of the directory counts), so `--workdir ~`, `start --sandbox ~`, `--mount ~:ro` and `--mount /:ro` all refuse (`sandbox mount <path> contains the machine's Claude config directory <dir>: the machine login would enter the sandbox. Mount a narrower directory`). Every refusal happens before the backend is called. The login must never land as a regular file on the mount: the host sync promotes a newer regular grant-bearing store into the machine store, which is exactly what a sandbox login must not do. The machine login is never read, copied or moved. Every path that crosses into the sandbox is absolute and symlink-resolved (the target is what exists at that path inside): the working directory, the playbook root, the extra mounts (a missing one refuses the launch), and `CLAUDE_CONFIG_DIR` itself, which inside the sandbox names the resolved config directory, so a linked registry entry (`link`) mounts and addresses its target. The root is not mounted again when it lies inside the working directory, and a config directory outside the mounted root is mounted on its own. `sbx ls -q` decides whether `cpb-<name>` exists; `--sandbox-fresh` removes it (`sbx rm -f`); a sandbox that is reused is inspected first (`sbx ls --json`, its `workspaces`): mounts are creation-time, so the existing mounts must cover every path this launch needs, by containment (a working directory below an existing mount is covered; a different one would not exist inside) and pass the same guards as new mounts (the machine-login and profiles guard, and in proxy mode the manifest-key guard over the existing, possibly wider, mounts), else the launch refuses and names `--sandbox-fresh` (`sandbox cpb-<name> was created with mounts ...; this launch also needs ..., which a reused sandbox cannot add. Recreate it with --sandbox-fresh` / `sandbox cpb-<name> mounts <path>, which contains <what>: the machine login would be inside. Recreate it with --sandbox-fresh`); a missing sandbox is created with `sbx create --name cpb-<name> [--clone] claude <workdir> <playbook root> <extra mounts...>`. A service on this machine is a special case: inside the sandbox `localhost` is the sandbox itself, so an `ANTHROPIC_BASE_URL` at `localhost`, `127.0.0.1` or `::1` is rewritten for the sandbox to the backend's host alias (`host.docker.internal` under `sbx`), scheme, port and path kept, with `ANTHROPIC_BASE_URL names this machine: inside the sandbox it is <url>` on stderr; and the backend's policy and secret store know that service as `localhost` whatever name the sandbox used (verified on `sbx` 0.38.0: an allow rule or a secret for `host.docker.internal` never matches, one for `localhost` does), so allow rules and secret registrations for the host alias, `localhost`, `127.0.0.1` or `::1` are spelled `localhost`, in `[sandbox].allow_net` too. After creation, and only then, every host in `[sandbox].allow_net` and the host of `ANTHROPIC_BASE_URL` (when the effective environment sets one) is allowed for that sandbox (`sbx policy allow network --sandbox cpb-<name> <host>`; a failure is a warning, the launch continues), and a `[sandbox].claude_version` pin installs that Claude Code inside the sandbox through the official installer (a failure is a warning; the image's own version runs). **Remote sandbox host (v3.13.0).** `sbx` drives only the machine it runs on, so "the host the sandbox runs on" is the host where `claude-playbook` runs. With `--sandbox-host USER@HOST` (or the manifest's `[sandbox] host`, used whenever the launch is sandboxed) the whole launch is forwarded there over ssh, as the same subcommand rebuilt from what the launch parser consumed (never from the raw text, so `claude`'s own arguments travel verbatim and nothing in them is mistaken for a flag): `ssh [-t] -- USER@HOST 'env CPB_CMD=<base64> sh -c '"'"'eval "$(printf %s "$CPB_CMD" | base64 --decode)"'"'"''`, where the decoded text is `PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH" exec claude-playbook run [--playbooks-dir=D] --sandbox[=BACKEND] [--sandbox-fresh] [--clone] [--workdir=W] [--mount=M]... [--env-profile=P | --env=K=V | --unset=K]... NAME [claude args...]` (`start` likewise, with `--delete` before the path). The transport exists because ssh hands the text to the remote user's login shell, whose family is unknown (tcsh breaks POSIX single quotes on a newline and expands `!`) and whose non-interactive PATH lacks `~/.local/bin`: the outer text is plain words every shell family passes through untouched, only POSIX `sh` parses the command, `sh`'s stdin is the ssh channel and its exit status is `claude-playbook`'s (`exec`), and the PATH is widened inside with the installer's and the package managers' directories. `base64 --decode` is GNU and BSD alike, every value flag in its inline form so a value that looks like a flag stays a value there too (a value that is exactly `--` travels as two words, since a standalone `--` is where the registry scan stops on both sides; every `--workdir` occurrence is forwarded in order, the last winning there as here), every argument single-quoted into the one command string ssh hands the remote shell, the launch flags forwarded as typed and unevaluated (no env file is read for a remote launch, whether the host comes from the flag or from the manifest: the flags are evaluated only once the launch is known to be local), `-t` only when this process has a terminal on both ends, ssh's own options ended by `--` before the destination. The host is validated like the manifest key (no whitespace, no slash, no leading dash: `--sandbox-host "<value>" must be an ssh destination such as user@host`). `--sandbox` is always present, so the remote launch is sandboxed there whatever its manifest says; the remote host's own registry, profiles, manifest and secrets apply, and the sandbox, its login and its proxy mappings live there. Requirements on the remote: `claude-playbook` in `~/.local/bin` (the installer's default), `/opt/homebrew/bin`, `/usr/local/bin`, or on the PATH an ssh session gets, a credential store `sbx` can read from a non-interactive ssh session (a Linux host with a headless keyring unlocked at boot; a macOS host keeps the Hub session in the login Keychain, which an ssh session cannot read until `security unlock-keychain` runs, so `sbx` fails there with `cannot prompt the user for password`), the playbook installed there (for `start`, the path is a path there; a directory manifest naming a host forwards a sandboxed `start` the same way, and `--delete` then acts there), `sbx` logged in. With the flag, the playbook need not be registered here at all. A `--playbooks-dir` travels as given, a path on that host. Refused: `--env-file` (a local file, refused by name; it is never opened), and `--sandbox-host` together with `--no-sandbox`; `--no-sandbox` on a playbook whose manifest names a host runs it here, on this host. Printed first: `Sandbox on USER@HOST: claude-playbook run ...`. The exit status is the remote launch's.

**Secrets stay on the host (v3.12.1).** Before attaching, every backend API key the environment carries (`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`) is registered with the backend as a proxy-injected secret scoped to the sandbox, for the endpoint host (the host of `ANTHROPIC_BASE_URL`, else `api.anthropic.com`): `sbx secret set-custom --host <host> --env <KEY> --value <value> --placeholder <sandbox>-<KEY> --sandbox <sandbox>`. Inside, the variable holds the placeholder; every request from the sandbox passes through the host-side proxy, which swaps the real value into the request headers only on its way to that host (verified on sbx 0.38.0 for `Authorization: Bearer` and `x-api-key`; a request to any other host carries the placeholder). The key never enters the sandbox's filesystem or environment. That holds only for keys that live outside the mounts, which is where env profiles live (`<playbooks root>/.env-profiles/`); a key set in the target's own `[env.set]` sits in a `.playbook` on the mounted root (the config directory's, or the install root's for a `subdir` install, or one above a `start` directory that the working-directory mount carries; every manifest walking up from the config directory whose directory lies under one of this launch's mounts is checked, and one that cannot be read refuses too, `cannot check <path> for keys the sandbox would mount: <error> ...`, since it may hold a key), so a proxy-mode launch refuses it before any backend call (`<KEY> is set in <path>/.playbook, which the sandbox mounts: the key would be readable inside. Move it to an env profile, which lives outside the mount (cpb env-profile <profile> set <KEY>=...; cpb env <playbook> use <profile>; cpb env <playbook> clear <KEY>), or set [sandbox] secrets = "env" to accept the exposure`). Files the pilot places on a mount themselves (`--env-file` under the working directory, a `settings.json` `env`) are the pilot's own exposure. The placeholder is deterministic, so registering it again on the next launch updates the value in place (rotation follows the profile). A mapping registered by an earlier launch for a key the environment no longer carries is **revoked** on the next launch (`sbx secret ls --sandbox` lists what is registered; the placeholder is re-registered as its own value for the current endpoint host, so the proxy substitutes it with itself, `Secret <KEY> revoked at the proxy: it is no longer in the environment`), since anyone inside could otherwise keep sending the predictable placeholder. One mapping exists per key per sandbox: two sessions of one sandbox launched with different one-off keys share it, and the later launch's key serves both (a known limitation; the placeholder must stay stable for rotation to work). `sbx` 0.38.0 cannot delete a custom secret; it persists in the backend's store after the sandbox or the playbook is gone, and the next launch of a sandbox of that name overwrites it. The value travels on `sbx`'s argument list for the moment of the call. A registration that fails prints `Warning: <KEY> could not be injected at the proxy (...); it enters the sandbox as a plain value` and passes the value, never silently; `[sandbox] secrets = "env"` chooses plain values outright. The subscription token (`CLAUDE_CODE_OAUTH_TOKEN`) is not injected: Claude Code checks its shape locally, and a placeholder would not pass. Sandboxes are created with `--no-share-skills` unless `[sandbox] share_skills = true`: `sbx` otherwise mounts its shared skills store read-write into every sandbox, and a sandbox could plant a skill a later sandbox runs. Creation-time choices cannot be read back from `sbx`, so creation writes a marker inside (`~/.claude-playbook-sandbox`, `skills=private` or `skills=shared`) and a reused sandbox must present the marker this launch expects: one without it was created by claude-playbook 3.12.0 or earlier with the store mounted, one with another value was created under a different `share_skills`; both refuse (`sandbox <name> was created with other creation-time settings (found "...", this launch needs "..."): an earlier claude-playbook, or a changed share_skills. Recreate it with --sandbox-fresh`).

The launch then attaches with `sbx exec -i [-t] -e KEY=VALUE... cpb-<name> bash -lc 'cd <workdir> && exec claude <args>'`; `-t` (a pty) is requested only when claude-playbook's own stdin and stdout are terminals (a real terminal test; `/dev/null` is a character device but not a terminal), since `sbx exec -t` without one produces no output and exits 0, which would make a piped `-p` launch silently do nothing. The environment and the arguments travel as exec arguments (each argument single-quoted), never through a file inside the sandbox. `claude`'s exit status is preserved. The sandbox's network policy (deny-by-default with the pilot's preset) applies to everything not allowed explicitly; a sandboxed playbook that cannot reach a service is a policy question first (`sbx policy log`).

Launcher dispatch forwards to `run`, so `sre --sandbox -p "..."` launches inside `cpb-sre`, and a playbook with `always` is sandboxed through its launcher. `start` sandboxes a directory the same way (below).

**Errors:**
- Playbook not found → `unknown playbook "experiment". Run 'claude-playbook list' to see available playbooks`
- Launch flag without a value → `flag needs an argument: --env`
- `--env` without `=` → `--env expects KEY=VALUE, got "X"`; `--unset` with `=` → `--unset expects a variable name, got "K=V"`
- Invalid key, reserved key, or bad value → the manifest's `[env]` errors (`invalid environment variable name "x"`, `CLAUDE_CONFIG_DIR is managed by claude-playbook and cannot be overridden`, ...)
- `--env-file` problems → `--env-file: <path>:<line>: expected KEY=VALUE` or `<path>:<line>: invalid environment variable name (not shown; the line may hold a secret)`: neither the line nor a rejected key is echoed, since a secret containing `=` splits into a bogus key; the reserved-key and value errors are the manifest's, prefixed with `<path>:<line>`
- `--env-profile` missing or broken → the launch refusal from `env` (`env profile "x" not found in <dir> ...`)
- `claude` not on PATH → `'claude' command not found. Install Claude Code first: https://claude.ai/download`. **Reported after every input error above it in this list** (v3.15.0): the pilot's own input is validated before the machine is inspected, so a mistyped flag names itself instead of sending someone to install an agent they may already have. Launch-flag values, a reserved or malformed key, an `--env-file`, and a profile that does not resolve are all decided first. Everything that MUTATES stays after this check — credential quarantine and sync — so a machine with no agent is never written to; the profile resolution done here is a read-only pre-pass, the same resolution the authentication preparation performs again when the launch proceeds. Before v3.15.0 only a flag missing its value was reported, that alone being caught while arguments are parsed. `start` has the same order.
- Sandbox flag without a sandbox → `--sandbox-fresh, --clone, --workdir and --mount apply to a sandboxed launch: add --sandbox`
- `--sandbox` with `--no-sandbox` → `--sandbox and --no-sandbox together: pick one`; `--sandbox-host` with `--no-sandbox` → `--sandbox-host and --no-sandbox together: pick one`
- `--sandbox-host` with `--env-file` → `--env-file names a local file: a launch on <host> cannot read it. Use an env profile on that host, or --env KEY=VALUE`; `ssh` missing → `'ssh' not found; a sandbox on <host> is reached over ssh`
- Unknown backend (flag or manifest) → `unknown sandbox backend "tart" (available: sbx)`; `--sandbox=` → `flag needs a non-empty argument: --sandbox=BACKEND`
- The machine config directory → `<path> is the machine's Claude config directory: a sandbox would mount the machine login. Sandbox a playbook or another directory`
- A mount above the machine login → `sandbox mount <path> contains the machine's Claude config directory <dir>: the machine login would enter the sandbox. Mount a narrower directory` (or `... the machine's long-lived token file <file> ...`)
- `--workdir`/`--mount` without a value → `flag needs an argument: --workdir`; empty → `flag needs a non-empty argument: --mount`
- `sbx` not on PATH → `'sbx' (Docker Sandboxes) not found; install it (macOS: brew trust docker/tap && brew install docker/tap/sbx) and run 'sbx login' once, or launch without --sandbox`
- `sbx ls` fails (not logged in, daemon down) → `sbx is not ready (run 'sbx login' if it reports not authenticated): <sbx error>`
- Working directory missing → `sandbox working directory <path> is not a directory`
- A `[sandbox].mounts` or `--mount` path missing → `sandbox mount <path>: <error>`
- Sandbox creation or removal fails → `could not create sandbox cpb-<name>: <sbx error>` / `could not remove sandbox cpb-<name>: <sbx error>`

---

### `claude-playbook start [launch-flags] <path> [claude-flags...]`

Starts an ad-hoc Claude Code session at any directory. Creates the directory if it doesn't exist. No playbook registration, no `.playbook` file, no discovery — just set `CLAUDE_CONFIG_DIR` and run. The throwaway-experiment command. It never appears in `list`; `link` registers a directory that should.

**Sandboxed start (v3.12.0).** `start` takes the same sandbox flags as `run` (`--sandbox[=BACKEND]`, `--sbx`, `--no-sandbox`, `--sandbox-fresh`, `--clone`, `--workdir`, `--mount`), in the same leading positions as `--delete` and the launch flags. The sandbox is `cpbstart-<directory basename>` (the prefix differs from a registered playbook's `cpb-` before the first hyphen, so no playbook name can produce it; `cpb-start-<x>` would collide with playbooks `start-x` and `start_x`); the directory is the mounted config root; a `.playbook` in the directory supplies `[sandbox]` (`always` and the defaults). Everything else is as under `run`, the shared-login detach included. With `--sandbox`, `--delete` removes the sandbox when the session ends (a failure is a warning), then the directory; a launch refused before a session attached (the machine config directory, a mount carrying the machine login, a missing backend) removes nothing.

```bash
claude-playbook start --sandbox --workdir ~/proj /tmp/scratch -p "..."
claude-playbook start --sandbox --delete /tmp/scratch          # sandbox and directory gone afterwards
```

```bash
claude-playbook start /tmp/scratch
claude-playbook start /tmp/scratch --model claude-opus-5
claude-playbook start /tmp/scratch --delete
claude-playbook start --env-profile glm /tmp/scratch             # launch flags go before the path
```

The launch flags of `run` (`--env-profile`, `--env`, `--unset`, `--env-file`) are accepted before the path, with the same semantics; profiles resolve from the root named by `--playbooks-dir` or `CLAUDE_PLAYBOOKS_DIR`.

Equivalent to:
```bash
CLAUDE_CONFIG_DIR=/tmp/scratch claude [claude-flags...]
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--delete` | Delete the directory when the session ends |
| `--env-profile`, `--env`, `--unset`, `--env-file` | One-off environment layers; see `run` |

All wrapper flags, `--delete` included, are recognised only as a **leading run** before the path and again immediately after it; the first other argument, or a literal `--`, ends the run and everything from there on is `claude`'s verbatim. A `--delete` that appears later -- after another `claude` argument, as the value of a `claude` flag such as `-p --delete`, or after `--` -- is forwarded, never treated as permission to remove the directory. `--` is never accepted as the path itself (`path required`).

`--delete` runs after `claude` exits regardless of exit code. If deletion fails, a warning is printed to stderr but the tool preserves `claude`'s exit code.

**Errors:**
- Path exists and is a file → `"/tmp/foo" is not a directory`
- Cannot create directory → `could not create "/tmp/foo": <reason>`
- No path given → `path required`
- `claude` not on PATH → same as `run`

---

### `claude-playbook install <source>`

Installs a single playbook from a Git repository or a local directory. The result is always **one flat playbook** under the playbooks root.

`install` always **copies** the source into the playbooks root — both Git URLs (via clone) and local directories (via recursive copy). The installed playbook is a self-contained, independent copy; later edits to the original source do not affect it. To keep an *external* directory in place and expose it under the playbooks root as a symlink instead of a copy, use the separate [`link`](#claude-playbook-link-target) command.

```bash
# Git repo (derives install name from the URL)
claude-playbook install https://github.com/user/pai

# Git repo with a custom install name
claude-playbook install https://github.com/user/repo --name myrepo

# Install a specific branch/tag
claude-playbook install https://github.com/user/repo --branch dev

# Local directory (copied into the playbooks root, becomes independent of source)
claude-playbook install ~/dev/my-playbook

# Install one playbook out of a monorepo (the primary multi-playbook-repo path)
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks/tree/main/playbooks/sre --name sre --alias sre

# Same thing, spelled with explicit flags
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks --subdir playbooks/sre --branch main --name sre --alias sre
```

**Source types:**

| Source | Behaviour |
|--------|-----------|
| URL (`http://`, `https://`, `git@`, `git://`, `ssh://`, `file://`) | Shallow-cloned (`git clone --depth=1`) into the install directory |
| GitHub `/tree/<ref>/<path>` URL | Recognized and split automatically into clone URL + `--branch <ref>` + `--subdir <path>`; remote refs are consulted so branch names containing `/` work |
| Anything else | Treated as a local filesystem path and **copied** into the install directory |

**Flags:**

| Flag | Description |
|------|-------------|
| `--name <name>` | Override the install directory name under the playbooks root |
| `--subdir <path>` | Install only this subdirectory of the source (see below) |
| `--branch <ref>` | Git URL only: clone this branch/tag/ref instead of the default branch |
| `--alias <alias>` | Custom launcher command name for the installed playbook |
| `--no-alias` | Skip launcher creation |
| `--sandbox` | Set `[sandbox] always = true` and `isolate_auth = true` on the installed manifest: every launch is sandboxed and the playbook authenticates on its own. A `[sandbox]` block shipped by the source is never adopted, with or without this flag (`Note: ignoring the [sandbox] block shipped in the source's .playbook; sandbox settings are install-local ...`). |

**Steps (no `--subdir`):**
1. Stage the source (Git URL → `git clone --depth=1`, with `--branch <ref>` if given, into a temp dir; local path → read in place) so its `.playbook` can be consulted before choosing a name.
2. Derive the install directory name from `--name`, or the manifest `name`, or the last path segment of the URL (stripped of `.git`), or the source directory's name.
3. Check the target doesn't already exist under the playbooks root.
4. Preflight command names against the registry under the registry lock — the install name and the effective alias (`--alias`, or the staged manifest's `alias`) — erroring **before anything is copied** if a name already addresses another playbook.
5. Copy the staged tree into the target. The installed directory **is** the playbook. If a `.playbook` is present it supplies metadata; if not, the directory is still a valid playbook.
6. Register a launcher command per the rules below.
7. Print a summary.

`install` never writes a `.playbook` into the source, and never needs one to succeed.

**Steps (with `--subdir <path>`):**
1. Fetch the source as above into a scratch location (for URLs, a temp directory; for local paths, the source itself).
2. Verify `<source>/<path>` exists and is a directory.
3. Copy `<source>/<path>` into `~/.claude-playbooks/<name>/`. For URL sources, the rest of the clone is discarded.
4. Treat the result as one flat playbook. Any `.playbook` already inside `<source>/<path>` provides metadata for that one playbook.
5. Default install name (`--name` not given) is the last segment of `<path>`.

`--subdir` is how you consume a monorepo — a repo laid out as `playbooks/sre`, `playbooks/dba`, `playbooks/frontend`, etc. Each `install --subdir` (or `/tree/<ref>/<path>` URL) copies everything under that one directory into its own playbook. To take several, run several installs; there is intentionally no "install the whole suite at once."

**Default command name**

One launcher is registered, named by the `--alias` value, or the manifest's `alias` field, or its `name` field, or the install directory name, in that order. `--no-alias` skips it. When `--alias` differs from what the installed manifest records, the alias is written into the installed playbook's `.playbook` — a custom command name is only resolvable at invocation time through the manifest `alias` field (on manifest-write failure the install is rolled back).

**Command-name collision handling**: collisions against the registry are a hard **pre-copy error**, not a skip-with-warning — `command name "sre" already addresses playbook "other". Pick another name or alias`, and nothing is copied. Only a *foreign file* (not a launcher) already occupying the name in the launcher directory degrades to a post-install warning: the playbook is installed and runnable via `claude-playbook run <name>`, and the warning suggests renaming or removing the conflicting file.

**CLAUDE.md warning:** if the installed playbook has no `CLAUDE.md`, a warning is printed. Claude Code works without one, but most playbooks benefit from having one.

**Errors:**
- `--branch` with a local path → `--branch only applies to Git URLs`
- `--subdir` path missing in source → `source.subdir "<path>" not found below <source root>: <stat error>` (the flag is resolved as a source-relative path, so it reports under that field name)
- Source not found → `'~/dev/foo' not found`
- Source is a file → `'~/dev/foo' is not a directory`
- Install name already taken → `"myrepo" already exists at ~/.claude-playbooks/myrepo. Use --name to choose a different name`
- Command name taken → `command name "sre" already addresses playbook "other". Pick another name or alias`
- `git` not on PATH → `'git' command not found`
- Clone fails → git's error output is shown directly

**Sample output:**
```
Cloning https://github.com/ramazanpolat/awesome-playbooks (branch main) (subdir playbooks/sre)...
Installed "sre" at ~/.claude-playbooks/sre
Command:  sre  (launcher at /Users/you/.local/bin/sre)

Run it now:
  sre
```

No shell reload is needed — the launcher is a symlink in a PATH directory, live the moment it is written. If the launcher directory is not on PATH, or another executable shadows the command, a warning explains the fix.

---

### `claude-playbook link <target>`

Symlinks an existing **external** directory into the playbooks root, exposing it as a playbook without copying it. Unlike `install` (which always copies and leaves the source untouched), `link` keeps the directory where it lives and points a symlink at it — edits made in either place are the same files. This is the way to develop a playbook in a working tree while running it through `claude-playbook`.

```bash
claude-playbook link ~/dev/my-playbook
claude-playbook link ~/dev/my-playbook --name mp --alias mp
claude-playbook link ~/dev/my-playbook --no-alias
```

**Steps:**
1. Resolve `<target>` to an absolute path; it must exist and be a directory.
2. Pick the link name from `--name`, or the target's basename. It must be a single-segment name (no `/`).
3. Check `<root>/<name>` does not already exist.
4. If the target has no `.playbook`:
   - If stdin is a TTY, prompt interactively for a playbook name, alias, and description (the `--alias` value seeds the alias default), and **write a `.playbook` into the target directory** with those values. The prompt runs *before* the registry lock is taken; if a concurrent link initialized the manifest in the meantime, that manifest wins and the prompted metadata is discarded with a note.
   - If stdin is not a TTY, error out — there is nothing to prompt with. Add a `.playbook` to the target first.
5. Preflight command names against the registry under the registry lock — the link name and the effective alias (`--alias`, or the target manifest's `alias`) — erroring before the symlink joins the registry if a name already addresses another playbook.
6. Create the symlink `<root>/<name>` → `<target>`.
7. Unless `--no-alias`, register a launcher command. The command name comes from `--alias`, or the target manifest's `alias`, or the link name.

**`--alias` persistence.** A custom command name must be resolvable at invocation time, so `--alias` is persisted into the target's `.playbook` as its `alias` field — but only when that manifest was created by this invocation (the flag then wins over whatever was typed at the prompt, and it is persisted before the symlink joins the registry). A **pre-existing** target manifest is shared state: the same external directory may already be linked from other registry roots whose launchers resolve through it, so `--alias` that differs from its recorded alias — including adding one where none exists — is refused: `target's .playbook is shared state (alias "x"); --alias "y" would mutate it for every registration of this target. Use the manifest's alias or edit the target's .playbook directly`. `--alias` matching the recorded alias is fine (nothing to write).

**Flags:**

| Flag | Description |
|------|-------------|
| `--name <name>` | Name under the playbooks root (default: the target's basename) |
| `--alias <alias>` | Launcher command name (default: the link name) |
| `--no-alias` | Skip launcher creation |

`--alias` and `--no-alias` cannot be combined.

**Errors:**
- Target not found → `'~/dev/foo' not found`
- Target is a file → `'~/dev/foo' is not a directory`
- Name contains a slash → `link name may not contain '/'`
- Name already taken → `"mp" already exists at ~/.claude-playbooks/mp. Use --name to choose a different name`
- Command name taken → `command name "mp" already addresses playbook "other". Pick another name or alias`
- `--alias` against a pre-existing shared manifest → the shared-state refusal above
- No `.playbook` and stdin is not a TTY → `target has no .playbook and stdin is not a TTY; cannot prompt for metadata. Add a .playbook to the target first`

Because the entry under the playbooks root is a symlink, `info` reports its `Type` as `symlink → <target>` (or `symlink → <target> (BROKEN)` if the target is gone), and `delete` removes only the link, never the target.

---

### `claude-playbook info <name>`

Shows detailed information about a playbook.

```bash
claude-playbook info sre
```

**Output:**
```
Name:        sre
Version:     1.2.0
Path:        ~/.claude-playbooks/sre
Type:        directory
Alias:       sre
Size:        24 files, 3 directories
Last used:   2 hours ago
Description: Site Reliability Engineering assistant
Homepage:    https://github.com/ramazanpolat/awesome-playbooks
Author:      Ramazan Polat
Update from: https://github.com/ramazanpolat/awesome-playbooks
Migrations:  migrations/apply.sh
```

**Fields:**

| Field | Meaning |
|-------|---------|
| `Name` | Playbook name (its directory name under the playbooks root) |
| `Version` | `version` field from the `.playbook` manifest, if set |
| `Path` | Absolute path to the directory |
| `Type` | `directory`, `symlink → <target>`, or `symlink → <target> (BROKEN)` |
| `Alias` | The playbook's manifest alias, or `(none)` |
| `Size` | File and directory counts |
| `Last used` | Human-readable time since the directory was last modified |
| `Description` | From `.playbook` manifest, if present |
| `Homepage` | From `.playbook` manifest, if present |
| `Author` | From `.playbook` manifest, if present |
| `Env` | One line per `[env]` entry (`profiles a, b`, `set KEY=VALUE`, `unset KEY`) when the manifest declares any; omitted otherwise |
| `Sandbox` | The `[sandbox]` block in one line (`always`, `backend sbx`, `host user@host`, `share_skills`, `secrets env`, `workdir ~/p`, `mounts ...`, `allow_net ...`, `claude 2.1.263`, comma-separated, only the keys set) when the manifest declares any; omitted otherwise |
| `Update from` | `[source].repository` from the manifest, else `(no [source] metadata; cannot update)` |
| `Migrations` | `migrations/apply.sh` when it exists and is executable; omitted otherwise |

**Errors:**
- Target not found → `unknown playbook "experiment"`

---

### `claude-playbook rename <old-name> <new-name>`

Renames a playbook directory and keeps its command registrations consistent.

```bash
claude-playbook rename experiment exp-1
claude-playbook rename sre site-reliability
```

**Steps:**
1. Validate the old name exists and the new name doesn't. Both must be single-segment names.
2. Persist any requested manifest-alias change, then rename the directory.
3. Retire the old name's launcher (claim-aware) and register the new command.

**Flags:**

| Flag | Description |
|------|-------------|
| `--alias <alias>` | Set the manifest alias (and its launcher) for the renamed playbook |
| `--no-alias` | Drop the alias and launcher registration |

`--alias` and `--no-alias` cannot be combined.

**Errors:**
- Old name not found → `unknown playbook "experiment"`
- New name already exists → `"exp-1" already exists at ~/.claude-playbooks/exp-1`
- Either name contains a slash → `playbook name cannot contain '/'`
- Both `--alias` and `--no-alias` → `--no-alias and --alias cannot be used together`

---

### `claude-playbook alias [name] [new-alias]`

Shows or manages a playbook's **alias**: one alternate command name, recorded in the playbook's `.playbook` manifest and materialized as a launcher command. A playbook is addressed by its directory name plus at most one alias. **Read-only with zero or one argument** — no hidden side effects.

```bash
claude-playbook alias                    # list every playbook's alias
claude-playbook alias sre                # show the alias for this playbook, or say "none"
claude-playbook alias sre s              # set the alias to 's' (replaces any previous one)
claude-playbook alias sre --remove       # remove the alias and its launcher
```

**No arguments** — lists all playbooks and their aliases:

```
experiment    exp
sre           s
dba           (no alias)
```

**One argument, no alias** — reports only; does **not** create one.
```
Playbook "dba" has no alias set.
Use 'claude-playbook alias dba <alias-name>' to set one.
```

**Two arguments** — sets the alias: validates the name (reserved names and the playbook's own name are refused), preflights registry ownership under the registration lock, records the alias in the manifest (bootstrapping one for a flat playbook), retires the previous alias's launcher (claim-aware), and writes the new launcher.

**With `--remove`** — clears the manifest alias and removes its launcher (claim-aware: a name that still addresses another playbook keeps its launcher).

Linked playbooks: the manifest is the LINK TARGET's shared state, so alias mutations are refused — edit the target's manifest directly if you really mean it.

**Flags:**

| Flag | Description |
|------|-------------|
| `--remove` | Remove the alias for the named playbook |

**Errors:**
- Playbook not found → `unknown playbook "sre"`
- Alias name reserved, invalid, or already addressing another playbook
- Linked playbook → refused (shared manifest)

---

### `claude-playbook env [name] [set KEY=VALUE... | unset KEY... | clear KEY...]`

Shows or manages a playbook's **environment overrides**: the `[env]` block of its `.playbook` manifest, applied to the child `claude` process by `run`, `start`, and launcher dispatch. **Read-only with zero or one argument.**

```bash
claude-playbook env                                    # list every playbook that declares overrides
claude-playbook env router                             # show this playbook's overrides
claude-playbook env router set A=1 B=2                 # record values (replacing previous ones)
claude-playbook env router unset CLAUDE_CODE_OAUTH_TOKEN
claude-playbook env router clear A                     # forget the entry; the shell's value applies again
claude-playbook env router use glm no-oauth            # attach env profiles (must exist)
claude-playbook env router unuse no-oauth              # detach
```

**Output** (show). When profiles are attached, the flattened result follows the declared block:

```
Environment overrides for "router":
  profiles  glm
  set    MODEL=own
Effective at launch:
  set    ANTHROPIC_BASE_URL=http://proxy:1/v1
  set    MODEL=own
  unset  CLAUDE_CODE_OAUTH_TOKEN
```

**Redaction (v3.15.0, `--reveal`).** A `set` key that looks like a credential
prints a masked value instead of the resolved one, everywhere a `set` entry
is shown: `env` (list, show, and the profile-expanded `Effective at launch`
block alike), `env-profile` show, and `info`. There is no separate per-field
secret marker in an env profile's TOML (its `set` is a plain
`map[string]string`); a key-name heuristic (case-insensitive) is what decides
it, in three rules, each as wide as it can be without swallowing an obvious
non-secret:

| Rule | Matches | Because | Cost |
|---|---|---|---|
| substring anywhere | `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`, `PASSPHRASE`, `CREDENTIAL` | `GITHUBTOKEN` has no underscore to split on | over-matches `TOKENIZER_PATH` |
| whole `_`-separated segment | `AUTH`, `PWD`, `PASS`, `PAT` | `MYSQL_PWD` is the standard MySQL password variable; as substrings these would redact `AUTHOR_NAME`, `COMPASS_URL` and `PATH` | over-matches a bare `PWD` |
| end of a `_`-separated segment | `KEY`, `KEYS` | `API_KEY`, `MY_APIKEY` and `GPG_SIGNKEY` are all keys | spares `KEYBOARD_LAYOUT` |
| exemption | a `PUBLIC` or `PUB` segment defeats the `KEY`/`KEYS` rule only | `PUBLIC_KEY` is meant to be read and compared | a `PUBLIC_SECRET` is still masked |

Over-matching is deliberate throughout: a missed credential is a silent leak,
an over-redacted ordinary key costs one `--reveal`. A bare `PWD` is masked on
those terms -- it usually names the working directory, but it could name a
password, and guessing "directory" is the assumption that leaks.

**A credential inside a connection URL is masked from the value**, since no
rule above can see it: `DATABASE_URL`, `REDIS_URL`, `AMQP_URL` and
`MONGODB_URI` name nothing secret while carrying a password. Only the URL's
userinfo is masked, not the whole value -- the scheme, host and database stay
legible, because a wholly masked `DATABASE_URL` would train the pilot to
reach for `--reveal` by habit, which is how a feature like this stops being
used.

**Both userinfo fields are masked**, because a colon says only that there are
two fields, never which one holds the secret:

| Shape | Where the credential is |
|---|---|
| `postgres://user:pw@host` | the password, on the right |
| `https://TOKEN:x-oauth-basic@host` | the username -- GitHub's documented form, beside a fixed dummy |
| `https://TOKEN:@host` | the username -- what `git credential` writes, beside an empty password |
| `https://TOKEN@host` | the whole userinfo, no colon at all |

Nothing in the structure distinguishes them, so masking one side leaks the
other half the time; the username is the cheaper thing to lose. An empty
field stays empty rather than becoming `<redacted, 0 chars>`, which would
be noise that also advertises which shape the URL is.

The authority ends at the first `/`, `?` or `#`, none of which may appear in
userinfo -- otherwise the `@` in
`https://service.test?email=a@example.com` reads as a userinfo delimiter and
an ordinary callback URL is mangled as though it carried a credential.

```
  set    DATABASE_URL=postgres://<redacted, 4 chars>:<redacted, 11 chars>@db.internal:5432/app
```

A credential in a URL **query parameter** (`?password=`, a presigned
signature) is **not** covered: recognising one needs per-scheme parameter
knowledge, and a list of parameter names would go stale the way a list of key
names does.

The length is stated
outright rather than left to be inferred from a run of masking characters. A
value keeps up to 4 characters at each end, scaled down as it shortens, and
**at least 8 characters always stay hidden** -- so anything under 12
characters is redacted whole rather than showing half of itself, which
matters because `PASSWORD` is in scope and a human-chosen password is short
and guessable enough that half of one is most of one:

```
  set    ANTHROPIC_AUTH_TOKEN=sk-a...7f2c (43 chars)
  set    ANTHROPIC_BASE_URL=http://proxy:1/v1
```

A value too short to leave that gap is redacted whole
(`<redacted, N chars>`). `--reveal` on
`env`, `env-profile`, and `info` opts back into the resolved value for that
one invocation; nothing is written to disk either way, and a key that does
not match the heuristic (`ANTHROPIC_BASE_URL` above) is never touched. This
brings the command in line with the presence-only convention the rest of the
tool's secret handling already follows for env profiles (`0600` profile
files, the sandbox's proxy-injection refusal for a key in `[env.set]`) --
only this display path used to print the resolved value outright.

**Layering.** Later layers win:

```text
process environment
  + the registry default profile, when one is set (`env-profile <name> default`)
  + each profile in env.profiles, in list order
  + the block's own env.set
  - the block's own env.unset
  + one-off launch flags, in command-line order
  + CLAUDE_CONFIG_DIR (bound by the tool; reserved -- to the playbook's
    install directory, or to $CLAUDE_CONFIG_DIR_OVERRIDE when the caller set it)
  - CLAUDE_CONFIG_DIR_OVERRIDE (consumed by the launch; never reaches the child.
    Bound here, after every layer above, so no layer can reintroduce it)
  = the child claude process's environment
```

**Caller-supplied config directory (v3.14.0).** A playbook directory serves two roles at once: it is the playbook's **content** (`CLAUDE.md`, `settings.json`, `hooks/`, `skills/`) and the sink for Claude Code's **state** (`.claude.json`, `sessions/`, `projects/`, `history.jsonl`, `cache/`).

Several sessions that share one playbook's content while each keeps its own state are **already expressible without this**, two ways: run the same playbook from different working directories, which partitions transcripts into `projects/<cwd>/` while sharing settings and login; or install the playbook more than once, which separates everything at the cost of a copy per install. Either is the ordinary answer.

What this variable adds is narrower: a caller may supply a config directory **it built itself**, without registering a playbook for it. That is the case neither of the above covers — a supervisor that composes its own directory (playbook content reached however it likes, state real and local) and wants a playbook launch to bind it. It is a seam for such a consumer, with none in this repository; unset, which is the default, nothing about a launch changes.

```bash
CLAUDE_CONFIG_DIR_OVERRIDE=/path/to/record  claude-playbook run <name>
CLAUDE_CONFIG_DIR_OVERRIDE=/path/to/record  <launcher>
```

`run` and launcher dispatch then bind the child's `CLAUDE_CONFIG_DIR` from that value instead of from the install directory. Everything downstream operates on it uniformly -- the authentication decision, credential sync and quarantine, the governing-manifest lookup -- with no special case for where the directory came from. The value is expanded (a leading `~`) and must be **absolute**: the child resolves `CLAUDE_CONFIG_DIR` against its own working directory, so a relative request would name different directories from different places, and resolving it here would hide that (`CLAUDE_CONFIG_DIR_OVERRIDE "<value>" must be an absolute or ~-prefixed path`). An empty value means unset. Existence is not checked and the directory is **not created**: provisioning it, and any playbook content it must expose, belongs to the caller; cpb binds what it is given and has no opinion about what is inside. A `settings.json` hook command spelled `$CLAUDE_CONFIG_DIR/hooks/...` therefore errors at startup if the supplied directory has no `hooks/` -- the caller's seeding problem, not cpb's.

A **bare inherited `CLAUDE_CONFIG_DIR` is still discarded**, exactly as before. The opt-in is deliberately its own variable: a value left exported in a shell would otherwise silently redirect every launcher on the machine, running a playbook's content against an unrelated directory. Keeping the names separate also keeps the two halves distinct -- `CLAUDE_CONFIG_DIR_OVERRIDE` is what the caller *requests*, `CLAUDE_CONFIG_DIR` is what the child *receives*.

`start` **names its own config directory**, so an override has nothing to override and the path argument wins: the command line outranks the environment. It says so, because the variable is consumed either way and silence would be indistinguishable from having been honoured: `CLAUDE_CONFIG_DIR_OVERRIDE ignored: start uses the directory you named, <path>` on stderr — printed before a `--sandbox-host` forward as well, since the remote never receives the variable and names the path as given there (resolving it locally would print a directory that is not the one being used) — omitted when the override names that same directory, since then nothing was ignored. A **malformed** override is reported there too (`Warning: <the refusal> (start uses the directory you named, <path>)`) rather than passed over: a caller whose value is wrong should hear about it even on the one launch shape that would not have used it. Both are warnings, not refusals — `start`'s own path is valid and the session runs.

`CLAUDE_CONFIG_DIR_OVERRIDE` is a **reserved key**, like `CLAUDE_CONFIG_DIR`: a manifest `[env]` block, an env profile, `--env` and `--env-file` all refuse it (`CLAUDE_CONFIG_DIR_OVERRIDE is managed by claude-playbook and cannot be overridden`). Declaring it could never redirect the launch that declares it — the request is read from the process environment before any layer is applied — but it would place the variable in the child's environment, from where it *would* redirect a further launch made inside the session. A key that cannot do the thing it names, yet silently affects the next launch, is refused outright.

The override is **consumed**: the same step that binds `CLAUDE_CONFIG_DIR` removes `CLAUDE_CONFIG_DIR_OVERRIDE`, on every path and whether or not this launch used it. It happens **after** the authentication branch and after every override layer, so a layer that names the key cannot reintroduce it; the reservation above and this binding are the same two-layer arrangement `CLAUDE_CONFIG_DIR` already has, where the binding is what makes the refusal unnecessary to trust. Every other subprocess the tool starts with an explicitly chosen `CLAUDE_CONFIG_DIR` — the `migrations/apply.sh` runner, the `claude auth status` probe — drops the variable too: that choice is authoritative, and a migration script that called `claude-playbook run` would otherwise be redirected. Left in place it would reach `claude` and every process below, so an agent inside the session running `claude-playbook run <other>` would have that launch redirected into this one's directory -- the wrong playbook writing into the wrong state. Stripping is an omission while building the child's environment array, not a mutation with a matching restore: a process environment is copied at spawn, is private to that process, and dies with it, so no signal, kill or crash can leave it half-done, and the tool cannot alter its parent's environment at all. One consequence worth stating: an override `export`ed interactively persists in that shell for later commands, since nothing can un-export a parent's variable, so every launch from that shell honours it -- the operator's explicit request, not a leak.

A **sandboxed launch with an override is refused** (`CLAUDE_CONFIG_DIR_OVERRIDE and a sandboxed launch together are not supported: a sandbox mounts the config directory, and content reached through symlinks dangles inside it. Launch on the host, or unset CLAUDE_CONFIG_DIR_OVERRIDE`), for `--sandbox`, `--sandbox-host`, and a manifest's `[sandbox] always = true` alike. The backend mounts directories, so a supplied directory whose playbook content is reached through symlinks -- the shape a caller provisioning its own necessarily produces -- dangles inside, the same failure a shared login's symlinked credentials have. The refusal precedes every backend call.

The **manifest refusal is unchanged**: `[env.set] CLAUDE_CONFIG_DIR` remains a hard error. A shared playbook repository must never be able to redirect a config directory; only the caller's own environment gains that power, which is a different trust model.

The registry default applies to every launch of every playbook, manifest or not, and to `start`. It is recorded in `<playbooks root>/.env-profiles/.default` (mode `0600`, the profile's name). A default that names a missing or invalid profile refuses the launch like any other profile. Only an absent marker means "no default": an empty marker, one holding an invalid name, or a dangling symlink refuses the launch too, rather than silently dropping the layer (and the token decision it may carry); `env-profile <name> undefault` clears such a marker and says why.

**Semantics at launch.** The block is first flattened: each profile in `env.profiles`, in order, then the block's own `set`/`unset` on top, where a later `set` cancels an earlier `unset` of the same key and vice versa. `PrepareLaunchEnv` then builds the child's environment as: the process environment; the authentication branch (isolation, long-lived token, or stored credentials); flattened `set` entries overriding any inherited value; flattened `unset` entries removed; finally `CLAUDE_CONFIG_DIR` bound to the launch's config directory (the playbook, or the caller's override, below). A profile named by the manifest that does not exist under `<playbooks root>/.env-profiles/` refuses the launch: `env profile "x" not found in <dir> (create it with: claude-playbook env-profile x set KEY=VALUE)`; one that exists but cannot be read or parsed refuses it too: `env profile "x": invalid env profile at <path>: <reason>`. Neither is downgraded to the advisory warning other preparation failures get, and the refusal happens before any credential sync or quarantine touches the config directory. `start` resolves profiles from the root named by its `--playbooks-dir` (or `CLAUDE_PLAYBOOKS_DIR`), the same root `run` uses. Whether the long-lived token is active is decided **with the block applied**: `unset` of `CLAUDE_CODE_OAUTH_TOKEN` means inactive (the stored-credentials path runs, the playbook's own grant is not quarantined, an inherited token is stripped); `set` of it supplies a per-playbook token that replaces the machine-global file's. The manifest governing a config directory is the nearest valid one walking up from it, so a manifest `subdir` layout is covered by the install root's block, unless the subdirectory (or a directory between it and the root) carries a manifest of its own, which then governs; `env <playbook>` shows the governing block and says which manifest it is when that is not the root's, since `env <playbook> set` edits the root manifest.

**Mutations** parse and validate every argument before taking the registry lock and rewriting the manifest (bootstrapping one for a flat playbook). `set` removes the key from `unset`; `unset` removes it from `set`; `clear` removes it from both; `use` appends profile names (moving an already-listed one to the end) after checking each exists; `unuse` removes them. An emptied block is dropped from the file. A manifest that cannot be parsed is reported as an advisory launch warning and treated as declaring nothing.

**Install-local.** `update` carries the live block forward and ignores the source's, assembling the final manifest in the staged tree *before* the overlay so a source-shipped block is never live, even transiently; `install` drops a source-shipped block with a note, assembling the install in a dot-prefixed staging directory (invisible to discovery) and renaming it into the registry only once its manifest is sanitized. A local source directory is always staged into a private copy first (in the system temp dir, or the user cache dir when that lies inside the source), so neither command ever writes into the pilot's source. A published manifest must not be able to redirect an install's API endpoint or strip its authentication.

Linked playbooks: the manifest is the LINK TARGET's shared state, so mutations are refused — edit the target's manifest directly if you really mean it.

**Errors:**
- Playbook not found → `unknown playbook "router"`
- `set` without `=` → `set expects KEY=VALUE, got "X"`
- Invalid variable name → `invalid environment variable name "bad-name"`
- `CLAUDE_CONFIG_DIR` → `CLAUDE_CONFIG_DIR is managed by claude-playbook and cannot be overridden`
- Unknown action → `unknown action "frob": expected set, unset, clear, use, or unuse`
- `use` of a profile that does not exist → `unknown env profile "x". Create it with 'claude-playbook env-profile x set KEY=VALUE'`
- Linked playbook → refused (shared manifest)

---

### `claude-playbook env-profile [name] [set KEY=VALUE... | unset KEY... | clear KEY... | describe TEXT | delete]`

Shows or manages **env profiles**: named, reusable `set`/`unset` blocks stored as `<playbooks root>/.env-profiles/<name>.toml` (mode `0600`; values may be secrets) and attached to playbooks with `env <playbook> use <name>`. **Read-only with zero or one argument.**

```bash
claude-playbook env-profile                                # list profiles as a table
claude-playbook env-profile --values                       # ...and what each one sets
claude-playbook env-profile --values --reveal               # ...with credential values in full
claude-playbook env-profile glm                            # show one, and which playbooks use it
claude-playbook env-profile glm set ANTHROPIC_BASE_URL=http://proxy:1/v1   # creates the profile on first use
claude-playbook env-profile glm unset CLAUDE_CODE_OAUTH_TOKEN
claude-playbook env-profile glm clear ANTHROPIC_BASE_URL
claude-playbook env-profile glm describe "GLM 5.3 through the local router"
claude-playbook env-profile glm default                    # registry default: under every playbook's own block
claude-playbook env-profile glm undefault                  # clear it (only if glm is the default)
claude-playbook env-profile glm delete                     # refused while any playbook uses it, or while it is the default
```

**File format** — the manifest `[env]` shape hoisted to top level:

```toml
description = "GLM 5.3 through the local router"
unset = ["CLAUDE_CODE_OAUTH_TOKEN"]

[set]
ANTHROPIC_BASE_URL = "http://proxy:1/v1"
```

Profile files are written mode `0600` and an existing file is tightened to it on every write. Profile names match `[A-Za-z0-9][A-Za-z0-9._-]*`. Keys follow the `[env]` rules (valid variable names, `CLAUDE_CONFIG_DIR` reserved, no key in both lists). Only `set` creates a profile; every other action on an unknown name is an error. Mutations validate every argument before taking the registry lock. `delete` scans the registry and refuses while a playbook references the profile, naming the users. The directory is never touched by `install` or `update`, and `delete` refuses to address it (see [`claude-playbook delete <name>`](#claude-playbook-delete-name)).

The listing marks the default with `registry default` (matched by file identity, so a case variant of the name on a case-insensitive filesystem counts) and ends with a warning when the marker is unreadable or names a profile that does not exist, since every launch is refused in that state; `env <playbook>` shows a `default   <name>` line and includes it in "Effective at launch"; a playbook without a block is shown what the default contributes at launch, or the refusal it would hit.

**Errors:**
- Unknown profile → `unknown env profile "glm". Create it with 'claude-playbook env-profile glm set KEY=VALUE'`
**Listing (v3.15.0).** The no-argument form prints an aligned table — `NAME`, `SET`, `UNSET`, `USED BY`, `DESCRIPTION` — with the counts right-aligned and the registry default marked `*` in the name column, followed by a legend when one is marked. `DESCRIPTION` is the flexible column: when stdout is a terminal it is clipped to the remaining width with an ellipsis, and when stdout is **not** a terminal nothing is clipped, so a pipe or a redirect receives every row whole. Before v3.15.0 the listing concatenated all of it into one sentence per profile (`<description> (6 set, 0 unset; used by a, b)`), which nested parentheses inside descriptions that had their own and wrapped on any ordinary terminal.

`--values` expands each profile under the table: its description, then its `set` keys with values and its `unset` keys, aligned. **Credential values are masked**: a key whose name matches `TOKEN`, `SECRET`, `PASSWORD`, `CREDENTIAL`, `APIKEY`, `AUTH`, or ends in `_KEY` (case-insensitive) prints as `first…last (N chars)` rather than in full — enough to tell one secret from another, which is the ordinary reason to look, without putting it on screen. At most 4 runes are shown at each end, and never so many that fewer than 8 stay hidden, so a short value is masked entirely as `<redacted, N chars>`. `--reveal` prints values in full and is the only way to do so.

The masking exists because profiles are where credentials live — this spec writes those files `0600` *because* "values may be secrets", and a sandboxed launch refuses a key set in a playbook's own `[env.set]` in order to push it into a profile. A status display that printed them would therefore print credentials by default, into terminals and agent transcripts, which is not a place a secret returns from.

- Delete while attached → `env profile "glm" is used by router, sre; detach it first with 'claude-playbook env <playbook> unuse glm'`
- Delete while default → `env profile "glm" is the registry default; clear it first with 'claude-playbook env-profile glm undefault'`
- `undefault` of a profile that is not the default → `the registry default is "x", not "glm"` or `no registry default is set`; `undefault` with an unreadable marker clears it and reports `Registry default marker was invalid (<reason>); cleared.`
- Invalid file on disk → `invalid env profile at <path>: TOML syntax error at line <n> (content not shown)` (listing and launch both fail loudly rather than skip it; the parser's message, which quotes file content, is never echoed)

---

### `claude-playbook auth status [name...]`

Read-only view of how each playbook authenticates and what its stored login looks like. Nothing is written, no credential value is read into the output, no network call is made, no process is spawned unless `--claude` is given.

```bash
claude-playbook auth status                 # every playbook, ~/.claude first
claude-playbook auth status sre router      # named ones
claude-playbook auth status --json
claude-playbook auth status --claude        # add 'claude auth status --json' per directory
```

**Columns:**

| Column | Meaning |
|--------|---------|
| `MODE` | How a launch would authenticate, decided exactly as `run` decides it: `token` (machine-global long-lived token injected), `own-token` (a token the manifest or a profile sets), `own-login` (token unset for this playbook; stored login), `shared-login` (no token anywhere; stored login), `isolated` (`isolate_auth`), `error` (the launch would be refused, reason in `NOTE`). An isolated playbook whose manifest or profile sets a non-empty token is `own-token (isolated)`, matching the launch, which injects that token and quarantines the stored grant. For `~/.claude`, which claude-playbook does not launch, only an exported `CLAUDE_CODE_OAUTH_TOKEN` counts as `token`. |
| `STORE` | What sits at `.credentials.json`: `symlink -> <target>`, `file`, `file (no grant)`, or `absent`. |
| `EXPIRES` | The stored grant's `expiresAt` as `in 6h12m`, `expired`, `unknown`, or `-` when there is no grant. |
| `DAEMON` | Claude Code's `daemon-auth-status.json`: `auth_required` when its `since` is at or after the current grant's refresh instant (`expiresAt` minus 4 minutes, the daemon's proactive-refresh lead) and the row is a stored-login mode, `<status> (stale)` otherwise (the file is never cleared on recovery; under a token mode the stored login is quarantined and unused; for an isolated playbook whose store is still a symlink to the shared one, the marker concerns a login the launch detaches, so it never counts against that playbook), `-` when absent. |
| `NOTE` | `launch refused` (with a sanitized reason: no file content is ever echoed), `re-auth required`, `no login; stale account state, purged at launch` (an isolated playbook without a login of its own still carrying `oauthAccount` or cached feature flags; `--json` lists them under `stale_identity`), `stale account state, purged at launch (the shared login is detached at launch)` (the same, for an isolated playbook whose store is still a symlink to the shared one), `no login`, `grant expired (refreshes at launch if the refresh token is still valid)`, or empty. Token modes have no stored login to judge and show nothing. An explicitly empty token set by the manifest or a profile is `own-login`, matching the launch decision. |
| `CLAUDE` | With `--claude`: `logged in, <subscription>`, `not logged in`, or `error: <reason>`. |

When a long-lived token file exists, a trailing line names it and notes that its own expiry is not recorded anywhere.

`--json` emits one object per row with the raw fields (`name`, `dir`, `mode`, `mode_error`, `isolated`, `store`, `store_target`, `has_grant`, `expires_at`, `expired`, `daemon_status`, `daemon_since`, `reauth_required`, `stale_identity` (when non-empty), `token_file`, and `claude` when requested). `reauth_required` is only ever true for a stored-login mode with a grant present whose `expiresAt` is known and a marker whose `since` is known; a marker that cannot be ordered against the grant is reported as stale.

**Errors:**
- Named playbook not found → `unknown playbook "x". Run 'claude-playbook list' to see available playbooks`

---

### `claude-playbook dealias <name>`

Removes the playbook's alias and its launcher. Exactly equivalent to `claude-playbook alias <name> --remove`, provided as a standalone verb for convenience.

```bash
claude-playbook dealias sre
```

The playbook directory itself is untouched.

**Errors:**
- Playbook not found → `unknown playbook "sre". Run 'claude-playbook list' to see available playbooks`

---

### `claude-playbook delete <name>`

Deletes a playbook. (Aliases: `uninstall`, `unlink`.)

```bash
claude-playbook delete experiment        # prompts
claude-playbook delete sre -y            # skip the prompt
```

**Confirmation prompt:**
```
Playbook: sre
Location: ~/.claude-playbooks/sre
Alias:    sre
Command:  sre (launcher will be removed)
Contents: 12 files, 3 directories

Permanently delete? [y/N]
```

The `Alias` line shows the manifest alias (`(none)` when unset) and promises nothing about its launcher; a `Command` line appears for each launcher matching the playbook's name or manifest alias, stating what the delete will do with it: `launcher will be removed`, `launcher kept; still addresses playbook "x"`, or `launcher kept; ownership could not be verified` when the registry cannot be scanned.

**Deletion scope:**
- The target directory (for a symlink, the link is removed; the symlink target is preserved).
- Launcher symlinks named after the playbook or its manifest alias, claim-aware: a command name that still resolves to another playbook, by spelling or by directory-entry identity, keeps its launcher outright (`Kept command "sre" (still addresses playbook "other")`); one whose ownership cannot be verified (registry scan failed) is kept with a warning; every other launcher is removed (`Removed command "sre"`), receipt line included. See the retirement rule under [Launcher Commands (v2.13.0)](#launcher-commands-v2130).

**Flags:**

| Flag | Description |
|------|-------------|
| `-y`, `--yes` | Skip the confirmation prompt |

**Errors:**
- Name not found → `"experiment" not found under ~/.claude-playbooks`
- Name is the profile store → `".env-profiles" is the registry's env profile store, not a playbook; remove a profile with 'claude-playbook env-profile <name> delete'`. Discovery skips dot-prefixed entries, so without this guard the name would reach the orphan path below and remove every profile and the default marker in one confirmation. The store is protected along its whole resolution, established the way the kernel establishes it, one component at a time: the registry entry (matched by `Lstat` identity even when it is a dangling link), every symlink met on the way whether in the final component (`.env-profiles -> .bridge -> /x/profiles`) or in a parent component (`.env-profiles -> .bridge/profiles` with `.bridge -> .leftover/sub`), every real directory traversed (one entered and left again through `..`, `.env-profiles -> .leftover/../.profiles`, is still required by the kernel), and the final physical directory. The kernel's own verdict on the entry (`Stat`) is taken first, and the component walk must agree with it; a resolution that cannot be established (a dangling link, a link loop, a chain the kernel refuses such as a long acyclic one, a file used as a directory, an unreadable component) refuses every delete outright until the store is repaired: `cannot verify that deleting "<name>" leaves the registry's env profile store intact (<store>: <reason>); nothing removed`. Every check is by file identity (`os.SameFile`), never by spelling: a case variant on a case-insensitive filesystem, a relative `--playbooks-dir`, or a symlink on either side changes nothing. A path that IS one of those elements is refused (`Lstat` identity, so a leftover symlink that merely points at the store is still deletable, the link alone going; a separate hard link to the entry shares that identity and is refused on the safe side); an intermediate link or traversed directory reports `"<name>" is a link|a directory the registry's env profile store resolves through (<store> -> <element>); the store would become unreachable. Repoint <store> first`. A profile file (`*.toml`) or the `.default` marker that is a directory entry of the store's physical directory (reachable when the store resolves to the directory holding the entry, `.env-profiles -> .`) is refused too, while a linked playbook or a stray file beside them is not the store's and stays deletable: `"<name>" is an entry of the registry's env profile store (<store> resolves to the directory holding it); remove a profile with 'claude-playbook env-profile <name> delete'`, so the reference and default checks of `env-profile <name> delete` cannot be bypassed. A real directory whose subtree contains any of those elements is refused as well, judged both by walking each element's ancestors and by descending the directory itself the way `RemoveAll` would (without following symlinks) and comparing identities, which is what catches an element reachable only through a bind mount inside the directory: `"<name>" contains the registry's env profile store or a path it resolves through (<store> -> <element>); move the store out or remove profiles with 'claude-playbook env-profile <name> delete' first`. The checks run before the prompt and again under the registry lock against the path actually removed, so a store created, moved, or linked while the prompt was open is still protected.

**Threat model of the guard.** The store is protected against what `claude-playbook` itself and an ordinary pilot can do: the name, its case variants, a relocated store (a symlinked entry, however many hops), a relative or symlinked playbooks root, and the deletion subtree. It is not a defence against a pilot who rearranges their own registry by hand into shapes the tool never creates (a store bind-mounted into a leftover, a store resolving to the playbooks root); those are covered where cheap and refused as unverifiable where not, but the general doctrine holds: hand-mutated state fails loudly, it is not guarded exhaustively.

**Graceful cases:** if the directory is already gone, the command still cleans up any dangling aliases and reports success. A dot-named directory that exists under the root but is not a discoverable playbook (a leftover, never the profile store) is removed through the orphan path after an explicit confirmation naming it as such.

---

### `claude-playbook update [name]`

Updates either the `claude-playbook` tool itself, or a specific playbook — based on whether a name is given.

#### `claude-playbook update` (no arguments) — self-update

Updates the running `claude-playbook` binary in place to the latest GitHub release.

```bash
claude-playbook update            # download + install the latest release
claude-playbook update --check    # report the latest version without installing
claude-playbook update --force    # reinstall even if already on the latest
```

It resolves the latest release tag from the GitHub API, downloads the asset for
the running OS/architecture (`claude-playbook-<goos>-<goarch>`), verifies it by
running `--version` against the downloaded file, and then atomically replaces
the current executable (it stages a temp file in the executable's own directory
and `rename`s it into place, so the swap is atomic and never a partial write).
Symlinks are resolved first, so invoking through the `cpb` symlink updates the
real binary and leaves the link intact.

```
Current version: v1.2.0
Latest version:  v1.3.0
Downloading claude-playbook-darwin-arm64 v1.3.0 (darwin/arm64)...
Updated to v1.3.0 at /Users/you/.local/bin/claude-playbook.
```

If already on the latest version, it prints `Already up to date.` and exits
(pass `--force` to reinstall anyway). If the install directory is not writable
(e.g. a root-owned `/usr/local/bin`), it reports that elevated privileges are
needed. `GITHUB_TOKEN`, when set, is used for the GitHub API request to avoid
rate limits.

**A Nix-managed binary is never replaced.** When the resolved executable lies in
`/nix/store/` (installed through devbox, `nix profile` or the flake), the store
is read-only and content-addressed: replacing a file there would corrupt the
package, and the generic permission advice would suggest `sudo` against it. So
`update` and `update --force` exit non-zero **before any release lookup** (no
network, whatever the latest version is), telling the operator to change the
tag in `devbox.json` and run `devbox install` (a `devbox add` with a different
ref appends a second package rather than replacing the first); an up-to-date store binary never answers *Already up to
date.* as if it could update itself. `update --check` still reports, and prints
the same hint instead of *Run 'claude-playbook update'*. The decision uses the
symlink-resolved path, because under devbox `argv[0]` is the profile's symlink,
not the store.

#### `claude-playbook update <name>` — update a playbook

Updates the playbook from the `[source]` metadata recorded in its `.playbook`. The CLI owns the update end to end; there is no delegated update script.

```bash
claude-playbook update sre
claude-playbook update sre --check
```

**Behaviour:**
1. Resolve the named playbook and require `[source].repository` metadata.
2. Refuse linked playbooks and installs whose config is selected through a top-level `subdir`.
3. Validate `[update].preserve` before touching anything, so an escaping path fails before the overlay starts.
4. Fetch `[source].repository` at the recorded branch and source subdirectory into a staging directory.
5. With `--check`, report the installed version (`version` from the live `.playbook`) against the available one (`version` from the staged `.playbook`, falling back to the staged `VERSION` file), and stop.
6. Take the registry lock and re-read the live manifest. If the install directory is no longer the same filesystem object, or any `[source]` field changed while staging ran, activate nothing.
7. Move every top-level entry the staged source also provides into a timestamped `.<name>.bak.<stamp>` beside the install, then copy the staged source over the install **in place**. Entries the source does not ship — `data/`, `projects/`, `sessions/`, `history.jsonl` and the like — are never read, moved, or copied, so concurrent writes to them cannot be lost. The live root directory keeps its own mode; only entries below it take the source's. A failure at any point rolls back: entries the source **introduced** are removed and moved entries are restored from the backup; if any restoration fails the backup is kept and named in the error rather than deleted.
8. Restore the preserved files over the incoming copies: `settings.json`, `settings.local.json`, `.credentials.json`, `.claude.json`, plus every path in `[update].preserve`. A preserved file the source ships but the install did not have is removed rather than adopted, whether it arrived under a moved entry or a newly introduced one. Preserved paths under an entry the overlay never touched are left alone. Whether a preserved path's top-level entry was touched is decided by filesystem identity, not spelling, so on a case-insensitive filesystem a source-shipped `SETTINGS.JSON` is the preserved `settings.json`. Before anything is removed or written, every ancestor between a nested preserved path and its top-level entry is checked for **physical** containment inside that entry (symlinks evaluated, nearest existing ancestor; the final component itself is never followed, so a preserved entry that is a symlink such as the shared `.credentials.json` is recreated as a link). A source that turned a directory on the path into a symlink — pointing outside the install, or into a sibling entry the overlay never backed up — makes the update fail and roll back: `<rel> resolves outside <top>/ after the update (a symlinked ancestor); refusing to restore through it`.
9. Assemble the manifest that goes live in the staged tree before the overlay: local alias, authentication-isolation, `[env]` overrides, the `[sandbox]` block, and source metadata are preserved from the live manifest (a source-shipped `[env]` or `[sandbox]` block is never adopted, not even transiently), and the install's `name` is always reset to its directory name.
10. If `migrations/apply.sh` exists in the updated install and is executable, run it as `migrations/apply.sh <from-version> <to-version> <install-dir>` with working directory the install and `CLAUDE_CONFIG_DIR`, `CLAUDE_PLAYBOOK_TARGET`, `CLAUDE_PLAYBOOK_PATH` in the environment. Runners are expected to be idempotent; the CLI does not track which migrations have run. Migrations are skipped with a warning when either side has no `version`.

**Errors:**
- Target not found → `unknown playbook "sre". Run 'claude-playbook list' to see available playbooks`
- Source metadata missing → `"sre" has no [source] metadata in .playbook; nothing to update from`
- Linked install → `"sre" is linked; native update is disabled to avoid replacing its external source`
- Subdir-selected install → `"sre" uses manifest subdir "...": native update requires a flat playbook`
- Extra arguments → `unexpected argument "..."; `update <name>` accepts only --check`
- Install changed while staging → `playbook "sre" changed while the update was staging (deleted, re-created, or re-sourced); nothing activated -- re-run update`
- Migration runner exits non-zero → `"sre" is at code version <v> but migrations failed: <err>`

#### `claude-playbook update --all` — withdrawn (v3.14.0–v3.14.x)

Shipped in v3.14.0 and **withdrawn in v3.15.0**; it may be implemented again
later. Playbooks are updated one at a time:

```bash
claude-playbook update <name>
```

The flag remains in the parser deliberately. Without the case, `--all` falls
through to the argument branch and is taken for a playbook name, so a script
carrying it over from v3.14.0 would get `unknown playbook "--all"` rather than
an explanation.

**Errors:**
- `--all` in any form → `--all is not available in this version; update playbooks one at a time (`claude-playbook update <name>`)`

---

### `claude-playbook self-uninstall`

Removes `claude-playbook` and everything it created: all playbooks, their launcher commands, any `source <(... completion ...)` lines in shell rc files, the playbooks root directory, and the binary itself. The complete undo for an install.

```bash
claude-playbook self-uninstall               # prompts
claude-playbook self-uninstall -y            # skip the prompt
claude-playbook self-uninstall --dry-run     # show what would be removed
claude-playbook self-uninstall --keep-data   # remove the binary but keep playbooks
claude-playbook self-uninstall --binary-only # remove binary/launchers/completions, keep playbooks (what uninstall.sh runs)
```

**Steps:**
1. For each discovered playbook (unless `--keep-data` or `--binary-only`): remove its directory.
2. Unless `--keep-data` or `--binary-only`, remove the playbooks root directory.
3. Unless `--keep-binary`, sweep **all** launcher symlinks pointing at the binary — every one of them would dangle once the binary is gone, whichever registry root it served. The sweep unions two sources: a resolution scan of the standard launcher directories (the resolved launcher dir plus the `~/.local/bin` fallback), and the launcher receipt file (`~/.local/state/claude-playbook/launchers`, one absolute launcher path per line; the two tab-separated fields v3.10.1 appended are still read by their path and dropped when a line is rewritten) in which every launcher the tool creates is recorded — covering custom `--launcher-dir` locations the scan cannot know about. Every candidate is verified to still be a symlink resolving to this binary (or dangling); a link the user renamed or repointed resolves elsewhere and is left alone. The reserved names (`claude-playbook`, `cpb`) are owned by the binary-removal step. Once the launchers are gone, the receipt is removed too. With `--keep-binary`, no launchers are touched: a same-named command may be serving another registry root, and a removed default-root playbook's launcher fails loudly as stale rather than being silently deleted.
4. Unless `--keep-binary`, remove any `source <(claude-playbook|cpb completion bash|zsh)` lines from `~/.bashrc` and `~/.zshrc` — after the binary is gone they would error on every new shell.
5. Unless `--keep-binary`, remove the running binary and its sibling `cpb`/`claude-playbook` link. If removal is denied by permissions, print the `sudo rm <path>` command to run manually rather than failing.
6. Print a summary of what was removed. When completion lines were removed from rc files, remind the user that already-open shells still hold the stale completion functions until reloaded. In `--binary-only` mode, state explicitly that the playbooks directory was not touched.

**Flags:**

| Flag | Description |
|------|-------------|
| `-y`, `--yes` | Skip the confirmation prompt |
| `--keep-data` | Preserve the playbooks directory and its playbooks |
| `--keep-binary` | Leave the binary in place |
| `--binary-only` | Remove only the binary, its `cpb` sibling, launchers, and completion lines — playbooks stay untouched (the mode `uninstall.sh` delegates to) |
| `--dry-run` | Print what would be removed without changing anything |

`--dry-run` never prompts and never modifies anything. Without `--dry-run` or `-y`, the command prints what will be removed and asks for confirmation.

---

### `claude-playbook completion [bash|zsh|fish|powershell]`

Generates a shell completion script. Auto-generated by cobra and includes completion for subcommands, flags, and playbook names.

```bash
# zsh
claude-playbook completion zsh > "${fpath[1]}/_claude-playbook"

# bash
claude-playbook completion bash > /etc/bash_completion.d/claude-playbook

# fish
claude-playbook completion fish > ~/.config/fish/completions/claude-playbook.fish
```

Playbook name completion is wired for commands that take a name: `run`, `delete`, `info`, `rename`, `alias`, `dealias`, `auth status`, `env`, and `update`. It completes the first argument only.

The bash script needs bash 4.2+ and the bash-completion package (it calls `_get_comp_words_by_ref`); the zsh script needs `compinit` to have run (it calls `compdef`). See `docs/installation.md`.

---

## Playbook Manifest

The `.playbook` manifest is **optional** and holds **metadata only**. It never affects discovery, and it never declares other playbooks. A directory without a `.playbook` is a perfectly valid playbook.

**Format:**

```toml
version = "1.0.0"
name = "sre"
alias = "sre"
description = "Site Reliability Engineering assistant"
homepage = "https://github.com/ramazanpolat/awesome-playbooks"
author = "Ramazan Polat"

[source]
repository = "https://github.com/example/playbooks"
branch = "main"
subdir = "playbooks/sre"

[update]
preserve = ["settings.json"]
```

**Fields:**

| Field | Meaning |
|-------|---------|
| `version` | Version of the playbook itself (free-form semver string). Shown by `info`. Not enforced by the tool. |
| `name` | Preferred playbook name. `install` uses it as a suggestion; the actual name is always the install directory name. |
| `alias` | Preferred alias for `install`/`create` to suggest when writing the default alias. |
| `subdir` | Optional. Used for backward compatibility. Points at a subdirectory of the install that holds the Claude config. New installations will automatically extract the subdir flatly into the target directory and clear this field in the manifest to ensure all playbooks remain flat at the root level. |
| `description` | Human-readable description, shown by `info`. |
| `homepage` | Optional URL, shown by `info`. |
| `author` | Optional author name or contact, shown by `info`. |
| `isolate_auth` | When true, detach shared credentials and do not copy global credentials or account metadata into this playbook; while it has no login of its own, account state left from a non-isolated past (`oauthAccount`, cached feature flags) is removed at launch. The machine-global long-lived token and plan descriptors never reach it; a `CLAUDE_CODE_OAUTH_TOKEN` this playbook's own `env.set` (or an attached profile) supplies is honoured as its own token, with its stored grant quarantined as on the shared token path. The manifest governing a config directory is the nearest valid one walking up from it; an unreadable manifest on the way is reported but does not switch isolation off. |
| `env.profiles` | Optional list of env profile names (files under `<playbooks root>/.env-profiles/<name>.toml`) layered under `env.set`/`env.unset` at launch, in list order. A profile that is missing, unreadable, or invalid refuses the launch. Install-local. |
| `env.set` | Optional table of environment variables applied to the child `claude` process on every launch, overriding inherited values. Install-local: never adopted from a source. A manifest carrying any `env.set` value is written owner-only (its existing mode masked to `0600`; values may be tokens); otherwise an existing file keeps its mode exactly, and a new one is `0644`. A rewrite never loosens a file. |
| `env.unset` | Optional list of environment variable names removed from the child's environment on every launch, even when the shell exports them. Install-local. Unsetting `CLAUDE_CODE_OAUTH_TOKEN` makes the long-lived token inactive for this playbook (stored-credentials path: no injection, no quarantine). |
| `source.repository` | Git URL or local source used by native update. Git installs populate this automatically. |
| `source.branch` | Optional Git branch or tag used by native update. |
| `source.subdir` | Optional source-relative directory selected during native update. Must remain physically below the fetched source, including through symlinks. |
| `update.preserve` | Optional list of install-local paths that survive an update even when the source ships its own copy. Each must be relative to and physically below the playbook root. `settings.json`, `settings.local.json`, `.credentials.json` and `.claude.json` are always preserved and need not be listed. |
| `sandbox.always` | When true, every launch of this playbook is sandboxed (`run`, launcher dispatch; `start` for a directory carrying the manifest); `--no-sandbox` overrides one launch, loudly. Install-local: `create --sandbox` and `install --sandbox` write it with `isolate_auth = true`; never adopted from a source; preserved by `update`. |
| `sandbox.host` | Optional ssh destination (`user@host`; ports and jump hosts through `~/.ssh/config`) where sandboxed launches of this playbook run, with `claude-playbook` and the playbook installed there. `--sandbox-host` overrides it for one launch. Install-local. |
| `sandbox.backend` | Optional sandbox implementation name; the only value is `sbx` (Docker Sandboxes), which is also the default. `--sandbox=BACKEND` overrides it for one launch. Install-local. |
| `sandbox.share_skills` | When true, the backend's shared skills store is mounted into the sandbox (what `sbx` does on its own). Default false: `create` passes `--no-share-skills`, so a sandbox cannot plant a skill a later sandbox runs. Creation-time. |
| `sandbox.secrets` | How backend API keys reach the sandbox: `"proxy"` (default; an empty string is the same as omitting the key) registers them as proxy-injected secrets and hands the sandbox a placeholder; `"env"` passes the values as plain variables. Under `"proxy"`, a key set in the playbook's own `[env.set]` refuses the launch (the manifest is on the mount); keep such keys in env profiles. |
| `sandbox.workdir` | Optional default working directory for `run --sandbox`, absolute or `~`-prefixed; `--workdir` overrides it, the invocation directory is used when neither is given. |
| `sandbox.mounts` | Optional list of extra host paths mounted into the sandbox at the same absolute path, each absolute or `~`-prefixed, with `:ro` as the only accepted option (read-only). Nothing else of the host is visible inside. |
| `sandbox.allow_net` | Optional list of hosts (domains, wildcards, CIDR ranges, no whitespace) allowed for this playbook's sandbox on top of the active sandbox policy, applied once when the sandbox is created. The host of an `ANTHROPIC_BASE_URL` the effective environment sets is allowed automatically. |
| `sandbox.claude_version` | Optional Claude Code version (`2.1.263`) installed inside the sandbox at creation; empty runs the sandbox image's own. A playbook routed to a backend that rejects a newer Claude Code's tool schemas pins the last version that works. |

**Forward compatibility:** unknown fields are ignored. Manifest authors may include fields for future tool versions without breaking older installs.

**Errors:**
- Invalid TOML → `invalid .playbook at <path>: TOML syntax error at line <n> (content not shown)`. The parser's own message is never echoed: since v3.5.0 a manifest may hold credential values under `[env.set]`, and this error reaches the terminal from every command that discovers playbooks.
- `subdir`, `source.subdir`, or any `update.preserve` entry escapes its root → `invalid .playbook at <path>: <field> must be a relative path below the playbook root`. One helper validates all three, so the field name is the only difference between them.
- `subdir` or `source.subdir` names a path that does not exist, or is not a directory → `<field> "<value>" not found below <root>: <stat error>` / `<field> "<value>" is not a directory below <root>`. Raised when the path is resolved, so it carries the root it was resolved against rather than the manifest path.
- An `env` key is not a valid variable name → `invalid .playbook at <path>: env.set: invalid environment variable name "<key>"`
- An `env` key is `CLAUDE_CONFIG_DIR` or `CLAUDE_CONFIG_DIR_OVERRIDE` (the reserved keys) → `invalid .playbook at <path>: env.set: <key> is managed by claude-playbook and cannot be overridden`
- A key appears in both `env.set` and `env.unset` → `invalid .playbook at <path>: env: <key> is both set and unset`
- An `env.set` value is not valid UTF-8 → `invalid .playbook at <path>: env.set: value of <key> is not valid UTF-8 and cannot be stored in a manifest`.
- A `sandbox.mounts` entry is relative, carries another option than `:ro`, or contains a colon or line break → `invalid .playbook at <path>: sandbox.mounts entry "<entry>" must be an absolute or ~-prefixed path, optionally suffixed :ro`
- A `sandbox.allow_net` entry is empty or contains whitespace → `invalid .playbook at <path>: sandbox.allow_net entry "<entry>" must be a host, wildcard or CIDR without whitespace`
- `sandbox.claude_version` is not `MAJOR.MINOR.PATCH` → `invalid .playbook at <path>: sandbox.claude_version "<value>" must look like 2.1.263`
- `sandbox.workdir` is relative → `invalid .playbook at <path>: sandbox.workdir "<value>" must be an absolute or ~-prefixed path`
- `sandbox.backend` unknown → `invalid .playbook at <path>: sandbox.backend "<value>" is not a known backend (sbx)`
- `sandbox.host` with whitespace, a slash, or a leading `-` → `invalid .playbook at <path>: sandbox.host "<value>" must be an ssh destination such as user@host`
- `sandbox.secrets` not `proxy` or `env` → `invalid .playbook at <path>: sandbox.secrets "<value>" must be "proxy" or "env"`
- An `env.set` value contains a NUL byte → `invalid .playbook at <path>: env.set: value of <key> contains a NUL byte, which cannot be passed in an environment` (refused on write and on read; a NUL in an environment fails every launch). Any other value round-trips: control characters are written as TOML `\uXXXX` escapes.
- An `env.profiles` entry is not a valid profile name → `invalid .playbook at <path>: env.profiles: invalid profile name "<name>": use letters, digits, dots, dashes, underscores`

---

## Aliases

A playbook's alias is one alternate command name, stored as the `alias` field of its `.playbook` manifest — no rc files, no separate registry. Dispatch resolves directory names first, then manifest aliases; the alias is materialized as a launcher command like the playbook's own name. `create --alias`, `install --alias`, `link --alias`, `rename --alias`, and `alias <name> <alias>` all write the same field.

---

## Global Flags

These flags work on every command.

| Flag | Description |
|------|-------------|
| `--playbooks-dir <path>` | Override the playbooks root directory. Default: `~/.claude-playbooks` |
| `--launcher-dir <path>` | Override the launcher directory. Default: the directory of the binary as invoked, falling back to `~/.local/bin` when unwritable |
| `--version` | Print the version of `claude-playbook` |
| `--help`, `-h` | Show help for the command or subcommand |

### Environment variables

| Variable | Flag equivalent |
|----------|----------------|
| `CLAUDE_PLAYBOOKS_DIR` | `--playbooks-dir` |
| `CLAUDE_LAUNCHER_DIR` | `--launcher-dir` |

**Resolution precedence:** CLI flag → environment variable → default.

These have no flag equivalent:

| Variable | Effect |
|----------|--------|
| `CLAUDE_PLAYBOOKS_ISOLATE_AUTH=true` | Forces the isolation branch of the authentication decision for this launch, as `isolate_auth = true` in the manifest does. |
| `CLAUDE_PLAYBOOKS_OAUTH_TOKEN_FILE` | Overrides the long-lived token file read in step 2 of the authentication decision. Default `~/.config/claude-code/oauth-token`. |
| `CLAUDE_CONFIG_DIR_OVERRIDE` | The config directory `run` and launcher dispatch bind, in place of the playbook's install directory. Absolute or `~`-prefixed; empty means unset; never created. Consumed -- stripped from the child's environment after every layer. A **reserved key**: a manifest, profile, `--env` or `--env-file` that declares it is refused. Refused together with a sandboxed launch. A bare `CLAUDE_CONFIG_DIR` is still discarded. See *Environment overrides*. |
| `XDG_STATE_HOME` | Parent of the launcher receipt directory (`<XDG_STATE_HOME>/claude-playbook/launchers`). Default `~/.local/state`. |
| `CLAUDE_LAUNCHER_RECEIPT` | Absolute path of the launcher receipt file, overriding the `XDG_STATE_HOME` computation. A test seam; not part of the supported surface. |
| `GITHUB_TOKEN` | Sent as the bearer credential on the release-API requests `update` (no name) makes, raising the anonymous rate limit. |
| `CLAUDE_PLAYBOOK_UPDATE_REPO`, `CLAUDE_PLAYBOOK_UPDATE_API_BASE`, `CLAUDE_PLAYBOOK_UPDATE_DOWNLOAD_BASE` | Redirect self-update at another repository, API, or asset host. Test seams; not part of the supported surface. |

A variable marked *test seam* is honoured by the binary but carries no compatibility promise: it exists so the suites can run without network or a real release, and may change or disappear in any version.

---

## Exit Codes and Error Conventions

- Exit code `0` on success, non-zero on failure. Cobra's default is `1` for user errors.
- All errors go to stderr.
- Messages are plain English, one line, no stack traces. Always suggest the next action where possible.

**Examples:**
```
Error: "myrepo" already exists at ~/.claude-playbooks/myrepo. Use --name to choose a different name
Error: unknown playbook "typo". Run 'claude-playbook list' to see available playbooks
Error: 'claude' command not found. Install Claude Code first: https://claude.ai/download
Error: source.subdir "playbooks/sre" not found below /tmp/stage: lstat /tmp/stage/playbooks: no such file or directory
Error: "sre" has no [source] metadata in .playbook; nothing to update from
Error: invalid .playbook at ~/.claude-playbooks/foo/.playbook: toml: line 3: expected '=', got ':'
```
