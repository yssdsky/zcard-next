// Production browser fixture: builds into a temporary directory and uses no real catalog.
// PLAYWRIGHT_MODULE=/path/to/playwright/test CHROME_PATH=/path/to/chrome node scripts/test_home_recommendations.cjs
const { chromium, expect } = require(process.env.PLAYWRIGHT_MODULE || '@playwright/test');
const { createServer } = require('node:http');
const { readFileSync, existsSync, writeFileSync, mkdirSync, mkdtempSync } = require('node:fs');
const { join, extname, resolve } = require('node:path');
const { tmpdir } = require('node:os');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');
const output = process.env.ZCARD_TEST_OUTPUT || mkdtempSync(join(tmpdir(), 'zcard-home-recommendations-'));
mkdirSync(output, { recursive: true });
const dist = join(output, 'dist');
const product = (id, name) => ({ id, name, slug: 'fixture-' + id, description: '<p>商品说明</p>', price_cents: 1000, stock_type: 'card', stock: 5, stock_status: 'fresh', stock_visible: true, category_id: 1, controls: [], reviews: [], skus: [] });
const ordinary = Array.from({ length: 22 }, (_, i) => product(i + 1, '普通商品' + (i + 1)));
const recommended = Array.from({ length: 10 }, (_, i) => ({ ...product(101 + i, '精选商品' + (i + 1)), is_recommend: true }));
let state;
const reset = () => { state = { recommended: [...recommended], fail: false, requests: [] }; };
reset();
const server = createServer(async (req, res) => {
  const fixture = state;
  const url = new URL(req.url, 'http://localhost');
  const pathname = url.pathname;
  res.setHeader('Cache-Control', 'no-store');
  if (pathname.startsWith('/api/')) {
    res.setHeader('Content-Type', 'application/json');
    if (pathname.endsWith('/config')) {
      const settings = { 'site.name': '推荐验证商店', 'trade.cart_enabled': false, 'theme.notice_auto': false, 'template.per_page': 20, 'template.category_nav_style': fixture.navStyle || 'list' };
      return res.end(JSON.stringify({ entries: Object.entries(settings).map(([key, value]) => ({ key, value_json: JSON.stringify(value) })) }));
    }
    if (pathname === '/api/v1/storefront/products') {
      const isRecommended = url.searchParams.get('recommend_only') === 'true';
      const page = Number(url.searchParams.get('page') || 1);
      const size = Number(url.searchParams.get('page_size') || 20);
      const preview = isRecommended && size === 8;
      fixture.requests.push({ isRecommended, page, size, preview });
      let items = isRecommended ? fixture.recommended : ordinary;
      const keyword = url.searchParams.get('keyword');
      if (keyword) items = items.filter(p => p.name.includes(keyword));
      const response = { items: items.slice((page - 1) * size, page * size), total: items.length, page, page_size: size };
      if (preview && fixture.fail) { res.statusCode = 503; return res.end(JSON.stringify({ message: 'fixture failure' })); }
      return res.end(JSON.stringify(response));
    }
    if (/^\/api\/v1\/storefront\/products\/\d+$/.test(pathname)) {
      const id = Number(pathname.split('/').pop());
      return res.end(JSON.stringify([...ordinary, ...recommended].find(p => p.id === id)));
    }
    if (pathname.endsWith('/categories')) return res.end(JSON.stringify({ categories: [{ id: 1, name: '测试分类', parent_id: 0 }] }));
    return res.end(JSON.stringify({ items: [], posts: [], banners: [], currencies: [], total: 0 }));
  }
  let file = join(dist, pathname === '/' ? 'index.html' : pathname);
  if (!existsSync(file) && !extname(pathname)) file = join(dist, 'index.html');
  if (!existsSync(file)) { res.statusCode = 404; return res.end(); }
  res.setHeader('Content-Type', ({ '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml' })[extname(file)] || 'application/octet-stream');
  res.end(readFileSync(file));
});

(async () => {
  let buildLog = '';
  const build = spawn('pnpm', ['exec', 'vite', 'build', '--outDir', dist], { cwd: resolve(__dirname, '../storefront'), env: process.env });
  build.stdout.on('data', d => buildLog += d);
  build.stderr.on('data', d => buildLog += d);
  const code = await new Promise(resolve => build.on('exit', resolve));
  writeFileSync(join(output, 'build.log'), buildLog);
  assert.equal(code, 0, buildLog);
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const base = 'http://127.0.0.1:' + server.address().port;
  const browser = await chromium.launch({ headless: true, ...(process.env.CHROME_PATH ? { executablePath: process.env.CHROME_PATH } : {}) });
  const results = [];
  try {
    for (const width of [1440, 375]) {
      reset();
      const context = await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: 'reduce' });
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', error => errors.push(error.message));
      const section = page.locator('.home-recommendations');
      const cards = section.locator('.product-card');
      const mainCards = page.locator('.home-content > .product-grid .product-card');
      await page.goto(base);
      await expect(cards).toHaveCount(8);
      await expect(mainCards).toHaveCount(20);
      await expect(section).toHaveAttribute('aria-busy', 'false');
      await expect(cards.first()).toContainText('精选商品1');
      assert(state.requests.some(r => r.preview), 'homepage did not independently request recommended products');
      const columns = await section.locator('.product-grid').evaluate(el => getComputedStyle(el).gridTemplateColumns.split(' ').length);
      assert(columns >= 2, 'recommendations did not use responsive columns');
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'horizontal overflow');
      const more = section.getByRole('link', { name: '查看全部', exact: true });
      assert((await more.boundingBox()).height >= 44, 'view-all touch target too small');
      await page.screenshot({ path: join(output, `recommendations-${width}.png`) });
      await more.focus();
      await page.keyboard.press('Enter');
      await expect(page).toHaveURL(/recommend_only=1/);
      await expect(section).toHaveCount(0);
      await expect(mainCards).toHaveCount(10);
      await expect(page.locator('.catalog-title')).toHaveText('推荐商品');

      await page.goto(base + '/?category_id=1');
      await expect(mainCards).toHaveCount(20);
      await expect(section).toHaveCount(0);
      await expect(page.locator('.catalog-title')).toHaveText('测试分类');
      await page.goto(base + '/?keyword=' + encodeURIComponent('普通商品22'));
      await expect(mainCards).toHaveCount(1);
      await expect(section).toHaveCount(0);
      if (width > 767) {
        await page.goto(base + '/?page=2');
        await expect(mainCards).toHaveCount(2);
        await expect(section).toHaveCount(0);
      }

      await page.goto(base);
      await expect(cards).toHaveCount(8);
      await cards.first().click();
      await expect(page.locator('.pd-name')).toHaveText('精选商品1');
      state.recommended = [{ ...recommended[1], name: '更新后的推荐商品' }];
      await page.goBack();
      await expect(cards).toHaveCount(1);
      await expect(cards.first()).toContainText('更新后的推荐商品');

      // Focus recovery refreshes the preview independently from ordinary pagination.
      state.fail = true;
      await page.evaluate(() => { const now = Date.now; Date.now = () => now() + 61_000; window.dispatchEvent(new Event('focus')); });
      await expect(section.getByRole('alert')).toContainText('推荐商品加载失败');
      await expect(cards).toHaveCount(1);
      state.fail = false;
      state.recommended = [];
      await section.getByRole('button', { name: '重试', exact: true }).click();
      await expect(section).toHaveCount(0);
      await expect(mainCards).toHaveCount(20);
      assert.equal(errors.length, 0, errors.join('\n'));
      await context.close();
      results.push({ width, passed: true });
    }

    // A late request from a deactivated homepage cannot replace a newer preview.
    reset();
    let release;
    const gate = new Promise(resolve => { release = resolve; });
    let markDelivered;
    const delivered = new Promise(resolve => { markDelivered = resolve; });
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage();
    let held = false;
    // Interception disables browser cache coalescing, allowing the older GET to finish last.
    await page.route('**/api/v1/storefront/products?**', async route => {
      const params = new URL(route.request().url()).searchParams;
      if (!held && params.get('recommend_only') === 'true' && params.get('page_size') === '8') {
        held = true;
        await gate;
        await route.fulfill({ json: { items: recommended.slice(0, 8), total: recommended.length, page: 1, page_size: 8 } });
        markDelivered();
        return;
      }
      return route.continue();
    });
    await page.goto(base);
    const section = page.locator('.home-recommendations');
    await expect(section).toHaveAttribute('aria-busy', 'true');
    await expect(page.locator('.home-content > .product-grid .product-card')).toHaveCount(20);
    await page.locator('.home-content > .product-grid .product-card').first().click();
    await expect(page.locator('.pd-name')).toHaveText('普通商品1');
    state.recommended = [{ ...recommended[1], name: '最新推荐' }];
    await page.goBack();
    await expect(section.locator('.product-card')).toHaveCount(1).catch(async error => {
      writeFileSync(join(output, 'late-request-failure.html'), await page.content());
      console.error('late request diagnostics', page.url(), JSON.stringify(state.requests));
      throw error;
    });
    await expect(section).toContainText('最新推荐');
    release();
    await delivered;
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    await expect.poll(() => page.locator('.home-recommendations .pc-name').allTextContents()).toEqual(['最新推荐']);
    await page.screenshot({ path: join(output, 'recommendations-desktop.png') });
    await context.close();
    results.push({ scenario: 'late-request', passed: true });

    reset();
    state.fail = true;
    const emptyContext = await browser.newContext({ viewport: { width: 375, height: 900 } });
    const emptyPage = await emptyContext.newPage();
    await emptyPage.goto(base);
    await expect(emptyPage.locator('.home-recommendations').getByRole('alert')).toContainText('推荐商品加载失败');
    await expect(emptyPage.locator('.home-content > .product-grid .product-card')).toHaveCount(20);
    state.fail = false;
    await emptyPage.locator('.home-recommendations').getByRole('button', { name: '重试', exact: true }).click();
    await expect(emptyPage.locator('.home-recommendations .product-card')).toHaveCount(8);
    await emptyPage.screenshot({ path: join(output, 'recommendations-mobile.png') });
    state.recommended = [];
    await emptyPage.reload();
    await expect(emptyPage.locator('.home-content > .product-grid .product-card')).toHaveCount(20);
    await expect(emptyPage.locator('.home-recommendations')).toHaveCount(0);
    await expect(emptyPage.locator('.tree-recommended')).toHaveCount(0);
    await emptyContext.close();
    results.push({ scenario: 'initial-error-retry', passed: true });

    for (const width of [1440, 375]) {
      for (const navStyle of ['list', 'grid']) {
        reset();
        state.recommended = [];
        state.navStyle = navStyle;
        const context = await browser.newContext({ viewport: { width, height: 1000 } });
        const page = await context.newPage();
        const selected = page.locator(navStyle === 'grid' ? '.chip.active' : '.tree-recommended.active:visible');
        await page.goto(base + '/?recommend_only=1');
        await expect(page.locator('.catalog-title')).toHaveText('推荐商品');
        await expect(page.locator('.empty-state')).toContainText('暂无商品');
        await expect(page.locator('.product-card')).toHaveCount(0);
        await expect(page.locator('.home-recommendations')).toHaveCount(0);
        await expect(selected).toHaveText('推荐商品');
        await expect(page).toHaveURL(/recommend_only=1/);
        assert(state.requests.every(r => r.isRecommended), 'empty recommendation link mixed in ordinary products');

        // Removing the last recommendation while this filter is open must keep its selection.
        state.recommended = [recommended[0]];
        await page.reload();
        await expect(page.locator('.product-card')).toHaveCount(1);
        state.recommended = [];
        await page.evaluate(() => { const now = Date.now; Date.now = () => now() + 61_000; window.dispatchEvent(new Event('focus')); });
        await expect(page.locator('.empty-state')).toContainText('暂无商品');
        await expect(page.locator('.product-card')).toHaveCount(0);
        await expect(page.locator('.catalog-title')).toHaveText('推荐商品');
        await expect(selected).toHaveText('推荐商品');
        await expect(page).toHaveURL(/recommend_only=1/);
        await context.close();
        results.push({ scenario: 'empty-recommendation-filter', width, navStyle, passed: true });
      }
    }
    console.log(JSON.stringify({ output, results }, null, 2));
  } finally {
    await browser.close();
    server.close();
  }
})().catch(error => { console.error(error); server.close(); process.exitCode = 1; });
