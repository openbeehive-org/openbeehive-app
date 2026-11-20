import type { Page } from '@playwright/test';
import { expect, test } from './support/fixtures';
import { expectSynced, syncedChip } from './support/app';

// On the apiaries page.
async function addApiary(page: Page, name: string) {
  await page.getByRole('button', { name: '+ New' }).click();
  await page.getByLabel('Name').fill(name);
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('link', { name })).toBeVisible();
}

test('a device that uploads after another one wrote still receives what it had not pulled', async ({
  api,
  device
}) => {
  const account = await api.signUp();
  const a = await device({ session: account });
  const b = await device({ session: account });
  for (const { page } of [a, b]) {
    await page.goto('/apiaries');
    await expectSynced(page);
  }

  // A is out of range, so it cannot have pulled what B writes now.
  await a.context.setOffline(true);
  await addApiary(b.page, 'Written on B');
  await expectSynced(b.page);

  await addApiary(a.page, 'Written on A');
  await a.context.setOffline(false);
  await expectSynced(a.page);
  await expect(a.page.getByRole('link', { name: 'Written on B' })).toBeVisible();
  await expect(a.page.getByRole('link', { name: 'Written on A' })).toBeVisible();

  // B asks for a sync by tapping the status chip.
  await syncedChip(b.page).click();
  await expect(b.page.getByRole('link', { name: 'Written on A' })).toBeVisible();
  await expect(b.page.getByRole('link', { name: 'Written on B' })).toBeVisible();
});

test('edits of different fields of one visit on two devices both survive', async ({
  api,
  device
}) => {
  const account = await api.signUp();
  const as = api.as(account);
  const apiary = await as.createApiary('Orchard Meadow');
  const hive = await as.createHive(apiary, 'Hive 7');
  await as.createInspection(hive, { note: 'as recorded', weather: 'sunny' });

  const a = await device({ session: account });
  const b = await device({ session: account });
  for (const { page } of [a, b]) {
    await page.goto(`/hives/${hive}`);
    await expect(page.getByText('as recorded')).toBeVisible();
    await expectSynced(page);
  }
  const editVisit = async (page: Page, label: string, value: string) => {
    await page.getByRole('button', { name: /^Edit: Visit/ }).click();
    const form = page
      .locator('form')
      .filter({ has: page.getByRole('heading', { name: 'Edit visit' }) });
    await form.getByLabel(label).fill(value);
    await form.getByRole('button', { name: 'Save' }).click();
    await expect(form).toBeHidden();
  };

  // A corrects the note without reception; B corrects the weather meanwhile.
  await a.context.setOffline(true);
  await editVisit(a.page, 'Note', 'corrected on A');
  await editVisit(b.page, 'Weather', 'rain, corrected on B');
  await expectSynced(b.page);

  await a.context.setOffline(false);
  await expectSynced(a.page);
  await syncedChip(b.page).click();

  for (const { page } of [a, b]) {
    const card = page
      .getByRole('listitem')
      .filter({ has: page.getByRole('button', { name: /^Edit: Visit/ }) });
    await expect(card).toContainText('corrected on A');
    await expect(card).toContainText('rain, corrected on B');
  }
  const [stored] = await as.listInspections(hive);
  expect(stored).toMatchObject({ note: 'corrected on A', weather: 'rain, corrected on B' });
});

// BUG (app, src/routes/(app)/apiaries/[id]/+page.svelte, saveEdit): the apiary
// form saves name, address, note and position on every Save, also the fields
// the user did not touch, and each gets a fresh stamp. Merging is per field
// by stamp, so the device that saves last also "wins" the fields it never
// edited: here A's rename is replaced by the old name B still had. The other
// record forms send only what changed (changedFields in lib/local/history.ts).
test.fail(
  'edits of different fields of one apiary on two devices both survive',
  async ({ api, device }) => {
    const account = await api.signUp();
    const as = api.as(account);
    const apiary = await as.createApiary('Orchard Meadow', { note: 'behind the barn' });

    const a = await device({ session: account });
    const b = await device({ session: account });
    for (const { page } of [a, b]) {
      await page.goto(`/apiaries/${apiary}`);
      await expect(page.getByRole('heading', { name: 'Orchard Meadow' })).toBeVisible();
      await expectSynced(page);
    }
    const editApiary = async (page: Page, label: string, value: string) => {
      await page.getByRole('button', { name: 'Edit apiary' }).click();
      await page.getByLabel(label, { exact: true }).fill(value);
      await page.getByRole('button', { name: 'Save' }).click();
      await expect(page.getByRole('button', { name: 'Save' })).toBeHidden();
    };

    await a.context.setOffline(true);
    await editApiary(a.page, 'Name', 'Renamed on A');
    await editApiary(b.page, 'Note', 'note from B');
    await expectSynced(b.page);

    await a.context.setOffline(false);
    await expectSynced(a.page);
    await syncedChip(b.page).click();

    for (const { page } of [a, b]) {
      await expect(page.getByText('note from B')).toBeVisible();
      await expect(page.getByRole('heading', { name: 'Renamed on A' })).toBeVisible({
        timeout: 5000
      });
    }
  }
);
