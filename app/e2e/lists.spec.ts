// The dashboard's due list and the hive list it leads to.
import { expect, test } from './support/fixtures';
import { expectSynced } from './support/app';

const DAY_MS = 24 * 60 * 60 * 1000;

test('only hives past the due interval are due; "see all" opens the list sorted by last visit', async ({
  api,
  device
}) => {
  const account = await api.signUp();
  const as = api.as(account);
  const apiary = await as.createApiary('Orchard');
  const fresh = await as.createHive(apiary, 'Visited today');
  await as.createInspection(fresh, { date: new Date().toISOString() });
  const old = await as.createHive(apiary, 'Visited long ago');
  await as.createInspection(old, { date: new Date(Date.now() - 20 * DAY_MS).toISOString() });
  for (let i = 1; i <= 5; i++) await as.createHive(apiary, `New ${i}`);

  const { page } = await device({ session: account });
  await page.goto('/');
  await expectSynced(page);
  const due = page.getByRole('region', { name: 'Due inspections' });
  await expect(due.getByRole('link', { name: /New 1/ })).toBeVisible();
  await expect(due.getByRole('link', { name: /Visited today/ })).toHaveCount(0);

  await due.getByRole('link', { name: 'See all 6 due hives' }).click();
  await expect(page).toHaveURL(/\/hives\?sort=inspected$/);
  await expect(page.getByLabel('Sort')).toHaveValue('inspected');
  await expect(page.getByRole('listitem').last()).toContainText('Visited today');
});

test('the hive list keeps its search, filter and sort after opening a hive', async ({
  api,
  device
}) => {
  const account = await api.signUp();
  const as = api.as(account);
  const apiary = await as.createApiary('Orchard');
  for (const name of ['Alpha', 'Beta', 'Gamma']) await as.createHive(apiary, name);

  const { page } = await device({ session: account });
  await page.goto('/hives');
  await expectSynced(page);
  // Typing keeps the field focused while the URL follows along.
  await page.getByLabel('Search').pressSequentially('beta');
  await expect(page.getByLabel('Search')).toBeFocused();
  await page.getByLabel('Sort').selectOption('apiary');
  await expect(page).toHaveURL(/q=beta/);
  await page.getByRole('link', { name: /Beta/ }).click();
  await expect(page.getByRole('heading', { name: 'Beta' })).toBeVisible();

  await page.goBack();
  await expect(page.getByLabel('Search')).toHaveValue('beta');
  await expect(page.getByLabel('Sort')).toHaveValue('apiary');
  await expect(page.getByRole('link', { name: /Alpha/ })).toHaveCount(0);

  // The "Hives" tab opens the whole list again.
  await page.getByRole('navigation').getByRole('link', { name: 'Hives' }).click();
  await expect(page).toHaveURL(/\/hives$/);
  await expect(page.getByLabel('Search')).toHaveValue('');
  await expect(page.getByLabel('Sort')).toHaveValue('name');
  await expect(page.getByRole('link', { name: /Alpha/ })).toBeVisible();
});
