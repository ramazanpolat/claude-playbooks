# 26: a secret cpb keeps itself

```
cpb play --check ./recipe.cpb            # names the secret, where it would be kept, whether it is stored
cpb play --dry-run --json ./recipe.cpb   # the same for a program: "secrets": [{ref, stored, store, path}]
cpb play ./recipe.cpb                    # after you type the reference, asks for the value twice and stores it
cpb APPLY uses-secret.cpb                # once stored, a playbook of your own uses it by reference
```

A `cpb:<name>` reference needs no secret helper: cpb keeps the value in your
operating system's store and hands it to `claude` only, at launch.

- **Where it goes:** your macOS Keychain; on Linux, your Secret Service
  keyring (GNOME Keyring, KWallet); on a Linux machine with no keyring (a
  server, a container), a file, `~/.local/share/cpb/secrets/<name>`, mode
  `0600`. That file is plaintext at rest, readable by you and root, and
  `--check` says so before anything is stored.
- **How it gets there:** `cpb play` shows the recipe's secret as not stored
  yet, and after you type the reference to confirm, asks for the value
  twice with echo off, from your terminal only: never with `--yes`, never
  without a terminal, and not when it finds an agent's session in the
  environment (a courtesy check, not a lock). It never replaces a stored
  value; store a new one under a new name.
- **Using it:** any statement can name it, `SET VAR SERVICE_TOKEN FROM
  'cpb:service-token'`. A reference that is not stored is refused when it is
  written, and `SHOW CREATE` prints the reference, never the value.

CI applies `playbook.cpb` and runs `.check`: the report and the refusals
everywhere, and on its Linux runner (no keyring) the file store, with a
placeholder put there as `cpb play` would.

Guides: [Try someone else's playbook](../../docs/guides/play.md) (the store
step), [Secrets in SPEC.md](../../SPEC.md#secrets-optional).
