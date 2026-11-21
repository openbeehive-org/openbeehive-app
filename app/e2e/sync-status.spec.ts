// The sync status chip, its sheet, and the way to the refused changes.
import { expect, test } from './support/fixtures';
import { expectSynced, syncedChip } from './support/app';

const PHONE = { viewport: { width: 360, height: 740 } };

test('the chip opens a sheet with what is waiting; refused changes are one tap away', async ({
  api,
  device
}) => {
  const account = await api.signUp();
  const { page } = await device({ session: account, context: PHONE });
  await page.goto('/apiaries');
  await expectSynced(page);

  // A thumb-sized target, and tapping explains instead of acting silently.
  const box = await syncedChip(page).boundingBox();
  expect(box?.height).toBeGreaterThanOrEqual(44);
  await syncedChip(page).click();
  const sheet = page.getByRole('dialog', { name: 'Sync status' });
  await expect(sheet).toBeVisible();
  await expect(sheet).toContainText('Last synced');
  await expect(sheet).toContainText('Photos waiting to upload');
  await expect(sheet.getByRole('heading', { name: 'Sync status' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(sheet).toBeHidden();
  await expect(syncedChip(page)).toBeFocused();

  // The server refuses the next upload.
  await page.route('**/openbeehive.v1.SyncService/Push', (route) =>
    route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        rejected: [{ index: 0, code: 'invalid', reason: 'refused by the test' }],
        serverTimeMs: String(Date.now())
      })
    })
  );
  await page.goto('/apiaries?new=1');
  await page.getByLabel('Name').first().fill('Refused apiary');
  await page.getByRole('button', { name: 'Save' }).click();
  const chip = page.getByRole('button', { name: /^Sync problems: 1/ });
  await expect(chip).toBeVisible({ timeout: 20_000 });
  await page.unroute('**/openbeehive.v1.SyncService/Push');

  await chip.click();
  await expect(sheet).toContainText('The server refused 1 change.');
  await sheet.getByRole('link', { name: /Show sync problems/ }).click();
  await expect(page).toHaveURL(/\/settings#sync-problems$/);
  const problems = page.locator('#sync-problems');
  await expect(problems).toBeFocused();
  await expect(problems).toBeInViewport();
  await expect(problems.getByRole('button', { name: 'Try again' })).toBeVisible();
  // The server's English reason is not the visible text; it sits behind a
  // translated "Technical details" toggle.
  await expect(problems.locator('[title]')).toHaveCount(0);
  await expect(problems.getByText('refused by the test')).toBeHidden();
  await problems.getByText('Technical details').click();
  await expect(problems.getByText('refused by the test')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Data & sync' })).toHaveAttribute(
    'aria-current',
    'page'
  );
});
