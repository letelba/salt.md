# Browser regressions

These tests use synthetic data only. They build a temporary Salt binary, start it
on a randomly selected loopback port with a fresh temporary data directory, and
remove it afterwards. They never connect to an existing Salt instance.

From the repository root, with Go and Node.js installed:

```sh
npm ci --prefix web
npm run build --prefix web
npm ci --prefix web/tests
npx --prefix web/tests playwright install chromium
npm test --prefix web/tests
```

`GO_BINARY` may select a Go executable. To use an already installed Chrome instead
of downloading Chromium, skip the browser installation and set
`PLAYWRIGHT_CHANNEL=chrome` when running the tests.

The browser dependency is isolated in this test package; the application has no
additional runtime dependencies. Tests do not upload fixtures or screenshots.

Run just the sign-in coverage with `npm run test:login --prefix web/tests` after
the build and browser setup above. It checks a fresh signed-out screen, the
configured provider button and instance name, navigation through the real OIDC
start endpoint to a synthetic loopback provider, PKCE parameters, safe relative
return links, rejection of external return links, recovery after a temporary
policy failure, and native password sign-in when OIDC is disabled. The synthetic
provider cannot issue tokens; callback validation and actual Slack/MFA acceptance
remain separate checks.

`npm run check --prefix web` also runs `scripts/check-session-auth.mjs`, which
loads the real API client and verifies public policy/recovery requests after 401
and provider 503 failures, blocked protected reads/writes/uploads, deliberate
retry without mutation replay, and ordinary errors that must not pause recovery.

Callout coverage: all seven preset icons, immediate background changes when
cycling icons, existing saved blocks, light/dark appearance, dark-mode text
contrast, and persistence after reload without changing the callout text.

Property ordering covers dragging in both directions and across multiple rows,
Save/Cancel, persistence after reload, unchanged row values and view settings,
ignoring unrelated drags, and narrow layouts.

Menus and arrows covers four reported bugs: a right-click on a view's tab acts
on that view (#24), a template opens its menu on a right-click and closes it on
a click elsewhere (#20), a row menu at the foot of the sidebar's tree stays
whole and on top (#21), ↑ from the second block stops in an empty first block
instead of jumping to the title (#25), and the library's shelves scroll inside
a narrow pane (#19).

Shortcuts covers the single letters (#23): each acts only while nothing is
being typed, stays a plain letter while something is, and appears in the sheet.

Embeds covers a collection inside a document (#22, #26): the embed keeps its
filters to itself and across a reload, a second embed and the collection's own
page still show the saved view, and the embed's toolbar leaves out what belongs
to the collection page.

People covers a person property with several people and a list of who can be
picked (#11): only those are offered, nobody else can be typed in, the list
stays open for several picks, the setting is in the Properties dialog, and a
row holding several keeps them until somebody picks one after a switch back.
