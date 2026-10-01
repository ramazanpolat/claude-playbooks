# site/

The cpb website: plain HTML, CSS and a little JS, no build step.

## Hosting

The site is served by Cloudflare Pages, straight from `site/` on `main`
(Git integration: no build command, output directory `site`).

- A merge to `main` that touches `site/` deploys on its own. No deploy
  workflow lives in this repo.
- A pull request that touches `site/` gets a Cloudflare preview link.

## Keeping it honest

Every command and output on the page comes from a real run of cpb, and two
scripts keep the page from drifting from the grammar:

- `verify-snippets.sh <path to cpb>` re-runs the page's commands against a
  build, in a throwaway `HOME`, and checks the key lines of their output.
- `check-tui-goldens.py` diffs the `cpb tui` blocks against
  `internal/tui/testdata/*.golden`, the real screens. Those blocks show the
  fixtures' own names (`kommander-dev`, `k9`, `9router`), so change them in
  the goldens, never on the page alone.

CI runs both (`.github/workflows/site-verify.yml`) on changes to `site/`,
`README.md`, `docs/`, `examples/` and the goldens.
