# 02 — env sets, in order

```
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK work     # every variable a launch sets, and the layer that decided it
cpb run work
```

An env set is a named group of variables that several playbooks can share.
`USE ENV tools thinking` attaches both, in order: a later set overrides an
earlier one. The playbook's own `SET VAR` overrides every set, so `work` runs
with `MAX_THINKING_TOKENS=16000` while other users of `thinking` get `8000`.
`BLOCK VAR` removes a variable at launch even when your shell exports it.
`ADD ENV x FIRST` or `ADD ENV x AFTER tools` places one set without restating
the list. `REPLACE ENV tools WITH fast` swaps one set for another at the same
place; run again, it changes nothing. A recipe states `USE ENV`; `REPLACE` is
for the prompt, and `SHOW CREATE` never writes it.

Where requests go and how they authenticate (`ANTHROPIC_BASE_URL`, the keys,
the Claude token, the model variables) is not an env set's: it is an
**account's** ([example 23](../23-accounts/)). An env set that sets one of
those variables refuses the launch, naming the set; the `.check` shows it.
