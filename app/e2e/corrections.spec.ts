import { expect, test } from './support/fixtures';
import { acceptDialogs, expectSynced } from './support/app';

test('an edited treatment is marked as amended', async ({ api, device }) => {
  const account = await api.signUp();
  const as = api.as(account);
  const apiary = await as.createApiary('Orchard Meadow');
  const hive = await as.createHive(apiary, 'Hive 7');
  await as.createTreatment(hive, 'Oxuvar', { dose: '50 ml', method: 'trickling' });

  const { page } = await device({ session: account });
  await page.goto(`/hives/${hive}`);
  const card = page.getByRole('listitem').filter({ hasText: 'Oxuvar' });
  await expect(card).toContainText('Dose: 50 ml');
  await expect(card).not.toContainText('Amended on');

  await card.getByRole('button', { name: /^Edit: Oxuvar/ }).click();
  const form = page.locator('form').filter({ has: page.getByLabel('Product') });
  await expect(form.getByLabel('Product')).toHaveValue('Oxuvar');
  await form.getByLabel('Dose').fill('30 ml');
  await form.getByRole('button', { name: 'Save' }).click();

  await expect(card).toContainText('Dose: 30 ml');
  await expect(card).toContainText(/Amended on \S+/);
  await expectSynced(page);
  const [stored] = await as.listTreatments(hive);
  expect(stored).toMatchObject({ dose: '30 ml', product: 'Oxuvar' });
  expect(stored?.amendedAt).toBeTruthy();
});

test('a deleted harvest no longer counts towards the honey totals', async ({ api, device }) => {
  const account = await api.signUp();
  const as = api.as(account);
  const apiary = await as.createApiary('Orchard Meadow');
  const hive = await as.createHive(apiary, 'Hive 7');

  // A British browser: the account has chosen no units, so they are metric.
  const { page } = await device({ session: account, context: { locale: 'en-GB' } });
  const asked = acceptDialogs(page);
  await page.goto(`/hives/${hive}`);
  await expect(page.getByRole('heading', { name: 'Hive 7' })).toBeVisible();

  for (const [amount, variety] of [
    ['12', 'spring blossom'],
    ['8', 'forest']
  ] as const) {
    await page.getByRole('button', { name: 'Record harvest' }).click();
    const form = page.locator('form').filter({ has: page.getByLabel('Amount (kg)') });
    await form.getByLabel('Amount (kg)').fill(amount);
    await form.getByLabel('Variety').fill(variety);
    await form.getByRole('button', { name: 'Save' }).click();
    await expect(page.getByRole('listitem').filter({ hasText: variety })).toBeVisible();
  }
  await expect(page.getByRole('heading', { name: 'Honey · 20 kg' })).toBeVisible();

  // The season total on the overview is summed from the timeline events.
  const season = page.getByText('Honey this season', { exact: true }).locator('..');
  await page.getByRole('navigation').getByRole('link', { name: 'Overview' }).click();
  await expect(season).toHaveText(/^\s*20\s*kg/);

  await page.goBack();
  await page
    .getByRole('listitem')
    .filter({ hasText: 'forest' })
    .getByRole('button', { name: /^Delete: 8 kg/ })
    .click();
  expect(asked.at(-1)).toMatch(/^Delete the harvest of 8 kg from /);
  await expect(page.getByRole('heading', { name: 'Honey · 12 kg' })).toBeVisible();
  await expect(page.getByRole('listitem').filter({ hasText: 'forest' })).toHaveCount(0);

  await page.getByRole('navigation').getByRole('link', { name: 'Overview' }).click();
  await expect(season).toHaveText(/^\s*12\s*kg/);

  await expectSynced(page);
  const stored = await as.listHarvests(hive);
  expect(stored.map((h) => h.amountKg)).toEqual([12]);
  const honey = await as.rpc<{ perYear?: { kg: number }[] }>('StatsService/GetHoneyStats', {});
  expect(honey.perYear?.map((y) => y.kg)).toEqual([12]);
});
