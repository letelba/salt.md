# Board card drag selection

## Purpose

- Isolated application worktree for preventing native page selection when dragging board cards. The deployed card-ordering commit is the base.

## Ownership

- Owns selection handling in the board drag hook and its browser regression. The deployment repository owns releases and the synthetic preview.

## Local Contracts

- Dragging from the card surface must suppress native text selection from the start of the gesture. Text outside the card remains selectable.
- Preserve same-column ordering, cross-column moves, card clicks and controls, and touch scrolling and drag behavior.
- Let the user test the synthetic preview before opening or updating upstream PRs.

## Work Guidance

- Reuse the board drag hook and existing browser fixtures. Use `.local/board-card-reorder-preview/` for private acceptance.

## Verification

- Run Go tests and vet, frontend checks, a Vite build, `node web/tests/board-reorder.mjs`, `node web/tests/board-tick.mjs` and `git diff --check`.
- Use the installed toolchain via `GO_BINARY` and installed Chrome via `PLAYWRIGHT_CHANNEL=chrome`.
- Preserve screenshot fingerprints and report retake gates separately from functional checks.

## Child DOX Index
