# site-tools/

Everything that keeps the cpb website honest. The site itself, plain HTML,
CSS and a little JS with no build step, is in [`site/`](../site/); these
files are not served.

## The site

- `site/index.html` is the home page: a shelf of five playbook "notebooks",
  one per `CLAUDE_CONFIG_DIR`, with the four isolation layers and the recipe
  demo. Its cards are drawn from a JSON block in the page (`showcase-data`).
- `site/tour.html` is the tour: every command with the output it really
  printed.

## Hosting

The site is served by Cloudflare Pages, straight from `site/` on `main`
(Git integration: no build command, output directory `site`, watch path
`site/*`; `site-tools/` is not part of what is served).

- A merge to `main` that touches `site/` deploys on its own. No deploy
  workflow lives in this repo.
- A pull request that touches `site/` gets a Cloudflare preview link.

## Keeping it honest

`verify-snippets.sh <path to cpb>` runs everything below against a build, in
throwaway homes, and CI runs it (`.github/workflows/site-verify.yml`) on
changes to `site/`, `site-tools/`, `README.md`, `docs/`, `examples/` and the
TUI goldens.

- **The tour.** It re-runs the commands `tour.html` shows and checks the
  lines a reader relies on. `check-tui-goldens.py` diffs its `cpb tui` blocks
  against `internal/tui/testdata/*.golden`, the real screens. Those blocks
  show the fixtures' own names (`kommander-dev`, `k9`, `9router`), so change
  them in the goldens, never on the page alone.
- **The home page.** `showcase.cpb` is the recipe behind the five cards.
  `showcase-data.py` applies it in a throwaway home (the `claude` and secret
  helper stand-ins in `examples/.ci` keep it offline), reads what cpb reports
  (`SHOW PLAYBOOK --json`, `EXPLAIN PLAYBOOK --json`, the `APPLY --dry-run
  --json` plan, the files cpb wrote), and either checks the page's JSON block
  against it (`--check`, what CI does) or rewrites the block (`--write`).
  So a card can only say what cpb said.

To change the cards, edit `showcase.cpb` and regenerate:

```
site-tools/showcase-data.py --cpb "$(command -v cpb)" --write
```

A section of the recipe starts at its `-- <name>: <tagline>` comment line; the
tagline on the card is that comment.
