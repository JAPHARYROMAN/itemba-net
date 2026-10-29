import { expect, test, type Page } from '@playwright/test';

// A vault that is already open with one observed stream, so the protected
// browser and the relay player both render without a backend.
const VAULT = {
  active: true,
  sessionId: 'mobile-session',
  mode: 'rbi',
  currentUrl: 'https://example.org/very/long/path/that/keeps/going/forever/and/ever',
  pageTitle: 'A long protected page title that should be truncated on a narrow screen',
  theaterActive: false,
  webrtcState: 'connected',
  framesStreamed: 0,
  uptimeSeconds: 12,
  extractedStreams: [
    { url: 'https://cdn.example.org/movie/master.m3u8', format: 'HLS', resolution: '1920x1080' },
  ],
  adBlockingEnabled: true,
};

const DISCOVERY = {
  mode: 'popular',
  region: 'ALL',
  checkedAt: '2026-10-28T12:29:00Z',
  cached: true,
  detail: '',
  providers: [{ name: 'Apple movie charts', status: 'ok', detail: '' }],
  items: [
    {
      id: 'test-pattern-feature',
      title: 'Test Pattern Feature',
      query: 'Test Pattern Feature 2018',
      sourceUrl: 'https://tv.apple.com/us/movie/test-pattern-feature/umc.cmc.test',
      sourceName: 'Apple movie charts',
      markets: ['US'],
      releaseDate: '2018-09-20',
    },
  ],
};

const stubConsole = async (page: Page) => {
  await page.route('**/api/status', (route) => route.fulfill({ json: { vault: VAULT } }));
  await page.route('**/api/scout/discover**', (route) => route.fulfill({ json: DISCOVERY }));
  await page.route('**/api/formats', (route) =>
    route.fulfill({ json: { host: { converters: [], hls: true, browser: false }, formats: [] } }),
  );
  await page.route('**/api/vault/**', (route) => route.fulfill({ status: 503, body: '' }));
};

// The failure that matters on a phone is content escaping the scrollport, not
// an exact pixel measurement: compare the document with the space it has.
const pageFits = (page: Page) =>
  page.evaluate(() => ({
    client: document.documentElement.clientWidth,
    doc: document.documentElement.scrollWidth,
  }));

const rect = (page: Page, selector: string) =>
  page.locator(selector).first().evaluate((el) => {
    const r = el.getBoundingClientRect();
    return { left: r.left, top: r.top, right: r.right, bottom: r.bottom };
  });

test.beforeEach(async ({ page }) => {
  await stubConsole(page);
});

test('keeps the protected browser chrome inside the stage', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'Protected browser' }).click();

  const stage = await rect(page, '.viewport-stage');
  const chrome = await rect(page, '.rbi-chrome');
  const title = await rect(page, '.rbi-title strong');

  expect(chrome.top).toBeGreaterThanOrEqual(stage.top - 1);
  expect(chrome.bottom).toBeLessThanOrEqual(stage.bottom + 1);
  expect(title.left).toBeGreaterThanOrEqual(stage.left - 1);
  expect(title.right).toBeLessThanOrEqual(stage.right + 1);

  const fit = await pageFits(page);
  expect(fit.doc).toBeLessThanOrEqual(fit.client);
});

test('keeps both launch actions reachable with no sandbox open', async ({ page }) => {
  // Registered after the shared stub, so it wins for this test only.
  await page.route('**/api/status', (route) => route.fulfill({ json: { vault: { ...VAULT, active: false } } }));
  await page.goto('/');

  const archive = page.getByRole('button', { name: /Isolate Internet Archive Player/ });
  const testbench = page.getByRole('button', { name: /Adversarial Ad-Trap Testbench/ });
  await expect(archive).toBeVisible();
  await expect(testbench).toBeVisible();

  const stage = await rect(page, '.viewport-stage');
  for (const button of [archive, testbench]) {
    const bounds = await button.evaluate((el) => {
      const r = el.getBoundingClientRect();
      return { top: r.top, bottom: r.bottom };
    });
    expect(bounds.top).toBeGreaterThanOrEqual(stage.top - 1);
    expect(bounds.bottom).toBeLessThanOrEqual(stage.bottom + 1);
  }
});

test('switches to Arabic on a phone without widening the page', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('combobox', { name: 'Language' }).selectOption('ar');

  await expect(page.locator('html')).toHaveAttribute('lang', 'ar');
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
  await expect(page.getByRole('heading', { level: 2, name: 'اعثر على أفلام كاملة عبر الويب.' })).toBeVisible();
  await expect(page.getByRole('button', { name: /متصفح محمي/ })).toBeVisible();

  const fit = await pageFits(page);
  expect(fit.doc).toBeLessThanOrEqual(fit.client);
});

test('replaces the transport with a readable relay failure', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: /Try in player/ }).first().click();

  const retry = page.getByRole('button', { name: /Retry with ad filtering/ });
  await expect(retry).toBeVisible({ timeout: 10_000 });

  // The control bar would wrap under the error and cover its retry action.
  await expect(page.locator('.mp-controls')).toHaveCount(0);

  const stage = await rect(page, '.media-stage-view');
  const action = await retry.evaluate((el) => {
    const r = el.getBoundingClientRect();
    return { top: r.top, bottom: r.bottom };
  });
  expect(action.top).toBeGreaterThanOrEqual(stage.top - 1);
  expect(action.bottom).toBeLessThanOrEqual(stage.bottom + 1);

  const fit = await pageFits(page);
  expect(fit.doc).toBeLessThanOrEqual(fit.client);
});

test('keeps the console usable when the player panel fails to load', async ({ page }) => {
  await page.route(/\/assets\/MediaStage-[^/]+\.js$/, (route) => route.abort());
  await page.goto('/');
  await page.getByRole('button', { name: /Try in player/ }).first().click();

  const failure = page.getByRole('alert').filter({ hasText: 'This panel did not load' });
  await expect(failure).toBeVisible();
  await expect(failure.getByRole('button', { name: 'Reload console' })).toBeFocused();
  await expect(page.getByRole('heading', { level: 2, name: 'Find full-length movies across the web.' })).toBeVisible();
});

test('fetches the HLS runtime only for an HLS source', async ({ page }) => {
  const hlsChunks: string[] = [];
  page.on('request', (request) => {
    if (/\/assets\/hls-[^/]+\.js$/.test(new URL(request.url()).pathname)) hlsChunks.push(request.url());
  });

  await page.goto('/');
  await page.getByRole('button', { name: 'Media player' }).click();
  await expect(page.getByText('Protected media player')).toBeVisible();
  expect(hlsChunks).toHaveLength(0);

  // The observed stream is an .m3u8 playlist, so playing it needs hls.js.
  await page.getByRole('button', { name: /Try in player/ }).first().click();
  await expect(page.getByRole('button', { name: /Retry with ad filtering/ })).toBeVisible({ timeout: 10_000 });
  expect(hlsChunks.length).toBeGreaterThan(0);
});
