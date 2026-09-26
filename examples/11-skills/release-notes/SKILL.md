---
name: release-notes
description: Draft release notes from the commits since the last tag.
---

List the commits since the last tag (`git log $(git describe --tags --abbrev=0)..HEAD --oneline`),
group them by area, and write one line per user-visible change.
