# Query your setup with SQL

Since v3.21.0, `cpb SELECT` does this for one table at a time (see
[example 13](../../examples/13-select/) and the reference's
[SELECT](../reference/cli-grammar.md#select-v3210)):

```bash
cpb "SELECT name, envs FROM PLAYBOOKS"                                  # built in
cpb "SELECT playbook, key FROM VARS WHERE effective ORDER BY playbook"  # through clickhouse-local
```

`cpb DESCRIBE playbooks` lists a table's columns and types. On a terminal
`SELECT` prints a table, or one block per row for more than 6 columns,
with objects as JSON. In a pipe it prints TSV, and a `FORMAT` in the query
always wins.

This guide is the manual form underneath it, for what `SELECT` does not do:
joining two reads, or feeding ClickHouse settings of your own. Every read has
a stable `--json` form, and ClickHouse's `clickhouse local` (`ch local`) runs
full SQL over it without a server: pipe one into the other.

```bash
cpb SHOW PLAYBOOKS --json | ch local --input-format JSONEachRow -q "SELECT … FROM table"
```

The plural reads (`SHOW PLAYBOOKS`, `SHOW ENVS`) print an array of objects and
the singular ones (`SHOW PLAYBOOK <name>`, `SHOW ENV <name>`, `SHOW DEFAULTS`,
`EXPLAIN PLAYBOOK <name>`) print one object; `JSONEachRow` reads either, as
the rows of a table named `table` (one row for a single object). Nested fields (`source`, `vars`, `layer`, `plugins`) arrive
as nested values, so `arrayJoin`, `ARRAY JOIN` and dotted names work.
Credential-looking values are masked in `--json`, so no secret reaches the
query.

## Recipes

Playbooks that use an env set, with how many variables of their own they set:

```bash
cpb SHOW PLAYBOOKS --json | ch local --input-format JSONEachRow -q "
  SELECT name, version, envs, length(vars) AS own_vars
  FROM table WHERE length(envs) > 0 ORDER BY name FORMAT PrettyCompact"
```

How many playbooks attach each env set:

```bash
cpb SHOW PLAYBOOKS --json | ch local --input-format JSONEachRow -q "
  SELECT arrayJoin(envs) AS env, count() AS playbooks
  FROM table GROUP BY env ORDER BY playbooks DESC FORMAT PrettyCompact"
```

Env sets, their users, and which are in `DEFAULTS`:

```bash
cpb SHOW ENVS --json | ch local --input-format JSONEachRow -q "
  SELECT name, length(used_by) AS users, default
  FROM table ORDER BY users DESC, name FORMAT PrettyCompact"
```

Every variable a launch of one playbook sets, and the layer that decided it:

```bash
cpb EXPLAIN PLAYBOOK work --json | ch local --input-format JSONEachRow -q "
  SELECT v.key AS key, v.layer.kind AS layer, v.layer.name AS from
  FROM table ARRAY JOIN vars AS v ORDER BY key FORMAT PrettyCompact"
```

Load the state into a ClickHouse table to keep a history (with a server):

```bash
cpb SHOW PLAYBOOKS --json | ch local --input-format JSONEachRow -q "SELECT * FROM table FORMAT JSONEachRow" \
  | ch client -q "INSERT INTO playbooks FORMAT JSONEachRow"
```

## What you can rely on

The `--json` objects are the contract
([reference, Output](../reference/cli-grammar.md#output)): fields may be
added, and a field never changes meaning within a major version. Parse
them, never the human form.

`cpb SELECT …` does this for you ([reference](../reference/cli-grammar.md#select-v3210)):
columns alone are answered by cpb itself, and any other query is piped to
`clickhouse local` the same way, over the same `--json` rows. This page is
the manual form, for a pipeline of your own.
