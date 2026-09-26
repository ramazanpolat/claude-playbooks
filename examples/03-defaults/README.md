# 03 — DEFAULTS for every playbook

```
cpb APPLY playbook.cpb
cpb SHOW DEFAULTS
cpb EXPLAIN PLAYBOOK <any playbook>   # the DEFAULTS layer appears as "DEFAULTS (ENV …)"
```

`DEFAULTS` is an ordered list of env sets under every playbook. A playbook's
own sets and variables still win over it. `ALTER DEFAULTS DROP ENV thinking`
removes one; `DROP ENV thinking` is refused while `DEFAULTS` (or any playbook)
uses it.
