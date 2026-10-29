import { expect, test, type Page } from '@playwright/test';

// One shelf answer for every test: a single chart title, so result chrome
// (markets, release date, the action line) renders without a backend.
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

const shelfCard = (page: Page) => page.getByRole('button', { name: /Test Pattern Feature/ });

test.beforeEach(async ({ page }) => {
  await stubConsole(page);
});

test('opens in English with the shelf rendered', async ({ page }) => {
  await page.goto('/');

  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page.locator('html')).toHaveAttribute('dir', 'ltr');
  await expect(page.getByRole('heading', { level: 2, name: 'Find full-length movies across the web.' })).toBeVisible();
  await expect(shelfCard(page)).toBeVisible();
  await expect(shelfCard(page)).toContainText('Find watch options');
  await expect(page.getByRole('combobox', { name: 'Language' })).toHaveValue('en');
});

test('switches to Arabic, mirrors the page and keeps the shelf', async ({ page }) => {
  let discoverRequests = 0;
  page.on('request', (request) => {
    if (request.url().includes('/api/scout/discover')) discoverRequests += 1;
  });

  await page.goto('/');
  await expect(shelfCard(page)).toContainText('Find watch options');
  expect(discoverRequests).toBe(1);

  await page.getByRole('combobox', { name: 'Language' }).selectOption('ar');

  await expect(page.locator('html')).toHaveAttribute('lang', 'ar');
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
  await expect(page.getByRole('heading', { level: 2, name: 'اعثر على أفلام كاملة عبر الويب.' })).toBeVisible();
  await expect(page.getByRole('combobox', { name: 'اللغة' })).toHaveValue('ar');
  await expect(page.getByRole('combobox', { name: 'تفضيل المنطقة (اختياري)' })).toBeVisible();
  await expect(shelfCard(page)).toContainText('اعثر على خيارات المشاهدة');

  // A language switch re-renders the shelf; it must not restart discovery.
  expect(discoverRequests).toBe(1);
});

test('remembers the chosen locale across a reload', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('combobox', { name: 'Language' }).selectOption('ar');
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');

  await page.reload();

  await expect(page.locator('html')).toHaveAttribute('lang', 'ar');
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
  await expect(page.getByRole('combobox', { name: 'اللغة' })).toHaveValue('ar');
});

test('switches to Kiswahili and returns to left-to-right', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('combobox', { name: 'Language' }).selectOption('ar');
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');

  await page.getByRole('combobox', { name: 'اللغة' }).selectOption('sw');

  await expect(page.locator('html')).toHaveAttribute('lang', 'sw');
  await expect(page.locator('html')).toHaveAttribute('dir', 'ltr');
  await expect(page.getByRole('heading', { level: 2, name: 'Pata filamu kamili kote mtandaoni.' })).toBeVisible();
  await expect(shelfCard(page)).toContainText('Pata chaguo za kutazama');
});

test('offers a skip link that moves focus past the header', async ({ page }) => {
  await page.goto('/');

  await page.keyboard.press('Tab');
  const skip = page.getByRole('link', { name: 'Skip to main content' });
  await expect(skip).toBeFocused();

  await page.keyboard.press('Enter');

  await expect(page.locator('#main-content')).toBeFocused();
  await expect(page).toHaveURL(/#main-content$/);
});

test('sets no cookies of its own', async ({ page, context }) => {
  await page.goto('/');
  await expect(shelfCard(page)).toBeVisible();

  expect(await context.cookies()).toEqual([]);
  expect(await page.evaluate(() => document.cookie)).toBe('');
});

test('discloses the two browser-storage keys in the trust dialog', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'Data & trust' }).click();

  const dialog = page.getByRole('dialog', { name: 'Data, evidence and trust' });
  await expect(dialog.getByRole('heading', { name: 'What stays in your browser' })).toBeVisible();
  await expect(dialog).toContainText('itemba.locale');
  await expect(dialog).toContainText('itemba.player.prefs');
});

test('keeps the console alive when a lazy dialog fails to load', async ({ page }) => {
  await page.route(/\/assets\/TrustDataModal-[^/]+\.js$/, (route) => route.abort());
  await page.goto('/');
  await page.getByRole('button', { name: 'Data & trust' }).click();

  const failure = page.getByRole('alert').filter({ hasText: 'This panel did not load' });
  await expect(failure).toBeVisible();
  await expect(failure.getByRole('button', { name: 'Reload console' })).toBeFocused();
  await expect(shelfCard(page)).toBeVisible();

  await failure.getByRole('button', { name: 'Close' }).click();
  await expect(failure).toBeHidden();
  await expect(page.getByRole('button', { name: 'Data & trust' })).toBeFocused();
});

test('stops the page scrolling behind an open dialog', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'Data & trust' }).click();
  await expect(page.getByRole('dialog', { name: 'Data, evidence and trust' })).toBeVisible();
  await expect(page.locator('body')).toHaveCSS('overflow-y', 'hidden');

  await page.keyboard.press('Escape');
  await expect(page.locator('body')).toHaveCSS('overflow-y', 'auto');
});

test('keeps the retry countdown out of the socket live region', async ({ page }) => {
  // Closing every attempt puts the badge on its retry countdown for certain.
  await page.routeWebSocket(/\/api\/ws$/, (ws) => ws.close());
  await page.goto('/');
  const badge = page.locator('.ws-badge');
  const visible = badge.locator('[aria-hidden="true"]').last();
  const spoken = badge.locator('.visually-hidden');

  await expect(visible).toContainText(/Reconnecting in \d+s/);
  expect(await spoken.textContent()).toBe('Console: Reconnecting…');
});

test('closes the data and trust dialog with Escape and restores focus', async ({ page }) => {
  await page.goto('/');
  const trigger = page.getByRole('button', { name: 'Data & trust' });
  await trigger.click();

  const dialog = page.getByRole('dialog', { name: 'Data, evidence and trust' });
  await expect(dialog).toBeVisible();
  await expect(page.getByRole('button', { name: 'Close data and trust' })).toBeFocused();

  await page.keyboard.press('Escape');

  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
});

test('traps focus in the topology dialog and restores it on Escape', async ({ page }) => {
  await page.goto('/');
  const trigger = page.getByRole('button', { name: 'Architecture Topology' });
  await trigger.click();

  const dialog = page.getByRole('dialog', { name: /Interactive Isolation Topology/ });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('button', { name: /Close/ })).toBeFocused();

  // Shift+Tab from the first control wraps to the last, not out of the page.
  await page.keyboard.press('Shift+Tab');
  await expect(dialog.getByRole('button', { name: /STAGE 05/ })).toBeFocused();

  await dialog.getByRole('button', { name: /Layer 1/ }).click();
  await expect(dialog).toContainText('Container / Socket Boundary');

  await page.keyboard.press('Escape');

  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
});
