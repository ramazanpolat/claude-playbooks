# site-tools/

Everything that keeps the cpb website honest. The site itself, plain HTML,
CSS and a little JS with no build step, is in [`site/`](../site/); these
files are not served.

## The site

- `site/index.html` is the home page: a slot-machine headline (`slot.js`), a
  shelf of playbook "notebooks" and one ephemeral "ghost" one, each with its
  own `CLAUDE_CONFIG_DIR` (`home.js`), the four isolation layers, a map of the
  runtimes the agents run in and what they reach (`runtimes.js`), and the
  recipe demo. Its cards are drawn from a JSON block in the page
  (`showcase-data`).
- `site/tour.html` is the tour: every command with the output it really
  printed.
- `site/templates.html` is the template gallery and customizer
  (`templates.js`). The templates are plain recipes, `site/p/<id>.cpb`, served
  at `/p/<id>.cpb`, listed in `site/p/index.txt`. The customizer writes a `.cpb`
  file live from a template and a few switches.

## Hosting

The site is served by Cloudflare Pages, straight from `site/` on `main`
(Git integration: no build command, output directory `site`, watch path
`site/*`; `site-tools/` is not part of what is served).

- A merge to `main` that touches `site/` deploys on its own. No deploy
  workflow lives in this repo.
- A pull request that touches `site/` gets a Cloudflare preview link.
- `site/_headers` serves `/p/*` (the template files) as `text/plain`, so a
  browser shows one instead of downloading it, with `nosniff` and a short cache.

## Keeping it honest

`verify-snippets.sh <path to cpb>` runs everything below against a build, in
throwaway homes, and CI runs it (`.github/workflows/site-verify.yml`) on
changes to `site/`, `site-tools/`, `README.md`, `docs/`, `examples/` and the
TUI goldens.

- **The tour.** It re-runs the commands `tour.html` shows and checks the
  lines a reader relies on. `check-tui-goldens.py` diffs its `cpb tui` blocks
  against `internal/tui/testdata/*.golden`, the real screens. Those blocks
  show the fixtures' own names (`alpha`, `rt`, `proxy`), so change
  them in the goldens, never on the page alone.
- **The home page.** `showcase.cpb` is the recipe behind the five cards.
  `showcase-data.py` applies it in a throwaway home (the `claude` stand-in in
  `examples/.ci` and `stand-in-secret-helper` here keep it offline), reads what cpb reports
  (`SHOW PLAYBOOK --json`, `EXPLAIN PLAYBOOK --json`, the `APPLY --dry-run
  --json` plan, the files cpb wrote), and either checks the page's JSON block
  against it (`--check`, what CI does) or rewrites the block (`--write`).
  So a card can only say what cpb said. It also runs `cpb start <dir>
  --delete` for real (with a `claude` that records its session) to check the
  ephemeral notebook, and checks every launch command in `runtimes.json`
  against this cpb: its flags in `run --help` / `start --help`, and its
  sandbox backend (`sbx`) from the error for an unknown one.
- **The logos.** `brand-icons.py` writes the tools' marks (Simple Icons, CC0,
  pinned to one release) into the page's inline sprite; the marks for the
  playbooks and runtimes are hand-drawn in the same sprite. `check-sprites.py`
  fails if the page, its scripts or the data ask for a mark the sprite lacks,
  including a mark for each playbook in the showcase.

- **The sprite.** One inline sprite (`sprite.html`) is copied into every page
  between `<!-- sprite:start -->` and `<!-- sprite:end -->`;
  `sync-sprite.py --write` copies it, `--check` (CI) fails if a page differs.
  Edit the sprite, never one page's copy. `brand-icons.py` writes the tools'
  marks into it.
- **The templates.** `site/customizer-core.js` is the one implementation of
  the catalog (MCP servers, skills, plugins, models), of the templates and of
  the `.cpb` text they render. The page loads it, and so does CI under Node, so
  what a visitor copies is what was tested. `templates-cases.js --write`
  regenerates `site/p/*.cpb` from it. `test-customizer.js` unit-tests the logic.
  `check-templates.py` checks that `site/p/` is what the code renders, and plans
  (`APPLY --dry-run --json`) every template's defaults, an "everything on"
  selection and a reproducible set of random ones, each as a playbook file and
  as a recipe, requiring `ok` from cpb for all of them; the defaults and the
  "everything on" selections are also applied for real, twice, in a throwaway
  home (stand-ins for `claude` and the secret helper, a local mirror for the
  `github:` skill sources). The template files themselves are checked against
  cpb by `check-template-files.py` (`cpb play --check site/p`, the header
  convention, no secret, apply to a new playbook, apply again as a no-op), which
  has its own workflow, `templates-verify.yml`.
  To add a template, add it to `TEMPLATES` in `customizer-core.js`, run
  `templates-cases.js --write`, and a glyph for it to the sprite. A tool
  can fetch a template by its id.

To change the cards, edit `showcase.cpb` and regenerate:

```
site-tools/showcase-data.py --cpb "$(command -v cpb)" --write
```

A section of the recipe starts at its `-- <name>: <tagline>` comment line; the
tagline on the card is that comment.
