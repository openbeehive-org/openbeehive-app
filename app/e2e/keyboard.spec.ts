// Getting around without a mouse, and the navigation at phone width.
import { expect, test } from './support/fixtures';
import { expectSynced } from './support/app';

test('a skip link leads past the navigation, and an opened form takes the focus', async ({
  api,
  device
}) => {
  const account = await api.signUp();
  const as = api.as(account);
  const hive = await as.createHive(await as.createApiary('Orchard'), 'Hive 7');

  const { page } = await device({ session: account });
  await page.goto(`/hives/${hive}`);
  await expect(page.getByRole('heading', { name: 'Hive 7' })).toBeVisible();
  await expectSynced(page);

  await page.keyboard.press('Tab');
  const skip = page.getByRole('link', { name: 'Skip to content' });
  await expect(skip).toBeFocused();
  await expect(skip).toBeInViewport();
  await page.keyboard.press('Enter');
  await expect(page.locator('main')).toBeFocused();

  await page.getByRole('button', { name: 'Record visit' }).first().click();
  await expect(page.getByRole('heading', { name: 'Record visit' })).toBeFocused();
});
