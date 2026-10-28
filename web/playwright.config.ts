import { defineConfig } from '@playwright/test';

// Smoke tests run against the built console on a dedicated port, with every
// browser API stubbed, so they neither depend on a live backend nor contact a
// provider. System Chrome is used directly, so no browser download is needed.
export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  expect: { timeout: 5_000 },
  fullyParallel: false,
  reporter: 'list',
  use: {
    baseURL: 'http://127.0.0.1:4173',
    channel: 'chrome',
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'desktop',
      testIgnore: /(mobile|tablet)\.spec\.ts/,
      use: { viewport: { width: 1280, height: 900 } },
    },
    {
      // Below 1120px the workspace is one column; a tablet is the widest
      // layout on that path and still has room for the full header.
      name: 'tablet',
      testMatch: /tablet\.spec\.ts/,
      use: {
        viewport: { width: 768, height: 1024 },
        deviceScaleFactor: 2,
        hasTouch: true,
      },
    },
    {
      name: 'mobile',
      testMatch: /mobile\.spec\.ts/,
      use: {
        viewport: { width: 390, height: 844 },
        deviceScaleFactor: 2,
        isMobile: true,
        hasTouch: true,
      },
    },
    {
      // The narrowest layout the console claims to support; the relay error
      // state is tightest here.
      name: 'mobile-small',
      testMatch: /mobile\.spec\.ts/,
      use: {
        viewport: { width: 320, height: 740 },
        deviceScaleFactor: 2,
        isMobile: true,
        hasTouch: true,
      },
    },
  ],
  webServer: {
    command: 'npm run build && npm run preview -- --host 127.0.0.1 --port 4173 --strictPort',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: true,
    timeout: 120_000,
  },
});
