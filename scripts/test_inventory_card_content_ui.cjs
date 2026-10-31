// Build admin first. Disposable API fixtures; credentials never reach a real service.
const fs = require('node:fs'), path = require('node:path'), http = require('node:http'), assert = require('node:assert/strict');
const { chromium, expect } = require(process.env.PLAYWRIGHT_MODULE || '@playwright/test');
const root = path.resolve(__dirname, '../admin/dist'), out = process.env.ZCARD_TEST_OUTPUT || '/tmp/zcard-card-content-ui';
fs.mkdirSync(out, { recursive: true });
const server = http.createServer((req, res) => {
  let file = path.join(root, new URL(req.url, 'http://local').pathname.replace(/^\/admin\/?/, ''));
  if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(root, 'index.html');
  res.setHeader('Content-Type', ({ '.html': 'text/html', '.js': 'application/javascript', '.css': 'text/css' })[path.extname(file)] || 'application/octet-stream'); res.end(fs.readFileSync(file));
});
(async () => {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless: true, ...(process.env.CHROME_PATH ? { executablePath: process.env.CHROME_PATH } : {}) });
  try {
    for (const width of [1440, 390]) {
      const page = await browser.newPage({ viewport: { width, height: 1000 } }); page.setDefaultTimeout(15000);
      const errors = [], requests = []; let permissions = ['*'], version = 0, plain = 'HYDYG6666----666666', failSave = true, slowView = false, releaseView;
      const rows = [{ id: 1, product_id: 10, status: 'available', sku_id: 101, sku_name: '月卡', masked_content: '****6666' }, { id: 2, product_id: 10, status: 'used', masked_content: '****0002' }];
      page.on('pageerror', e => errors.push(e.message));
      await page.addInitScript(() => { localStorage.setItem('token', JSON.stringify('fixture')); localStorage.setItem('refreshToken', JSON.stringify('fixture')); });
      await page.route('**/api/v1/**', async route => {
        const req = route.request(), p = new URL(req.url()).pathname, body = req.postData() ? req.postDataJSON() : undefined;
        requests.push({ p, method: req.method(), body });
        let data = { categories: [], items: [], batches: [], total: 0 };
        if (p.endsWith('/auth/profile')) data = { admin: { id: 1, username: 'fixture' }, permissions };
        if (p.endsWith('/admin/products')) data = { products: [{ id: 10, name: '卡密商品', price_cents: 1000 }], total: 1 };
        if (p.endsWith('/inventory/targets')) data = { product_id: 10, product_name: '卡密商品', revision: 1, targets: [{ sku_id: 0, can_import: true }, { sku_id: 101, sku_name: '月卡', can_import: true }] };
        if (p.endsWith('/inventory/cards')) data = { cards: rows, total: rows.length };
        if (/\/cards\/\d+\/content$/.test(p)) {
          const id = Number(p.match(/cards\/(\d+)/)[1]);
          if (req.method() === 'PUT') {
            if (failSave) return route.fulfill({ status: 409, json: { reason: 'inventory.CARD_CHANGED', message: '卡密已变化，请重新查看' } });
            assert.equal(body.expected_version, version); version++; plain = body.content; rows[0].masked_content = '****ATED'; data = { id, version };
          } else { if (slowView) await new Promise(resolve => { releaseView = resolve; }); data = { id, content: plain, ...(version ? { version } : {}), can_edit: id === 1, edit_block_reason: id === 2 ? '已售卡密不能编辑' : '' }; }
        }
        if (p.endsWith('/inventory/preview')) data = { total: 1, revision: 1, sku_id: body.sku_id };
        if (p.endsWith('/inventory/import')) data = { id: 99, product_id: 10, imported: 1 };
        await route.fulfill({ json: data });
      });
      const url = `http://127.0.0.1:${server.address().port}/admin/inventory?product_id=10`;
      await page.goto(url);
      const readCount = () => requests.filter(r => r.method === 'GET' && r.p.endsWith('/content')).length;
      const button = page.getByRole('button', { name: '查看卡密 1', exact: true }); await button.waitFor();
      const box = await button.boundingBox(); assert(box.x >= 0 && box.x + box.width <= width, 'card actions must be visible without horizontal scrolling');
      assert.equal(readCount(), 0, 'list must not request plaintext');
      await button.click(); assert.equal(readCount(), 0, 'read must wait for confirmation');
      await page.getByRole('button', { name: '确认查看', exact: true }).click();
      const modal = page.locator('.n-modal').filter({ hasText: '卡密详情' });
      await expect(modal.getByLabel('完整卡密', { exact: true })).toHaveText(plain);
      await modal.getByRole('button', { name: '编辑卡密', exact: true }).click();
      await modal.getByLabel('编辑完整卡密').fill('UPDATED----SERIAL----ACTIVATED');
      await modal.getByRole('button', { name: '保存卡密', exact: true }).click();
      await expect(modal.getByText(/未保存修改/)).toBeVisible();
      await expect(modal.getByLabel('编辑完整卡密')).toHaveValue('UPDATED----SERIAL----ACTIVATED');
      failSave = false;
      await page.screenshot({ path: path.join(out, `edit-${width}.png`) });
      await modal.getByRole('button', { name: '保存卡密', exact: true }).click(); await expect(modal).not.toBeVisible();
      await expect(page.getByText(plain, { exact: true })).toHaveCount(0);
      const stored = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } })); assert(!stored.includes('ACTIVATED'), 'plaintext must not enter browser storage');
      await page.getByRole('button', { name: '查看卡密 2', exact: true }).click(); await page.getByRole('button', { name: '确认查看', exact: true }).click();
      await expect(modal.getByText('已售卡密不能编辑', { exact: true })).toBeVisible(); await expect(modal.getByRole('button', { name: '编辑卡密', exact: true })).toHaveCount(0);
      await modal.getByRole('button', { name: '关闭', exact: true }).click();
      slowView = true; await button.click(); await page.getByRole('button', { name: '确认查看', exact: true }).click();
      await expect.poll(() => typeof releaseView).toBe('function'); await modal.getByRole('button', { name: '关闭', exact: true }).click();
      const late = page.waitForResponse(r => r.url().endsWith('/cards/1/content')); releaseView(); await late;
      await expect(modal).not.toBeVisible(); slowView = false;
      await page.getByRole('button', { name: '导入卡密', exact: true }).click();
      const importing = page.locator('.n-modal').filter({ hasText: '按规格导入卡密' });
      await importing.getByLabel('导入卡密内容').fill('HYDYG6666----666666'); await importing.getByRole('button', { name: '预览', exact: true }).click();
      await expect(importing.getByRole('button', { name: '确认导入', exact: true })).toBeEnabled(); assert.equal(requests.filter(r => r.p.endsWith('/preview')).at(-1).body.import_format, 'line');
      await importing.getByLabel('导入格式', { exact: true }).click(); await page.getByText('靓号号码（仅取 --- 前的号码）', { exact: true }).last().click();
      await expect(importing.getByText(/后续字段不会作为卡密交付/)).toBeVisible();
      await expect(importing.getByRole('button', { name: '确认导入', exact: true })).toBeDisabled();
      await importing.getByRole('button', { name: '预览', exact: true }).click(); await expect(importing.getByRole('button', { name: '确认导入', exact: true })).toBeEnabled();
      await importing.getByRole('button', { name: '确认导入', exact: true }).click(); await expect(importing).not.toBeVisible();
      assert.equal(requests.filter(r => r.p.endsWith('/import')).at(-1).body.import_format, 'premium');
      permissions = ['inventory:read', 'catalog:read']; await page.reload(); await page.getByText('****ATED', { exact: true }).waitFor();
      await expect(page.getByRole('button', { name: /^查看卡密 / })).toHaveCount(0);
      assert.deepEqual(errors, []); console.log(`inventory content ${width}px: passed`); await page.close();
    }
  } finally { await browser.close(); server.close(); }
})().catch(error => { console.error(error); server.close(); process.exitCode = 1; });
