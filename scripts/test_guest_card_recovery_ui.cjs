// Verify the current production storefront build against an isolated API fixture.
// PLAYWRIGHT_MODULE=/path/to/playwright/test CHROME_PATH=/path/to/chrome node scripts/test_guest_card_recovery_ui.cjs
const { chromium, expect } = require(process.env.PLAYWRIGHT_MODULE || '@playwright/test');
const { createServer } = require('node:http');
const { existsSync, readFileSync, mkdirSync, mkdtempSync } = require('node:fs');
const { join, extname, resolve } = require('node:path');
const { tmpdir } = require('node:os');
const assert = require('node:assert/strict');
const dist = resolve(__dirname, '../storefront/dist');
const output = process.env.ZCARD_TEST_OUTPUT || mkdtempSync(join(tmpdir(), 'zcard-guest-card-recovery-'));
mkdirSync(output, { recursive: true });
const number = 'GUEST-VIRTUAL-RECOVERY';
const token = 'A'.repeat(43);
const content = 'ACCOUNT-001----password-ABC123\nhttps://example.test/activation\n完整备注：第三行也应保留';
const server = createServer((req, res) => {
  const pathname = new URL(req.url, 'http://localhost').pathname;
  let file = join(dist, pathname === '/' ? 'index.html' : pathname);
  if (!existsSync(file) && !extname(pathname)) file = existsSync(file + '.html') ? file + '.html' : join(dist, 'index.html');
  if (!existsSync(file)) { res.statusCode = 404; return res.end(); }
  res.setHeader('Content-Type', ({ '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml' })[extname(file)] || 'application/octet-stream');
  res.setHeader('Cache-Control', 'no-store');
  res.end(readFileSync(file));
});

(async () => {
  assert(existsSync(join(dist, 'index.html')), 'Build storefront before running the fixture');
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const base = 'http://127.0.0.1:' + server.address().port;
  const browser = await chromium.launch({ headless: true, ...(process.env.CHROME_PATH ? { executablePath: process.env.CHROME_PATH } : {}) });
  try {
    for (const width of [1440, 390]) {
      const context = await browser.newContext({ viewport: { width, height: 1000 } });
      const page = await context.newPage();
      const errors = [], lookups = [], deliveries = [], recoveries = [];
      page.on('pageerror', error => errors.push(error.message));
      await page.addInitScript(() => {
        window.fixtureCopied = '';
        Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async text => { window.fixtureCopied = text; } } });
      });
      await page.route('**/api/v1/**', async route => {
        const request = route.request(), path = new URL(request.url()).pathname;
        let data = { items: [], categories: [], posts: [], banners: [], currencies: [], total: 0 };
        if (path.endsWith('/config')) data = { entries: Object.entries({ 'site.name': '游客订单回归', 'trade.cart_enabled': false, 'theme.notice_auto': false, 'i18n.default_locale': 'zh_CN' }).map(([key, value]) => ({ key, value_json: JSON.stringify(value) })) };
        if (path.endsWith('/access-code')) { assert.equal(request.postDataJSON().email, 'guest@example.test'); data = {}; }
        if (path.endsWith('/access-recover')) {
          const body = request.postDataJSON(); recoveries.push(body);
          if (body.code !== '123456') return route.fulfill({ status: 400, json: { reason: 'order.ACCESS_CODE_INVALID', message: '验证码无效，请重新填写' } });
          data = { order_access_token: token };
        }
        if (path === '/api/v1/storefront/orders/' + number) {
          lookups.push(request.headers());
          if (request.headers()['x-order-access-token'] !== token) return route.fulfill({ status: 404, json: { reason: 'order.ACCESS_REQUIRED', message: '请通过下单邮箱验证' } });
          data = { order_no: number, status: 'delivered', total_cents: 1000, paid_total_cents: 1000, created_at: 1700000000, items: [{ id: 1, product_id: 1, product_name: '完整卡密商品', goods_type: 'virtual', quantity: 1, unit_price_cents: 1000, amount_cents: 1000, fulfillment_status: 'delivered' }] };
        }
        if (path.endsWith('/delivery/fetch')) {
          const body = request.postDataJSON(); deliveries.push(body);
          assert.equal(body.order_no, number);
          assert.equal(body.order_access_token, token, 'recovered token did not reach the delivery request');
          assert(!body.query_password, 'fixture must exercise password-free email recovery');
          data = { order_no: number, status: 'delivered', fetch_count: deliveries.length, items: [{ item_id: 1, delivery_id: 1, product_name: '完整卡密商品', kind: 'card', content, masked: false }] };
        }
        await route.fulfill({ json: data });
      });
      await page.goto(base + '/fetch?order_no=' + number);
      const form = page.locator('.order-recovery');
      await form.locator('input[type=email]').fill('guest@example.test');
      await form.getByRole('button', { name: '发送验证码', exact: true }).click();
      await expect(form.getByRole('status')).toContainText('若邮箱与订单匹配');
      const code = form.locator('input[autocomplete="one-time-code"]');
      await code.fill('000000');
      await form.getByRole('button', { name: '验证并查看订单', exact: true }).click();
      await expect(form.getByRole('alert')).toContainText('验证码无效');
      assert.equal(lookups.length, 0, 'invalid OTP queried a private order');
      assert.equal(deliveries.length, 0, 'invalid OTP fetched a card');
      await code.fill('123456');
      await form.getByRole('button', { name: '验证并查看订单', exact: true }).click();
      await expect(page.locator('.delivery-item pre')).toHaveCount(1);
      assert.equal(await page.locator('.delivery-item pre').textContent(), content, 'delivery card text was truncated');
      await page.getByRole('button', { name: '复制内容', exact: true }).click();
      assert.equal(await page.evaluate(() => window.fixtureCopied), content, 'copy lost part of the card');
      assert.equal(recoveries.length, 2);
      assert.equal(lookups.at(-1)['x-order-access-token'], token);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      await page.screenshot({ path: join(output, `guest-recovery-${width}.png`), fullPage: true });

      const before = deliveries.length;
      await page.goto(base + '/payment/' + number);
      await expect(page.locator('.pay-state-title')).toHaveText('支付成功');
      await expect(page.locator('.delivery-item pre')).toHaveCount(1);
      assert.equal(await page.locator('.delivery-item pre').textContent(), content);
      assert(deliveries.length > before, 'payment return did not auto-fetch with the recovered token');
      assert.deepEqual(errors, []);
      await page.screenshot({ path: join(output, `guest-payment-${width}.png`), fullPage: true });
      await context.close();
      console.log(`Guest virtual OTP/token, full card/copy, and payment auto-fetch ${width}px: passed`);
    }
    console.log(JSON.stringify({ output }));
  } finally { await browser.close(); server.close(); }
})().catch(error => { console.error(error); server.close(); process.exitCode = 1; });
