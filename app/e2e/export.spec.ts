import fs from 'node:fs/promises';
import { expect, test } from './support/fixtures';
import { expectSynced } from './support/app';

const workspaceOf = async (path: string) =>
  JSON.parse(await fs.readFile(path, 'utf8')) as {
    workspace_id: string;
    source: string;
    data: Record<string, { id: string; name?: string }[]>;
  };

test('a backup comes back on a new device, with a preview and an undo', async ({
  api,
  device
}, testInfo) => {
  const owner = await api.signUp();
  const as = api.as(owner);
  const apiary = await as.createApiary('Orchard Meadow');
  const hive = await as.createHive(apiary, 'Hive 7');
  await as.createInspection(hive, { note: 'calm and busy' });

  // Export on the device that holds the workspace.
  const first = await device({ session: owner });
  await first.page.goto('/settings');
  await expectSynced(first.page);
  const [download] = await Promise.all([
    first.page.waitForEvent('download'),
    first.page.getByRole('button', { name: 'Download full backup' }).click()
  ]);
  const file = testInfo.outputPath('backup.json');
  await download.saveAs(file);
  const doc = await workspaceOf(file);
  expect(doc.workspace_id).toBe(owner.orgId);
  expect(doc.data.apiary?.map((a) => a.name)).toEqual(['Orchard Meadow']);
  await first.context.close();

  // A wiped device, in a fresh workspace: the import shows what it will do.
  const fresh = await api.signUp();
  const { page } = await device({ session: fresh });
  await page.goto('/settings');
  await expectSynced(page);
  await page.getByLabel('Choose file…').setInputFiles(file);
  const preview = page.getByRole('region', { name: 'Preview' });
  await expect(preview).toContainText('This file comes from another workspace');
  await expect(preview.getByRole('row', { name: /^Apiaries/ })).toContainText('1');
  await expect(preview.getByRole('row', { name: /^Visits/ })).toBeVisible();
  await expect(preview.getByLabel('Overwrite existing records')).not.toBeChecked();
  await preview.getByRole('button', { name: 'Import now' }).click();
  await expect(page.getByText(/^Imported: \d+ new, 0 overwritten, 0 restored\.$/)).toBeVisible();

  // The records are back and reach the server.
  await page.getByRole('navigation').getByRole('link', { name: 'Apiaries' }).click();
  await expect(page.getByText('Orchard Meadow')).toBeVisible();
  await expectSynced(page);
  const [mine] = await api
    .as(fresh)
    .rpc<{ apiaries?: { id: string; name: string }[] }>('ApiaryService/ListApiaries', {})
    .then((r) => r.apiaries ?? []);
  expect(mine?.name).toBe('Orchard Meadow');
  expect(mine?.id).not.toBe(apiary);

  // The same file again changes nothing; undo removes what the import made.
  await page.goto('/settings');
  await page.getByLabel('Choose file…').setInputFiles(file);
  await expect(preview).toContainText('Nothing to import');
  await preview.getByRole('button', { name: 'Cancel' }).click();
  await page.getByRole('button', { name: 'Undo last import' }).click();
  await expect(page.getByText(/^Import undone: \d+ records deleted\.$/)).toBeVisible();
  await page.getByRole('navigation').getByRole('link', { name: 'Apiaries' }).click();
  await expect(page.getByText('Orchard Meadow')).toHaveCount(0);
});

test('the server copy downloads from Settings and with an API key', async ({
  api,
  device,
  server
}, testInfo) => {
  const owner = await api.signUp();
  const as = api.as(owner);
  await as.createApiary('Lime Avenue');

  const { page } = await device({ session: owner });
  await page.goto('/settings');
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('button', { name: 'Download server copy' }).click()
  ]);
  expect(download.suggestedFilename()).toMatch(/^openbeehive-server-\d{4}-\d{2}-\d{2}\.json$/);
  const file = testInfo.outputPath('server.json');
  await download.saveAs(file);
  const doc = await workspaceOf(file);
  expect(doc).toMatchObject({ workspace_id: owner.orgId, source: 'server' });
  expect(doc.data.apiary?.map((a) => a.name)).toEqual(['Lime Avenue']);

  // Scripts use an API key; a read-only key may not export.
  const key = async (scope: string) =>
    (await as.http<{ token: string }>('/auth/api-keys', { name: scope, scope })).token;
  const get = (token: string) =>
    fetch(server.url + '/export?photos=1', { headers: { Authorization: `Bearer ${token}` } });
  const zip = await get(await key('write'));
  expect(zip.status).toBe(200);
  expect(zip.headers.get('content-type')).toBe('application/zip');
  expect((await get(await key('read'))).status).toBe(403);
});
