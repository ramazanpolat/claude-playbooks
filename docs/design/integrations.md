# cpb and integrations

cpb has no dependency on pilot-profile, oi, or any other integration. It
installs, runs and passes its tests with none of them present, and its code,
tests and docs name none of them.

## The extension points are generic

- **`SET VAR` and `USE ENV`.** An integration's settings are variables the
  pilot sets, in a playbook or in an env set. cpb hands them to the launch.
- **`ADD PLUGIN`**, with `ADD MARKETPLACE`. An integration's behaviour is a
  Claude Code plugin: its hooks, skills, agents, commands and MCP servers.
  cpb installs it through `claude plugin`.
- **`INCLUDE`.** Several playbooks share a base recipe: each child recipe
  starts with `INCLUDE '<base>.cpb';`. There is no `SET BASE`.

## What cpb does not do

- **It never interprets a variable's meaning.** A value is passed through as
  it was set. Redaction looks at the shape of a key and of its value (a
  credential-looking key, a URL that carries a password), never at what the
  value is for.
- **It never reads a plugin's contents.** It asks Claude Code to install the
  plugin, and reports what Claude Code records.
- **`INCLUDE` copies a base's statements blindly.** They run as if written in
  place, and cpb knows nothing about what the base is for.

So an integration is a plugin, plus the variables the pilot sets, plus a base
recipe that applies them. cpb gains no code for it.

## Enforced

`.github/scripts/no-integration-names.sh` runs in CI and fails when a tracked
file names an integration; the list of names is in the script. It skips
`CHANGELOG.md`, which records history, and this page. The word "pilot" is not
on the list: it is cpb's word for the human who drives it (see the
[reference](../../SPEC.md)).
