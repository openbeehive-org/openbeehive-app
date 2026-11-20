import { expect, test } from './support/fixtures';
import { expectSynced } from './support/app';

test('a second tab shows a notice, takes over on request and loses no data', async ({
  api,
  device
}) => {
  const account = await api.signUp();
  const as = api.as(account);
  const apiary = await as.createApiary('Orchard Meadow');

  const { context, page: first } = await device({ session: account });
  await first.goto(`/apiaries/${apiary}`);
  await expect(first.getByRole('heading', { name: 'Orchard Meadow' })).toBeVisible();
  await expectSynced(first);

  // A hive that exists only in the local database of this browser so far.
  await context.route('**/openbeehive.v1.SyncService/Push', (route) => route.abort());
  await first.getByRole('button', { name: '+ Add hive' }).click();
  await first.getByLabel('Name').fill('Only on this device');
  await first.getByRole('button', { name: 'Save' }).click();
  await expect(first.getByRole('link', { name: /Only on this device/ })).toBeVisible();

  const notice = (page: typeof first) =>
    page.getByRole('alertdialog', { name: 'Openbeehive is open in another tab' });

  const second = await context.newPage();
  await second.goto(`/apiaries/${apiary}`);
  await expect(notice(second)).toBeVisible();
  await expect(notice(first)).toBeHidden();

  await notice(second).getByRole('button', { name: 'Use it here' }).click();
  await expect(notice(second)).toBeHidden();
  await expect(second.getByRole('link', { name: /Only on this device/ })).toBeVisible();
  await expect(notice(first)).toBeVisible();

  // The second tab now does the work, including the upload the first one never got through.
  await second.getByRole('button', { name: '+ Add hive' }).click();
  await second.getByLabel('Name').fill('Added in the second tab');
  await second.getByRole('button', { name: 'Save' }).click();
  await expect(second.getByRole('link', { name: /Added in the second tab/ })).toBeVisible();
  await context.unroute('**/openbeehive.v1.SyncService/Push');
  await second.getByRole('button', { name: /^(Sync failed|Waiting to upload|Syncing)/ }).click();
  await expectSynced(second);
  const hives = await as.rpc<{ hives?: { name: string }[] }>('HiveService/ListHives', {});
  expect(hives.hives?.map((h) => h.name).sort()).toEqual([
    'Added in the second tab',
    'Only on this device'
  ]);

  // And back: the first tab takes over again and has everything.
  await notice(first).getByRole('button', { name: 'Use it here' }).click();
  await expect(notice(first)).toBeHidden();
  await expect(first.getByRole('link', { name: /Only on this device/ })).toBeVisible();
  await expect(first.getByRole('link', { name: /Added in the second tab/ })).toBeVisible();
  await expect(notice(second)).toBeVisible();
});
