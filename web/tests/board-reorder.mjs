import assert from 'node:assert/strict';
import { eventually, withFixture } from './fixture.mjs';

await withFixture(async ({ page, context, api, base, workspace, path, read }) => {
  page.setDefaultTimeout(15000);
  const collectionId = path.split('/').pop();
  const config = await read();
  const boardView = config.views.find((v) => v.id === 'board');
  await api('PUT', path, {
    schema: [...config.schema, { id: 'rank', name: 'Rank', type: 'number' }],
    views: [boardView],
  });
  const list = async () => (await api('GET', `${path}/rows`)).rows;
  const [first, other] = await list();
  const makeRow = (title, rank) => api('POST', '/api/pages', {
    parentId: collectionId, title, props: { status: 'todo', rank },
  });
  const middle = await makeRow('Middle card', 2);
  const last = await makeRow('Last card', 3);
  await api('PATCH', `/api/pages/${first.id}`, { propsPatch: { rank: 1 } });
  // Old data can share a position. Dropping after the first row must still
  // place the card between it and the next row with that position.
  await api('PATCH', `/api/pages/${middle.id}`, { position: first.position });
  // Ensure a known order even if both fixtures were created in one tick.
  const initial = (await list()).filter((r) => r.props.status === 'todo').map((r) => r.id);
  const [top, next] = initial;
  assert.equal(initial[2], last.id);

  await page.goto(`${base}/p/${collectionId}`);
  const cards = page.locator('[data-col="todo"] [data-board-row]');
  const card = (id) => page.locator(`[data-board-row="${id}"]`);
  const order = () => cards.evaluateAll((nodes) => nodes.map((node) => node.dataset.boardRow));
  const see = (ids) => eventually(async () => (await order()).join() === ids.join());
  await see(initial);

  async function drag(row, anchor, after, indicator = true) {
    const a = await card(row).boundingBox();
    const b = await card(anchor).boundingBox();
    assert(a && b, 'both cards are on the board');
    await page.mouse.move(a.x + 45, a.y + 16);
    await page.mouse.down();
    await page.mouse.move(b.x + 45, b.y + b.height * (after ? 0.8 : 0.25), { steps: 12 });
    if (indicator) await page.locator('[data-insert]').waitFor();
    else assert.equal(await page.locator('[data-insert]').count(), 0);
    await page.mouse.up();
  }

  const observer = await context.newPage();
  await observer.goto(page.url());
  await observer.locator(`[data-board-row="${top}"]`).waitFor();
  await drag(last.id, top, true);
  await see([top, last.id, next]);
  await eventually(async () => {
    const ids = await observer.locator('[data-col="todo"] [data-board-row]')
      .evaluateAll((nodes) => nodes.map((node) => node.dataset.boardRow));
    return ids.join() === [top, last.id, next].join();
  });
  await page.reload();
  await see([top, last.id, next]);
  await drag(top, next, true);
  await see([last.id, next, top]);
  await drag(top, last.id, false);
  await see([top, last.id, next]);
  assert.equal((await api('GET', `/api/pages/${last.id}`)).props.status, 'todo');

  // The card menu offers the same action to keyboard users.
  const more = card(last.id).getByRole('button', { name: 'More', exact: true });
  await more.focus();
  await more.press('Enter');
  await card(last.id).getByRole('button', { name: 'Move up', exact: true }).press('Enter');
  await see([last.id, top, next]);

  await page.route(`**${path}/rows/reorder`, (route) => route.fulfill({
    status: 500, json: { error: 'synthetic failure' },
  }));
  await drag(last.id, next, true);
  await page.getByRole('region', { name: 'Notifications' }).getByText('Change not saved').waitFor();
  await see([last.id, top, next]);
  await page.unroute(`**${path}/rows/reorder`);

  // Existing cross-column behavior still changes the grouping property.
  await drag(last.id, other.id, false, false);
  await eventually(async () => (await api('GET', `/api/pages/${last.id}`)).props.status === 'done');
  await see([top, next]);

  // A property sort takes precedence and does not change the stored order.
  const saved = (await list()).map((r) => [r.id, r.position]);
  await api('PUT', path, { views: [{ ...boardView, sort: { property: 'rank', dir: 'desc' } }] });
  await page.reload();
  await see([middle.id, first.id]);
  await drag(first.id, middle.id, false, false);
  assert.deepEqual((await list()).map((r) => [r.id, r.position]), saved);

  // Touch scrolling still works without a hold. With a hold, a vertical
  // gesture reorders among the cards visible in a filtered board.
  const hidden = await makeRow('Hidden card', 0);
  await api('PUT', path, {
    views: [{ ...boardView, filters: [{ property: 'rank', op: 'gt', value: '0' }] }],
  });
  await page.reload();
  await see([top, next]);
  const cdp = await context.newCDPSession(page);
  await cdp.send('Emulation.setTouchEmulationEnabled', { enabled: true });
  const touch = (type, x, y) => cdp.send('Input.dispatchTouchEvent', {
    type,
    touchPoints: type === 'touchEnd' ? [] : [{ x, y }],
  });
  const from = await card(next).boundingBox();
  const to = await card(top).boundingBox();
  await touch('touchStart', from.x + 45, from.y + 16);
  await card(next).and(page.locator('.is-armed')).waitFor();
  for (let step = 1; step <= 8; step++) {
    const y = from.y + 16 + (to.y + 8 - from.y - 16) * step / 8;
    await touch('touchMove', to.x + 45, y);
  }
  await page.locator('[data-insert="before"]').waitFor();
  await touch('touchEnd');
  await see([next, top]);
  assert.equal(await card(hidden.id).count(), 0);
  assert.equal((await list()).find((r) => r.id === hidden.id).position, hidden.position);
  await touch('touchStart', from.x + 45, from.y + 16);
  await touch('touchMove', to.x + 45, to.y + 8);
  assert.equal(await page.locator('.board-drag-ghost').count(), 0);
  await touch('touchEnd');
  await see([next, top]);
  await cdp.detach();

  // Reject anchors outside this collection, including readable pages.
  const beforeRejection = (await list()).map((r) => [r.id, r.position]);
  const outside = await api('POST', '/api/pages', { title: 'Outside', workspaceId: workspace.id });
  const res = await context.request.post(`${base}${path}/rows/reorder`, {
    data: { rowId: first.id, anchorId: outside.id },
  });
  assert.equal(res.status(), 404);
  assert.deepEqual((await list()).map((r) => [r.id, r.position]), beforeRejection);

  console.log('Board reorder: pointer, touch, keyboard, reload, live updates, legacy ties, filters and failed saves passed.');
});
