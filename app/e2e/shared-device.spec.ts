import type { Page } from '@playwright/test';
import { expect, test } from './support/fixtures';
import type { Session } from './support/api';
import { expectSynced } from './support/app';

// What signing out (keeping the data) and signing in as someone else leaves
// in the browser: the sign-in page stores the new session and reloads.
async function signInAs(page: Page, s: Session) {
  await page.evaluate((s) => {
    for (const key of ['session', 'obh.userId', 'obh.orgId', 'obh.email']) {
      localStorage.removeItem(key);
    }
    localStorage.setItem('session', s.token);
    localStorage.setItem('obh.userId', s.userId);
    localStorage.setItem('obh.orgId', s.orgId);
  }, s);
  await page.goto('/');
}

test('two people on one device in one workspace never upload or see each other’s unsent work', async ({
  api,
  device
}) => {
  const alice = await api.signUp();
  const bobAccount = await api.signUp();
  const invite = await api.as(alice).invite(alice.orgId, bobAccount.email);
  const bob = await api.as(bobAccount).acceptInvite(invite);
  expect(bob.orgId).toBe(alice.orgId);

  const { context, page } = await device({ session: alice });
  await page.goto('/tasks');
  await expectSynced(page);

  // Alice writes, but nothing reaches the server: a personal task and an apiary.
  const push = '**/openbeehive.v1.SyncService/Push';
  await context.route(push, (route) => route.abort('internetdisconnected'));
  await page.getByLabel('New task').fill('Alice’s own reminder');
  await page.getByRole('button', { name: 'Add' }).click();
  await expect(page.getByText('Alice’s own reminder')).toBeVisible();
  await page.goto('/apiaries');
  await page.getByRole('button', { name: '+ New' }).click();
  await page.getByLabel('Name').fill('Written by Alice offline');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('link', { name: 'Written by Alice offline' })).toBeVisible();

  // She leaves the device; Bob signs in to the same workspace, online.
  await signInAs(page, bob);
  await context.unroute(push);
  await page.goto('/apiaries');
  await expectSynced(page);
  await expect(page.getByRole('link', { name: 'Written by Alice offline' })).toHaveCount(0);
  await page.goto('/tasks');
  await expectSynced(page);
  await expect(page.getByText('Alice’s own reminder')).toHaveCount(0);

  // Nothing of hers went up with Bob's session.
  const listed = async () =>
    (
      (await api.as(alice).rpc<{ apiaries?: { name: string }[] }>('ApiaryService/ListApiaries', {}))
        .apiaries ?? []
    ).map((a) => a.name);
  expect(await listed()).not.toContain('Written by Alice offline');

  // Alice signs in here again: her changes upload under her own name.
  await signInAs(page, alice);
  await page.goto('/tasks');
  await expectSynced(page);
  await expect(page.getByText('Alice’s own reminder')).toBeVisible();
  await expect.poll(listed).toContain('Written by Alice offline');
});

test('the second account of a workspace on one device keeps its data on the device', async ({
  api,
  device
}) => {
  const alice = await api.signUp();
  const bobAccount = await api.signUp();
  const invite = await api.as(alice).invite(alice.orgId, bobAccount.email);
  const bob = await api.as(bobAccount).acceptInvite(invite);

  const { context, page } = await device({ session: alice });
  await page.goto('/tasks');
  await expectSynced(page);

  // Bob signs in after Alice in the same tab: the app opens his own store
  // after looking at hers.
  await signInAs(page, bob);
  await page.goto('/tasks');
  await expectSynced(page);
  const banner = page.getByText(/is not saving data on this device/);
  await expect(banner).toHaveCount(0);

  // What he writes while nothing reaches the server survives a reload, so
  // it is stored on the device and not in memory.
  await context.route('**/openbeehive.v1.SyncService/Push', (route) =>
    route.abort('internetdisconnected')
  );
  await page.getByLabel('New task').fill('Bob’s unsent reminder');
  await page.getByRole('button', { name: 'Add' }).click();
  await expect(page.getByText('Bob’s unsent reminder')).toBeVisible();
  await page.reload();
  await expect(page.getByText('Bob’s unsent reminder')).toBeVisible();
  await expect(banner).toHaveCount(0);
});
