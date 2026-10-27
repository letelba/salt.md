import assert from 'node:assert/strict';
import { createServer } from 'vite';

// Exercise the real API client: a protected request can fail before Login mounts.
// Its public policy still needs to load while requests for signed-in work pause.
const server = await createServer({ server: { middlewareMode: true }, appType: 'custom' });
const originalFetch = globalThis.fetch;
const originalWindow = globalThis.window;
globalThis.window = new EventTarget();
const calls = [];
let respond;
globalThis.fetch = async (url, init) => {
  calls.push({ url, method: init?.method ?? 'GET' });
  return respond(url, init);
};

try {
  const { api, sessionRequestsArePaused, resumeSessionRequests } = await server.ssrLoadModule('/src/api.ts');
  let checks = 0;
  for (const failure of [
    { status: 401, body: { error: 'unauthorized' }, event: 'salt:unauthorized' },
    { status: 503, body: { error: 'Provider unavailable', code: 'auth_unavailable' }, event: 'salt:auth-unavailable' },
  ]) {
    resumeSessionRequests();
    calls.length = 0;
    let events = 0;
    window.addEventListener(failure.event, () => events++, { once: true });
    respond = (url) => url === '/api/settings'
      ? Response.json(failure.body, { status: failure.status })
      : Response.json({ mode: 'invite', instanceName: 'Example', oidcName: 'Example Login' });
    await assert.rejects(api.getSettings(), (error) => error.status === failure.status);
    assert.equal(events, 1);
    assert.equal(sessionRequestsArePaused(), true);
    const policy = await api.signupPolicy();
    assert.equal(policy.oidcName, 'Example Login');
    assert.equal(policy.instanceName, 'Example');
    assert.equal(sessionRequestsArePaused(), true, 'Public bootstrap must not resume protected work');
    const count = calls.length;
    for (const request of [
      () => api.getSettings(),
      () => api.createPage(null, 'Unsaved draft'),
      // A protected subroute must not inherit the public /api/me exception.
      () => api.putPrefs({ language: 'en' }),
      () => api.upload(new File(['draft'], 'draft.txt')),
    ]) await assert.rejects(request(), (error) => error.code === 'session_expired');
    assert.equal(calls.length, count, 'Protected reads, writes and uploads must not reach the network');
    for (const request of [
      () => api.me(),
      () => api.login('test@example.test', 'invented-password'),
      () => api.setup('Test', 'test@example.test', 'invented-password'),
      () => api.signup('Test', 'test@example.test', 'invented-password'),
      () => api.logout(),
    ]) await request();
    assert.equal(sessionRequestsArePaused(), true, 'Recovery requests alone must not release queued work');
    const beforeResume = calls.length;
    respond = () => Response.json({ ok: true });
    resumeSessionRequests();
    await new Promise(setImmediate);
    assert.equal(calls.length, beforeResume, 'Resuming must not replay failed or blocked requests');
    await api.getSettings();
    assert.equal(calls.length, beforeResume + 1, 'Only an explicit retry may send another protected request');
    checks++;
  }

  // An ordinary transient HTTP failure must not be mistaken for an expired session.
  respond = () => Response.json({ error: 'Temporary failure' }, { status: 503 });
  await assert.rejects(api.getSettings(), (error) => error.status === 503);
  assert.equal(sessionRequestsArePaused(), false);
  checks++;

  // A rejected password must leave public provider discovery available.
  respond = (url) => url === '/api/login'
    ? Response.json({ error: 'Invalid password', code: 'invalid_credentials' }, { status: 401 })
    : Response.json({ oidcName: 'Example Login' });
  await assert.rejects(api.login('test@example.test', 'wrong-password'), (error) => error.code === 'invalid_credentials');
  assert.equal(sessionRequestsArePaused(), false);
  assert.equal((await api.signupPolicy()).oidcName, 'Example Login');
  checks++;

  calls.length = 0;
  respond = () => Response.json({ error: 'unauthorized' }, { status: 401 });
  await assert.rejects(api.updatePage('invented', { title: 'Draft' }), (error) => error.status === 401);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].method, 'PATCH');
  respond = () => Response.json({ ok: true });
  resumeSessionRequests();
  await new Promise(setImmediate);
  assert.equal(calls.length, 1, 'A failed mutation must not replay when sign-in recovery resumes');
  await api.updatePage('invented', { title: 'Draft' });
  assert.equal(calls.length, 2, 'Only a deliberate mutation retry may resend the draft');
  checks++;
  console.log(`Session recovery: ${checks} scenarios passed (401, provider 503, ordinary 503, invalid password, failed mutation).`);
} finally {
  globalThis.fetch = originalFetch;
  globalThis.window = originalWindow;
  await server.close();
}
