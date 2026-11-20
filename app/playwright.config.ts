// Browser tests (Chromium) against the built single binary: `make e2e`.
// Each worker starts its own server on a fresh database (e2e/support).
import { defineConfig, devices } from '@playwright/test';

const ci = !!process.env.CI;

export default defineConfig({
  testDir: 'e2e',
  outputDir: 'test-results',
  globalSetup: './e2e/support/global-setup.ts',
  fullyParallel: true,
  forbidOnly: ci,
  // A retry would hide a flaky test locally; in CI it is reported as flaky.
  retries: ci ? 1 : 0,
  // Every worker runs a server and one or more browsers.
  workers: ci ? 2 : 4,
  timeout: 45_000,
  expect: { timeout: 10_000 },
  reporter: ci
    ? [['github'], ['list'], ['html', { open: 'never' }]]
    : [['list'], ['html', { open: 'never' }]],
  use: {
    // The app is used at the hive, on a phone.
    ...devices['Pixel 7'],
    locale: 'en-US',
    trace: 'retain-on-failure'
  }
});
