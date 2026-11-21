import type { Page } from '@playwright/test';
import { expect, test } from './support/fixtures';
import { expectSynced } from './support/app';

const DEMO_ON = { DEMO: 'true' };

/** From the sign-in page into the demo. */
async function enterDemo(page: Page) {
  await page.goto('/');
  await expect(page).toHaveURL(/\/login$/);
  await expect(
    page.getByText(
      'No account needed: look around with sample data. The demo is read-only, so changes are not saved.'
    )
  ).toBeVisible();
  await page.getByRole('button', { name: 'Try the demo' }).click();
  await expect(page.getByText(/exploring the Openbeehive demo\. It is read-only/)).toBeVisible();
  await expectSynced(page);
}

/**
 * On the tasks page: adds a task although Add is off in the demo (as a change
 * made some other way would be) and returns the server's answer to the upload.
 */
async function tryToAddTask(page: Page, title: string) {
  await page.getByRole('navigation').getByRole('link', { name: 'Tasks' }).click();
  const upload = page.waitForResponse((r) => r.url().endsWith('/openbeehive.v1.SyncService/Push'));
  await page.getByLabel('New task').fill(title);
  await expect(page.getByRole('button', { name: 'Add' })).toBeDisabled();
  await page.locator('form.adder').evaluate((form: HTMLFormElement) => form.requestSubmit());
  return (await upload).status();
}

test('"Try the demo" opens the sample data; a refused change does not stay queued', async ({
  startServer,
  apiFor,
  device
}) => {
  // Two complete downloads of the demo workspace (before and after a reload).
  test.slow();
  const server = await startServer(DEMO_ON);
  const { page } = await device({ server });
  await enterDemo(page);
  await expect(page.getByRole('main').getByText('Hives', { exact: true }).locator('..')).toHaveText(
    /^15\s*Hives$/
  );

  expect(await tryToAddTask(page, 'Change the demo')).toBe(403);
  // The refused upload is given up, not retried for ever.
  await expectSynced(page);
  await expect(page.getByRole('button', { name: /Changes waiting|Sync failed/ })).toHaveCount(0);
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

// Push answers a change made in the demo with 403 ("the demo account is
// read-only"). The visitor is told, and the change is undone on screen.
test('the demo says clearly that a change was refused', async ({ startServer, device }) => {
  test.slow();
  const server = await startServer(DEMO_ON);
  const { page } = await device({ server });
  await enterDemo(page);
  expect(await tryToAddTask(page, 'Change the demo')).toBe(403);
  await expect(page.getByRole('alert').filter({ hasText: /demo/i })).toBeVisible({ timeout: 5000 });
  await expect(page.getByText('Change the demo')).toHaveCount(0);
  await expectSynced(page);
});

// Forms say up front that the demo cannot save them, and Save is off.
test('in the demo, the visit form cannot be saved and says so', async ({ startServer, device }) => {
  test.slow();
  const server = await startServer(DEMO_ON);
  const { page } = await device({ server });
  await enterDemo(page);
  await page.getByRole('navigation').getByRole('link', { name: 'Hives' }).click();
  await page.getByRole('listitem').first().getByRole('link').click();
  await page.getByRole('button', { name: 'Record visit' }).first().click();
  await expect(
    page.getByText('This is the read-only demo: this form cannot be saved.').first()
  ).toBeVisible();
  await expect(page.getByRole('button', { name: 'Save' })).toBeDisabled();

  // So are the forms for a new apiary, a new hive and a task.
  await page.getByRole('navigation').getByRole('link', { name: 'Apiaries' }).click();
  await page.getByRole('button', { name: '+ New' }).click();
  await page.getByLabel('Name').fill('Not saved');
  await expect(page.getByRole('button', { name: 'Save' })).toBeDisabled();
  await page.getByRole('navigation').getByRole('link', { name: 'Hives' }).click();
  await page.getByRole('button', { name: '+ New' }).click();
  await page.getByRole('textbox', { name: 'Name' }).fill('Not saved');
  await expect(page.getByRole('button', { name: 'Save' })).toBeDisabled();
  await page.getByRole('navigation').getByRole('link', { name: 'Tasks' }).click();
  await page.getByLabel('New task').fill('Not saved');
  await expect(page.getByRole('button', { name: 'Add' })).toBeDisabled();
  await expect(
    page.getByText('This is the read-only demo: this form cannot be saved.')
  ).toBeVisible();
});
