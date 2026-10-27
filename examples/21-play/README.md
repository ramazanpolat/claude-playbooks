# 21: try someone else's playbook

```
cpb play --check ./reviewer.cpb                  # what it holds, and what to look at
cpb play --dry-run --json ./reviewer.cpb         # the plan, for a program
cpb play ./reviewer.cpb                          # preview, yes, run, removed afterwards
cpb play --account router ./router.cpb           # on your router account
cpb play ./reviewer.cpb --keep                   # keep it as a playbook of your own
cpb update reviewer                              # fetch it again; the diff before anything changes
```

`cpb play` runs a recipe someone else wrote in a throwaway playbook, after it
has shown you what the recipe would do and you said yes. Here the recipes are
local files; the same works for a curated template name (`cpb play
code-reviewer`), an https URL, or `github:<owner>/<repo>/<path>.cpb@<tag>`.

- `reviewer.cpb` is a recipe a team might share: a model and tool rules, with
  a header (`title`, `description`, `min-cpb`) the preview shows.
- `refused.cpb` holds what a played recipe may not: `USE ENV` would attach your
  env sets, a plaintext token is never shared, and `ANTHROPIC_BASE_URL` is an
  account's. `--check` names each line
  and exits 1.
- `router.cpb` is a GLM recipe. Where its requests go is not the recipe's
  to say: a recipe that sets `ANTHROPIC_BASE_URL` or any other account
  variable is refused. `--account router` plays it on your own router
  account (`.setup` makes one); on an account that is not Anthropic's, the
  played playbook gets its own login and your shell's credentials are
  blocked in it.
- `--keep` builds the recipe as a playbook in your store (here `reviewer`),
  records where it came from (`SHOW PLAYBOOK reviewer`: `Played from:`), and
  runs nothing. `cpb update` fetches it again and shows the diff first.

`playbook.cpb` is the other path: once you trust a recipe, include it in a
playbook of your own and `APPLY` it, like any other file. That is what CI
applies here; `.check` runs the `cpb play` lines above.

Guide: [Try someone else's playbook](../../docs/guides/play.md).
