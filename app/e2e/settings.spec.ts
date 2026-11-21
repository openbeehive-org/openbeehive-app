// Settings: split into groups, with the right words for the server's mode.
import { expect, test } from './support/fixtures';
import { expectSynced } from './support/app';

const PHONE = { viewport: { width: 360, height: 740 } };

test('settings are split into groups, with deleting last in each', async ({ api, device }) => {
  const account = await api.signUp();
  const { page } = await device({ session: account, context: PHONE });
  await page.goto('/settings');
  await expectSynced(page);
  const groups = page.getByRole('navigation', { name: 'Settings sections' });
  await expect(groups.getByRole('link')).toHaveText([
    'Preferences',
    'Workspace',
    'Data & sync',
    'Account'
  ]);
  await expect(page.getByRole('heading', { name: 'Language' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Delete account' })).toHaveCount(0);

  await groups.getByRole('link', { name: 'Workspace' }).click();
  await expect(page.locator('.st-card').last()).toContainText('Danger zone');
  await groups.getByRole('link', { name: 'Account' }).click();
  await expect(page.locator('.st-card').last()).toContainText('Delete account');
  await expect(page.getByRole('button', { name: 'Sign out', exact: true })).toBeVisible();
  // A page of a few screens, not one of twenty.
  const height = await page.evaluate(() => document.documentElement.scrollHeight);
  expect(height).toBeLessThan(3200);
});

// A server without sign-in has no accounts: Settings does not talk about one.
test('without a login, Settings has no account and no API keys', async ({
  startServer,
  device
}) => {
  const server = await startServer({ PASSWORD_AUTH: 'false', ADMIN_EMAIL: '', ADMIN_PASSWORD: '' });
  const { page } = await device({ server, context: PHONE });
  await page.goto('/settings');
  await expectSynced(page);
  await expect(page.getByRole('heading', { name: 'No sign-in' })).toBeVisible();
  await expect(page.getByText(/saved with your account/)).toHaveCount(0);
  await expect(
    page.getByRole('navigation', { name: 'Settings sections' }).getByRole('link')
  ).toHaveText(['Preferences', 'Data & sync']);
  await page.getByRole('link', { name: 'Data & sync' }).click();
  await expect(page.getByRole('heading', { name: 'Download from the server' })).toBeVisible();
  await expect(page.getByText(/API key/)).toHaveCount(0);
  await expect(page.getByText('local@openbeehive')).toHaveCount(0);
});
