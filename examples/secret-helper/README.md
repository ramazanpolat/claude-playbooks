# A sample secret helper

cpb stores secret **references**, never values. At launch it asks a secret
helper you choose to resolve them, and only that launch sees the values. Any
program that follows cpb's helper interface works: a wrapper around your
keychain or password manager, or this sample.

[`cpb-secret-file`](cpb-secret-file) resolves `file:<name>` references from
files under `~/.config/cpb-secrets` (or `$CPB_SECRET_FILES`), one value per
file. It is a short POSIX shell script, meant to be read and adapted.

```
cp cpb-secret-file ~/.local/bin/
mkdir -p ~/.config/cpb-secrets && chmod 700 ~/.config/cpb-secrets
(umask 077 && read -r v && printf '%s' "$v" > ~/.config/cpb-secrets/router-token)

cpb ALTER DEFAULTS SET SECRET HELPER cpb-secret-file
cpb ALTER ENV router SET ANTHROPIC_AUTH_TOKEN FROM 'file:router-token'
```

The interface, which any helper implements
([reference, Secrets](../../SPEC.md#secrets-optional)):

- `<helper> --check KEY=REF …`: cpb runs it when a statement writes a
  reference. Exit 0 means every reference resolves; anything else fails the
  statement with the helper's message, which never contains a value.
- `<helper> KEY=REF … -- claude <args>`: cpb runs it at launch. The helper
  sets each `KEY` in the environment of that one command and runs it.

Examples [04](../04-secret-references/) and [09](../09-mcp-servers/) use
this helper. Their `.setup` writes a placeholder file, as you would write the
real value.
