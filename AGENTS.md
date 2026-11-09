# Editor page command

## Purpose

- Isolated application worktree for creating a child page from the editor's slash menu.

## Ownership

- Owns the Page slash-menu entry, its translations, browser regression and user documentation.
- Root owns production configuration; this worktree uses synthetic local data only.

## Local Contracts

- `/page` creates a document under the current page, including when the current page is a collection row.
- Reuse the existing create-and-open callback: parent permissions, inherited workspace, error feedback and title focus remain authoritative.
- The Sub-pages entry remains a listing block; Page creates a child and opens it for editing.
- Do not deploy or open/update upstream PRs before the user's preview acceptance.

## Work Guidance

- Reuse Salt's existing styles, SQLite, event stream, and browser fixture. Add no runtime dependencies.
- Keep private preview state and generated artifacts under ignored data paths.

## Verification

- Run `npm run build --prefix web`, `node web/tests/slash-menu.mjs` and `git diff --check`.
- Set `GO_BINARY` to the installed Go toolchain and `PLAYWRIGHT_CHANNEL=chrome` to use installed Chrome.
- Verify document/row parenting, workspace inheritance, keyboard/mouse selection, title focus, reload persistence and rejected creation without navigation.

## Child DOX Index
