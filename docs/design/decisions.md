# Design decisions

The decisions behind cpb's grammar, with when they were made. The
[reference](../reference/cli-grammar.md) states the rules; this page keeps the
record of why they are the rules. Newest last.

| Date | Decision | Where it lives |
|---|---|---|
| 2026-09-25 | Secrets are references, never values: `SET … FROM '<ref>'`, resolved only at launch; the reference's scheme disambiguates it from a value | Secrets |
| 2026-09-25/27 | One regular grammar, read and written like DDL, replaces the `env` / `env-profile` / `create` … commands | Shape, Grammar |
| 2026-09-26 | A statement is whole or nothing: lifecycle clauses (`RENAME TO`, `ALIAS`) are not combined with environment clauses | Grammar |
| 2026-09-26 | cpb defines a secret helper interface (`--check`, then exec) and never names or discovers a helper | Secrets |
| 2026-09-26 | Keys cpb reads itself (`CLAUDE_CODE_OAUTH_TOKEN`) never take a reference | Secrets |
| 2026-09-26 | A credential-looking literal is refused; `AS PLAINTEXT` stores one knowingly | Secrets |
| 2026-09-26 | Two senses of "playbook" are kept apart: the installed `PLAYBOOK`, and the playbook *file* | playbook.cpb |
| 2026-09-26 | `APPLY` takes several files, validates every statement first, and writes nothing on any refusal | playbook.cpb |
| 2026-09-26 | Source drift is a warning, never an error | playbook.cpb |
| 2026-09-26 | A file never consents to `DROP PLAYBOOK`: `APPLY` needs `--yes` for it | playbook.cpb |
| 2026-09-26 | The grammar ships in v3.20.0 beside the pre-grammar commands, which v4.0.0 removes | — |
| 2026-09-26 | `SELECT` queries SHOW's state as tables, built in for simple queries and through `clickhouse local` for full SQL (a first design was withdrawn the same day) | SELECT |
| 2026-09-26 | `INCLUDE` stacks playbook files; a relative path resolves against the including file, and only on this machine | INCLUDE |
| 2026-09-26 | Plugin clauses act through Claude Code's own CLI (`claude plugin …`), not by writing `settings.json` (an earlier cut did) | Plugins and the agent |
| 2026-09-26 | A marketplace-declared command is never accepted on your behalf: the statement fails and shows the command to review | Plugins and the agent |
| 2026-09-26 | `SET AGENT` pins the main-thread agent in the playbook's user-scope settings (verified with nine `claude -p` runs) | Plugins and the agent |
| 2026-09-26 | A playbook file can be a recipe: a name-less `ALTER PLAYBOOK`, whose target is chosen by `USE PLAYBOOK` or `APPLY … TO` | Targets |
| 2026-09-26 | A playbook file describes an agent completely: MCP servers, tools, status line and model, skills | An agent's configuration |
| 2026-09-27 | `APPLY --dry-run --json` prints the plan as one JSON object on stdout, refusals included, and only adds fields within a major version | APPLY --dry-run --json |
| 2026-09-27 | The `/model` picker is a playbook's `modelPicker` setting (`ADD MODEL`, `SET MODEL PICKER`) | Model picker |
| 2026-10-01 | A recipe may open with `-- key: value` header lines (`title`, `description`, `needs`, `create-with`), the template convention the site's templates use | cpb play |
| 2026-10-02 | cpb names no other tool: the default `CLAUDE.md` imports nothing, and the clause, field and warning about imports, the status line host rule and panels leave cpb; `SET STATUSLINE … IF UNSET` offers a status line without imposing one | Objects, Status line and model |
| 2026-10-02 | No release is marked stable or not stable | — |
