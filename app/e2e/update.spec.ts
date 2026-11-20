import type { Page, TestInfo } from '@playwright/test';
import type { Api } from './support/api';
import { expect, test, type Device, type DeviceOptions } from './support/fixtures';
import { expectInstalled, expectSynced } from './support/app';
import { writeNextAppVersion, type Server } from './support/server';

const prompt = (page: Page) => page.getByText('A new version is available.');

/**
 * Opens the app, then deploys a newer build of it on the same server and
 * lets the browser find it. Returns the page, still running the old build
 * with the prompt showing, and the new build's version.
 */
async function openThenDeploy(
  fixtures: {
    startServer: () => Promise<Server>;
    apiFor: (server: Server) => Api;
    device: (options?: DeviceOptions) => Promise<Device>;
  },
  testInfo: TestInfo
) {
  const server = await fixtures.startServer();
  const account = await fixtures.apiFor(server).signUp();
  const { page } = await fixtures.device({ session: account, server });
  await page.goto('/');
  await expectSynced(page);
  await expectInstalled(page);
  await expect(prompt(page)).toBeHidden();
  const [installedCache] = await page.evaluate(() => caches.keys());

  const nextBuild = testInfo.outputPath('next-web');
  const version = writeNextAppVersion(nextBuild);
  await server.stop();
  await server.start({ WEB_DIR: nextBuild });

  // What the browser does by itself now and then: look for a new worker.
  await page.evaluate(async () => (await navigator.serviceWorker.ready).update());
  await expect(prompt(page)).toBeVisible();
  return { page, version, installedCache };
}

test('a new app version waits behind a prompt while the open page keeps working', async ({
  startServer,
  apiFor,
  device
}, testInfo) => {
  const { page, version, installedCache } = await openThenDeploy(
    { startServer, apiFor, device },
    testInfo
  );

  // Nothing was swapped under the open page: it still runs on its own files
  // and goes on working, including routes it had not loaded before.
  expect(await page.evaluate(() => caches.keys())).toContain(installedCache);
  await page.getByRole('navigation').getByRole('link', { name: 'Tasks' }).click();
  await page.getByLabel('New task').fill('Order new frames');
  await page.getByRole('button', { name: 'Add' }).click();
  await expect(page.getByText('Order new frames')).toBeVisible();
  await expectSynced(page);
  await expect(prompt(page)).toBeVisible();

  await page.getByRole('button', { name: 'Reload' }).click();
  // Only the new build's files are left once the page runs it.
  await expect
    .poll(() => page.evaluate(() => caches.keys()).catch(() => []))
    .toEqual([`obh-shell-${version}`]);
  await expect(prompt(page)).toBeHidden();
  await expect(page.getByText('Order new frames')).toBeVisible();
  await expectSynced(page);
});

// BUG (app, lib/components/UpdatePrompt.svelte): "Reload" and "Later" in the
// update banner are 36 px tall (min-height: 36px), below the 44 px the app
// keeps for buttons everywhere else.
test.fail(
  'the buttons of the update prompt are at least 44 px tall',
  async ({ startServer, apiFor, device }, testInfo) => {
    const { page } = await openThenDeploy({ startServer, apiFor, device }, testInfo);
    for (const name of ['Reload', 'Later']) {
      const box = await page.getByRole('button', { name }).boundingBox();
      expect.soft(box?.height, name).toBeGreaterThanOrEqual(44);
    }
  }
);
