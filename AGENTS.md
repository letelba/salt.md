# Board card ordering

## Purpose

- Isolated application worktree for persistent card ordering within board columns.

## Ownership

- Owns drag placement, card-menu ordering, the relative row-order API, user documentation and focused regressions. The deployment repository owns releases.

## Local Contracts

- Same-column moves with **No sort** save shared collection row positions. All unsorted views and copies of a card use that position. Sorted boards follow their selected property.
- Resolve drop anchors and position changes transactionally. Require write access to the collection and moved card, and read access to the anchor. Both rows must be live children of the same collection.
- Preserve the relative order of other rows, even if the view filters them out. Cross-column drops continue to change the grouping property.
- Let the user test the synthetic preview before opening or updating upstream PRs.

## Work Guidance

- Reuse page positions, row-change events, the pointer drag hook and the card menu. Keep private previews outside this source checkout.

## Verification

- Run Go tests and vet, frontend checks, a Vite build, `node web/tests/board-reorder.mjs`, `node web/tests/board-tick.mjs` and `git diff --check`.
- Use the installed toolchain via `GO_BINARY` and installed Chrome via `PLAYWRIGHT_CHANNEL=chrome`.
- Preserve screenshot fingerprints and report retake gates separately from functional checks.

## Child DOX Index
