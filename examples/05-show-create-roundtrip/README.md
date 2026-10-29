# 05 — a machine's setup as one file

```
cpb SHOW CREATE ALL > playbook.cpb     # this machine, as statements
cpb APPLY playbook.cpb --dry-run       # on the next machine: what would change
cpb APPLY playbook.cpb                 # and do it
cpb APPLY playbook.cpb                 # again: 0 created, 0 changed
```

`SHOW CREATE` writes only forms that are safe to repeat (`CREATE OR REPLACE
ENV`, `CREATE OR REPLACE ACCOUNT`, `CREATE OR ALTER PLAYBOOK`, `USE ENV` and
`USE ACCOUNT` with the full list), so the file converges instead of piling
up. `SHOW CREATE ALL` writes the secret helper, env sets, accounts,
`DEFAULTS`, then playbooks, so each statement finds what it names. `CREATE OR ALTER PLAYBOOK … SET …`
creates the playbook with those properties, or, when it exists, applies the
`SET` list as `ALTER PLAYBOOK … SET` would; what is fixed at creation (`FROM`,
`BRANCH`, `SUBDIR`, `LINK`) is only compared, and a different source is a
warning. `CREATE PLAYBOOK IF NOT EXISTS` leaves a playbook that exists as it
is. `SHOW CREATE` writes the launcher, login, global instructions and sandbox on the
`CREATE`, so one statement brings a playbook back to them. Credential-looking literals are written
as comments and make it exit non-zero until you store them by reference
(`--skip-secrets` accepts the output without them; such an env set is written
as `CREATE ENV IF NOT EXISTS` plus `ALTER ENV`, so applying the file keeps the
stored value). `APPLY` validates every
statement before writing anything, runs them in order, and stops at the first
failure; running the fixed file again finishes the job.
