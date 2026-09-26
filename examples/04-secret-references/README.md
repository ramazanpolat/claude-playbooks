# 04 — secrets by reference

```
cpb APPLY playbook.cpb        # checks the reference with `with-secret --check` before writing
cpb SHOW ENV router           # shows the reference, never a value
cpb run routed                # launches through the helper
```

`SET K FROM '<ref>'` stores a reference; a configured secret helper resolves
it at launch (`<helper> K=<ref> -- claude …`) and the value reaches claude
only. Any helper with that interface works; `with-secret` is one. A
credential-looking literal (`SET TOKEN=…`) is refused unless you write
`AS PLAINTEXT`, and `SHOW CREATE` never prints one. Store the secret first,
yourself: `with-secret --store router-token`.
