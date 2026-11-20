import type { Page } from '@playwright/test';
import { expect, test } from './support/fixtures';
import { expectSynced } from './support/app';

const DEMO_ON = { DEMO: 'true' };

/** From the sign-in page into the demo. */
async function enterDemo(page: Page) {
  await page.goto('/');
  await expect(page).toHaveURL(/\/login$/);
  await expect(
    page.getByText('No account needed: explore with sample data. It resets every hour.')
  ).toBeVisible();
  await page.getByRole('button', { name: 'Try the demo' }).click();
  await expect(page.getByText(/exploring the Openbeehive demo/)).toBeVisible();
  await expectSynced(page);
}

/** On the tasks page: tries to add a task and returns the server's answer to the upload. */
async function tryToAddTask(page: Page, title: string) {
  await page.getByRole('navigation').getByRole('link', { name: 'Tasks' }).click();
  const upload = page.waitForResponse((r) => r.url().endsWith('/openbeehive.v1.SyncService/Push'));
  await page.getByLabel('New task').fill(title);
  await page.getByRole('button', { name: 'Add' }).click();
  return (await upload).status();
}

test('"Try the demo" opens the sample data; a refused change does not stay queued', async ({
  startServer,
  apiFor,
  device
}) => {
  const server = await startServer(DEMO_ON);
  const { page } = await device({ server });
  await enterDemo(page);
  await expect(page.getByRole('main').getByText('Hives', { exact: true }).locator('..')).toHaveText(
    /^15\s*Hives$/
  );

  expect(await tryToAddTask(page, 'Change the demo')).toBe(403);
  // The refused upload is given up, not retried for ever.
  await expectSynced(page);
  await expect(page.getByRole('button', { name: /Waiting to upload|Sync failed/ })).toHaveCount(0);
  await page.reload();
  await expectSynced(page);

  // The demo itself is unchanged, for this visitor's session and every other.
  const token = await page.evaluate(() => localStorage.getItem('session'));
  const tasks = await apiFor(server)
    .as({ token: token ?? '' })
    .rpc<{ tasks?: { title: string }[] }>('TaskService/ListTasks', {});
  expect((tasks.tasks ?? []).map((t) => t.title)).not.toContain('Change the demo');
  await page.getByRole('navigation').getByRole('link', { name: 'Settings' }).click();
  await expect(page.getByRole('heading', { name: 'Delete account' })).toHaveCount(0);
});

// BUG (app): a change made in the demo is refused by the server (Push answers
// 403 "the demo account is read-only"), but the app says nothing. The record
// stays on screen as if saved, the refused upload is dropped silently
// (lib/local/sync.ts, pushFailure "drop") and the status chip reads "Synced".
// The visitor only learns from the banner that data "resets every hour", not
// that this change was never stored.
test.fail('the demo says clearly that a change was refused', async ({ startServer, device }) => {
  const server = await startServer(DEMO_ON);
  const { page } = await device({ server });
  await enterDemo(page);
  expect(await tryToAddTask(page, 'Change the demo')).toBe(403);
  await expect(page.getByRole('alert').filter({ hasText: /demo/i })).toBeVisible({ timeout: 5000 });
});
