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

  // Taking a card out of the board must not start selecting the surrounding page.
  const title = await card(top).locator('.board-card-title').boundingBox();
  const board = await page.locator('.board').boundingBox();
  assert(title && board);
  await page.mouse.move(title.x + 12, title.y + title.height / 2);
  await page.mouse.down();
  await page.mouse.move(title.x + 40, board.y - 70, { steps: 16 });
  await page.locator('.board-drag-ghost').waitFor();
  assert.equal(await page.evaluate(() => window.getSelection()?.toString()), '');
  await page.mouse.up();
  assert.equal(await page.evaluate(() => window.getSelection()?.toString()), '');
  await see(initial);

  const pageTitle = page.locator('textarea.page-title');
  const heading = await pageTitle.boundingBox();
  assert(heading);
  await page.mouse.move(heading.x + 3, heading.y + heading.height / 2);
  await page.mouse.down();
  await page.mouse.move(heading.x + 100, heading.y + heading.height / 2, { steps: 10 });
  await page.mouse.up();
  assert(await pageTitle.evaluate((input) => input.selectionEnd > input.selectionStart));

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
  await page.keyboard.press('Control+z');
  await see(initial);
  // The same card twice, undone twice: each undo finds the state it expects.
  await drag(last.id, next, false);
  await see([top, last.id, next]);
  await drag(last.id, top, false);
  await see([last.id, top, next]);
  await page.keyboard.press('Control+z');
  await see([top, last.id, next]);
  await page.keyboard.press('Control+z');
  await see(initial);
  await drag(last.id, top, true);
  await see([top, last.id, next]);
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

  // Board cards can be dropped directly onto another collection in the sidebar.
  const destination = await api('POST', '/api/pages', { title: 'Destination', type: 'collection', workspaceId: workspace.id });
  // The same properties, so the move carries everything over and asks nothing.
  await api('PUT', `/api/collections/${destination.id}`, { schema: (await api('GET', path)).schema });
  const existing = await api('POST', '/api/pages', { parentId: destination.id, title: 'Existing destination card', props: { status: 'todo' } });
  const cardPath = `/api/pages/${first.id}`;
  const original = await api('GET', cardPath);
  const orderBefore = (await list()).map((r) => r.id);
  await page.reload();
  await card(first.id).waitFor();
  const destinationRow = page.locator(`[data-board-drop-collection="${destination.id}"]`);
  const sourceRow = page.locator(`[data-board-drop-collection="${collectionId}"]`);
  const drop = async (target, highlight = false) => {
    const from = await card(first.id).boundingBox();
    const to = await target.boundingBox();
    assert(from && to, 'card and sidebar row are visible');
    await page.mouse.move(from.x + 45, from.y + 16);
    await page.mouse.down();
    await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2, { steps: 12 });
    await page.locator('.board-drag-ghost').waitFor();
    if (highlight) await target.and(page.locator('[data-board-drop-active]')).waitFor();
    else assert.equal(await page.locator('[data-board-drop-active]').count(), 0);
    await page.mouse.up();
    await page.locator('[data-board-drop-active]').waitFor({ state: 'hidden' });
  };
  await drop(sourceRow);
  assert.equal((await api('GET', cardPath)).parentId, collectionId);

  const movePath = `${path}/rows/move`;
  await page.route(`**${movePath}`, (route) => route.fulfill({ status: 500, json: { error: 'Move failed for test' } }));
  await drop(destinationRow, true);
  await page.getByRole('region', { name: 'Notifications' }).getByText('Move failed for test').waitFor();
  assert.equal((await api('GET', cardPath)).parentId, collectionId);
  await page.unroute(`**${movePath}`);

  const destinationBoard = await context.newPage();
  await destinationBoard.goto(`${base}/p/${destination.id}`);
  await destinationBoard.locator(`[data-board-row="${existing.id}"]`).waitFor();
  await observer.reload();
  await observer.locator(`[data-board-row="${first.id}"]`).waitFor();
  await drop(destinationRow, true);
  await card(first.id).waitFor({ state: 'hidden' });
  await observer.locator(`[data-board-row="${first.id}"]`).waitFor({ state: 'hidden' });
  const movedCard = destinationBoard.locator(`[data-col="todo"] [data-board-row="${first.id}"]`);
  await movedCard.waitFor();
  await destinationBoard.reload();
  await movedCard.waitFor();
  const moved = await api('GET', cardPath);
  assert.equal(moved.parentId, destination.id);
  assert.deepEqual(moved.props, original.props);
  assert(moved.position > existing.position);

  // A retry from the previous source cannot change the already-moved row.
  const stale = await context.request.post(`${base}${movePath}`, { data: { rowId: first.id, collectionId: destination.id } });
  assert.equal(stale.status(), 404);

  await page.keyboard.press('Control+z');
  await card(first.id).waitFor();
  await observer.locator(`[data-board-row="${first.id}"]`).waitFor();
  await movedCard.waitFor({ state: 'hidden' });
  const restored = await api('GET', cardPath);
  assert.equal(restored.parentId, collectionId);
  // Back between the same neighbours; the number itself may differ.
  assert.deepEqual((await list()).map((r) => r.id), orderBefore);
  assert.deepEqual(restored.props, original.props);

  // Ctrl+Z also works when pressed before the move response arrives.
  let releaseMove;
  const gate = new Promise((resolve) => { releaseMove = resolve; });
  await page.route(`**${movePath}`, async (route) => {
    if (route.request().method() === 'POST' && route.request().postDataJSON().collectionId === destination.id) await gate;
    await route.continue();
  });
  const undoRequest = page.waitForRequest((request) => request.method() === 'POST' &&
    request.url().endsWith(`/api/collections/${destination.id}/rows/move`) &&
    request.postDataJSON()?.restore !== undefined);
  await drop(destinationRow, true);
  await page.keyboard.press('Control+z');
  releaseMove();
  await undoRequest;
  await eventually(async () => (await api('GET', cardPath)).parentId === collectionId);

  // Near the top edge of a scrolling column the drag scrolls the list; the
  // slot saved on release is the one the insertion line showed.
  for (let i = 0; i < 14; i++) await makeRow(`Long column card ${i}`, 10 + i);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(`${base}/p/${collectionId}`);
  const column = page.locator('[data-col="todo"] .board-cards');
  await eventually(async () => (await cards.count()) > 14);
  await eventually(async () => column.evaluate((el) => el.scrollHeight > el.clientHeight + 200));
  // Put some card's middle 25px below the top of the list.
  const target = await column.evaluate((el) => {
    el.scrollTop = 300;
    const top = el.getBoundingClientRect().top;
    const list = [...el.querySelectorAll('[data-board-row]')];
    const card = list.find((c) => c.getBoundingClientRect().top > top + 5);
    const mid = card.getBoundingClientRect().top + card.getBoundingClientRect().height / 2;
    el.scrollTop += mid - (top + 25);
    return card.dataset.boardRow;
  });
  const targetBox = await card(target).boundingBox();
  const ids = await order();
  const mover = ids[ids.indexOf(target) + 2];
  const moverBox = await card(mover).locator('.board-card-title').boundingBox();
  await page.mouse.move(moverBox.x + 12, moverBox.y + moverBox.height / 2);
  await page.mouse.down();
  // Stop just outside the edge zone, measure, then one last step to 4px below
  // the target's middle: that step scrolls the list, moving the middle below
  // the pointer.
  const listTop = await column.evaluate((el) => el.getBoundingClientRect().top);
  await page.mouse.move(targetBox.x + 45, listTop + 60, { steps: 10 });
  await page.waitForTimeout(100);
  // The way here may have passed the bottom edge and scrolled; put the
  // target's middle back 25px below the top of the list.
  const targetMiddle = await page.evaluate((id) => {
    const list = document.querySelector('[data-col="todo"] .board-cards');
    const el = document.querySelector(`[data-board-row="${id}"]`);
    const middle = () => { const r = el.getBoundingClientRect(); return r.top + r.height / 2; };
    list.scrollTop += middle() - (list.getBoundingClientRect().top + 25);
    return middle();
  }, target);
  const edge = await column.evaluate((el) => el.getBoundingClientRect().top);
  assert(targetMiddle + 4 - edge < 40, 'the last step lands in the scrolling edge');
  await page.mouse.move(targetBox.x + 45, targetMiddle + 4);
  await page.waitForTimeout(100);
  const shown = await page.locator('[data-insert]').evaluate((el) => ({ id: el.dataset.boardRow, where: el.dataset.insert }));
  await page.mouse.up();
  await eventually(async () => {
    const now = await order();
    const i = now.indexOf(mover);
    return shown.where === 'before' ? now[i + 1] === shown.id : now[i - 1] === shown.id;
  });

  console.log('Board reorder: pointer, touch, keyboard, reload, live updates, legacy ties, filters, failed saves, undo and edge scrolling passed.');
  console.log('Sidebar collection move: highlight, live refresh, persistence, failed saves, stale drops and immediate undo passed.');
});
