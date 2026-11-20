import { expect, test } from './support/fixtures';
import { expectSynced, syncedChip } from './support/app';

test('a new device starts from a snapshot of the workspace and ends synced', async ({
  api,
  device
}) => {
  const account = await api.signUp();
  const as = api.as(account);
  const apiary = await as.createApiary('Orchard Meadow');
  const hive = await as.createHive(apiary, 'Hive 7');
  const gone = await as.createHive(apiary, 'Hive sold last spring');
  await as.rpc('HiveService/DeleteHive', { id: gone });
  await as.createInspection(hive, { note: 'Recorded through the API' });

  // The first device adds a visit of its own.
  const a = await device({ session: account });
  await a.page.goto(`/hives/${hive}`);
  await expect(a.page.getByText('Recorded through the API')).toBeVisible();
  await a.page.getByRole('button', { name: 'Record visit' }).first().click();
  await a.page.getByLabel('Note').fill('Recorded on the first device');
  await a.page.getByRole('button', { name: 'Save' }).click();
  await expect(a.page.getByText('Recorded on the first device')).toBeVisible();
  await expectSynced(a.page);

  // A device that has never synced: it loads the current rows, then follows the log.
  const b = await device({ session: account });
  const calls: { method: string; protocol: string | undefined }[] = [];
  b.page.on('request', (request) => {
    const method = /\/openbeehive\.v1\.SyncService\/(\w+)$/.exec(request.url())?.[1];
    if (method) calls.push({ method, protocol: request.headers()['openbeehive-sync-protocol'] });
  });
  const snapshot = b.page.waitForResponse((r) =>
    r.url().endsWith('/openbeehive.v1.SyncService/Snapshot')
  );
  await b.page.goto('/hives');
  expect((await snapshot).status()).toBe(200);
  await expect(b.page.getByRole('link', { name: /Hive 7/ })).toBeVisible();
  // Deleted before this device existed: not part of the snapshot.
  await expect(b.page.getByRole('link', { name: /Hive sold last spring/ })).toHaveCount(0);
  await expectSynced(b.page);

  const reads = calls.map((c) => c.method).filter((m) => m === 'Snapshot' || m === 'Pull');
  expect(reads[0]).toBe('Snapshot');
  expect(reads).toContain('Pull');
  expect(reads.filter((m) => m === 'Snapshot')).toHaveLength(1);
  // Every request of the sync engine says which protocol the app speaks.
  expect(calls.filter((c) => c.protocol !== '1')).toEqual([]);

  await b.page.getByRole('link', { name: /Hive 7/ }).click();
  await expect(b.page.getByText('Recorded through the API')).toBeVisible();
  await expect(b.page.getByText('Recorded on the first device')).toBeVisible();

  // A later sync continues from the stored position, without a second snapshot.
  const pulls = () => calls.filter((c) => c.method === 'Pull').length;
  const before = pulls();
  await syncedChip(b.page).click();
  await expect.poll(pulls).toBeGreaterThan(before);
  await expectSynced(b.page);
  expect(calls.filter((c) => c.method === 'Snapshot')).toHaveLength(1);
});
