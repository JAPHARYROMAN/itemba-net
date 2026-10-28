import { expect, test, type Page } from '@playwright/test';

// Tablets sit between the two layouts the console has already been verified
// at: the workspace is a single column here, but the header is still sticky
// and the search form is not yet phone-tight.
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
  await page.route('**/api/status', (route) => route.fulfill({ json: {} }));
  await page.route('**/api/scout/discover**', (route) => route.fulfill({ json: DISCOVERY }));
  await page.route('**/api/formats', (route) =>
    route.fulfill({ json: { host: { converters: [], hls: true, browser: false }, formats: [] } }),
  );
  await page.route('**/api/vault/**', (route) => route.fulfill({ status: 503, body: '' }));
};

const box = (page: Page, selector: string) =>
  page.locator(selector).first().evaluate((el) => {
    const r = el.getBoundingClientRect();
    return { left: r.left, top: r.top, right: r.right, bottom: r.bottom };
  });

test.beforeEach(async ({ page }) => {
  await stubConsole(page);
});

test('stacks the workspace into one column without sideways scrolling', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: /Test Pattern Feature/ })).toBeVisible();

  const { inner, doc } = await page.evaluate(() => ({
    inner: window.innerWidth,
    doc: document.documentElement.scrollWidth,
  }));
  expect(doc).toBe(inner);

  const columns = await page.locator('.workspace-grid').evaluate((el) => getComputedStyle(el).gridTemplateColumns);
  expect(columns.split(' ')).toHaveLength(1);

  const main = await box(page, '.main-column');
  const sidebar = await box(page, '.sidebar-column');
  expect(sidebar.top).toBeGreaterThanOrEqual(main.bottom - 1);
});

test('keeps the trust dialog inside the tablet viewport', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'Data & trust' }).click();
  await expect(page.getByRole('dialog', { name: 'Data, evidence and trust' })).toBeVisible();

  const card = await box(page, '.modal-card');
  expect(card.left).toBeGreaterThanOrEqual(0);
  expect(card.right).toBeLessThanOrEqual(768);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(768);
});
