# 04 — secrets by reference

```
cpb APPLY playbook.cpb        # checks the reference with the helper before writing
cpb SHOW ENV router           # shows the reference, never a value
cpb run routed                # launches through the helper
```

`SET K FROM '<ref>'` stores a reference, and a configured secret helper
resolves it at launch (`<helper> K=<ref> -- claude …`), so the value reaches
claude only. Any helper with cpb's interface works: this example uses the
sample [`cpb-secret-file`](../secret-helper/), which reads `file:<name>`
references from `~/.config/cpb-secrets`. Store the secret there first,
yourself, as `.setup` does with a placeholder.

- The helper checks a reference when a statement writes it: one it cannot
  resolve is refused, and nothing is stored.
- A credential-looking literal (`SET TOKEN=…`) is refused unless you write
  `AS PLAINTEXT`, and `SHOW CREATE` never prints one.
- `ALTER DEFAULTS DELETE secret_helper` removes the helper. Launching a
  playbook whose layers still hold a reference is then refused in one line,
  "no secret helper configured".
