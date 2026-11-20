import { expect, test } from './support/fixtures';
import { acceptDialogs, expectSynced } from './support/app';

test('records a visit with the steppers, saves it once, corrects it, deletes it with undo and for good', async ({
  api,
  device
}) => {
  const account = await api.signUp();
  const as = api.as(account);
  const apiary = await as.createApiary('Orchard Meadow');
  const hive = await as.createHive(apiary, 'Hive 7');

  const { page } = await device({ session: account });
  const asked = acceptDialogs(page);
  await page.goto(`/hives/${hive}`);
  await expect(page.getByRole('heading', { name: 'Hive 7' })).toBeVisible();
  const visits = page
    .getByRole('listitem')
    .filter({ has: page.getByRole('button', { name: /^Edit: Visit/ }) });

  await test.step('record with the steppers', async () => {
    await page.getByRole('button', { name: 'Record visit' }).first().click();
    const form = page
      .locator('form')
      .filter({ has: page.getByRole('heading', { name: 'Record visit' }) });
    // Counts start empty: "not recorded" is not the same as zero.
    await expect(form.getByRole('spinbutton', { name: 'Occupied frames' })).toHaveValue('');
    for (let i = 0; i < 3; i++)
      await form.getByRole('button', { name: 'Increase: Occupied frames' }).click();
    await form.getByRole('button', { name: 'Increase: Brood frames' }).click();
    await form.getByRole('button', { name: 'Increase: Brood frames' }).click();
    await form.getByRole('button', { name: 'Decrease: Brood frames' }).click();
    // One tap on minus records an observed zero.
    await form.getByRole('button', { name: 'Decrease: Swarm cells' }).click();
    await expect(form.getByRole('spinbutton', { name: 'Occupied frames' })).toHaveValue('3');
    await expect(form.getByRole('spinbutton', { name: 'Brood frames' })).toHaveValue('1');
    await expect(form.getByRole('spinbutton', { name: 'Swarm cells' })).toHaveValue('0');
    await form.getByLabel('Queen seen').check();
    await form.getByLabel('Note').fill('First look after winter');

    // Save activated twice before the page can react: the worst double tap.
    const submits = await form
      .getByRole('button', { name: 'Save' })
      .evaluate((save: HTMLButtonElement) => {
        let count = 0;
        save.form?.addEventListener('submit', () => count++, { capture: true });
        save.click();
        save.click();
        return count;
      });
    expect(submits).toBe(2);

    await expect(form).toBeHidden();
    await expect(visits).toHaveCount(1);
    await expect(visits.first()).toContainText('First look after winter');
    await expectSynced(page);
    const stored = await as.listInspections(hive);
    expect(stored).toHaveLength(1);
    expect(stored[0]).toMatchObject({ frames: 3, broodFrames: 1, queenCells: 0, queenSeen: true });
  });

  await test.step('correct it', async () => {
    await visits
      .first()
      .getByRole('button', { name: /^Edit: Visit/ })
      .click();
    const form = page
      .locator('form')
      .filter({ has: page.getByRole('heading', { name: 'Edit visit' }) });
    await expect(form.getByRole('spinbutton', { name: 'Occupied frames' })).toHaveValue('3');
    await form.getByRole('button', { name: 'Increase: Occupied frames' }).click();
    await form.getByLabel('Note').fill('First look after winter, four frames');
    await form.getByRole('button', { name: 'Save' }).click();
    await expect(form).toBeHidden();
    await expect(visits).toHaveCount(1);
    await expect(visits.first()).toContainText('First look after winter, four frames');
    await expectSynced(page);
    const stored = await as.listInspections(hive);
    expect(stored).toHaveLength(1);
    expect(stored[0]).toMatchObject({ frames: 4, note: 'First look after winter, four frames' });
  });

  await test.step('delete it and take that back', async () => {
    await visits
      .first()
      .getByRole('button', { name: /^Delete: Visit/ })
      .click();
    expect(asked.at(-1)).toMatch(/^Delete the visit of /);
    await expect(page.getByText('No visits recorded yet.')).toBeVisible();
    await expect(page.getByText(/^Visit of .+ deleted$/)).toBeVisible();
    await page.getByRole('button', { name: 'Undo' }).click();
    await expect(visits).toHaveCount(1);
    await expect(visits.first()).toContainText('First look after winter, four frames');
    await expectSynced(page);
    expect(await as.listInspections(hive)).toHaveLength(1);
  });

  await test.step('delete it for good', async () => {
    await visits
      .first()
      .getByRole('button', { name: /^Delete: Visit/ })
      .click();
    await expect(page.getByText('No visits recorded yet.')).toBeVisible();
    await expectSynced(page);
    expect(await as.listInspections(hive)).toHaveLength(0);
    // Still gone after the app restarts.
    await page.reload();
    await expect(page.getByRole('heading', { name: 'Hive 7' })).toBeVisible();
    await expect(page.getByText('No visits recorded yet.')).toBeVisible();
  });
});
