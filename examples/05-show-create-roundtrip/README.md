# 05 — a machine's setup as one file

```
cpb SHOW CREATE ALL > playbook.cpb     # this machine, as statements
cpb APPLY playbook.cpb --dry-run       # on the next machine: what would change
cpb APPLY playbook.cpb                 # and do it
cpb APPLY playbook.cpb                 # again: 0 created, 0 changed
```

`SHOW CREATE` writes only forms that are safe to repeat (`CREATE OR REPLACE
ENV`, `CREATE PLAYBOOK IF NOT EXISTS`, `USE ENV` with the full list), so the
file converges instead of piling up. Credential-looking literals are written
as comments and make it exit non-zero until you store them by reference
(`--skip-secrets` accepts the output without them). `APPLY` validates every
statement before writing anything, runs them in order, and stops at the first
failure; running the fixed file again finishes the job.
