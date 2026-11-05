# Comment replies and inbox

## Purpose

- Isolated application worktree for threaded comments and persistent personal reply notifications.

## Ownership

- Owns shared comment writes, SQLite migrations, comment threads, inbox, navigation, live updates, tests, and user documentation.
- Root owns production configuration; this worktree uses synthetic local data only.

## Local Contracts

- Replies stay within one page; threads display one indentation level and resolve as a unit.
- Reply and notification creation is atomic and idempotent. Notify the target and root authors, deduplicated and excluding the actor.
- Inbox content and unread counts require current page access and credential scope. Read state belongs to the recipient.
- Preserve other authors' replies on deletion, original text anchors, and unsent drafts through failed requests and sign-in recovery.
- Do not deploy or open/update upstream PRs before the user's preview acceptance.

## Work Guidance

- Reuse Salt's existing styles, SQLite, event stream, and browser fixture. Add no runtime dependencies.
- Keep private preview state and generated artifacts under ignored data paths.

## Verification

- Run Go tests, frontend checks/build, focused two-user browser regressions, and git diff --check.
- Verify a running local dev preview, unread delivery, exact reply navigation, permission denial, responsive layouts, and reload persistence.

## Child DOX Index
