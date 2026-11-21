import { expect, test } from './support/fixtures';
import { expectSynced, syncNowFromChip } from './support/app';

const visitForm = (page: import('@playwright/test').Page, heading: string) =>
  page.locator('form').filter({ has: page.getByRole('heading', { name: heading }) });

test('a visit entered in pounds and Fahrenheit is stored metric, and an unchanged save keeps it', async ({
  api,
  device
}) => {
  const account = await api.signUp();
  const as = api.as(account);
  const hive = await as.createHive(await as.createApiary('Orchard Meadow'), 'Hive 7');

  // An American browser and no choice on the account: pounds and Fahrenheit.
  const { page } = await device({ session: account, context: { locale: 'en-US' } });
  await page.goto(`/hives/${hive}`);
  await page.getByRole('button', { name: 'Record visit' }).first().click();
  const form = visitForm(page, 'Record visit');
  await form.getByLabel('Hive weight (lb)').fill('80');
  await form.getByText('Climate', { exact: true }).click();
  await form.getByLabel('Hive temperature (°F)').fill('95');
  await form.getByRole('button', { name: 'Save' }).click();
  await expect(form).toBeHidden();
  await expect(
    page.getByRole('listitem').filter({ hasText: 'Hive temperature 95 °F' })
  ).toContainText('80 lb');
  await expectSynced(page);
  let [stored] = await as.listInspections(hive);
  expect(stored).toMatchObject({ weightKg: 36.2874, tempHive: 35 });

  // Opened again and saved without touching the numbers: nothing is
  // converted twice.
  await page
    .getByRole('button', { name: /^Edit: Visit/ })
    .first()
    .click();
  const edit = visitForm(page, 'Edit visit');
  await expect(edit.getByLabel('Hive weight (lb)')).toHaveValue('80');
  await edit.getByLabel('Note').fill('weighed with the hanging scale');
  await edit.getByRole('button', { name: 'Save' }).click();
  await expect(edit).toBeHidden();
  await expectSynced(page);
  [stored] = await as.listInspections(hive);
  expect(stored).toMatchObject({
    weightKg: 36.2874,
    tempHive: 35,
    note: 'weighed with the hanging scale'
  });
});

test('an open form keeps its units when the account switches them on another device', async ({
  api,
  device
}) => {
  test.setTimeout(90_000);
  const account = await api.signUp();
  const as = api.as(account);
  const hive = await as.createHive(await as.createApiary('Orchard Meadow'), 'Hive 7');

  const { page: field } = await device({ session: account, context: { locale: 'en-GB' } });
  await field.goto(`/hives/${hive}`);
  await expectSynced(field);
  await field.getByRole('button', { name: 'Record visit' }).first().click();
  const form = visitForm(field, 'Record visit');
  await form.getByLabel('Hive weight (kg)').fill('40');

  // Meanwhile, on another device, the account switches to pounds.
  const { page: desk } = await device({ session: account, context: { locale: 'en-GB' } });
  await desk.goto('/settings');
  await desk.getByRole('radio', { name: 'lb pounds' }).check();
  await expectSynced(desk);

  // The device with the open form receives the change ...
  await syncNowFromChip(field);
  await field.waitForFunction(
    () => JSON.parse(localStorage.getItem('obh.settings') ?? '{}').weight_unit?.v === 'lb',
    undefined,
    { timeout: 40_000 }
  );
  // ... and the form still reads what was typed as kilograms.
  await expect(form.getByLabel('Hive weight (kg)')).toHaveValue('40');
  await form.getByRole('button', { name: 'Save' }).click();
  await expect(form).toBeHidden();
  // Shown in the new units from now on.
  await expect(field.getByRole('listitem').filter({ hasText: '88.18 lb' })).toBeVisible();
  await expectSynced(field);
  const [stored] = await as.listInspections(hive);
  expect(stored).toMatchObject({ weightKg: 40 });
});
