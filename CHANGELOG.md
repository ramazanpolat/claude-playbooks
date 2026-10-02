# Changelog

## [v4.0.0-rc1] -- unreleased

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
