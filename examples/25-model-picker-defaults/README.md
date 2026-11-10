# 25 — the newest model of every family, in every playbook

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb SHOW DEFAULTS                  # Model picker: opus (Opus (newest)), sonnet (Sonnet (newest)), …
cpb EXPLAIN PLAYBOOK daily         # Model picker at launch: adds opus, sonnet, haiku, fable from DEFAULTS
cpb EXPLAIN PLAYBOOK fixed         # Model picker at launch: adds nothing (the playbook has model_picker_defaults = 'excluded')
```

`ALTER DEFAULTS ADD MODEL` keeps `/model` rows for every playbook, existing
and future, in the playbooks root. cpb writes nothing into the playbooks.
Every launch through cpb (a launcher, `cpb run`, `cpb start`, sandboxed or
resumed) passes `claude` one `--settings` holding the playbook's own picker,
unchanged, followed by these rows.

The rows are Claude Code's aliases, so they follow its releases. When the
menu already offers the newest model of a family, the alias row is the same
model and shows once. When the `/model` menu carries an older row for a
family, sent by the server at start-up, the alias row is the one that offers
the newest model.

What gets no rows:

- a launch that does not go to Anthropic's own API;
- a picker in mode `only` ([example 14](../14-model-picker/));
- a launch that passes its own `--settings`;
- a playbook with `model_picker_defaults = 'excluded'`, like `fixed` here.

[When a new model ships](../../docs/guides/new-model.md) has the whole story.
