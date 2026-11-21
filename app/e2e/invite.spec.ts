// The invite link: what it is for, and what happens when it cannot be used
// by the account that signs in.
import { expect, test } from './support/fixtures';
import { expectSynced, submitSignIn } from './support/app';

test('an invite names its workspace and says why the wrong account cannot join', async ({
  api,
  device
}) => {
  const owner = await api.signUp();
  const club = await api.as(owner).createWorkspace('Bee Club');
  const token = await api.as(club).invite(club.orgId, 'friend@bees.example.org');
  const stranger = await api.signUp();

  const { page } = await device();
  await page.goto(`/login?invite=${token}`);
  await expect(page.getByText('invited to the workspace “Bee Club”')).toBeVisible();
  await expect(page.getByText('f…@b….org')).toBeVisible();

  await page.getByRole('button', { name: /Already have an account/ }).click();
  await submitSignIn(page, stranger.email, stranger.password);
  // Joining is refused: the page says so instead of continuing silently.
  await expect(page.getByRole('alert')).toHaveText(/This invite is for f…@b….org/);
  await expect(page).toHaveURL(/\/login/);

  await page.getByRole('button', { name: 'Continue without joining' }).click();
  await expect(page).toHaveURL(/\/$/);
  await expectSynced(page);
});

test('an expired or revoked invite says so before signing in', async ({ api, device }) => {
  const owner = await api.signUp();
  const token = await api.as(owner).invite(owner.orgId, 'gone@bees.example.org');
  await api.as(owner).http('/tenants/invite/revoke', {
    org_id: owner.orgId,
    id: (
      await api
        .as(owner)
        .http<{ invites: { id: string }[] }>(`/tenants/invites?org_id=${owner.orgId}`)
    ).invites[0]?.id
  });
  const { page } = await device();
  await page.goto(`/login?invite=${token}`);
  await expect(page.getByRole('alert')).toHaveText(/no longer valid/);
});
