import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { withFixture } from './fixture.mjs';

let scenarios = 0;
await withFixture(async ({ context, api, base, providerBase }) => {
  await api('PUT', '/api/settings', { instanceName: 'Example workspace', signupMode: 'invite' });
  const signedOut = await context.browser().newContext({ locale: 'en-US' });
  try {
    const page = await signedOut.newPage();
    page.setDefaultTimeout(10000);
    const provider = page.getByRole('link', { name: 'Example Login', exact: true });
    const ready = async () => {
      await page.getByRole('heading', { name: 'Example workspace', exact: true }).waitFor();
      await provider.waitFor();
      assert.equal(await page.getByPlaceholder('Email', { exact: true }).isVisible(), true);
      assert.equal(await page.getByPlaceholder('Password', { exact: true }).isVisible(), true);
    };

    // A fresh anonymous browser must show the configured provider, even when
    // startup's protected requests have already failed before Login mounts.
    await page.goto(base);
    await ready();
    assert.equal(await provider.getAttribute('href'), '/api/oidc/start');
    assert.equal(await page.getByRole('button', { name: 'Create account', exact: true }).count(), 0);
    await provider.click();
    await page.getByRole('heading', { name: 'Local test provider', exact: true }).waitFor();
    const authorization = new URL(page.url());
    assert.equal(authorization.origin, providerBase);
    assert.equal(authorization.pathname, '/authorize');
    assert.equal(authorization.searchParams.get('client_id'), 'browser-fixture');
    assert.equal(authorization.searchParams.get('redirect_uri'), base + '/api/oidc/callback');
    assert.equal(authorization.searchParams.get('response_type'), 'code');
    assert.equal(authorization.searchParams.get('code_challenge_method'), 'S256');
    for (const field of ['state', 'nonce', 'code_challenge']) assert(authorization.searchParams.get(field));
    scenarios++;

    const destination = '/p/invented-document?view=board';
    await page.goto(base + '/?next=' + encodeURIComponent(destination));
    await ready();
    assert.equal(await provider.getAttribute('href'), '/api/oidc/start?next=' + encodeURIComponent(destination));
    scenarios++;
    for (const unsafe of ['https://outside.invalid/path', '//outside.invalid/path', '/\\outside.invalid/path']) {
      await page.goto(base + '/?next=' + encodeURIComponent(unsafe));
      await ready();
      assert.equal(await provider.getAttribute('href'), '/api/oidc/start');
      scenarios++;
    }

    // A temporary bootstrap failure must recover on a deliberate reload.
    let policyRequests = 0;
    await signedOut.route('**/api/signup-policy', async (route) => {
      if (policyRequests++ === 0) await route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":"Temporary failure"}' });
      else await route.continue();
    });
    const failedPolicy = page.waitForResponse(response => new URL(response.url()).pathname === '/api/signup-policy' && response.status() === 503);
    await page.goto(base);
    await failedPolicy;
    await page.getByRole('button', { name: 'Sign in', exact: true }).waitFor();
    assert.equal(await provider.count(), 0);
    await page.reload();
    await ready();
    scenarios++;
  } finally {
    await signedOut.close();
  }
}, { oidc: true });

await withFixture(async ({ context, api, base, workspace }) => {
  const password = randomUUID();
  const account = await api('POST', '/api/users', { name: 'Native recovery', email: 'recovery@example.test',
    password, isAdmin: false, workspaces: [{ id: workspace.id, role: 'member' }] });
  const signedOut = await context.browser().newContext({ locale: 'en-US' });
  try {
    const page = await signedOut.newPage();
    page.setDefaultTimeout(10000);
    await page.goto(base);
    await page.getByRole('button', { name: 'Sign in', exact: true }).waitFor();
    assert.equal(await page.locator('a[href^="/api/oidc/start"]').count(), 0);
    await page.getByPlaceholder('Email', { exact: true }).fill('recovery@example.test');
    await page.getByPlaceholder('Password', { exact: true }).fill(password);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('button', { name: 'Sign in', exact: true }).waitFor({ state: 'detached' });
    const me = await (await signedOut.request.get(base + '/api/me')).json();
    assert.equal(me.authenticated, true);
    assert.equal(me.user.id, account.id);
    scenarios++;
  } finally {
    await signedOut.close();
  }
});
console.log(`Sign-in browser regression: ${scenarios} scenarios passed using disposable loopback fixtures.`);
