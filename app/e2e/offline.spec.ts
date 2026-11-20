import { expect, test } from './support/fixtures';
import { expectInstalled, expectSynced } from './support/app';

test('opens from a cold start without a network, shows the records and takes a new visit', async ({
  startServer,
  apiFor,
  device
}) => {
  // A server of its own: it is stopped below, which the shared one must not be.
  const server = await startServer();
  const api = apiFor(server);
  const account = await api.signUp();
  const as = api.as(account);
  const apiary = await as.createApiary('Orchard Meadow');
  const hive = await as.createHive(apiary, 'Hive 7');
  await as.createInspection(hive, { note: 'Calm colony, plenty of brood' });

  const { context, page } = await device({ session: account, server });
  await page.goto(`/hives/${hive}`);
  await expect(page.getByText('Calm colony, plenty of brood')).toBeVisible();
  await expectSynced(page);
  await expectInstalled(page);
  await page.close();

  // In the field: no reception, and the server could not be reached anyway.
  await context.setOffline(true);
  await server.stop();

  const cold = await context.newPage();
  await cold.goto('/');
  await expect(cold.getByText('Offline: changes are saved and synced later.')).toBeVisible();
  await cold.getByRole('navigation').getByRole('link', { name: 'Hives' }).click();
  await cold.getByRole('link', { name: /Hive 7/ }).click();
  await expect(cold.getByRole('heading', { name: 'Hive 7' })).toBeVisible();
  await expect(cold.getByText('Calm colony, plenty of brood')).toBeVisible();

  await cold.getByRole('button', { name: 'Record visit' }).first().click();
  await cold.getByRole('button', { name: 'Increase: Occupied frames' }).click();
  await cold.getByLabel('Note').fill('Recorded without reception');
  await cold.getByRole('button', { name: 'Save' }).click();
  await expect(cold.getByText('Recorded without reception')).toBeVisible();
  await expect(cold.getByRole('button', { name: /^Offline · \d+ waiting/ })).toBeVisible();

  // Back in range.
  await server.start();
  await context.setOffline(false);
  await expectSynced(cold);
  const notes = (await as.listInspections(hive)).map((i) => i.note);
  expect(notes).toContain('Recorded without reception');
  expect(notes).toHaveLength(2);
});
