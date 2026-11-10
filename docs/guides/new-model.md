# What happens when a new model ships

Claude Code names models two ways:

- **An alias,** such as `opus`, `sonnet`, `haiku`, `fable`, or `fable[1m]` for the 1M context. It always means the newest model of that family the installed Claude Code knows, and Claude Code updates itself.
- **A release id,** such as `claude-opus-5-5` or `claude-fable-5-1[1m]`. It names one release and stays on it.

So a playbook that uses aliases gets a new model as soon as its Claude Code knows it, with nothing to change in cpb. `--model fable` and `/model fable` typed by name reach it at once.

## The `/model` menu can lag a release

The menu shows Claude Code's own rows, any rows of the playbook's `modelPicker`, and rows the Anthropic server sends when a session starts.

A server row can name the previous release for a while. With Claude Code 2.1.322, a server-sent Fable row named the previous Fable, and while such a row was present Claude Code added no Fable row of its own. The menu then offered the old model only, even though `/model fable` reached the new one.

## One statement, once per machine

Give every playbook a row per family, written as the alias:

```
cpb "ALTER DEFAULTS
  ADD MODEL 'opus' LABEL 'Opus (newest)'
  ADD MODEL 'sonnet' LABEL 'Sonnet (newest)'
  ADD MODEL 'haiku' LABEL 'Haiku (newest)'
  ADD MODEL 'fable' LABEL 'Fable (newest)'"
```

That is all. When the next model ships, there is nothing to run.

**Which playbooks it reaches.** Every playbook, the ones you have and the ones you create later. cpb writes nothing into them: each launch through cpb passes `claude` the playbook's own picker with these rows after it. That covers a launcher, `cpb run`, `cpb start`, a sandboxed launch and a resumed session (`--resume`, `--continue`).

**No duplicate rows.**
- When the menu already offers the newest model of a family, the alias row is the same model, and Claude Code shows it once.
- When a server row lags, the alias row is the one that offers the newest model beside it.
- A row the playbook already has with the same model id is not repeated.

**Checking it.** `cpb SHOW DEFAULTS` lists the rows. `cpb EXPLAIN PLAYBOOK <name>` says what a launch adds:

```
Model picker at launch: adds opus, sonnet, haiku, fable from DEFAULTS
```

or why it adds nothing.

## What is left alone

A launch gets no rows, and runs exactly as it would without them, when:

- **It does not go to Anthropic's own API.** That is a gateway, a router or a cloud provider. There an alias means whatever the route maps it to.
- **The playbook's picker is mode `only`.** It shows exactly its own rows.
- **The playbook opts out:** `cpb ALTER PLAYBOOK <name> SET model_picker_defaults = 'excluded'`. `RESET model_picker_defaults` opts back in.
- **The launch passes its own `--settings` to `claude`.** Claude Code takes one.

## What is not covered

The rows reach sessions that cpb starts. These get none:

- **bare `claude`,** run with no playbook;
- **`claude` run with `CLAUDE_CONFIG_DIR` set by hand** instead of through cpb.

For the machine's own `~/.claude`, write the rows into its `settings.json` once:

```
cat > newest.cpb <<'EOF'
ALTER PLAYBOOK
  ADD MODEL 'opus' LABEL 'Opus (newest)'
  ADD MODEL 'sonnet' LABEL 'Sonnet (newest)'
  ADD MODEL 'haiku' LABEL 'Haiku (newest)'
  ADD MODEL 'fable' LABEL 'Fable (newest)';
EOF
cpb APPLY newest.cpb TO '~/.claude' --dry-run
cpb APPLY newest.cpb TO '~/.claude' --yes
```

Apply such a recipe only to a plain directory. `APPLY … TO <playbook>` records the recipe, and `cpb update <playbook>` applies it again.

## Pinned ids

A release id in a playbook's `model`, or in a picker row, stays on its release. Pinning can be the point: a fixed model for a review or a comparison. cpb never refuses one, but it says so:

```
$ cpb ALTER PLAYBOOK work SET model = 'claude-fable-5-1[1m]'
…
Note: 'claude-fable-5-1[1m]' names one release; 'fable[1m]' follows the newest the installed Claude Code knows.
```

`SHOW PLAYBOOK` marks such an id, `claude-fable-5-1[1m] (pinned; fable[1m] follows releases)`, and `EXPLAIN PLAYBOOK` says it at the end of its `Model:` line. To follow releases instead, use the alias: `cpb ALTER PLAYBOOK work SET model = 'fable[1m]'`.

See *Model picker* in [SPEC.md](../../SPEC.md) for every rule.
