# Application worktree

## Purpose

- Application source for the inline comments feature.

## Ownership

- The application owns editor interaction, comment persistence, and user-facing documentation.

## Local Contracts

- Inline comments attach to selections within one text block and retain the original quote if the anchor is no longer available.
- Existing page comments and page permissions remain supported. External page replacement must not silently reattach an old anchor.

## Work Guidance

- Reuse the existing BlockNote/Yjs editor and comments panel; keep deployment-specific material out of the application repository.

## Verification

- Run the web checks and relevant Go tests. Verify selection, navigation, reload, and deletion in a browser.

## Child DOX Index
