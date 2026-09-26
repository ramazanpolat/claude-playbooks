# 13 — SELECT

`SELECT` queries what `SHOW` prints, as tables: `PLAYBOOKS`, `ENVS`, `VARS`
(one row per variable, per layer, per playbook) and `DEFAULTS`.

```
cpb APPLY playbook.cpb
cpb "SELECT name, envs FROM PLAYBOOKS"                # built in
cpb "SELECT playbook, key, effective FROM VARS" --json
cpb "SELECT playbook, key FROM VARS WHERE effective ORDER BY playbook, key"   # clickhouse-local
cpb "SELECT name FROM PLAYBOOKS WHERE version_tuple >= [3, 12] ORDER BY version_tuple"
cpb EXPLAIN SELECT count() FROM VARS                  # which engine, and the exact command
```

Quote the statement: the shell would expand `*` and split `(`.

- **Built in:** exactly `SELECT <col>[, <col> …] FROM <table>`, columns spelled
  as the table has them. It needs nothing installed and prints a table, or the
  selected fields with `--json`.
- **Anything else** (`WHERE`, `ORDER BY`, functions, `count()`, `FORMAT`)
  goes to ClickHouse's `clickhouse local`, when `clickhouse` or `ch` is on
  `PATH` (or `CPB_CLICKHOUSE` names it). cpb pipes it exactly the objects
  `SHOW … --json` prints, one per line: a credential redacted, a reference
  shown as the reference. Nested fields read as JSON: `source.url`,
  `vars[1].key`. `version_tuple` compares versions as numbers.
- One table per query. To join, pipe `SHOW … --json` into ClickHouse
  yourself: [Query with SQL](../../docs/guides/query-with-sql.md).
