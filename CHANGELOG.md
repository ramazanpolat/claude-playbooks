# Changelog

## [Unreleased]

### Added

- **Playbook settings, ClickHouse style** (SPEC.md, *Playbook settings*):
  `CREATE PLAYBOOK … SETTINGS login = 'isolated', memory = 'shared'`,
  `ALTER PLAYBOOK … MODIFY SETTING …` and `RESET SETTING …`. Two settings:
  - `login` (`'shared'`, `'isolated'`) is what `ISOLATED LOGIN` was.
  - `memory` (`'isolated'`, `'shared'`) is new. Claude Code loads project
    memory from every ancestor of the working directory, so `~/.claude`'s
    own `CLAUDE.md` and `rules/` loaded into every playbook run under
    `$HOME`. `'isolated'` keeps them out with one `claudeMdExcludes` entry
    in the playbook's `settings.json`, which every launch path reads. **A
    new playbook is isolated by default**; an existing one keeps loading
    them until `MODIFY SETTING memory = 'isolated'` (nothing is migrated).
  - `SHOW`, `EXPLAIN` and their `--json` (`"settings"`) report them,
    `SELECT`'s `PLAYBOOKS` has a `settings` column, and `SHOW CREATE` writes
    `MODIFY SETTING` (memory always, so a recipe never leans on a default).
    `cpb play` keeps both isolated: a recipe may isolate more, never less.
- **`EXPLAIN PLAYBOOK --json` gains `route`** (#209): where a launch from
  this environment sends its requests (base URL, host, models), an
  authentication state (`none`, `token-set`, `oauth-login`, `unknown`) and an
  egress class (`anthropic`, `unknown`), from non-secret values and states
  only. `auth status` finds a login kept in the Keychain.

- **`cpb update` follows the files a playbook was applied from** (SPEC.md,
  *The `[apply]` record*). A clause a base file dropped used to stay in every
  playbook built on it until removed by hand.
  - An `APPLY` that gives a playbook name-less statements (by `TO` or `USE
    PLAYBOOK`) records the files in its manifest as `[apply]`, and what the
    statements wrote in `.apply/recipe.cpb`. The record holds references
    only: a credential-looking literal is kept as `<withheld>`.
  - `cpb update <name>` reads the files again and runs only the statements
    they give `<name>`. First, one `ALTER PLAYBOOK` removes what they wrote
    last time and no longer write the same way: the same undo as a played
    playbook's update. A status line `SET STATUSLINE … IF UNSET` offered is
    removed only where the files wrote it. `--dry-run` shows the line diff
    and the plan; `--json` gives the plan as `APPLY --dry-run --json` does.
  - Not recorded: a dry run, a directory target, a linked playbook, files
    from a pipe, a statement that names its playbook, and a playbook that
    updates from `[play]` or `[source]`.
  - `SHOW PLAYBOOK` has an `Applied from:` line, and a new last field
    `apply` in `--json`. `SELECT` has a new `apply` column. The error for a
    playbook with nothing to update from now names `[apply]`.

### Changed

- **`ISOLATED LOGIN` is gone** for `SETTINGS login = 'isolated'` (CREATE)
  and `MODIFY SETTING login = 'isolated' | 'shared'` (ALTER). A recipe from
  rc1 or rc2 that says `ISOLATED LOGIN` is refused with that hint; edit the
  line. State on disk is unchanged (`isolated_login` in the manifest).

- **`cpb update` undoes env sets and sandbox settings too** (SPEC.md,
  *`cpb update <name>`*). Before an update, the undo now also drops an env
  set the files no longer attach (`DROP ENV`) and unsets a sandbox setting
  they no longer set (`UNSET SANDBOX <key>`; `always` for a bare `SET
  SANDBOX`). A base that stops routing through an env set now stops routing
  its children through it at their update. The login is still never unset.
  A played recipe can hold neither `ENV` nor `SANDBOX` clauses, so for
  play only the shared code moves.
- **A setting set more than once is written once** (SPEC.md, *A setting set
  more than once*). In a stack of files (a base recipe, then the child that
  overrides it), `APPLY` used to write the base's value and then the
  child's on every run. A stack never converged: each re-apply reported
  changes, and the live configuration held the base's value in between.
  - `APPLY` now drops a clause, or an entry of a list clause, whose key a
    later statement for the same target replaces outright. That covers a
    variable, a tool rule, the model, the agent, the status line and its
    refresh, a sandbox setting, and `USE ENV`.
  - The run and the plan name what was dropped and where it is set again,
    and `--dry-run --json` adds `overridden` to the statement (a new field).
  - A statement left with nothing to write is `unchanged`, so applying a
    stack again reports `0 changed`.

### Fixed

- **A played playbook's update failed when the recipe dropped both
  its status line and its refresh.** Its undo held `UNSET STATUSLINE` and
  `UNSET STATUSLINE REFRESH`, which one statement may not combine. The undo
  is now `UNSET STATUSLINE` alone, which removes the refresh too.

## [v4.0.0-rc2] -- 2026-10-03

### Changed

- **A playbook whose manifest cannot be read no longer takes the others
  down** (SPEC.md, *Unreadable manifests*). In rc1, one `.playbook` with a
  key cpb does not define made every command fail, every launcher
  included.
  - Statements that name another playbook run, and so do their launchers.
  - The unreadable playbook itself is refused with its read error (the file
    and the line), and so is its launcher.
  - Lists print what they can read, then one stderr line per playbook left
    out (`playbook "<name>" is left out: …`), and exit 1. `--json` keeps
    its shape. The lists are `SHOW PLAYBOOKS`, bare `cpb`, `SELECT`,
    `SHOW ENV(S)`, `SHOW SESSIONS` and `cpb auth status`. The TUI shows the
    rows, with the line in its status bar.
  - Refused while a manifest cannot be read, naming the file:
    - `SHOW CREATE ALL`;
    - `DROP ENV`;
    - every statement that claims a launcher name (`CREATE PLAYBOOK`,
      `RENAME TO`, `LAUNCHER`, `cpb play --keep`).
    `APPLY` refuses a file holding a claim or a `DROP ENV` before it writes
    anything.
  - A launcher name that no readable playbook claims is no longer reported
    as stale when the owner may be the unreadable playbook.

- **`install.sh` names a rate limit as one.** When GitHub refuses the
  latest-release lookup with 403 or 429, the message says it was
  rate-limited and to set `CPB_INSTALL_VERSION`. Before, it blamed the
  internet connection. Any other status is given as its HTTP code. A
  transfer that fails, even after a status, counts as no answer, and nothing
  is installed. A `GITHUB_TOKEN` authenticates the lookup on GitHub's API
  only: it is passed to curl outside its command line, and no redirect is
  followed. `CPB_INSTALL_API_BASE` points the lookup elsewhere, for tests.
- **The npx shim names a rate limit as one too.** It applies to its own
  latest-release lookup, both for an unpinned run and for the stand-in
  release when the package's version is not published yet. A 403 or 429
  asks for a `CPB_NPX_VERSION` pin, or a retry. Any other status is given
  as its code. `CPB_INSTALL_API_BASE` applies to the shim as well.

### Fixed

- **`cpb self-uninstall` with a manifest it could not read** said it would
  remove `0 playbook(s)` and then deleted the whole playbooks root. It now
  refuses and removes nothing. `--keep-data` and `--binary-only` still run.

## [v4.0.0-rc1] -- 2026-10-03

cpb v4 is one CLI with one grammar. It breaks v3 on purpose: names, files,
flags and JSON change, and v4 reads no v3 name. Nothing converts v3 state
inside the product. [SPEC.md](SPEC.md) describes v4 as it is.

### Upgrading from v3

- **Install v4 fresh.** A v3 binary does not update itself into v4: it asks
  for `claude-playbook-<os>-<arch>`, which v4 releases do not ship, and
  `cpb self-update` never crosses a major version on its own (#177).
- **v4 reads no v3 name.** An old key in a manifest or env set file is the
  generic `unknown key "<key>" in <file>:<line>`, and that playbook does not
  launch (#176). An `.env-profiles/` directory, its `.default` and the old
  launcher receipt are not read. The old `CLAUDE_*` environment variables are
  ignored. A sandbox created by v3 has no v4 marker and is refused: remove
  it, or launch with `--sandbox-fresh`.
- **A one-off cutover script converts one machine.** It lives outside the
  product, and the release notes link it. It refuses while a session is live,
  backs up the whole playbooks root first, renames the keys and files listed
  under *Renamed*, proves every file it rewrites by parsing it before and
  after, then repoints the launchers and checks the result with v4. It never
  touches `CLAUDE.md`, `settings.json`, history or credentials.
- **Playbook sources must ship v4 keys** (`launcher`, not `alias`) and declare
  `[update] migrate` if they have a migration (#173, #176).

### Removed

- **The pre-grammar commands** (#172): `create`, `link`, `delete`, `rename`,
  `alias`, `dealias`, `list`, `info`, `env`, `env-profile`. Use `CREATE`,
  `ALTER`, `DROP` and `SHOW` statements. `sessions` is `SHOW SESSIONS`.
- **`install`** (#173): use `CREATE PLAYBOOK <name> FROM <source>`, with
  `BRANCH`, `SUBDIR`, `LAUNCHER`, `NO LAUNCHER`, `SANDBOX` and `ISOLATED
  LOGIN`. The name is required.
- **`update` with no name**, which updated cpb itself: use `cpb self-update`
  (#173). `update --all` and `update --check` are gone; `update <name>
  --dry-run` replaces the check.
- **`RESUME`** (#175): resume with `cpb run <playbook> --resume <id>` or
  `<launcher> --resume <id>`; `SHOW SESSIONS` prints that command for each session, with a `cd` into its folder first. The
  TUI's recent-sessions view and its `R` key are gone with it.
- **The `claude-playbook` executable name** (#178): there is one executable,
  `cpb`, and no `claude-playbook` link or reserved launcher name.
- **The pilot profile coupling** (#170): `NO PILOT PROFILE`, the
  `pilot_profile` field, column and TUI row, and the APPLY warning
  `pilot_profile_third_party_endpoint`. A new playbook's `CLAUDE.md` imports
  nothing.
- **Status line panels and the status line host rule** (#170): `ADD PANEL`,
  `DROP PANEL`, the `PANELS` table, the `panels` field and the warning
  `statusline_held_by_host`. Existing `statusline.d` files stay on disk,
  unmanaged.
- **The OpenShell sandbox backend** (#179). OpenShell needs Landlock, and the
  kernel of the sandboxes the end-to-end suites run in does not provide it, so
  no suite can run it end to end. `sbx` is the only backend.
- **`--sbx`** (#174): use `--sandbox=sbx`.
- **A manifest's top-level `subdir`**, a config directory below the playbook
  root: a playbook's config is its root. A source manifest or an installed
  playbook that names one is refused as an unknown key. `SUBDIR`, the slice
  of a source recorded as `[source] subdir`, stays.

### Renamed

The executable, assets and flake (#178):

| v3 | v4 |
|---|---|
| `claude-playbook` (with `cpb` a link to it) | `cpb` |
| release assets `claude-playbook-<os>-<arch>` | `cpb-<os>-<arch>` |
| flake attribute `#claude-playbook` | `#cpb` |

Environment variables (#178). The old names are not read.

| v3 | v4 |
|---|---|
| `CLAUDE_PLAYBOOKS_DIR` | `CPB_PLAYBOOKS_DIR` |
| `CLAUDE_LAUNCHER_DIR` | `CPB_LAUNCHER_DIR` |
| `CLAUDE_LAUNCHER_RECEIPT` | `CPB_LAUNCHER_RECEIPT` |
| `CLAUDE_PLAYBOOKS_ISOLATE_AUTH` | `CPB_ISOLATED_LOGIN` |
| `CLAUDE_PLAYBOOKS_OAUTH_TOKEN_FILE` | `CPB_OAUTH_TOKEN_FILE` |
| `CLAUDE_CONFIG_DIR_OVERRIDE` | `CPB_CONFIG_DIR` |
| `CLAUDE_PLAYBOOK_UPDATE_REPO`, `CLAUDE_PLAYBOOK_UPDATE_API_BASE`, `CLAUDE_PLAYBOOK_UPDATE_DOWNLOAD_BASE` | `CPB_UPDATE_REPO`, `CPB_UPDATE_API_BASE`, `CPB_UPDATE_DOWNLOAD_BASE` |
| `CLAUDE_PLAYBOOK_TARGET`, `CLAUDE_PLAYBOOK_PATH` (for a migration) | `CPB_PLAYBOOK_NAME`, `CPB_PLAYBOOK_DIR` |
| `install.sh` `VERSION`, `INSTALL_DIR`, `DEFAULT_INSTALL_DIR`, `INSTALL_URL`, `REPO`, `ASSET_PREFIX`, `DOWNLOAD_BASE_URL` | `CPB_INSTALL_VERSION`, `CPB_INSTALL_DIR`, `CPB_INSTALL_DEFAULT_DIR`, `CPB_INSTALL_URL`, `CPB_INSTALL_REPO`, `CPB_INSTALL_ASSET_PREFIX`, `CPB_INSTALL_DOWNLOAD_BASE` |
| npx shim `CPB_VERSION` | `CPB_NPX_VERSION` |

Files and keys (#176, #178):

| v3 | v4 |
|---|---|
| `.playbook` `alias` | `launcher` |
| `.playbook` `isolate_auth` | `isolated_login` |
| `.playbook` `[env] profiles` | `[env] sets` |
| `.playbook` `[env] unset`, an env set's `unset` | `block` |
| `migrations/apply.sh`, run whenever present | `[update] migrate = "<path>"`, run only when declared (#173) |
| `<root>/.env-profiles/`, its `.default` | `<root>/.env-sets/`, its `.defaults` |
| `~/.local/state/claude-playbook/launchers` | `~/.local/state/cpb/launchers` |
| `<cache>/claude-playbook/registry.lock`, `<rc>.claude-playbook.lock` | `<cache>/cpb/registry.lock`, `<rc>.cpb.lock` |
| sandbox `~/.claude-playbook-sandbox`, `~/.claude-playbook-logins/` | `~/.cpb-sandbox`, `~/.cpb-logins/` |

`~/.claude-playbooks` stays the default playbooks root.

Grammar words and flags (#180):

| v3 | v4 |
|---|---|
| `ALIAS <name>`, `NO ALIAS` | `LAUNCHER <name>`, `NO LAUNCHER` |
| `CREATE` / `ALTER ENV … DESCRIBE '<text>'` | `DESCRIPTION '<text>'` (`DESCRIBE` stays the statement that lists a SELECT table's columns) |
| `ADD MCP SERVER … ENV K=V`, `ENV K FROM '<ref>'` | `VAR K=V`, `VAR K FROM '<ref>'` |
| `--env-profile NAME` | `--env-set NAME` |
| `--unset KEY` | `--block KEY` |
| "env profile" | "env set" |
| "alias", "command name" (meaning a launcher) | "launcher" |

JSON (#174, #175, #183):

| v3 | v4 |
|---|---|
| APPLY: one `warning` per statement | `warnings`, every warning with its own code; `summary.warnings` counts warnings |
| `auth status` modes `isolated`, `own-token`, `own-login` | `isolated-login`, `playbook-token`, `shared-login` with `token_blocked: true` |
| `auth status` `isolated` | `isolated_login` |
| `auth status` `expires_at`, `daemon_since` (local offset) | the same keys, in UTC |
| `auth status --claude` `loggedIn`, `subscriptionType`, `authMethod` | `logged_in`, `subscription_type`, `auth_method` |
| EXPLAIN `layer.kind` `DEFAULTS`, `ENV`, `PLAYBOOK` | `defaults`, `env`, `playbook` |
| SHOW and SELECT `sandbox` (a boolean) | an object: `always`, `backend`, `host`, `workdir`, `mounts`, `allow_net`, `secrets`, `claude_version`, `share_skills` |
| SHOW SESSIONS `resume`: `<launcher> --resume <id>` | `cd '<cwd>' && <launcher> --resume <id>` |

The APPLY JSON schema stays 1.

### Changed

- **Strict decoding** (#176): `.playbook`, env set files and cpb's own state
  refuse every key they do not define, naming the key and its line. A launch
  refuses over a manifest it cannot read.
- **`cpb self-update`** (#173, #177) is its own command. It installs the
  newest release of the running major version only; `--major` crosses one.
  Drafts and pre-releases never count, and it installs nothing when the
  release list cannot be read.
- **`cpb update <name>`** (#173) refetches a played recipe or a recorded
  source, and needs exactly one name. A migrate step runs only when declared,
  after a yes (`--yes` or a terminal), outside the registry lock, and only if
  its checksum still matches what `--dry-run` showed.
- **`cpb run` and the launchers guard resuming** (#175): a `--resume <id>`
  whose session is live, or ran in another folder, is refused, and so is a
  `--continue` whose newest session in this folder is live.
- **`SET STATUSLINE` always applies** (#170). `… IF UNSET` applies only where
  no status line is set.
- **`ALTER PLAYBOOK … SET SANDBOX` and `UNSET SANDBOX`** write the
  `[sandbox]` table, bare or by key (#174), and `SHOW CREATE` writes it back
  that way.
- **SHOW PLAYBOOK** has `description`, `homepage`, `author`, `last_used` and
  `migrate` (#172, #173).
- **Every hint and error names a statement**, never a removed command (#172).
- **SELECT prints one shape on both engines**: a table on a terminal, TSV
  with a header row in a pipe, JSON rows with `--json`. A query run through
  `clickhouse local` printed bare TSV in a pipe and ignored `--json`. A
  `FORMAT` in the query still wins, and cannot be combined with `--json`.
  ClickHouse's `JSON` type keeps only an object's non-null keys.
- **DESCRIBE has a `comment` column**: `name`, `type`, `comment`, one line
  saying what each column means, as ClickHouse's `DESC` has; SPEC.md lists
  the same lines. It prints in SELECT's three forms (in a pipe, TSV with a
  header row, where v3 printed the terminal table).
- **SHOW's `launcher` is the command you type**: the playbook's recorded
  `LAUNCHER`, or its name when the default launcher is in place; null under
  `NO LAUNCHER` (and when the default launcher is not in place). v3 reported
  only an alternate name. SHOW SESSIONS' resume line
  uses it. cpb no longer writes `version = "0.1.0"` into a manifest it
  creates.

### Added

- **`cpb play <ref>`** (#162-#165, #167, #169): try someone else's playbook
  from a template name, an https URL, a `github:` ref or a local file. It
  fetches the recipe once, previews what it would do, asks for a typed
  confirmation when it would change an endpoint, a proxy, TLS or a secret,
  runs it in a throwaway playbook (sandboxed where `sbx` is installed), and
  cleans up on every exit. `--keep` keeps it as a playbook that `cpb update`
  refreshes.
- **A sample secret helper**, `examples/secret-helper/cpb-secret-file`
  (#171).
- **Examples** 08 (an agent stacked from playbook files), 17 (`SET STATUSLINE
  … IF UNSET`), 20 (an `sbx` sandbox) and 21 (`cpb play`); 15 is rebuilt as a
  routed playbook with its own login (#169, #170, #171, #179).
- **[SPEC.md](SPEC.md)**, one specification by statement, replacing `SPEC-v4.md` and `docs/reference/cli-grammar.md` (#185).

### Not in this release

- **Pilot integration is deferred.** cpb names no pilot-profile component.
  Wiring a playbook to a pilot profile is the pilot's own step, `pilot wire`,
  a pilot-profile command that cpb never runs.
- **Statement completion** is planned; only playbook names complete.

## Earlier releases

The notes below record the v3 changes that [SPEC.md](SPEC.md) used to carry
in its body. Every v3 release has its own notes on the
[releases page](https://github.com/ramazanpolat/claude-playbooks/releases).

### v3.27.0

- A `github:` marketplace source takes a branch or tag (`#<ref>` or
  `@<ref>`). A `github:` ref that looks like a commit is refused; a git URL's
  commit-like `#<ref>` is still accepted, with the warning
  `marketplace_ref_not_cloneable`.

### v3.26.0

- Built-in `SELECT --json` keys follow the query's column order; before,
  they were sorted.
- A sandboxed launch refuses when a backend key cannot be registered as a
  proxy secret (#148); before, it warned and passed the key in as a plain
  value.

### v3.25.0

- `cpb tui`, read only, and a `cpb tui` hint at the end of bare `cpb` on a
  terminal.
- `SHOW SESSIONS` and the `SESSIONS` table, `tty` included.
- `SET STATUSLINE PREVIOUS` and the status line history.

### v3.23.1

- A shared launch sets another account's own login aside
  (`.credentials.json.cpb-own-<stamp>`); before, it was copied over the
  machine's login
  ([write-up](https://github.com/ramazanpolat/claude-playbooks/blob/v3.27.0/docs/known-issues/shared-launch-copies-own-login-over-machine-login.md)).

### v3.23.0

- `SET ISOLATED LOGIN`, `UNSET ISOLATED LOGIN` and `CREATE PLAYBOOK …
  ISOLATED LOGIN`; before, only `SANDBOX` or a hand edit set
  `isolated_login`.
- `SET STATUSLINE … REFRESH <n>` and `SET` / `UNSET STATUSLINE REFRESH`, with
  `statusline_refresh` in `SHOW PLAYBOOK --json`.

### v3.22.1

- A security fix: `CREATE PLAYBOOK … FROM` leaves a source's
  `.credentials.json` and account state out of the install, and `LINK` sets
  them aside. Before, the first credential sync copied a source's login over
  the machine's, so installing a source could switch the account
  ([write-up](https://github.com/ramazanpolat/claude-playbooks/blob/v3.27.0/docs/known-issues/shared-launch-copies-own-login-over-machine-login.md)).

### v3.22.0

- The model picker: `ADD` / `DROP MODEL`, `SET MODEL PICKER ONLY | APPEND`,
  `UNSET MODEL PICKER`, and `model_picker` in `SHOW PLAYBOOK --json`.
- `APPLY --dry-run --json`, schema 1.
- `DESCRIBE [TABLE]` / `DESC`, and `SELECT`'s own tables and row blocks on a
  terminal; a ClickHouse query's output in a pipe stays TSV.

### v3.21.0

- `ADD` / `DROP MCP SERVER`, `ALLOW` / `DENY` / `UNSET TOOL`, `SET` / `UNSET
  STATUSLINE`, `SET` / `UNSET MODEL` and `ADD` / `DROP SKILL`, with
  `mcp_servers` and `skills` in `SHOW PLAYBOOK --json`.
- Recipes (`ALTER PLAYBOOK` without a name), `USE PLAYBOOK`, and `APPLY … TO
  <playbook|dir>`, a plain config directory included.
- A git URL's `#<ref>` is compared url and ref apart, so applying the same
  `url#ref` again changes nothing.

### v3.20.0

- The statement grammar, a front end over the manifest and env set files,
  with secret references (`SET … FROM '<ref>'`, `[env.refs]`) and `AS
  PLAINTEXT`. A credential-looking literal needs `AS PLAINTEXT`; files that
  already held one keep working.
- `ADD` / `DROP MARKETPLACE`, `ADD` / `DROP PLUGIN`, `SET` / `UNSET AGENT`,
  with `marketplaces`, `plugins` and `agent` in `SHOW PLAYBOOK --json`.
- `INCLUDE`, a reserved word. `SHOW CREATE` quotes a name that is a keyword,
  and a quoted keyword is accepted as a new name.

### v3.15.0

- Credential-looking values are redacted in `SHOW`, `EXPLAIN` and the TUI.
- The missing-`claude` error comes after every input error; before, only a
  flag missing its value was reported ahead of it.

### v3.14.0

- `CPB_CONFIG_DIR` (then `CLAUDE_CONFIG_DIR_OVERRIDE`): a caller-supplied
  config directory for `run` and launcher dispatch.

### v3.13.0

- `--sandbox-host` and `[sandbox] host`: a sandboxed launch on another
  machine, over ssh.

### v3.12.1

- Backend API keys reach a sandbox as proxy-injected secrets; the sandbox
  sees a placeholder.
- Sandboxes are created without the shared skills store unless `share_skills
  = true`, and carry a marker; sandboxes from cpb 3.12.0 and earlier mounted
  the store and have none.

### v3.12.0

- `[sandbox] always`, `--no-sandbox`, `CREATE PLAYBOOK … SANDBOX`, and `start
  --sandbox`.

### v3.11.0

- `run --sandbox`: Docker Sandboxes through `sbx`.

### v3.10.1

- Launcher receipt lines carry two tab-separated fields after the path.

### v3.5.0

- A manifest may hold credential values under `[env.set]`, so a TOML error
  no longer echoes the parser's message.
