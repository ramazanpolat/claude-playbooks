# Examples

One idea per directory, each a `playbook.cpb` you can apply as it is. CI
applies every one of them (`examples/check.sh`): dry run, apply, apply again
with no change, then what the README shows beyond `APPLY` (`.check`).

`examples/upgrade.sh` checks the upgrade from the previous release of the
same major, and CI runs it on every change. The previous release's binary
applies that release's own examples. This build then takes over the same state and must
read it identically:
- `SHOW CREATE ALL`, `EXPLAIN --json` and `auth status --json` are the same;
- a re-apply changes nothing;
- every playbook launches and drops;
- a made-up machine login is never touched.

[`secret-helper/`](secret-helper/) is not an example: it is a sample secret
helper, `cpb-secret-file`, which examples 04 and 09 use.

| | |
|---|---|
| [01-first-playbook](01-first-playbook/) | a playbook and its command |
| [02-env-sets-and-order](02-env-sets-and-order/) | env sets, their order, a playbook's own variables |
| [03-defaults](03-defaults/) | env sets under every playbook |
| [04-secret-references](04-secret-references/) | a token by reference, through a secret helper |
| [05-show-create-roundtrip](05-show-create-roundtrip/) | a machine as one file, applied elsewhere |
| [06-install-from-git](06-install-from-git/) | one playbook out of a Git repository, pinned |
| [07-plugins-local](07-plugins-local/) | a plugin and an agent from a local marketplace |
| [08-stacked-agent](08-stacked-agent/) | an agent from three stacked recipes, with its tools and status line |
| [09-mcp-servers](09-mcp-servers/) | MCP servers, a credential by reference |
| [10-tools-statusline-model](10-tools-statusline-model/) | tool permissions, the status line, the default model |
| [11-skills](11-skills/) | a skill, linked from a directory |
| [12-recipes-and-targets](12-recipes-and-targets/) | one recipe for two playbooks, `TO <playbook>`, `TO '<dir>'` |
| [13-select](13-select/) | `SELECT` over playbooks, env sets and variables |
| [14-model-picker](14-model-picker/) | the `/model` picker: its rows, ONLY or APPEND |
| [15-third-party-route](15-third-party-route/) | a playbook routed away from Anthropic: its own login, credentials blocked |
| [16-isolated-login](16-isolated-login/) | a playbook that shares no login with `~/.claude` |
| [17-statusline-if-unset](17-statusline-if-unset/) | a status line offered with IF UNSET, never imposed |
| [18-sessions](18-sessions/) | live sessions, `--resume` through the playbook, and what cpb refuses to resume |
| [19-tui](19-tui/) | what `cpb tui` shows, and the statements behind each screen |
| [20-play](20-play/) | `cpb play`: check, plan, run, keep and update someone else's recipe |
