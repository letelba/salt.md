# Automatic subpages

## Purpose

- Isolated application worktree for showing child pages in their parent's body automatically.

## Ownership

- Owns the shared child-page list, editor placement, styling, browser regression and feature documentation.
- The deployment repository owns releases; previews use synthetic local data only.

## Local Contracts

- Documents and collection rows show their readable, non-trashed direct children as individual page rows below the editor, in tree order. Each row has a page icon and title; do not wrap them in a boxed Sub-pages section or heading.
- Hide the automatic list when there are no children or a Sub-pages block already exists anywhere in the document.
- Existing Sub-pages blocks keep their chosen placement and empty-state behavior.
- Render from the existing permission-filtered tree; do not insert blocks, rewrite page content or change exports, permissions or collection row listings.
- Reuse normal, modified and middle-click page navigation. Keep `/page` creation intact.
- Do not deploy or open/update upstream PRs before the user's preview acceptance.

## Work Guidance

- Reuse existing styles, translations, tree updates and browser fixtures. Add no runtime dependencies.
- Keep private preview state and generated artifacts under ignored data paths.

## Verification

- Run frontend checks/build, `node web/tests/subpages.mjs`, `node web/tests/slash-menu.mjs` and `git diff --check`.
- Use `GO_BINARY` for the installed Go toolchain and `PLAYWRIGHT_CHANNEL=chrome` for installed Chrome.
- Verify documents/tasks, live hierarchy changes, manual-list deduplication, unchanged content, keyboard navigation and narrow layouts.
- Preserve upstream screenshot fingerprints and report any retake gate separately from functional results.

## Child DOX Index
