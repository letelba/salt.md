import assert from 'node:assert/strict';
import { createServer } from 'vite';

// Exercise the real API client: a protected request can fail before Login mounts.
// Its public policy still needs to load while requests for signed-in work pause.
const server = await createServer({ server: { middlewareMode: true }, appType: 'custom' });
const originalFetch = globalThis.fetch;
const originalWindow = globalThis.window;
globalThis.window = new EventTarget();
const calls = [];
globalThis.fetch = async (url, init) => {
  calls.push({ url, method: init?.method ?? 'GET' });
  if (url === '/api/signup-policy') {
    return Response.json({ mode: 'invite', instanceName: 'Example', oidcName: 'Example Login' });
  }
  return Response.json({ error: 'unauthorized' }, { status: 401 });
};

try {
  const { api, sessionRequestsArePaused, resumeSessionRequests } = await server.ssrLoadModule('/src/api.ts');
  await assert.rejects(api.getSettings(), (error) => error.status === 401);
  assert.equal(sessionRequestsArePaused(), true);
  const policy = await api.signupPolicy();
  assert.equal(policy.oidcName, 'Example Login');
  assert.equal(policy.instanceName, 'Example');
  assert.equal(sessionRequestsArePaused(), true, 'Public bootstrap must not resume protected work');
  const count = calls.length;
  await assert.rejects(api.getSettings(), (error) => error.code === 'session_expired');
  await assert.rejects(api.createPage('Unsaved draft'), (error) => error.code === 'session_expired');
  assert.equal(calls.length, count, 'Protected reads and writes must remain blocked without replay');
  resumeSessionRequests();
  assert.equal(sessionRequestsArePaused(), false);
  console.log('Session recovery: login policy loads while protected reads and writes stay paused.');
} finally {
  globalThis.fetch = originalFetch;
  globalThis.window = originalWindow;
  await server.close();
}
